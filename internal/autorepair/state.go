// Package autorepair implements a decision-reasoning layer that observes tool
// failures, classifies them into breakage classes, and scores "repairability".
// Later phases act on that verdict (auto-fix / quarantine); Phase 0 only
// detects + classifies, recording for the dashboards without mutating anything.
package autorepair

import (
	"sync"
	"time"
)

// Class is the failure classification of a tool failure.
type Class int

const (
	// ClassTransient — network blip, timeout, rate limit. No code change needed;
	// handled by backoff + retry.
	ClassTransient Class = iota
	// ClassMissingDep — binary/engine/dependency missing (e.g. ocr not installed).
	ClassMissingDep
	// ClassConfigDrift — config/env/base_url wrong, MCP disabled, provider revoked.
	ClassConfigDrift
	// ClassContractViolation — tool returns malformed JSON / wrong schema.
	ClassContractViolation
	// ClassLogicBug — deterministic wrong output (rendering, parsing).
	ClassLogicBug
	// ClassCrash — panic / data-race / OOM.
	ClassCrash
	// ClassUnknown — could not classify.
	ClassUnknown
)

// String returns a human-friendly label for a Class.
func (c Class) String() string {
	switch c {
	case ClassTransient:
		return "transient"
	case ClassMissingDep:
		return "missing_dep"
	case ClassConfigDrift:
		return "config_drift"
	case ClassContractViolation:
		return "contract_violation"
	case ClassLogicBug:
		return "logic_bug"
	case ClassCrash:
		return "crash"
	default:
		return "unknown"
	}
}

// Verdict is the decision produced for a tool's broken state.
type Verdict int

const (
	// VerdictNotBroken — single/input failure; no action needed.
	VerdictNotBroken Verdict = iota
	// VerdictRetry — transient; retry with backoff, no mutation.
	VerdictRetry
	// VerdictAdvisory — classify + warn, but require manual action.
	VerdictAdvisory
	// VerdictAutoFix — repairable (Phase 1).
	VerdictAutoFix
	// VerdictQuarantine — disable tool + surface message (Phase 2).
	VerdictQuarantine
)

// String returns a human-friendly label for a Verdict.
func (v Verdict) String() string {
	switch v {
	case VerdictNotBroken:
		return "ok"
	case VerdictRetry:
		return "retry"
	case VerdictAdvisory:
		return "advisory"
	case VerdictAutoFix:
		return "autofix"
	case VerdictQuarantine:
		return "quarantine"
	default:
		return "unknown"
	}
}

// Failure is a single recorded tool execution failure prior to classification.
type Failure struct {
	Tool     string        `json:"tool"`
	Err      string        `json:"err"`
	Elapsed  time.Duration `json:"elapsed_ns"`
	Panicked bool          `json:"panicked"`
	At       time.Time     `json:"at"`
}

// ClassifiedFailure attaches a Class + Verdict + Reason to a Failure.
type ClassifiedFailure struct {
	Failure
	Class   Class   `json:"class"`
	Verdict Verdict `json:"verdict"`
	Reason  string  `json:"reason,omitempty"`
}

// recordedState is the per-tool rolling state for breaking detection.
type recordedState struct {
	mu       sync.Mutex
	failures []Failure
	lastErr  string
	repeated int // consecutive identical errors
}

// Engine is the top-level Phase-0 detector. It collects failures per tool and
// computes "is this tool broken, and does it need autofix?".
type Engine struct {
	mu        sync.RWMutex
	records   map[string]*recordedState
	window    time.Duration
	threshold int
}

// New returns a new Engine. window is the rolling window over which failures are
// counted (default 5m when zero); threshold is the number of repeated failures
// required to mark a tool broken (default 3 when zero).
func New(window time.Duration, threshold int) *Engine {
	if window <= 0 {
		window = 5 * time.Minute
	}
	if threshold <= 0 {
		threshold = 3
	}
	return &Engine{
		records:   make(map[string]*recordedState),
		window:    window,
		threshold: threshold,
	}
}

// Record registers one tool failure and returns the counted window for that tool.
// It is thread-safe and called from the registry hook.
func (e *Engine) Record(f Failure) []Failure {
	e.mu.Lock()
	rs := e.records[f.Tool]
	if rs == nil {
		rs = &recordedState{}
		e.records[f.Tool] = rs
	}
	e.mu.Unlock()

	rs.mu.Lock()
	defer rs.mu.Unlock()
	// prune entries outside the rolling window
	cutoff := f.At.Add(-e.window)
	kept := rs.failures[:0]
	for _, ff := range rs.failures {
		if ff.At.After(cutoff) {
			kept = append(kept, ff)
		}
	}
	kept = append(kept, f)
	rs.failures = kept

	// Track consecutive identical errors (non-input-dependent signal).
	if f.Err != "" && f.Err == rs.lastErr {
		rs.repeated++
	} else if f.Err != "" {
		rs.lastErr = f.Err
		rs.repeated = 1
	} else {
		rs.lastErr = ""
		rs.repeated = 0
	}
	return rs.failures
}

// Recent returns the in-window failures for a tool without mutating state.
func (e *Engine) Recent(name string) []Failure {
	e.mu.RLock()
	rs := e.records[name]
	e.mu.RUnlock()
	if rs == nil {
		return nil
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make([]Failure, len(rs.failures))
	copy(out, rs.failures)
	return out
}

// RepeatedCount returns the consecutive identical-error count for a tool
// (0 = no repeated identical error recorded).
func (e *Engine) Repeated(name string) int {
	e.mu.RLock()
	rs := e.records[name]
	e.mu.RUnlock()
	if rs == nil {
		return 0
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.repeated
}

// windowSize returns the configured rolling window.
func (e *Engine) windowSize() time.Duration { return e.window }

// thresholdN returns the configured breakage threshold.
func (e *Engine) thresholdN() int { return e.threshold }