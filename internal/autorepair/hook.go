package autorepair

import (
	"sync"
	"time"
)

// defaultEng holds the process-wide detector used by the registry hook.
var (
	defaultOnce    sync.Once
	defaultEngine  *Engine
)

// Default returns the package-wide Engine (created lazily). All registry hooks
// funnel into this single instance, and the dashboards read from it.
func Default() *Engine {
	defaultOnce.Do(func() {
		defaultEngine = New(0, 0)
	})
	return defaultEngine
}

// ResetDefault replaces the package-wide engine (used by tests to isolate state).
func ResetDefault(e *Engine) {
	// Swap the instance behind the existing pointer we hand out.
	defaultEngine = e
	defaultOnce.Do(func() {})
}

// RecordFailure is the registry hook entry point. It is called from the tools
// registry's ExecuteContext defer block after a failed tool call. It records +
// classifies the failure and returns the classified result so the registry can
// act on a QUARANTINE verdict (disable the tool). It never mutates the
// environment itself.
//
//   - name: the tool name (e.g. "web_fetch", "bash")
//   - errMsg: the error string from the tool (may be empty on panic, in which
//     case panicked=true carries the signal)
//   - elapsed: wall-clock taken by the call
//   - panicked: true if the panic guard recovered
func RecordFailure(name, errMsg string, elapsed time.Duration, panicked bool) ClassifiedFailure {
	if name == "" {
		return ClassifiedFailure{}
	}
	return Default().judge(name, errMsg, elapsed, panicked)
}

// Quarantine records a tool as disabled (persisted to state). The registry
// hook calls this when RecordFailure returns a QUARANTINE verdict, then marks
// the tool disabled so it is no longer offered to the LLM.
func Quarantine(tool, classLabel, reason, lastErr string) {
	Default().Quarantine(tool, classLabel, reason, lastErr)
}

// Reenable removes a tool from quarantine (manual re-enable). Returns true when
// the tool was actually quarantined and has now been cleared.
func Reenable(tool string) bool { return Default().Reenable(tool) }

// IsQuarantined reports whether a tool is currently quarantined.
func IsQuarantined(tool string) bool { return Default().IsQuarantined(tool) }

// QuarantinedTools lists all currently quarantined tools (persisted).
func QuarantinedTools() []QuarantineRecord { return Default().Quarantined() }

// CountQuarantined returns how many tools are quarantined (TUI indicator).
func CountQuarantined() int { return Default().CountQuarantined() }

// LoadQuarantineState loads persisted quarantine records into the engine
// (called once at startup so the TUI/CLI reflect disabled tools from last run).
func LoadQuarantineState() { Default().LoadState() }

// SetAutofixEnabled toggles the process-wide opt-in autofix gate.
func SetAutofixEnabled(on bool) { Default().SetAutofix(on) }

// RepairTool drives a probe-gated repair for one tool (Phase 1). By default
// autofix is off, so it reports the advisory; with autofix on it applies the
// safe, idempotent fix and verifies via post-probe.
func RepairTool(tool string) RepairResult { return Default().Repair(tool) }

// RepairAllTools attempts repairs for every tracked tool and returns the rows.
func RepairAllTools() []RepairResult { return Default().RepairAll() }