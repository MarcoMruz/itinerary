package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

var (
	ErrNotFound = errors.New("itinerary not found")
	ErrExists   = errors.New("itinerary with this id already exists")
)

// Store is an in-memory registry persisted to a JSON file on every write.
// Writes publish a new snapshot only after its atomic file write succeeds.
type Store struct {
	path  string
	wmu   sync.Mutex   // serialises mutations so the file never goes back in time
	mu    sync.RWMutex // guards items and order
	items map[string]*Itinerary
	order []string
}

// OpenStore loads path, or initialises it from seed when it does not exist
// (e.g. a fresh, empty Docker volume).
func OpenStore(path string, seed []byte) (*Store, error) {
	s := &Store{path: path, items: map[string]*Itinerary{}}
	data, err := os.ReadFile(path)
	fresh := errors.Is(err, os.ErrNotExist)
	switch {
	case fresh:
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		data = seed
	case err != nil:
		return nil, err
	}

	var list []*Itinerary
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, it := range list {
		if it == nil {
			return nil, errors.New("itinerary list contains null item")
		}
		if err := it.Normalize(); err != nil {
			return nil, fmt.Errorf("itinerary %q: %w", it.ID, err)
		}
		if _, dup := s.items[it.ID]; dup {
			return nil, fmt.Errorf("duplicate itinerary id %q", it.ID)
		}
		s.items[it.ID] = it
		s.order = append(s.order, it.ID)
	}
	if fresh {
		if err := s.persist(s.items, s.order); err != nil {
			return nil, fmt.Errorf("write %s: %w", path, err)
		}
	}
	return s, nil
}

// List returns summaries in insertion order.
func (s *Store) List() []Summary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Summary, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.items[id].Summary())
	}
	return out
}

// Get returns an independent copy of the stored itinerary.
func (s *Store) Get(id string) (*Itinerary, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	it, ok := s.items[id]
	if !ok {
		return nil, false
	}
	return cloneItinerary(it), true
}

// Create stores a normalized itinerary. It returns ErrExists on id collision.
func (s *Store) Create(it *Itinerary) error {
	copy := cloneItinerary(it)
	return s.commit(func(items map[string]*Itinerary, order []string) ([]string, error) {
		if _, ok := items[copy.ID]; ok {
			return nil, ErrExists
		}
		items[copy.ID] = copy
		return append(order, copy.ID), nil
	})
}

func (s *Store) Update(id string, changes map[string]json.RawMessage) (*Itinerary, error) {
	var updated *Itinerary
	err := s.commit(func(items map[string]*Itinerary, order []string) ([]string, error) {
		previous, ok := items[id]
		if !ok {
			return nil, ErrNotFound
		}
		if len(changes) == 0 {
			return nil, invalid("changes must contain at least one field")
		}
		for key, value := range changes {
			if _, ok := itineraryUpdateSchema["properties"].(schema)[key]; !ok {
				return nil, invalid("unknown or immutable field %q", key)
			}
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return nil, invalid("%s must not be null", key)
			}
		}
		data, err := json.Marshal(changes)
		if err != nil {
			return nil, err
		}
		next := cloneItinerary(previous)
		if _, changed := changes["days"]; changed {
			next.Days = nil // JSON decoding reuses slice elements; replacement must start empty.
		}
		if _, changed := changes["checklist"]; changed {
			next.Checklist = nil
		}
		if err := decodeRaw(data, next); err != nil {
			return nil, invalid("invalid changes: %v", err)
		}
		if err := next.Normalize(); err != nil {
			return nil, err
		}
		updated = cloneItinerary(next)
		items[id] = next
		return order, nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// Replace swaps an existing itinerary for a normalized one with the same id.
func (s *Store) Replace(it *Itinerary) error {
	copy := cloneItinerary(it)
	return s.commit(func(items map[string]*Itinerary, order []string) ([]string, error) {
		if _, ok := items[copy.ID]; !ok {
			return nil, ErrNotFound
		}
		items[copy.ID] = copy
		return order, nil
	})
}

// Delete removes an itinerary. It returns ErrNotFound for unknown ids.
func (s *Store) Delete(id string) error {
	return s.commit(func(items map[string]*Itinerary, order []string) ([]string, error) {
		if _, ok := items[id]; !ok {
			return nil, ErrNotFound
		}
		delete(items, id)
		i := slices.Index(order, id)
		return slices.Delete(order, i, i+1), nil
	})
}

// Stored itineraries are immutable; only the map and order need copying.
func (s *Store) commit(apply func(map[string]*Itinerary, []string) ([]string, error)) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()

	s.mu.RLock()
	items, order := maps.Clone(s.items), slices.Clone(s.order)
	s.mu.RUnlock()
	order, err := apply(items, order)
	if err != nil {
		return err
	}
	if err := s.persist(items, order); err != nil {
		return err
	}
	s.mu.Lock()
	s.items, s.order = items, order
	s.mu.Unlock()
	return nil
}

func (s *Store) persist(items map[string]*Itinerary, order []string) error {
	snapshot := make([]*Itinerary, len(order))
	for i, id := range order {
		snapshot[i] = items[id]
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.path, append(data, '\n'))
}

func writeAtomic(path string, data []byte) error {
	return writeAtomicMode(path, data, 0o644)
}

func writeAtomicMode(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".itineraries-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), mode)
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func cloneItinerary(it *Itinerary) *Itinerary {
	copy := *it
	copy.Checklist = slices.Clone(it.Checklist)
	copy.Days = slices.Clone(it.Days)
	for i := range copy.Days {
		copy.Days[i].Activities = slices.Clone(it.Days[i].Activities)
	}
	return &copy
}
