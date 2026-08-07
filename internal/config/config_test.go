package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultPromptContainsAtomicCommitDiscipline guards D7: the default
// system prompt must carry the atomic-commit discipline as first-class
// instruction text, so the habit applies on arbitrary projects, not just
// inside the heist workflow. If someone trims the prompt and drops the rule,
// this test fails — the discipline silently degrades otherwise.
func TestDefaultPromptContainsAtomicCommitDiscipline(t *testing.T) {
	prompt := DefaultConfig().Agent.SystemPrompt

	for _, want := range []string{
		"ATOMIC COMMIT DISCIPLINE",
		"plan",
		"ONE logical change",
		"go build",
		"go vet",
		"go test",
		"conventional message",
		"feat:",
		"Never batch unrelated changes",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("default system prompt missing atomic-commit discipline fragment %q", want)
		}
	}
}

// TestDefaultPromptKeepsSearchRule ensures the D7 edit appended to the prompt
// rather than replacing the SEARCH RULE — the two rules coexist.
func TestDefaultPromptKeepsSearchRule(t *testing.T) {
	prompt := DefaultConfig().Agent.SystemPrompt
	if !strings.Contains(prompt, "SEARCH RULE") {
		t.Error("SEARCH RULE was dropped from the default system prompt")
	}
	if !strings.Contains(prompt, "ugrep") {
		t.Error("ugrep mandate was dropped from the default system prompt")
	}
}

// TestAutomateConfigRoundTrip guards D4: automation jobs added via
// `eling automate add` persist and survive a Load/Save round trip, including
// their LastRun/LastStatus bookkeeping fields.
func TestAutomateConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := DefaultConfig()
	cfg.Automate.Enabled = true
	cfg.Automate.Jobs = []AutomationJob{
		{Name: "nightly", Command: "go test ./...", Schedule: "0 2 * * *", Enabled: true},
		{Name: "digest", Goal: "Summarize yesterday's learnings", Schedule: "0 3 * * 1", Enabled: false},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !got.Automate.Enabled {
		t.Error("Automate.Enabled lost on round trip")
	}
	if len(got.Automate.Jobs) != 2 {
		t.Fatalf("jobs = %d, want 2", len(got.Automate.Jobs))
	}
	if got.Automate.Jobs[0].Name != "nightly" || got.Automate.Jobs[0].Schedule != "0 2 * * *" {
		t.Errorf("job0 = %+v", got.Automate.Jobs[0])
	}
	if got.Automate.Jobs[1].Goal == "" || got.Automate.Jobs[1].Enabled {
		t.Errorf("job1 = %+v", got.Automate.Jobs[1])
	}

	// Backwards compatibility: a config file without an `automate:` key must
	// load with the defaults (scheduler off, no jobs).
	old := "agent:\n  system_prompt: \"x\"\n"
	oldPath := filepath.Join(dir, "old.yaml")
	if err := os.WriteFile(oldPath, []byte(old), 0600); err != nil {
		t.Fatalf("write old: %v", err)
	}
	oldCfg, err := Load(oldPath)
	if err != nil {
		t.Fatalf("load old: %v", err)
	}
	if oldCfg.Automate.Enabled {
		t.Error("old config without automate: should default to disabled")
	}
	if len(oldCfg.Automate.Jobs) != 0 {
		t.Errorf("old config without automate: jobs = %d, want 0", len(oldCfg.Automate.Jobs))
	}
}

// TestSessionBudgetDefaultsAllZero guards that the session budget knobs
// (max_duration_sec / max_turns / idle_timeout_sec) default to off, so a
// fresh install behaves exactly as before — the budget is strictly opt-in.
func TestSessionBudgetDefaultsAllZero(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Session.MaxDurationSec != 0 {
		t.Errorf("MaxDurationSec = %d, want 0 (off)", cfg.Session.MaxDurationSec)
	}
	if cfg.Session.MaxTurns != 0 {
		t.Errorf("MaxTurns = %d, want 0 (off)", cfg.Session.MaxTurns)
	}
	if cfg.Session.IdleTimeoutSec != 0 {
		t.Errorf("IdleTimeoutSec = %d, want 0 (off)", cfg.Session.IdleTimeoutSec)
	}
}

// TestSessionBudgetRoundTrip guards the three new keys persist through a
// Load/Save round trip, and that an old config file without the keys loads
// with all-zero (off) budgets — no migration needed.
func TestSessionBudgetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := DefaultConfig()
	cfg.Session.MaxDurationSec = 3600
	cfg.Session.MaxTurns = 100
	cfg.Session.IdleTimeoutSec = 600
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Session.MaxDurationSec != 3600 {
		t.Errorf("MaxDurationSec = %d, want 3600", got.Session.MaxDurationSec)
	}
	if got.Session.MaxTurns != 100 {
		t.Errorf("MaxTurns = %d, want 100", got.Session.MaxTurns)
	}
	if got.Session.IdleTimeoutSec != 600 {
		t.Errorf("IdleTimeoutSec = %d, want 600", got.Session.IdleTimeoutSec)
	}

	// Backwards compatibility: a config file without session budget keys must
	// load with all-zero (off) budgets.
	old := "session:\n  auto_save: true\n  save_dir: \"/tmp/x\"\n"
	oldPath := filepath.Join(dir, "old.yaml")
	if err := os.WriteFile(oldPath, []byte(old), 0600); err != nil {
		t.Fatalf("write old: %v", err)
	}
	oldCfg, err := Load(oldPath)
	if err != nil {
		t.Fatalf("load old: %v", err)
	}
	if oldCfg.Session.MaxDurationSec != 0 || oldCfg.Session.MaxTurns != 0 || oldCfg.Session.IdleTimeoutSec != 0 {
		t.Errorf("old config without budget keys: got %+v, want all-zero", oldCfg.Session)
	}
}

// TestGuardrailsConfigInertByDefault pins P3: a fresh install must have the
// Guardrails block fully inert (audit=false, strict=false) so the white-box
// scaffolding never changes behavior unless explicitly enabled.
func TestGuardrailsConfigInertByDefault(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Guardrails.Audit {
		t.Fatal("Guardrails.Audit must default to false (inert)")
	}
	if cfg.Guardrails.Strict {
		t.Fatal("Guardrails.Strict must default to false (inert)")
	}
	if cfg.Guardrails.Active() {
		t.Fatal("Guardrails.Active() must be false on a fresh install")
	}
}

// TestGuardrailsConfigActiveFlags verifies Active() reflects either knob.
func TestGuardrailsConfigActiveFlags(t *testing.T) {
	g := GuardrailsConfig{Audit: true}
	if !g.Active() {
		t.Error("Audit-only block must be Active")
	}
	g = GuardrailsConfig{Strict: true}
	if !g.Active() {
		t.Error("Strict-only block must be Active")
	}
	g = GuardrailsConfig{}
	if g.Active() {
		t.Error("zero block must be inactive")
	}
}
