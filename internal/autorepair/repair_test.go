package autorepair

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// forceAutofix escalates a tool to VerdictAutoFix (repeated missing-dep errors)
// using an in-memory engine so Repair() can be exercised without touching PATH.
func forceAutofix(t *testing.T, e *Engine, tool, msg string) {
	t.Helper()
	for i := 0; i < 3; i++ {
		e.judge(tool, msg, time.Millisecond, false)
	}
	s := e.SnapshotOf(tool)
	if s.Verdict != VerdictAutoFix {
		t.Fatalf("expected autofix verdict, got %v (repeated=%d)", s.Verdict, s.Repeated)
	}
}

// TestRepair_AutofixOffIsAdvisory verifies that with autofix disabled (the
// Phase-1 default) Repair() reports an advisory and NEVER mutates, even when a
// fixer is registered and the probe would fail.
func TestRepair_AutofixOffIsAdvisory(t *testing.T) {
	e := New(0, 3)
	// Register a wildcard fixer for missing deps whose fix marks a flag.
	fixed := false
	e.RegisterFixer(Fixer{
		Class:   ClassMissingDep,
		Summary: "test fixer",
		Probe:   func() error { return errors.New("still missing") },
		Fix: func() error {
			fixed = true
			return nil
		},
	})

	forceAutofix(t, e, "ocrtest", "exec: \"ocrtest\": executable file not found")

	if e.AutofixEnabled() {
		t.Fatal("autofix must be OFF by default")
	}
	res := e.Repair("ocrtest")
	if res.Tried {
		t.Fatalf("repair must not be attempted when autofix off; Tried=%v", res.Tried)
	}
	if res.Fixed {
		t.Fatal("Repair must not run Fix() when autofix off")
	}
	if fixed {
		t.Fatal("Fix() was executed despite autofix being off")
	}
	if res.Verdict != VerdictAdvisory {
		t.Fatalf("expected advisory verdict when autofix off, got %v", res.VerdictLabel)
	}
	if !strings.Contains(res.Message, "DISABLED") {
		t.Fatalf("message should mention autofix disabled, got: %s", res.Message)
	}
}

// TestRepair_ProbeFirstVerifies verifies the Probe-before gate: if the tool is
// already healthy, no fix is run.
func TestRepair_ProbeFirstVerifies(t *testing.T) {
	e := New(0, 3)
	fixed := false
	e.RegisterFixer(Fixer{
		Tool:    "healthytool",
		Class:   ClassMissingDep,
		Summary: "healthy already",
		Probe:   func() error { return nil }, // already healthy
		Fix: func() error {
			fixed = true
			return nil
		},
	})
	forceAutofix(t, e, "healthytool", "exec: \"healthytool\": executable file not found")

	e.SetAutofix(true)
	res := e.Repair("healthytool")
	if fixed {
		t.Fatal("fix ran but probe said already healthy")
	}
	if !res.ProbeOK || !res.PostOK {
		t.Fatalf("expected pre/post probe ok, got %+v", res)
	}
	if res.Message != "already healthy; no fix required" {
		t.Fatalf("unexpected message: %s", res.Message)
	}
}

// TestRepair_AutofixOnAppliesAndVerifies runs the full probe→fix→post-probe
// cycle with autofix enabled and a fix that actually heals the probe.
func TestRepair_AutofixOnAppliesAndVerifies(t *testing.T) {
	e := New(0, 3)
	state := "broken"
	e.RegisterFixer(Fixer{
		Tool:  "fixme",
		Class: ClassMissingDep,
		Summary: "heal probe",
		Probe: func() error {
			if state == "broken" {
				return errors.New("binary missing")
			}
			return nil
		},
		Fix: func() error {
			if state != "broken" {
				return errors.New("idempotency violated: fix on healthy tool")
			}
			state = "ok"
			return nil
		},
	})
	forceAutofix(t, e, "fixme", "exec: \"fixme\": executable file not found")

	e.SetAutofix(true)
	res := e.Repair("fixme")
	if !res.Tried || !res.Fixed || !res.PostOK {
		t.Fatalf("expected full repair success, got %+v", res)
	}
	if res.Verdict != VerdictAutoFix {
		t.Fatalf("after successful repair verdict should stay autofix, got %v", res.VerdictLabel)
	}
}

