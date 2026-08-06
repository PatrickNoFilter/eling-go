package verify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestModule creates a temp Go module dir with go.mod + a file, returning
// the dir. content may be "" to skip writing the file.
func newTestModule(t *testing.T, filename, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/verifytest\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if content != "" {
		if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func editCall(path string) ToolCall {
	args := `{"file_path": "` + path + `"}`
	return ToolCall{Name: "edit", Args: args}
}

// ── unit: extraction ────────────────────────────────────────────────────────

func TestEditedFilesExtraction(t *testing.T) {
	got := editedFiles([]ToolCall{
		editCall("/a/b.go"),
		editCall("/a/b.go"), // dedupe
		{Name: "write", Args: `{"file_path": "/a/c.py"}`},
		{Name: "lsp_rename", Args: `{"file_path": "/a/d.ts"}`},
		{Name: "bash", Args: `{"cmd": "touch /a/e.go"}`}, // not an edit tool
		{Name: "edit", Args: `{"bad json`},               // unparseable
	})
	if len(got) != 3 {
		t.Fatalf("expected 3 edited files, got %d: %v", len(got), got)
	}
}

func TestTaskSelection(t *testing.T) {
	if taskFor([]string{"x.md"}) != taskDocs {
		t.Error("docs only → taskDocs")
	}
	if taskFor([]string{"x.md", "x.py"}) != taskStatic {
		t.Error("py + docs → taskStatic")
	}
	if taskFor([]string{"x.go", "x.md"}) != taskGo {
		t.Error("go + docs → taskGo (precedence)")
	}
	if taskFor([]string{"x.xyz"}) != taskNone {
		t.Error("unknown ext → taskNone")
	}
}

func TestFindGoModule(t *testing.T) {
	dir := newTestModule(t, "a.go", "package main\nfunc main() {}\n")
	nested := filepath.Join(dir, "pkg", "sub")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	if got := findGoModule(nested); got != dir {
		t.Fatalf("expected module root %s, got %s", dir, got)
	}
	nowhere := t.TempDir()
	if got := findGoModule(nowhere); got != "" {
		t.Fatalf("expected no module root, got %s", got)
	}
}

// ── unit: disabled / no-op paths ────────────────────────────────────────────

func TestDisabledVerifyNoOp(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = false
	v := New(cfg, nil)

	if msg := v.Round(context.Background(), []ToolCall{editCall("/tmp/x.go")}); msg != "" {
		t.Fatalf("disabled verifier must not prompt, got: %s", msg)
	}
	if block := v.Final(context.Background()); block != "" {
		t.Fatalf("disabled verifier must not attach evidence, got: %s", block)
	}
}

// TestEnableNoopWhenCommissionedDisabled: a verifier commissioned off
// (--no-verify / verify.enabled:false) can never be turned on by the per-turn
// plan-mode logic — Enable() must be a no-op.
func TestEnableNoopWhenCommissionedDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = false
	v := New(cfg, nil)
	v.Enable()
	if v.Enabled() {
		t.Fatal("Enable() must not resurrect a verifier commissioned disabled")
	}
}

// TestEnableRestoresCommissionedVerifier: for a verifier commissioned ON, the
// per-turn plan-mode Disable must be reversible by the next non-plan turn's
// Enable call.
func TestEnableRestoresCommissionedVerifier(t *testing.T) {
	v := Default() // commissioned on
	v.Disable()
	if v.Enabled() {
		t.Fatal("Disable should turn a commissioned verifier off for the turn")
	}
	v.Enable()
	if !v.Enabled() {
		t.Fatal("Enable should restore a commissioned verifier on the next turn")
	}
}

func TestNoEditsNoEvidence(t *testing.T) {
	v := Default()
	if msg := v.Round(context.Background(), []ToolCall{{Name: "read", Args: `{"file_path": "/tmp/x.go"}`}}); msg != "" {
		t.Fatalf("read is not an edit — no prompt expected, got: %s", msg)
	}
	if block := v.Final(context.Background()); block != "" {
		t.Fatalf("no edits — no evidence block expected, got: %s", block)
	}
}

func TestRoundHonorsContextCancel(t *testing.T) {
	v := Default()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled
	if msg := v.Round(ctx, []ToolCall{editCall("/tmp/x.go")}); msg != "" {
		t.Fatalf("canceled ctx must short-circuit, got: %s", msg)
	}
}

func TestDocsEditsNoEvidence(t *testing.T) {
	dir := newTestModule(t, "readme.md", "# hi\n")
	call := editCall(filepath.Join(dir, "readme.md"))
	v := Default()
	if msg := v.Round(context.Background(), []ToolCall{call}); msg != "" {
		t.Fatalf("docs edit must not prompt, got: %s", msg)
	}
	if block := v.Final(context.Background()); block != "" {
		t.Fatalf("docs edit must not attach evidence, got: %s", block)
	}
}

// ── integration: real `go test` evidence ────────────────────────────────────

func TestCleanGoEditPassesAndEvidenceBlock(t *testing.T) {
	dir := newTestModule(t, "main.go", "package main\n\nfunc main() {}\n")
	v := Default()

	msg := v.Round(context.Background(), []ToolCall{editCall(filepath.Join(dir, "main.go"))})
	if msg != "" {
		t.Fatalf("clean edit must not prompt repair, got: %s", msg)
	}

	block := v.Final(context.Background())
	if block == "" {
		t.Fatal("expected an Evidence block after a clean Go edit")
	}
	for _, want := range []string{"Evidence:", "go test ./...", "exit code: 0", "PASS"} {
		if !strings.Contains(block, want) {
			t.Fatalf("evidence block missing %q:\n%s", want, block)
		}
	}
}

func TestGoSyntaxErrorPromptsRepairAndHonestFail(t *testing.T) {
	dir := newTestModule(t, "broken.go", "package main\n\nfunc main() { var x =  // syntax error\n")
	call := editCall(filepath.Join(dir, "broken.go"))
	v := Default()

	msg := v.Round(context.Background(), []ToolCall{call})
	if !strings.Contains(msg, "[Verification failed") {
		t.Fatalf("syntax error must produce a [Verification failed] repair prompt, got:\n%s", msg)
	}
	if !strings.Contains(msg, "repair round 1/2") {
		t.Fatalf("repair prompt must mention the round budget, got:\n%s", msg)
	}

	// Final with the file still broken → honest FAIL, never success.
	block := v.Final(context.Background())
	if block == "" {
		t.Fatal("expected a failing Evidence block")
	}
	if !strings.Contains(block, "FAIL") || !strings.Contains(block, "STILL FAILING") {
		t.Fatalf("failing evidence must be honest, got:\n%s", block)
	}
	if strings.Contains(block, "Verification passed") {
		t.Fatalf("must never claim success with failing evidence:\n%s", block)
	}
}

func TestRepairBudgetBoundedByMaxRounds(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxRounds = 2 // explicit: the dedicated verify.max_rounds field
	v := New(cfg, nil)

	dir := newTestModule(t, "broken.go", "package main\n\nfunc main() { var x =  // syntax error\n")
	call := editCall(filepath.Join(dir, "broken.go"))

	first := v.Round(context.Background(), []ToolCall{call})
	second := v.Round(context.Background(), []ToolCall{call})
	third := v.Round(context.Background(), []ToolCall{call})

	if first == "" || second == "" {
		t.Fatal("first two failures must produce repair prompts")
	}
	if !strings.Contains(second, "repair budget (2 rounds) exhausted") {
		t.Fatalf("second prompt must announce exhaustion, got:\n%s", second)
	}
	if third != "" {
		t.Fatalf("round beyond max_rounds must not prompt again, got: %s", third)
	}
}

func TestRepairThenFixPasses(t *testing.T) {
	dir := newTestModule(t, "main.go", "package main\n\nfunc main() { var x =  // syntax error\n")
	v := Default()
	broken := editCall(filepath.Join(dir, "main.go"))

	if msg := v.Round(context.Background(), []ToolCall{broken}); msg == "" {
		t.Fatal("broken code must prompt repair")
	}

	// Model fixes the file, then the next round re-verifies clean.
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644)
	if msg := v.Round(context.Background(), []ToolCall{editCall(filepath.Join(dir, "main.go"))}); msg != "" {
		t.Fatalf("fixed code must not prompt repair, got: %s", msg)
	}

	block := v.Final(context.Background())
	if !strings.Contains(block, "PASS") {
		t.Fatalf("repaired work must finish with PASS evidence, got:\n%s", block)
	}
}

