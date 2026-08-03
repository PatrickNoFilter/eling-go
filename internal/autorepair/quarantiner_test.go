package autorepair

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestQuarantinePersistReenable verifies the Phase-2 quarantine lifecycle:
// record → persist to a temp state file → re-read → re-enable clears it.
func TestQuarantinePersistReenable(t *testing.T) {
	e := New(0, 0)
	e.statePath = filepath.Join(t.TempDir(), "autorepair_state.json")

	if e.CountQuarantined() != 0 {
		t.Fatalf("expected 0 quarantined initially, got %d", e.CountQuarantined())
	}

	e.Quarantine("boom_tool", "crash", "crash detected; recommend quarantine", "panic: idx")

	if !e.IsQuarantined("boom_tool") {
		t.Fatal("expected boom_tool to be quarantined")
	}
	if e.CountQuarantined() != 1 {
		t.Fatalf("expected 1 quarantined, got %d", e.CountQuarantined())
	}

	// Verify persisted file exists and parses.
	data, err := os.ReadFile(e.statePath)
	if err != nil {
		t.Fatalf("persisted state file missing: %v", err)
	}
	var sf stateFile
	if err := json.Unmarshal(data, &sf); err != nil {
		t.Fatalf("state file invalid json: %v", err)
	}
	if _, ok := sf.Quarantined["boom_tool"]; !ok {
		t.Fatalf("expected persisted record for boom_tool, got %#v", sf.Quarantined)
	}

	// Re-enable.
	if !e.Reenable("boom_tool") {
		t.Fatal("expected Reenable to return true for a quarantined tool")
	}
	if e.CountQuarantined() != 0 {
		t.Fatalf("expected 0 quarantined after re-enable, got %d", e.CountQuarantined())
	}
	// Re-enable of a non-quarantined tool returns false.
	if e.Reenable("boom_tool") {
		t.Fatal("expected Reenable to return false for a non-quarantined tool")
	}
}

// TestQuarantineLoadState verifies LoadState re-hydrates records persisted to
// disk into a fresh engine (restart-equivalent).
func TestQuarantineLoadState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "autorepair_state.json")
	// Simulate a prior run that wrote the file.
	first := New(0, 0)
	first.statePath = path
	first.Quarantine("grep_tool", "config_drift", "drift", "base_url wrong")

	// Fresh engine pointing at the same file.
	second := New(0, 0)
	second.statePath = path
	second.LoadState()

	if !second.IsQuarantined("grep_tool") {
		t.Fatal("expected LoadState to restore quarantine record for grep_tool")
	}
	if second.CountQuarantined() != 1 {
		t.Fatalf("expected 1 quarantined after LoadState, got %d", second.CountQuarantined())
	}
	// LoadState is idempotent (only loads once).
	second.LoadState()
	if second.CountQuarantined() != 1 {
		t.Fatalf("LoadState should be idempotent, got %d", second.CountQuarantined())
	}
}

// TestQuarantineMissingFileIsEmpty confirms a missing state file is treated as
// an empty (healthy) state rather than an error.
func TestQuarantineMissingFileIsEmpty(t *testing.T) {
	e := New(0, 0)
	e.statePath = filepath.Join(t.TempDir(), "does_not_exist.json")
	e.LoadState()
	if e.CountQuarantined() != 0 {
		t.Fatalf("expected 0 quarantined for missing file, got %d", e.CountQuarantined())
	}
}

// TestQuaranteedListSorted checks records list is sorted by tool name.
func TestQuarantineListSorted(t *testing.T) {
	e := New(0, 0)
	e.statePath = filepath.Join(t.TempDir(), "autorepair_state.json")
	e.Quarantine("zeta", "crash", "r", "e")
	e.Quarantine("alpha", "crash", "r", "e")
	recs := e.Quarantined()
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
	if recs[0].Tool != "alpha" {
		t.Fatalf("expected alpha first (sorted), got %s", recs[0].Tool)
	}
}