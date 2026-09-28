package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultStateDB(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	if got, want := defaultStateDB(), filepath.Join(home, ".local", "state", "satellite-data", "state.sqlite3"); got != want {
		t.Fatalf("default state DB = %q, want %q", got, want)
	}

	stateHome := filepath.Join(t.TempDir(), "custom-state")
	t.Setenv("XDG_STATE_HOME", stateHome)
	if got, want := defaultStateDB(), filepath.Join(stateHome, "satellite-data", "state.sqlite3"); got != want {
		t.Fatalf("XDG state DB = %q, want %q", got, want)
	}
}

func TestCheckLegacyStateDB(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "state.sqlite3")
	legacy := filepath.Join(dir, "state.db")
	if err := checkLegacyStateDB(current); err != nil {
		t.Fatalf("new installation: %v", err)
	}
	if err := os.WriteFile(legacy, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkLegacyStateDB(current); err == nil {
		t.Fatal("old database must not be silently replaced with an empty one")
	}
	if err := os.WriteFile(current, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkLegacyStateDB(current); err != nil {
		t.Fatalf("migrated installation: %v", err)
	}
}
