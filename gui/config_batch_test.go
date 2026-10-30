package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRefreshWithoutChangesDoesNotWrite — a country refresh that
// changes nothing must not write config.json at all (it runs in the
// background; a late write can outlive the caller).
func TestRefreshWithoutChangesDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveProfile(Profile{ID: "id-a", Name: "a", Address: "a.example"}); err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 64))

	// Stub the country lookup: it resolves, but changes nothing.
	old := countryLookup
	countryLookup = func(a *App, p *Profile) (bool, error) { return false, nil }
	t.Cleanup(func() { countryLookup = old })

	before := saveCount.Load()
	if _, err := app.RefreshAllCountries(); err != nil {
		t.Fatal(err)
	}
	if got := saveCount.Load(); got != before {
		t.Fatalf("a no-change refresh wrote config.json (%d -> %d)", before, got)
	}
}

// TestBatchProfileUpdatesMatchPerProfilePath — the batch write path
// (UpdateProfile + SaveFile) must leave config.json byte-identical to
// the per-profile path (SaveProfile), and UpdateProfile alone must not
// touch the file.
func TestBatchProfileUpdatesMatchPerProfilePath(t *testing.T) {
	// Batch store.
	batchDir := t.TempDir()
	batch, err := NewConfigStore(batchDir)
	if err != nil {
		t.Fatal(err)
	}
	// Per-profile store.
	perDir := t.TempDir()
	per, err := NewConfigStore(perDir)
	if err != nil {
		t.Fatal(err)
	}

	seed := func(s *ConfigStore) (Profile, Profile) {
		// Explicit ids so both stores hold byte-identical documents.
		a, err := s.SaveProfile(Profile{ID: "id-a", Name: "a", Protocol: "vless", Address: "a.example"})
		if err != nil {
			t.Fatal(err)
		}
		b, err := s.SaveProfile(Profile{ID: "id-b", Name: "b", Protocol: "vless", Address: "b.example"})
		if err != nil {
			t.Fatal(err)
		}
		return a, b
	}
	a1, b1 := seed(batch)
	a2, b2 := seed(per)
	if a1.ID != a2.ID || b1.ID != b2.ID {
		t.Fatal("ids must match for the comparison to be meaningful")
	}

	// Same logical change, two different write strategies.
	change := func(p Profile) Profile {
		p.Name = "changed"
		p.CountryCode = "NL"
		return p
	}
	batch.UpdateProfile(change(a1))
	batch.UpdateProfile(change(b1))
	if err := batch.SaveFile(); err != nil {
		t.Fatal(err)
	}
	if _, err := per.SaveProfile(change(a2)); err != nil {
		t.Fatal(err)
	}
	if _, err := per.SaveProfile(change(b2)); err != nil {
		t.Fatal(err)
	}

	batchBytes, err := os.ReadFile(filepath.Join(batchDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	perBytes, err := os.ReadFile(filepath.Join(perDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(batchBytes) != string(perBytes) {
		t.Fatalf("batch write differs from the per-profile path:\nbatch: %s\nper:   %s", batchBytes, perBytes)
	}
}

// TestUpdateProfileDoesNotWriteFile — UpdateProfile only changes the
// in-memory store; the file is written by the explicit SaveFile.
func TestUpdateProfileDoesNotWriteFile(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.SaveProfile(Profile{Name: "before"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	p.Name = "after"
	store.UpdateProfile(p)
	mid, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(mid) != string(before) {
		t.Fatal("UpdateProfile must not write the file")
	}
	// The in-memory store already has the new value.
	if got, ok := store.GetProfile(p.ID); !ok || got.Name != "after" {
		t.Fatalf("in-memory profile = %+v, want name 'after'", got)
	}

	if err := store.SaveFile(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) == string(before) {
		t.Fatal("SaveFile did not write the update")
	}
}
