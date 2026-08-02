package agent

import (
	"os"
	"path/filepath"
	"testing"

	"eling/internal/config"
)

// TestSaveStatsRoundtrip verifies A5: SaveStats writes a stats.json snapshot
// that LoadStats can read back with tool + provider sections.
func TestSaveStatsRoundtrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Agent.Providers = nil
	cfg.Session.SaveDir = t.TempDir()
	ag, err := New(cfg)
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}

	if err := ag.SaveStats(); err != nil {
		t.Fatalf("SaveStats: %v", err)
	}

	home, _ := os.UserHomeDir()
	p := filepath.Join(home, ".eling", StatsFileName)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("stats file not written: %v", err)
	}

	rt := LoadStats()
	if rt == nil {
		t.Fatal("LoadStats returned nil after SaveStats")
	}
	if _, ok := rt["saved_at"]; !ok {
		t.Errorf("stats snapshot missing saved_at")
	}
	tool, ok := rt["tool"].(map[string]interface{})
	if !ok {
		t.Fatalf("stats snapshot missing tool section: %v", rt)
	}
	for _, k := range []string{"tool_calls", "tool_failures", "tool_success_rate", "tool_avg_latency_ms", "per_tool"} {
		if _, ok := tool[k]; !ok {
			t.Errorf("tool section missing %q", k)
		}
	}
	if _, ok := rt["provider"].(map[string]interface{}); !ok {
		t.Errorf("stats snapshot missing provider section: %v", rt)
	}
}

// TestLoadStatsMissingFile verifies LoadStats returns nil when no snapshot
// exists yet (fresh install / no interactive session run).
func TestLoadStatsMissingFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if rt := LoadStats(); rt != nil {
		t.Fatalf("LoadStats should be nil for missing file, got %v", rt)
	}
}

// TestStatsPathIsolation verifies StatsPath points under ~/.eling.
func TestStatsPathIsolation(t *testing.T) {
	t.Setenv("HOME", "/tmp/eling-test-home")
	want := filepath.Join("/tmp/eling-test-home", ".eling", StatsFileName)
	if got := StatsPath(); got != want {
		t.Fatalf("StatsPath = %q, want %q", got, want)
	}
}
