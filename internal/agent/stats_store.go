package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// StatsFileName is the persisted runtime-metrics snapshot written on graceful
// shutdown and read by the standalone `eling stats` CLI (A5). It lets tool
// call / provider metrics accumulated during a live session survive the
// process and be inspected later.
const StatsFileName = "stats.json"

// StatsPath returns the stats snapshot path under ~/.eling.
func StatsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".eling", StatsFileName)
}

// SaveStats snapshots the live tool-registry and per-provider metrics to
// ~/.eling/stats.json so `eling stats` can display them across processes.
// Safe to call concurrently with tool execution (registry + provider stats
// are internally locked).
func (a *Agent) SaveStats() error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	data := map[string]interface{}{
		"saved_at": time.Now().Format(time.RFC3339),
		"tool":     a.ToolRegistry.Stats(),
		"provider": a.providerStatsSnapshot(),
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(StatsPath()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(StatsPath(), b, 0o644)
}

// LoadStats reads the persisted runtime-metrics snapshot written by the last
// graceful shutdown. Returns nil when the file is missing or unreadable so
// callers can fall back to an empty view.
func LoadStats() map[string]interface{} {
	data, err := os.ReadFile(StatsPath())
	if err != nil {
		return nil
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out
}