// TestRepair_DestructiveNeverAutoRuns verifies destructive fixers surface as
// advisory even with autofix enabled — a human must decide.
func TestRepair_DestructiveNeverAutoRuns(t *testing.T) {
	e := New(0, 3)
	fixed := false
	e.RegisterFixer(Fixer{
		Tool:        "destructive",
		Class:       ClassConfigDrift,
		Summary:     "rewrite config",
		Destructive: true,
		Probe:       func() error { return errors.New("drifted") },
		Fix: func() error {
			fixed = true
			return nil
		},
	})
	forceAutofix(t, e, "destructive", "provider base_url drifted")

	e.SetAutofix(true)
	res := e.Repair("destructive")
	if fixed {
		t.Fatal("destructive fix must not be auto-run")
	}
	if res.Verdict != VerdictAdvisory {
		t.Fatalf("destructive fix should be advisory, got %v", res.VerdictLabel)
	}
	if !strings.Contains(res.Message, "destructive") {
		t.Fatalf("message should flag destructive, got=%s", res.Message)
	}
}

// TestRepair_FixerExactBeatsWildcard verifies a tool-specific recipe wins over
// a wildcard ("" Tool) recipe for the same class.
func TestRepair_FixerExactBeatsWildcard(t *testing.T) {
	e := New(0, 3)
	e.RegisterFixer(Fixer{Tool: "abc", Class: ClassMissingDep, Summary: "exact", Probe: func() error { return errors.New("x") }, Fix: func() error { return nil }})
	e.RegisterFixer(Fixer{Tool: "", Class: ClassMissingDep, Summary: "wildcard", Probe: func() error { return errors.New("x") }, Fix: func() error { return nil }})

	f, ok := e.fixerFor("abc", ClassMissingDep)
	if !ok || f.Summary != "exact" {
		t.Fatalf("expected exact match to win, got summary=%q ok=%v", f.Summary, ok)
	}
}

// TestRepair_NoFixerIsAdvisory verifies a repeated broken tool with no known
// recipe downgrades to advisory (manual review) instead of failing silently.
func TestRepair_NoFixerIsAdvisory(t *testing.T) {
	e := New(0, 3)
	forceAutofix(t, e, "mtool", "exec: \"mtool\": executable file not found")
	e.SetAutofix(true)
	res := e.Repair("mtool")
	if res.Verdict != VerdictAdvisory {
		t.Fatalf("no-recipe repair should be advisory, got %v", res.VerdictLabel)
	}
	if !strings.Contains(res.Message, "no known fix") {
		t.Fatalf("message should indicate no known fix, got=%s", res.Message)
	}
}

// TestRepairabilityScoring verifies the weighted score degrades for destructive
// recipes (no naive all-destructive-autofix).
func TestRepairabilityScoring(t *testing.T) {
	safe := repairability(Fixer{Destructive: false})
	risky := repairability(Fixer{Destructive: true})
	if !(safe > risky) {
		t.Fatalf("non-destructive should outscore destructive: safe=%.2f risky=%.2f", safe, risky)
	}
}

// TestSetAutofixToggles verifies the opt-in gate flips and resets.
func TestSetAutofixToggles(t *testing.T) {
	e := New(0, 0)
	if e.AutofixEnabled() {
		t.Fatal("must default to off")
	}
	e.SetAutofix(true)
	if !e.AutofixEnabled() {
		t.Fatal("expected autofix on after SetAutofix(true)")
	}
	e.SetAutofix(false)
	if e.AutofixEnabled() {
		t.Fatal("expected autofix off after SetAutofix(false)")
	}
}