// ── static evidence (LSP-style) ─────────────────────────────────────────────

func TestStaticLSPFailurePromptsRepair(t *testing.T) {
	v := New(DefaultConfig(), func(path string) []string {
		if strings.HasSuffix(path, ".py") {
			return []string{"undefined name 'foo'"}
		}
		return nil
	})

	msg := v.Round(context.Background(), []ToolCall{
		{Name: "write", Args: `{"file_path": "/tmp/mod.py"}`},
	})
	if !strings.Contains(msg, "undefined name 'foo'") {
		t.Fatalf("static diagnostics must feed the repair prompt, got:\n%s", msg)
	}
}

func TestStaticLSPCleanNoPrompt(t *testing.T) {
	v := New(DefaultConfig(), func(path string) []string { return nil })
	if msg := v.Round(context.Background(), []ToolCall{
		{Name: "write", Args: `{"file_path": "/tmp/mod.py"}`},
	}); msg != "" {
		t.Fatalf("clean static evidence must not prompt, got: %s", msg)
	}
}

// ── timeout is inconclusive, never a failure ────────────────────────────────

func TestGoTimeoutIsInconclusive(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TimeoutSec = 1
	v := New(cfg, nil)

	dir := newTestModule(t, "slow_test.go", `package main

import (
	"testing"
	"time"
)

func TestSlow(t *testing.T) { time.Sleep(10 * time.Second) }
`)
	// go test ./... would take ≥10s; the 1s timeout must kill it and report
	// inconclusive (Passed=true, not a code failure).
	outc := v.runGo(context.Background(), []string{filepath.Join(dir, "slow_test.go")})
	if outc == nil {
		t.Fatal("expected an outcome")
	}
	if !outc.Passed {
		t.Fatalf("timeout must be inconclusive (not a failure), got exit %d: %s", outc.ExitCode, outc.Summary)
	}
	if !strings.Contains(outc.Summary, "timed out") {
		t.Fatalf("timeout summary must say so, got: %s", outc.Summary)
	}
}

