package verify

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Config mirrors the `verify:` section of config.yaml.
//
//	verify: { enabled: true, max_rounds: 2, timeout_sec: 60, evidence: auto }
type Config struct {
	Enabled    bool
	MaxRounds  int // repair iterations before verification stops prompting (default 2)
	TimeoutSec int // per-run evidence timeout (default 60)
	Evidence   string
}

// Validate returns a normalized copy of the config with defaults applied.
func (c Config) Validate() Config {
	if !c.Enabled {
		return c
	}
	if c.MaxRounds <= 0 {
		c.MaxRounds = 2
	}
	if c.TimeoutSec <= 0 {
		c.TimeoutSec = 60
	}
	if c.Evidence == "" {
		c.Evidence = "auto"
	}
	return c
}

// DefaultConfig is the built-in default: verification on, 2 repair rounds,
// 60 s per run, evidence auto-selected by task.
func DefaultConfig() Config {
	return Config{Enabled: true, MaxRounds: 2, TimeoutSec: 60, Evidence: "auto"}
}

// Outcome is the result of a single verification run.
type Outcome struct {
	Command  string
	ExitCode int
	Passed   bool
	Summary  string
	At       time.Time
}

// ToolCall is the minimal image of a provider tool call the verifier needs
// (keeps this package decoupled from internal/provider).
type ToolCall struct {
	Name string
	Args string // JSON object string
}

// LSPProvider returns static diagnostics (formatted one-per-line strings) for a
// path, or nil when the file is clean / unsupported. Wired to internal/lsp by
// the agent so non-Go languages reuse the Phase-3 language servers.
type LSPProvider func(path string) []string

// editTools are the tools whose file_path argument records a source edit.
var editTools = map[string]bool{
	"edit":       true,
	"write":      true,
	"lsp_rename": true,
}

// filePathFromArgs extracts the `file_path` argument from a JSON tool-call
// argument string ("" when absent/unparseable).
func filePathFromArgs(args string) string {
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(args), &m); err != nil {
		return ""
	}
	if p, ok := m["file_path"].(string); ok {
		return p
	}
	return ""
}

// editedFiles returns the deduplicated list of file paths edited by the round's
// tool calls (edit/write/lsp_rename with a file_path argument).
func editedFiles(calls []ToolCall) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range calls {
		if !editTools[c.Name] {
			continue
		}
		p := filePathFromArgs(c.Args)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// Verifier holds the per-turn verify→repair state machine. It is created once
// per Agent and Reset() at the start of every Ask/AskStream turn.
type Verifier struct {
	mu  sync.Mutex
	cfg Config
	lsp LSPProvider

	// commissioned is fixed at construction: it records whether verification
	// was enabled at build time. A verifier commissioned with Enable=false
	// (e.g. `--no-verify` or `verify.enabled: false`) can never be turned on
	// by the per-turn plan-mode logic — Enable() is a no-op for it. Plan mode
	// is only ever able to *opt out* for a turn, never resurrect verification
	// the operator explicitly disabled.
	commissioned bool

	// per-turn state (guarded by mu)
	roundUsed   int      // failed verification rounds consumed this turn
	exhausted   bool     // repair budget spent → stop Round prompting
	editsDone   bool     // any verifiable edit happened this turn
	editedFiles []string // files edited by the most recent editing round
	last        *Outcome // most recent verification outcome
}

// New returns a Verifier with the given config and LSP provider.
func New(cfg Config, lsp LSPProvider) *Verifier {
	return &Verifier{cfg: cfg.Validate(), lsp: lsp, commissioned: cfg.Enabled}
}

// Default returns a Verifier with built-in defaults (verification on) and no
// LSP provider.
func Default() *Verifier { return New(DefaultConfig(), nil) }

// Enabled reports whether verification is active.
func (v *Verifier) Enabled() bool { return v != nil && v.cfg.Enabled }

// Enable turns verification on (used by plan-mode turn restores and tests).
// It is a no-op when the verifier was commissioned disabled (--no-verify /
// verify.enabled: false) so per-turn logic can never resurrect verification
// the operator explicitly turned off.
func (v *Verifier) Enable() {
	if v != nil && v.commissioned {
		v.cfg.Enabled = true
	}
}

// Disable turns verification off (used by the --no-verify CLI flag).
func (v *Verifier) Disable() {
	if v != nil {
		v.cfg.Enabled = false
	}
}

// Reset clears per-turn state. Call at the start of every Ask/AskStream.
func (v *Verifier) Reset() {
	if v == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.roundUsed = 0
	v.exhausted = false
	v.editsDone = false
	v.editedFiles = nil
	v.last = nil
}

// Round is the loop gate: called after each tool-call round. If the round
// edited verifiable files, it runs evidence. On failure within the repair
// budget it returns a repair prompt to inject as the next user message so the
// model fixes the code instead of declaring done. On pass / no edits / budget
// exhausted it returns "" (no prompt).
//
// Bounded by design (DeepCode amendment D2): the round counter is the
// dedicated verify.max_rounds field — never the agent's global maxToolRounds —
// and every run is time-boxed by cfg.TimeoutSec and honors ctx cancellation.
func (v *Verifier) Round(ctx context.Context, calls []ToolCall) string {
	if v == nil || !v.cfg.Enabled {
		return ""
	}
	// Honor shutdown / turn-timeout first (amendment D2).
	if ctx != nil && ctx.Err() != nil {
		return ""
	}

	files := editedFiles(calls)
	if len(files) == 0 {
		return ""
	}

	v.mu.Lock()
	v.editsDone = true
	v.editedFiles = files
	exhausted := v.exhausted
	v.mu.Unlock()

	if exhausted {
		return "" // repair budget spent — no more prompting
	}

	outc := v.run(ctx, files)
	if outc != nil {
		v.mu.Lock()
		v.last = outc
		v.mu.Unlock()
	}
	if outc == nil || outc.Passed {
		return ""
	}

	// A genuine failure consumes one repair round.
	v.mu.Lock()
	v.roundUsed++
	used := v.roundUsed
	left := v.cfg.MaxRounds - used
	if used >= v.cfg.MaxRounds {
		v.exhausted = true
	}
	v.mu.Unlock()

	return buildRepairPrompt(outc, used, left)
}

// Final runs one last verification when the model is about to answer, so the
// Evidence block reflects the *latest* state (a failing outcome is always
// re-checked; a fresh PASS with unchanged files is reused). Returns the
// Evidence block to append to the final answer, or "" when verification is
// disabled / no verifiable edits happened this turn.
func (v *Verifier) Final(ctx context.Context) string {
	if v == nil || !v.cfg.Enabled {
		return ""
	}
	v.mu.Lock()
	if !v.editsDone || len(v.editedFiles) == 0 {
		v.mu.Unlock()
		return ""
	}
	files := append([]string(nil), v.editedFiles...)
	last := v.last
	v.mu.Unlock()

	outc := last
	if staleEvidence(last, files) {
		outc = v.run(ctx, files)
		if outc == nil { // docs-only edit — no evidence to report
			v.mu.Lock()
			v.last = nil
			v.mu.Unlock()
			return ""
		}
		v.mu.Lock()
		v.last = outc
		v.mu.Unlock()
	}
	if outc == nil {
		return ""
	}
	return v.renderBlock(outc)
}

// staleEvidence reports whether the recorded outcome no longer represents the
// current state of the edited files: no outcome yet, a failing outcome (always
// re-check — the model may have just fixed it), or any edited file modified
// after the outcome was recorded.
func staleEvidence(last *Outcome, files []string) bool {
	if last == nil {
		return true
	}
	if !last.Passed {
		return true
	}
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			return true
		}
		if st.ModTime().After(last.At) {
			return true
		}
	}
	return false
}

