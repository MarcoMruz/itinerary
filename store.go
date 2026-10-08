package main

import (
	"encoding/json"
	"errors"
	"fmt"
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
// Reads never touch the disk; writes are serialised and written atomically
// (temp file + rename), and rolled back in memory if the write fails.
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
		if err := s.commit(func() (func(), error) { return func() {}, nil }); err != nil {
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

// Get returns the stored itinerary. Callers must not mutate it.
func (s *Store) Get(id string) (*Itinerary, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	it, ok := s.items[id]
	return it, ok
}

// Create stores a normalized itinerary. It returns ErrExists on id collision.
func (s *Store) Create(it *Itinerary) error {
	return s.commit(func() (func(), error) {
		if _, ok := s.items[it.ID]; ok {
			return nil, ErrExists
		}
		prev := s.order
		s.items[it.ID] = it
		s.order = append(slices.Clip(s.order), it.ID)
		return func() { delete(s.items, it.ID); s.order = prev }, nil
	})
}

// Delete removes an itinerary. It returns ErrNotFound for unknown ids.
func (s *Store) Delete(id string) error {
	return s.commit(func() (func(), error) {
		it, ok := s.items[id]
		if !ok {
			return nil, ErrNotFound
		}
		prev := s.order
		i := slices.Index(prev, id)
		s.order = slices.Delete(slices.Clone(prev), i, i+1)
		delete(s.items, id)
		return func() { s.items[id] = it; s.order = prev }, nil
	})
}

// commit applies a mutation under the locks, persists the resulting snapshot
// and undoes the mutation if persisting fails, keeping memory and disk equal.
// Readers are only blocked while the snapshot is marshalled, not during I/O.
func (s *Store) commit(apply func() (undo func(), err error)) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()

	s.mu.Lock()
	undo, err := apply()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	snapshot := make([]*Itinerary, len(s.order))
	for i, id := range s.order {
		snapshot[i] = s.items[id]
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	s.mu.Unlock()

	if err == nil {
		err = writeAtomic(s.path, append(data, '\n'))
	}
	if err != nil {
		s.mu.Lock()
		undo()
		s.mu.Unlock()
	}
	return err
}

func writeAtomic(path string, data []byte) error {
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
		err = os.Chmod(tmp.Name(), 0o644)
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
