package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eling/internal/config"
)

// learningsTestEnv isolates ~/.eling state to a throwaway temp dir and
// returns a config that points sessions at another temp dir. Call this BEFORE
// seeding learnings or constructing the agent so nothing touches the real
// user home.
func learningsTestEnv(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Agent.Providers = nil
	cfg.Session.SaveDir = t.TempDir()
	return cfg
}

func seedLearnings(t *testing.T, entries ...string) {
	t.Helper()
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, ".eling", "learnings.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for _, e := range entries {
		sb.WriteString("- " + e + "\n")
	}
	if err := os.WriteFile(p, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLearningsLoadedAtBoot verifies A10: learnings from ~/.eling/learnings.md
// are loaded into the agent at construction time.
func TestLearningsLoadedAtBoot(t *testing.T) {
	cfg := learningsTestEnv(t)
	seedLearnings(t, "always back up before editing", "timeout tools after 30s")

	ag, err := New(cfg)
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if len(ag.learnings) != 2 {
		t.Fatalf("want 2 learnings loaded at boot, got %d: %v", len(ag.learnings), ag.learnings)
	}
	if ag.learnings[0] != "always back up before editing" {
		t.Errorf("unexpected first learning: %q", ag.learnings[0])
	}
}

// TestBuildMessagesInjectsLearnings verifies the learnings system message is
// injected into the per-turn prompt after the system prompt / summary.
func TestBuildMessagesInjectsLearnings(t *testing.T) {
	cfg := learningsTestEnv(t)
	seedLearnings(t, "lesson one", "lesson two")

	ag, err := New(cfg)
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	msgs := ag.buildMessages("hello")
	found := false
	for _, m := range msgs {
		if m.Role != "system" {
			continue
		}
		if strings.Contains(m.Content, "Durable learnings from past sessions") {
			found = true
			if !strings.Contains(m.Content, "- lesson one") || !strings.Contains(m.Content, "- lesson two") {
				t.Errorf("learnings message missing entries:\n%s", m.Content)
			}
		}
	}
	if !found {
		t.Errorf("buildMessages did not inject learnings; got %d messages", len(msgs))
	}
}

// TestLearnRefreshesInMemory verifies Learn() persists to the journal and
// immediately makes the lesson available for injection.
func TestLearnRefreshesInMemory(t *testing.T) {
	cfg := learningsTestEnv(t)
	ag, err := New(cfg)
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if len(ag.learnings) != 0 {
		t.Fatalf("want empty learnings at start, got %d", len(ag.learnings))
	}

	if err := ag.Learn("never delete without a backup"); err != nil {
		t.Fatalf("Learn: %v", err)
	}
	if len(ag.learnings) != 1 || ag.learnings[0] != "never delete without a backup" {
		t.Fatalf("learnings not refreshed in memory: %v", ag.learnings)
	}

	// Journal file must contain the timestamped entry.
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(filepath.Join(home, ".eling", "learnings.md"))
	if err != nil {
		t.Fatalf("journal not written: %v", err)
	}
	if !strings.Contains(string(data), "never delete without a backup") {
		t.Errorf("journal missing entry:\n%s", data)
	}
}

// TestGetStatsIncludesLearnings verifies the /stats surface exposes the
// learnings count.
func TestGetStatsIncludesLearnings(t *testing.T) {
	cfg := learningsTestEnv(t)
	seedLearnings(t, "one")
	ag, err := New(cfg)
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	stats := ag.GetStats()
	if v, ok := stats["learnings"]; !ok || v != 1 {
		t.Fatalf("GetStats missing learnings count: got %v, want 1", stats["learnings"])
	}
}