// buildRepairPrompt renders the `[verification failed]` user message that
// drives the repair iteration.
func buildRepairPrompt(o *Outcome, used, left int) string {
	var sb strings.Builder
	sb.WriteString("[Verification failed — ")
	if left > 0 {
		fmt.Fprintf(&sb, "repair round %d/%d — fix the failing evidence below", used, used+left)
	} else {
		fmt.Fprintf(&sb, "repair budget (%d rounds) exhausted — do NOT claim this is verified unless you fix it", used)
	}
	sb.WriteString("]\n")
	sb.WriteString("Verification evidence (FAILING):\n")
	fmt.Fprintf(&sb, "  command:   %s\n", o.Command)
	fmt.Fprintf(&sb, "  exit code: %d\n", o.ExitCode)
	sb.WriteString("  summary:\n" + indent(o.Summary) + "\n")
	sb.WriteString("\nVerification runs again automatically next round. Fix the code to make the evidence green, ")
	sb.WriteString("or report the concrete blocker honestly in your final answer — never claim success with failing evidence.")
	return sb.String()
}

// renderBlock builds the honest-reporting Evidence block appended to the final
// answer (command, exit code, status, single-line summary).
func (v *Verifier) renderBlock(o *Outcome) string {
	status := "PASS"
	if !o.Passed {
		status = "FAIL"
	}
	summary := o.Summary
	if runes := []rune(summary); len(runes) > 500 {
		summary = string(runes[:500]) + " …"
	}
	summary = strings.ReplaceAll(summary, "\n", " ")

	var sb strings.Builder
	sb.WriteString("\n\nEvidence:\n")
	fmt.Fprintf(&sb, "- command:   %s\n", o.Command)
	fmt.Fprintf(&sb, "- exit code: %d\n", o.ExitCode)
	fmt.Fprintf(&sb, "- status:    %s\n", status)
	fmt.Fprintf(&sb, "- summary:   %s\n", summary)
	if o.Passed {
		sb.WriteString("Verification passed — the edited code compiles and its checks are green.")
	} else {
		sb.WriteString("Verification is STILL FAILING — report this honestly; do not present the work as fully verified.")
	}
	return sb.String()
}

// indent prefixes every line of s with four spaces (for the repair prompt).
func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = "    " + lines[i]
	}
	return strings.Join(lines, "\n")
}
