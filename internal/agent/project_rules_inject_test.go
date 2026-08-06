package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProjectRulesLoadedAtBoot verifies D1: the project's own rules file
// (AGENTS.md) is ingested into the agent at construction time, read-only.
func TestProjectRulesLoadedAtBoot(t *testing.T) {
	cfg := learningsTestEnv(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("always run go vet before commit"), 0o644)
	t.Chdir(dir)

	ag, err := New(cfg)
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if ag.projectRules == "" {
		t.Fatal("expected project rules loaded at boot")
	}
	if !strings.Contains(ag.projectRules, "go vet") {
		t.Errorf("unexpected project rules content: %q", ag.projectRules)
	}
	if !strings.HasSuffix(ag.projectRulesFile, "AGENTS.md") {
		t.Errorf("unexpected rules file: %q", ag.projectRulesFile)
	}
}

// TestBuildMessagesInjectsProjectRules verifies the rules system message is
// injected into the per-turn prompt alongside learnings.
func TestBuildMessagesInjectsProjectRules(t *testing.T) {
	cfg := learningsTestEnv(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("repo rule: always back up before editing"), 0o644)
	t.Chdir(dir)

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
		if strings.Contains(m.Content, "Project rules — apply when relevant") {
			found = true
			if !strings.Contains(m.Content, "repo rule") {
				t.Errorf("project rules message missing content:\n%s", m.Content)
			}
		}
	}
	if !found {
		t.Errorf("buildMessages did not inject project rules; got %d messages", len(msgs))
	}
}

// TestMissingRulesSilentSkip verifies the acceptance case: a directory with no
// rules file loads cleanly with no crash and no injection.
func TestMissingRulesSilentSkip(t *testing.T) {
	cfg := learningsTestEnv(t)
	t.Chdir(t.TempDir()) // empty project dir, no AGENTS.md etc.

	ag, err := New(cfg)
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if ag.projectRules != "" {
		t.Fatalf("want no project rules for empty dir, got %q", ag.projectRules)
	}
	msgs := ag.buildMessages("hello")
	for _, m := range msgs {
		if strings.Contains(m.Content, "Project rules — apply when relevant") {
			t.Fatalf("should not inject project rules when none exist:\n%s", m.Content)
		}
	}
}

// TestProjectRulesDisabled verifies cfg.Agent.ProjectRules=false skips ingest.
func TestProjectRulesDisabled(t *testing.T) {
	cfg := learningsTestEnv(t)
	cfg.Agent.ProjectRules = false
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("should not load"), 0o644)
	t.Chdir(dir)

	ag, err := New(cfg)
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	if ag.projectRules != "" {
		t.Fatalf("want no project rules when disabled, got %q", ag.projectRules)
	}
}

// TestProjectRulesHonorsMaxChars verifies a tighter per-instance cap clips the
// injected content (protects small local-model budgets).
func TestProjectRulesHonorsMaxChars(t *testing.T) {
	cfg := learningsTestEnv(t)
	cfg.Agent.ProjectRulesMaxChars = 64
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(strings.Repeat("x", 2000)), 0o644)
	t.Chdir(dir)

	ag, err := New(cfg)
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	msgs := ag.buildMessages("hello")
	for _, m := range msgs {
		if strings.Contains(m.Content, "Project rules — apply when relevant") {
			if strings.Contains(m.Content, strings.Repeat("x", 100)) {
				t.Errorf("project rules not capped by ProjectRulesMaxChars")
			}
			return
		}
	}
	t.Error("project rules message not found")
}