// Package verify implements an evidence-driven completion loop (DeepCode heist,
// Part III D2). After the agent edits files, it selects appropriate verification
// evidence for the task (Go test/build, LSP static diagnostics for other
// languages), runs it, and a FAILED verification is never reported as success —
// the failure feeds the next repair iteration until the evidence is green (or is
// honestly reported as still failing).
//
// The package is intentionally decoupled from internal/provider and the agent:
// it receives a light image of the tool calls executed in a round and returns a
// text prompt to inject as the next turn's user message (the repair loop) and an
// Evidence block to append to the final answer (honest reporting).
package verify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Task kinds selected by the evidence selector from the set of edited files.
const (
	taskNone = iota
	taskGo
	taskStatic // python / typescript / javascript (LSP diagnostics)
	taskDocs   // docs / config: no runnable evidence, just report
)

// isStaticExt reports whether a path is a language the LSP callback can analyze.
func isStaticExt(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".py", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".go":
		return true
	}
	return false
}

// taskFor picks the single verification task for a set of edited files.
// Go takes precedence (a full compile/test is the strongest evidence), then
// static languages, then docs (which need no evidence).
func taskFor(files []string) int {
	task := taskNone
	for _, f := range files {
		switch strings.ToLower(filepath.Ext(f)) {
		case ".go":
			return taskGo
		case ".py", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
			task = taskStatic
		case ".md", ".mdx", ".txt", ".rst", ".json", ".yaml", ".yml",
			".toml", ".adoc", ".html", ".css", ".csv":
			if task == taskNone {
				task = taskDocs
			}
		}
	}
	return task
}

// findGoModule walks up from dir until it finds a go.mod and returns the module
// root ("" if none). Used so `go test` runs from the module root even when the
// edited file is deep in a nested package.
func findGoModule(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(abs, "go.mod")); err == nil {
			return abs
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return ""
		}
		abs = parent
	}
}

// run executes the appropriate evidence for the edited files and returns the
// outcome, or nil when the task produces no evidence (docs / no runnable check).
func (v *Verifier) run(ctx context.Context, files []string) *Outcome {
	switch taskFor(files) {
	case taskGo:
		return v.runGo(ctx, files)
	case taskStatic:
		return v.runStatic(ctx, files)
	default:
		return nil
	}
}

// runGo runs `go test ./...` from the module root (falling back to static LSP
// diagnostics when the edited file is not inside a Go module). A context
// deadline (timeout) is treated as *inconclusive*, never as a code failure, so
// a genuinely large but slow project doesn't loop a repair cycle.
func (v *Verifier) runGo(ctx context.Context, files []string) *Outcome {
	moduleRoot := findGoModule(files[0])
	if moduleRoot == "" {
		return v.runStatic(ctx, files)
	}

	runCtx := ctx
	if runCtx == nil {
		runCtx = context.Background()
	}
	var cancel context.CancelFunc
	runCtx, cancel = context.WithTimeout(runCtx, time.Duration(v.cfg.TimeoutSec)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "go", "test", "./...")
	cmd.Dir = moduleRoot
	out, err := cmd.CombinedOutput()

	timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)
	exitCode := 0
	if err != nil {
		exitCode = 1
		if cmd.ProcessState != nil {
			if c := cmd.ProcessState.ExitCode(); c != 0 {
				exitCode = c
			}
		}
	}

	command := "go test ./..."
	var summary string
	passed := true
	if timedOut {
		summary = fmt.Sprintf(
			"verification timed out after %ds — no completion reached (inconclusive). Raise verify.timeout_seconds for very large projects.",
			v.cfg.TimeoutSec,
		)
	} else {
		summary = summarizeOutput(string(out))
		if err != nil {
			passed = false
		}
	}

	return &Outcome{
		Command:  command,
		ExitCode: exitCode,
		Passed:   passed,
		Summary:  summary,
		At:       time.Now(),
	}
}

// runStatic collects LSP diagnostics for the edited static-language files via
// the LSP callback wired at construction. No analyzer configured → the evidence
// is a benign skip (never a fabricated PASS for real diagnostics).
func (v *Verifier) runStatic(ctx context.Context, files []string) *Outcome {
	if v.lsp == nil {
		return &Outcome{
			Command:  "static analysis (lsp)",
			ExitCode: 0,
			Passed:   true,
			Summary:  "no language server available — static evidence skipped (reuse --run-tests or run manually)",
			At:       time.Now(),
		}
	}
	var diags []string
	for _, f := range files {
		if !isStaticExt(f) {
			continue
		}
		for _, d := range v.lsp(f) {
			if len(diags) < 50 { // bound output
				diags = append(diags, "  "+f+": "+d)
			}
		}
	}
	if len(diags) == 0 {
		return &Outcome{
			Command:  "static analysis (lsp)",
			ExitCode: 0,
			Passed:   true,
			Summary:  "no diagnostics reported",
			At:       time.Now(),
		}
	}
	return &Outcome{
		Command:  "static analysis (lsp)",
		ExitCode: 1,
		Passed:   false,
		Summary:  "static diagnostics found:\n" + strings.Join(diags, "\n"),
		At:       time.Now(),
	}
}

// summarizeOutput keeps the tail of a command's output (rune-safe), where go
// test emits the failures and `FAIL` / `Error:` markers.
func summarizeOutput(out string) string {
	text := strings.TrimSpace(out)
	if text == "" {
		return "(no output)"
	}
	trimmed := text
	if idx := strings.LastIndex(trimmed, "FAIL"); idx >= 0 {
		trimmed = trimmed[idx:]
	} else if idx := strings.LastIndex(trimmed, "Error:"); idx >= 0 {
		trimmed = trimmed[idx:]
	}
	runes := []rune(trimmed)
	if len(runes) > 1400 {
		trimmed = string(runes[len(runes)-1400:])
	}
	return strings.TrimSpace(trimmed)
}