// ── Final re-checks stale failing evidence ──────────────────────────────────

func TestFinalRechecksFailingEvidenceAfterFix(t *testing.T) {
	dir := newTestModule(t, "main.go", "package main\n\nfunc main() { var x =  // syntax error\n")
	v := Default()

	v.Round(context.Background(), []ToolCall{editCall(filepath.Join(dir, "main.go"))})

	// Model fixes the file *after* the last round; Final must re-verify.
	time.Sleep(20 * time.Millisecond) // ensure mtime moves
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644)

	block := v.Final(context.Background())
	if !strings.Contains(block, "PASS") {
		t.Fatalf("Final must re-run failing evidence and pick up the fix, got:\n%s", block)
	}
}

// ── Reset clears per-turn state ─────────────────────────────────────────────

func TestResetClearsTurnState(t *testing.T) {
	dir := newTestModule(t, "broken.go", "package main\n\nfunc main() { var x =  // syntax error\n")
	v := Default()
	if msg := v.Round(context.Background(), []ToolCall{editCall(filepath.Join(dir, "broken.go"))}); msg == "" {
		t.Fatal("expected a repair prompt")
	}
	v.Reset()
	if block := v.Final(context.Background()); block != "" {
		t.Fatalf("after Reset, no evidence block expected, got: %s", block)
	}
}
