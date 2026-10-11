package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestStorePublishesOnlySuccessfulSnapshots(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "itineraries.json"), seedData)
	if err != nil {
		t.Fatal(err)
	}
	before := store.List()
	original, _ := store.Get(before[0].ID)
	rejected := errors.New("reject mutation")
	err = store.commit(func(items map[string]*Itinerary, order []string) ([]string, error) {
		delete(items, original.ID)
		got, ok := store.Get(original.ID)
		if !ok || !reflect.DeepEqual(original, got) || !reflect.DeepEqual(before, store.List()) {
			t.Error("reader saw uncommitted changes")
		}
		return order, rejected
	})
	if !errors.Is(err, rejected) || !reflect.DeepEqual(before, store.List()) {
		t.Fatal("rejected snapshot changed store")
	}
}

func TestStoreFailedMutationsPreserveSnapshot(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "itineraries.json"), seedData)
	if err != nil {
		t.Fatal(err)
	}
	before := store.List()
	original, _ := store.Get(before[0].ID)
	if err := os.Remove(store.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(store.path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(&Itinerary{ID: "new", Title: "New", Days: original.Days}); err == nil {
		t.Fatal("expected create persistence failure")
	}
	if err := store.Delete(original.ID); err == nil {
		t.Fatal("expected delete persistence failure")
	}
	replacement := cloneItinerary(original)
	replacement.Title = "Replacement"
	if err := store.Replace(replacement); err == nil {
		t.Fatal("expected replacement persistence failure")
	}
	after, ok := store.Get(original.ID)
	if !ok || !reflect.DeepEqual(original, after) || !reflect.DeepEqual(before, store.List()) {
		t.Fatal("failed write changed stored data or order")
	}
	if _, ok := store.Get("new"); ok {
		t.Fatal("failed create published itinerary")
	}
}

func TestStoreCopiesNestedSlices(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "itineraries.json"), []byte("[]"))
	if err != nil {
		t.Fatal(err)
	}
	it := &Itinerary{ID: "trip", Title: "Trip", Days: []Day{{Activities: []Activity{{Title: "Walk"}}}}, Checklist: []string{"Water"}}
	if err := store.Create(it); err != nil {
		t.Fatal(err)
	}
	it.Days[0].Activities[0].Title = "Changed"
	it.Checklist[0] = "Changed"
	got, _ := store.Get(it.ID)
	if got.Days[0].Activities[0].Title != "Walk" || got.Checklist[0] != "Water" {
		t.Fatal("create retained caller-owned slices")
	}
	got.Days[0].Activities[0].Title = "Changed"
	got.Checklist[0] = "Changed"
	updated, err := store.Update(it.ID, map[string]json.RawMessage{"title": json.RawMessage(`"Updated"`)})
	if err != nil {
		t.Fatal(err)
	}
	updated.Days[0].Activities[0].Title = "Changed"
	updated.Checklist[0] = "Changed"
	stored, _ := store.Get(it.ID)
	if stored.Days[0].Activities[0].Title != "Walk" || stored.Checklist[0] != "Water" {
		t.Fatal("get or update exposed stored slices")
	}
}
