package autorepair

import (
	"sort"
	"strings"
	"time"
)

// JudgeError records a failure and returns its verdict in one shot. This is the
// single funnel the registry hook calls (Phase 0). It never mutates the
// environment; it only records and classifies.
func (e *Engine) judge(name, errMsg string, elapsed time.Duration, panicked bool) ClassifiedFailure {
	f := Failure{
		Tool:     name,
		Err:      errMsg,
		Elapsed:  elapsed,
		Panicked: panicked,
		At:       time.Now(),
	}
	e.Record(f)
	return e.ClassifyFailure(f)
}

// Snapshot is a copyable per-tool health row for dashboards / CLI output.
type Snapshot struct {
	Tool       string   `json:"tool"`
	Class      Class    `json:"class"`
	Verdict    Verdict  `json:"verdict"`
	ClassLabel string   `json:"class_label"`
	VerdictLabel string `json:"verdict_label"`
	Failures   int      `json:"failures"`
	Repeated   int      `json:"repeated"`
	LastErr    string   `json:"last_err,omitempty"`
	Reason     string   `json:"reason,omitempty"`
}

// SnapshotOf returns a snapshot for one tool (latest classified failure or an
// "ok" row if no failure recorded).
func (e *Engine) SnapshotOf(name string) Snapshot {
	recent := e.Recent(name)
	s := Snapshot{Tool: name}
	if len(recent) == 0 {
		s.ClassLabel = ClassUnknown.String()
		s.VerdictLabel = VerdictNotBroken.String()
		s.Reason = "no failures recorded"
		return s
	}
	rep := e.Repeated(name)
	s.Failures = len(recent)
	s.Repeated = rep
	// classify from the most recent failure
	last := recent[len(recent)-1]
	s.LastErr = last.Err
	cf := e.ClassifyFailure(last)
	s.Class = cf.Class
	s.Verdict = cf.Verdict
	s.ClassLabel = cf.Class.String()
	s.VerdictLabel = cf.Verdict.String()
	s.Reason = cf.Reason
	return s
}

// Dashboard lists snapshots for every tool that has recorded a failure, sorted
// by tool name, together with a summary.
func (e *Engine) Dashboard() ([]Snapshot, Summary) {
	e.mu.RLock()
	names := make([]string, 0, len(e.records))
	for n := range e.records {
		names = append(names, n)
	}
	e.mu.RUnlock()
	sort.Strings(names)

	rows := make([]Snapshot, 0, len(names))
	seen := map[Verdict]int{}
	for _, n := range names {
		s := e.SnapshotOf(n)
		rows = append(rows, s)
		seen[s.Verdict]++
	}
	return rows, Summary{
		TotalTracked: len(rows),
		Autofix:      seen[VerdictAutoFix],
		Advisory:     seen[VerdictAdvisory],
		Quarantine:   seen[VerdictQuarantine],
		Transient:    seen[VerdictRetry],
		Broken:       seen[VerdictAutoFix] + seen[VerdictAdvisory] + seen[VerdictQuarantine],
	}
}

// Summary aggregates a health check across all tracked tools.
type Summary struct {
	TotalTracked int `json:"total_tracked"`
	Autofix      int `json:"autofix"`
	Advisory     int `json:"advisory"`
	Quarantine   int `json:"quarantine"`
	Transient    int `json:"transient"`
	Broken       int `json:"broken"`
}

// DisabledTools returns the tools whose verdict is Quarantine (Phase-2 surface).
// In Phase 0 this is informational only — nothing is actually disabled yet.
func (e *Engine) DisabledTools() []string {
	rows, _ := e.Dashboard()
	var out []string
	for _, r := range rows {
		if r.Verdict == VerdictQuarantine {
			out = append(out, r.Tool)
		}
	}
	return out
}

// CompactError renders a shortened, single-line form of an error for
// dashboards. Invalid UTF-8 bytes are sanitized (Phase 3) so malformed tool
// output never corrupts the TUI / persisted state.
func CompactError(err string) string {
	if err == "" {
		return ""
	}
	s := SanitizeUTF8(strings.Join(strings.Fields(err), " "))
	if len(s) > 90 {
		return s[:90] + "…"
	}
	return s
}