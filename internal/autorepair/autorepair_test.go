package autorepair

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// TestClassifyKnownSignals checks the core classifier maps error strings to the
// expected breakage classes.
func TestClassifyKnownSignals(t *testing.T) {
	cases := []struct {
		err    string
		panic  bool
		expect Class
	}{
		{"timed out after 5m0s", false, ClassTransient},
		{"too many requests", false, ClassTransient},
		{"exec: \"ocr\": executable file not found", false, ClassMissingDep},
		{"command not found: ugrep", false, ClassMissingDep},
		{"provider 'qwen' base_url is misconfigured", false, ClassConfigDrift},
		{"401 unauthorized: invalid api key", false, ClassConfigDrift},
		{"json: cannot unmarshal object into map", false, ClassContractViolation},
		{"unexpected end of JSON input", false, ClassContractViolation},
		{"index out of range [4] with length 2", false, ClassLogicBug},
		{"some unrelated business error", false, ClassUnknown},
	}
	for _, c := range cases {
		got := classifyClass(c.err, c.panic, 0)
		if got != c.expect {
			t.Errorf("classifyClass(%q) = %v, want %v", c.err, got, c.expect)
		}
	}
	// A panic always maps to crash regardless of the error string.
	if got := classifyClass("anything", true, 0); got != ClassCrash {
		t.Errorf("panic should classify as crash, got %v", got)
	}
}

// TestSingleFailureIsNotBroken verifies a lone failure never escalates to
// autofix/quarantine — the core "broken or not" guarantee.
func TestSingleFailureIsNotBroken(t *testing.T) {
	e := New(5*time.Minute, 3)
	cf := e.judge("web_fetch", "timed out after 5m", 9*time.Second, false)
	if cf.Verdict != VerdictRetry {
		t.Fatalf("transient single failure = %v, want retry", cf.Verdict)
	}

	// A single missing-dep failure is "observing", not autofix yet.
	cf = e.judge("ocr", "exec: \"ocr\": executable file not found", time.Second, false)
	if cf.Verdict == VerdictAutoFix || cf.Verdict == VerdictQuarantine {
		t.Fatalf("single missing-dep must not be autofixed, got %v", cf.Verdict)
	}
}

// TestRepeatedFailureEscalates verifies the repeated-threshold escalates a
// missing-dep failure to autofix only after the N=3 identical error signal.
func TestRepeatedFailureEscalates(t *testing.T) {
	e := New(5*time.Minute, 3)
	msg := "exec: \"ocr\": executable file not found"
	for i := 1; i <= 2; i++ {
		e.judge("ocr", msg, time.Second, false)
		snap := e.SnapshotOf("ocr")
		if snap.Verdict == VerdictAutoFix {
			t.Fatalf("escalated too early at failure %d", i)
		}
	}
	// third same error → repeated>=threshold → autofix verdict
	e.judge("ocr", msg, time.Second, false)
	snap := e.SnapshotOf("ocr")
	if snap.Verdict != VerdictAutoFix {
		t.Fatalf("expected autofix after threshold, got %v (repeated=%d)", snap.Verdict, snap.Repeated)
	}
}

// TestCrashAlwaysQuarantines verifies a panic is quarantined even once.
func TestCrashAlwaysQuarantines(t *testing.T) {
	e := New(0, 0)
	cf := e.judge("bash", "tool \"bash\" panicked: index out of range", time.Millisecond, true)
	if cf.Class != ClassCrash {
		t.Fatalf("class=%v want crash", cf.Class)
	}
	if cf.Verdict != VerdictQuarantine {
		t.Fatalf("verdict=%v want quarantine", cf.Verdict)
	}
}

// TestDashboardAndDisabled lists tools and surfaces quarantined ones.
func TestDashboardAndDisabled(t *testing.T) {
	e := New(0, 3)
	e.judge("ocr", "exec: \"ocr\": executable file not found", time.Second, false)
	e.judge("ocr", "exec: \"ocr\": executable file not found", time.Second, false)
	e.judge("ocr", "exec: \"ocr\": executable file not found", time.Second, false)
	e.judge("wf", "connection reset", time.Second, false)

	_, sum := e.Dashboard()
	if sum.TotalTracked != 2 {
		t.Fatalf("tracked=%d want 2", sum.TotalTracked)
	}
	if sum.Broken != 1 {
		t.Fatalf("broken=%d want 1 (ocr)", sum.Broken)
	}
	// a panic would quarantine; ensure that path lists the tool
	e.judge("crashy", "panic", time.Millisecond, true)
	disabled := e.DisabledTools()
	if len(disabled) != 1 || disabled[0] != "crashy" {
		t.Fatalf("disabled=%v want [crashy]", disabled)
	}
}

// TestConcurrentRecord verifies the engine is safe under concurrent writes.
func TestConcurrentRecord(t *testing.T) {
	e := New(0, 3)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			e.judge("t", "boom", time.Millisecond, n%5 == 0)
		}(i)
	}
	wg.Wait()
	rows, _ := e.Dashboard()
	if len(rows) != 1 {
		t.Fatalf("tracked=%d want 1", len(rows))
	}
	if s := e.SnapshotOf("t"); strings.TrimSpace(s.LastErr) == "" {
		t.Log("snapshot ok")
	}
}

// TestNewDefaults verifies default window/threshold are applied.
func TestNewDefaults(t *testing.T) {
	e := New(0, 0)
	if e.windowSize() != 5*time.Minute {
		t.Fatalf("window=%v", e.windowSize())
	}
	if e.thresholdN() != 3 {
		t.Fatalf("threshold=%d", e.thresholdN())
	}
}