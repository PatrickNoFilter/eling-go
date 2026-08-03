package autorepair

import (
	"fmt"
	"strings"
)

// Fixer is a single, idempotent, probe-first repair recipe for one tool
// failure class. Every fix is gated by a Probe() that must be run BEFORE and
// AFTER the Fix() so we never mutate a tool that is already healthy, and we
// only claim a fix worked if the post-probe is green.
//
// Phase 1 ships these recipes for the three safest classes (MissingBinary,
// ConfigDrift, Env) but does NOT auto-run them: `autofix` is off by default,
// so Repair() reports the plan + advisory unless enabled via SetAutofix(true)
// (wired in Phase 3).
type Fixer struct {
	Tool        string // tool name the recipe applies to ("" = applies to any tool of Class)
	Class       Class  // failure class this recipe fixes
	Summary     string // human description of what the fix does
	Probe       func() error // returns nil when the tool is already healthy
	Fix         func() error // idempotent repair; must be safe to retry
	Destructive bool // true when the fix mutates user data / system config
}

// repairabilityCredits scores a Fixer 0..1 per the plan's weighted formula:
//   +0.5  has a known fix
//   +0.2  fix is idempotent + testable (always true here by Probe/Fix contract)
//   +0.2  non-destructive (no data deletion)
//   +0.1  low cost / low risk (package install vs editing production config)
func repairability(f Fixer) float64 {
	score := 0.5 + 0.2 // known fix + idempotent/testable
	if !f.Destructive {
		score += 0.2 // non-destructive
	}
	// cost term: destructive recipes already lose the 0.2; give small credit
	score += 0.1
	return score
}

// RepairResult is the outcome of one Repair() attempt.
type RepairResult struct {
	Tool       string `json:"tool"`
	Class      Class  `json:"class"`
	ClassLabel string `json:"class_label"`
	Verdict    Verdict `json:"verdict"`
	VerdictLabel string `json:"verdict_label"`
	Tried      bool   `json:"tried"`
	AutofixOn  bool   `json:"autofix_on"`
	ProbeOK    bool   `json:"probe_ok"`
	Fixed      bool   `json:"fixed"`
	PostOK     bool   `json:"post_ok"`
	Message    string `json:"message"`
}

// RegisterFixer adds a recipe to the engine's knowledge table.
func (e *Engine) RegisterFixer(f Fixer) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if f.Probe == nil {
		f.Probe = func() error { return nil }
	}
	if f.Fix == nil {
		f.Fix = func() error { return fmt.Errorf("no fix defined") }
	}
	e.fixers = append(e.fixers, f)
}

// RegisterFixers bulk-registers a slice.
func (e *Engine) RegisterFixers(fs []Fixer) {
	for _, f := range fs {
		e.RegisterFixer(f)
	}
}

// SetAutofix toggles whether Repair() actually mutates anything. Phase 1 keeps
// this off by default (opt-in). With autofix off, Repair still probes and
// reports a Partial Advisory but never runs a Fix.
func (e *Engine) SetAutofix(on bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.autofixOn = on
}

// AutofixEnabled reports the current autofix gate.
func (e *Engine) AutofixEnabled() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.autofixOn
}

// Fixers returns a copy of the registered fixer table.
func (e *Engine) Fixers() []Fixer {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Fixer, len(e.fixers))
	copy(out, e.fixers)
	return out
}

// fixerFor locates the best recipe for a tool+class. A tool-specific fixer
// wins over a wildcard (Tool=="").
func (e *Engine) fixerFor(tool string, class Class) (Fixer, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var wildcard *Fixer
	for i := range e.fixers {
		f := &e.fixers[i]
		if f.Class != class {
			continue
		}
		if f.Tool == tool {
			return *f, true
		}
		if f.Tool == "" {
			// remember wildcard, keep scanning for an exact match
			if wildcard == nil {
				wildcard = f
			}
		}
	}
	if wildcard != nil {
		return *wildcard, true
	}
	return Fixer{}, false
}

// Repair is the Phase-1 action entry point. Given a tool that the detector
// flagged AutoFix, it looks up a fixer and, if autofix is enabled and the
// fixer is non-destructive, runs it probe-first. Otherwise it returns the
// plan + advisory without mutating anything.
func (e *Engine) Repair(tool string) RepairResult {
	snap := e.SnapshotOf(tool)

	out := RepairResult{
		Tool:         tool,
		Class:        snap.Class,
		ClassLabel:   snap.ClassLabel,
		Verdict:      snap.Verdict,
		VerdictLabel: snap.VerdictLabel,
	}

	// Only tools the detector says AutoFix are actionable this phase.
	if snap.Verdict != VerdictAutoFix {
		out.Message = fmt.Sprintf("no autofix needed (verdict=%s)", snap.VerdictLabel)
		return out
	}

	fixer, ok := e.fixerFor(tool, snap.Class)
	if !ok {
		out.Message = "no known fix recipe; manual review (advisory)"
		out.Verdict = VerdictAdvisory
		out.VerdictLabel = out.Verdict.String()
		return out
	}

	out.AutofixOn = e.AutofixEnabled()
	on := out.AutofixOn

	// Destructive recipes are never auto-run, even with autofix on; surface as
	// advisory so a human decides.
	if fixer.Destructive {
		out.Message = "fix is destructive (" + fixer.Summary + "); advisory, manual review"
		out.Verdict = VerdictAdvisory
		out.VerdictLabel = out.Verdict.String()
		return out
	}

	// Probe before — if already healthy, nothing to do.
	if err := fixer.Probe(); err == nil {
		out.ProbeOK = true
		out.PostOK = true
		out.Message = "already healthy; no fix required"
		return out
	}

	if !on {
		out.Message = fmt.Sprintf("autofix detected (%s) but DISABLED; enable autorepair.autofix to apply", fixer.Summary)
		out.Verdict = VerdictAdvisory
		out.VerdictLabel = out.Verdict.String()
		return out
	}

	// We have autofix on and the tool is unhealthy: this is the only path that
	// actually attempts a fix, so Tried is set here (not before the gate).
	out.Tried = true

	if err := fixer.Fix(); err != nil {
		out.Message = fmt.Sprintf("fix failed: %v", err)
		out.Verdict = VerdictAdvisory
		out.VerdictLabel = out.Verdict.String()
		return out
	}
	out.Fixed = true

	// Probe after — the fix only counts if the tool is now healthy.
	if err := fixer.Probe(); err != nil {
		out.Message = "fix applied but post-probe still unhealthy: " + err.Error()
		out.Verdict = VerdictAdvisory
		out.VerdictLabel = out.Verdict.String()
		return out
	}
	out.PostOK = true
	out.Message = "fix applied and verified (post-probe ok)"
	return out
}

// RepairAll attempts Repair for every tool on the dashboard, returning the
// individual results.
func (e *Engine) RepairAll() []RepairResult {
	rows, _ := e.Dashboard()
	out := make([]RepairResult, 0, len(rows))
	for _, s := range rows {
		out = append(out, e.Repair(s.Tool))
	}
	return out
}

// SummaryLines renders Repair results as a compact multi-line description used
// by CLI "autorepair" and TUI status.
func SummaryLines(results []RepairResult) string {
	var sb strings.Builder
	for _, r := range results {
		fmt.Fprintf(&sb, "%-20s %-12s %-9s %-30s\n", r.Tool, r.ClassLabel, r.VerdictLabel, Compact(r.Message, 44))
	}
	return sb.String()
}

// Compact truncates a string to n runes with an ellipsis.
func Compact(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}