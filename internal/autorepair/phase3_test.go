package autorepair

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// forceAutofixV3 escalates a tool to VerdictAutoFix using repeated identical
// missing-dep errors (same helper as Phase-1 tests; kept local for clarity).
func forceAutofixV3(t *testing.T, e *Engine, tool, msg string) {
	t.Helper()
	for i := 0; i < 3; i++ {
		e.judge(tool, msg, time.Millisecond, false)
	}
	if s := e.SnapshotOf(tool); s.Verdict != VerdictAutoFix {
		t.Fatalf("expected autofix verdict, got %v (repeated=%d)", s.Verdict, s.Repeated)
	}
}

// TestRepair_RetriesWithBackoff verifies the Phase-3 bounded retry loop: a fix
// that fails on the first attempt but succeeds on retry is applied, and the
// result reports how many attempts were used.
func TestRepair_RetriesWithBackoff(t *testing.T) {
	e := New(0, 3)
	e.backoffBase = time.Millisecond // keep the test fast
	attempts := 0
	e.RegisterFixer(Fixer{
		Tool:  "flakyfix",
		Class: ClassMissingDep,
		Summary: "flaky fix heals on retry",
		Probe: func() error {
			if attempts < 1 {
				return errors.New("still broken before any fix")
			}
			return nil
		},
		Fix: func() error {
			attempts++
			if attempts < 2 {
				return errors.New("first fix attempt fails (transient)")
			}
			return nil
		},
	})
	forceAutofixV3(t, e, "flakyfix", "exec: \"flakyfix\": executable file not found")

	e.SetAutofix(true)
	res := e.Repair("flakyfix")
	if !res.Tried {
		t.Fatalf("expected Tried=true, got false")
	}
	if !res.Fixed || !res.PostOK {
		t.Fatalf("expected fixed+post-ok after retry, got %+v", res)
	}
	if res.Attempts != 2 {
		t.Fatalf("expected Attempts=2 (first failed, second healed), got %d", res.Attempts)
	}
	if res.Verdict != VerdictAutoFix {
		t.Fatalf("successful retry should keep autofix verdict, got %v", res.VerdictLabel)
	}
}

// TestRepair_ExhaustsRetryBudget verifies the bounded retry loop gives up after
// maxRetries and surfaces an advisory instead of looping forever.
func TestRepair_ExhaustsRetryBudget(t *testing.T) {
	e := New(0, 3)
	e.backoffBase = time.Millisecond
	e.SetMaxRetries(2)
	fixCalls := 0
	e.RegisterFixer(Fixer{
		Tool:  "alwaysbroken",
		Class: ClassMissingDep,
		Summary: "fix never heals",
		Probe: func() error { return errors.New("binary still missing") },
		Fix: func() error {
			fixCalls++
			return nil // fix "succeeds" but probe stays unhealthy
		},
	})
	forceAutofixV3(t, e, "alwaysbroken", "exec: \"alwaysbroken\": executable file not found")

	e.SetAutofix(true)
	res := e.Repair("alwaysbroken")
	if !res.Tried {
		t.Fatalf("expected Tried=true, got false")
	}
	// Fix() returned nil (so Fixed=true) but the post-probe never healed —
	// the repair must be reported as NOT verified and escalate to advisory.
	if res.PostOK {
		t.Fatalf("post-probe must stay unhealthy after exhausting retries, got %+v", res)
	}
	if res.Attempts != 2 {
		t.Fatalf("expected Attempts=2 (max retries), got %d", res.Attempts)
	}
	if fixCalls != 2 {
		t.Fatalf("expected exactly 2 fix calls, got %d", fixCalls)
	}
	if res.Verdict != VerdictAdvisory {
		t.Fatalf("exhausted retries should be advisory, got %v", res.VerdictLabel)
	}
	if !strings.Contains(res.Message, "unhealthy after") {
		t.Fatalf("message should mention post-probe unhealthy after retries, got: %s", res.Message)
	}
}

// TestRepair_CommitGuardBlocksCodeMutation verifies a MutatesCode fixer is
// refused while the working tree is dirty, even with autofix on (Phase 3).
func TestRepair_CommitGuardBlocksCodeMutation(t *testing.T) {
	old := commitGuardCheck
	commitGuardCheck = func() (bool, error) { return true, nil } // dirty tree
	defer func() { commitGuardCheck = old }()

	e := New(0, 3)
	fixed := false
	e.RegisterFixer(Fixer{
		Tool:        "codemut",
		Class:       ClassMissingDep,
		Summary:     "patch a checked-in source file",
		MutatesCode: true,
		Probe:       func() error { return errors.New("missing") },
		Fix: func() error {
			fixed = true
			return nil
		},
	})
	forceAutofixV3(t, e, "codemut", "exec: \"codemut\": executable file not found")

	e.SetAutofix(true)
	res := e.Repair("codemut")
	if fixed {
		t.Fatal("commit guard must prevent the code-mutation fix from running")
	}
	if res.Tried {
		t.Fatalf("guard refusal should not count as a tried fix; Tried=%v", res.Tried)
	}
	if res.Verdict != VerdictAdvisory {
		t.Fatalf("commit-guard refusal should be advisory, got %v", res.VerdictLabel)
	}
	if !strings.Contains(res.Message, "commit guard") {
		t.Fatalf("message should mention the commit guard, got: %s", res.Message)
	}
}

