package autorepair

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// QuarantineRecord is a persisted, user-facing record of one tool that was
// disabled because it was judged broken in a way that cannot be safely auto
// repaired ("quarantine"). It ships a clear reason + timestamp so the TUI and
// CLI can surface "Tool X disabled due to ..." and so a human can re-enable it.
type QuarantineRecord struct {
	Tool       string `json:"tool"`
	ClassLabel string `json:"class_label,omitempty"`
	Reason     string `json:"reason"`
	LastError  string `json:"last_error,omitempty"`
	DisabledAt string `json:"disabled_at"`
}

// stateFile is the on-disk schema for autorepair_state.json.
type stateFile struct {
	Quarantined map[string]QuarantineRecord `json:"quarantined"`
	SavedAt     string                      `json:"saved_at"`
}

// defaultStatePath is the process-wide persistence path for quarantine records.
func defaultStatePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".eling", "autorepair_state.json")
}

// LoadState reads quarantine records from e.statePath into the engine. It is
// idempotent and safe to call any number of times; missing/unreadable files are
// treated as an empty state (never an error that halts the agent).
func (e *Engine) LoadState() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stateLoaded {
		return
	}
	e.stateLoaded = true
	if e.statePath == "" {
		return
	}
	data, err := os.ReadFile(e.statePath)
	if err != nil {
		return
	}
	var sf stateFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return
	}
	e.quarantines = sf.Quarantined
}

// Persist writes the current quarantine records to e.statePath atomically
// (temp-file + rename) so a crash mid-write never corrupts the state file.
func (e *Engine) Persist() error {
	e.mu.RLock()
	sf := stateFile{Quarantined: e.quarantines, SavedAt: time.Now().Format(time.RFC3339)}
	e.mu.RUnlock()
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return err
	}
	path := e.statePath
	if path == "" {
		path = defaultStatePath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Quarantine is the Quarantine action: records a tool as disabled (the actual
// registry disable is done by the caller — the registry hook) and persists the
// reason so the TUI/CLI can surface it and a human can re-enable later.
func (e *Engine) Quarantine(tool, classLabel, reason, lastErr string) {
	if tool == "" {
		return
	}
	e.mu.Lock()
	e.quarantines[tool] = QuarantineRecord{
		Tool:       tool,
		ClassLabel: classLabel,
		Reason:     reason,
		LastError:  lastErr,
		DisabledAt: time.Now().Format(time.RFC3339),
	}
	e.mu.Unlock()
	_ = e.Persist()
}

// IsQuarantined reports whether the given tool is currently quarantined.
func (e *Engine) IsQuarantined(tool string) bool {
	e.LoadState()
	e.mu.RLock()
	defer e.mu.RUnlock()
	_, ok := e.quarantines[tool]
	return ok
}

// Reenable removes a tool from the quarantine set, clearing its persistent
// record so the registry can offer it again on the next session. It is the
// manual re-enable path (CLI: `eling autorepair reenable <tool>` / agent).
func (e *Engine) Reenable(tool string) bool {
	e.LoadState()
	e.mu.Lock()
	_, ok := e.quarantines[tool]
	if ok {
		delete(e.quarantines, tool)
	}
	e.mu.Unlock()
	if ok {
		_ = e.Persist()
	}
	return ok
}

// Quarantined returns the list of quarantine records sorted by tool name.
func (e *Engine) Quarantined() []QuarantineRecord {
	e.LoadState()
	e.mu.RLock()
	recs := make([]QuarantineRecord, 0, len(e.quarantines))
	for _, r := range e.quarantines {
		recs = append(recs, r)
	}
	e.mu.RUnlock()
	sort.Slice(recs, func(i, j int) bool { return recs[i].Tool < recs[j].Tool })
	return recs
}

// CountQuarantined returns how many tools are currently quarantined (0 → a
// clean bill of health for the TUI indicator).
func (e *Engine) CountQuarantined() int {
	e.LoadState()
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.quarantines)
}