// TestRepair_CommitGuardAllowsCleanTree verifies a MutatesCode fix proceeds
// when the working tree is clean.
func TestRepair_CommitGuardAllowsCleanTree(t *testing.T) {
	old := commitGuardCheck
	commitGuardCheck = func() (bool, error) { return false, nil } // clean tree
	defer func() { commitGuardCheck = old }()

	e := New(0, 3)
	e.backoffBase = time.Millisecond
	state := "broken"
	e.RegisterFixer(Fixer{
		Tool:        "codemut2",
		Class:       ClassMissingDep,
		Summary:     "patch source file (tree clean)",
		MutatesCode: true,
		Probe: func() error {
			if state == "broken" {
				return errors.New("missing")
			}
			return nil
		},
		Fix: func() error {
			state = "ok"
			return nil
		},
	})
	forceAutofixV3(t, e, "codemut2", "exec: \"codemut2\": executable file not found")

	e.SetAutofix(true)
	res := e.Repair("codemut2")
	if !res.Tried || !res.Fixed || !res.PostOK {
		t.Fatalf("clean-tree code fix should succeed, got %+v", res)
	}
}

// TestSanitizeUTF8 verifies invalid UTF-8 bytes are replaced so dashboards and
// persisted state never carry malformed encodings.
func TestSanitizeUTF8(t *testing.T) {
	bad := "exec: \xff\xfe invalid utf8"
	got := SanitizeUTF8(bad)
	if !strings.Contains(got, "\uFFFD") {
		t.Fatalf("expected replacement char in sanitized output, got %q", got)
	}
	if strings.Contains(got, "\xff") {
		t.Fatalf("raw invalid bytes must not survive sanitization: %q", got)
	}
	// Valid UTF-8 passes through untouched.
	if SanitizeUTF8("plain: émoticône ✓") != "plain: émoticône ✓" {
		t.Fatalf("valid UTF-8 must be unchanged")
	}
}

// TestRecordFailureSanitizesUnUTF8 verifies the registry funnel sanitizes
// non-UTF-8 error strings before recording (Phase 3 advisory logging).
func TestRecordFailureSanitizesUnUTF8(t *testing.T) {
	e := New(0, 3)
	ResetDefault(e)
	cf := RecordFailure("badtool", "boom \xff\xfe", time.Millisecond, false)
	if strings.Contains(cf.Err, "\xff") {
		t.Fatalf("error must be sanitized before recording, got %q", cf.Err)
	}
}

// TestBackoffExponential verifies the backoff sequence doubles and caps.
func TestBackoffExponential(t *testing.T) {
	e := New(0, 3)
	e.backoffBase = 100 * time.Millisecond
	if got := e.backoff(0); got != 100*time.Millisecond {
		t.Fatalf("attempt 0 backoff = %v, want 100ms", got)
	}
	if got := e.backoff(1); got != 200*time.Millisecond {
		t.Fatalf("attempt 1 backoff = %v, want 200ms", got)
	}
	if got := e.backoff(2); got != 400*time.Millisecond {
		t.Fatalf("attempt 2 backoff = %v, want 400ms", got)
	}
	// Large attempt numbers cap at 30s.
	if got := e.backoff(20); got != 30*time.Second {
		t.Fatalf("large backoff should cap at 30s, got %v", got)
	}
}

// TestSetMaxRetries verifies the retry budget setter and default.
func TestSetMaxRetries(t *testing.T) {
	e := New(0, 3)
	if e.MaxRetries() != 3 {
		t.Fatalf("default MaxRetries = %d, want 3", e.MaxRetries())
	}
	e.SetMaxRetries(5)
	if e.MaxRetries() != 5 {
		t.Fatalf("MaxRetries after set = %d, want 5", e.MaxRetries())
	}
	e.SetMaxRetries(0) // 0 → reset to default
	if e.MaxRetries() != 3 {
		t.Fatalf("MaxRetries after reset = %d, want 3", e.MaxRetries())
	}
}
