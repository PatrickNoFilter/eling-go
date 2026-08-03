package lsp

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Real gopls rename integration: proves textDocument/rename returns edits for
// every reference across files. Only runs if gopls is on PATH.
func TestRealGoplsRename(t *testing.T) {
	if _, err := execLookPath("gopls"); err != nil {
		t.Skip("gopls not installed")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module rename-smoke\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Symbol used in two files: a.go defines oldName, b.go calls it.
	aPath := filepath.Join(dir, "a.go")
	bPath := filepath.Join(dir, "b.go")
	aContent := "package rename_smoke\n\nfunc oldName() {}\n"
	bContent := "package rename_smoke\n\nfunc Use() {\n\toldName()\n}\n"
	if err := os.WriteFile(aPath, []byte(aContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bPath, []byte(bContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Dedicated manager whose server runs with cwd = dir so gopls's workspace
	// root matches the temp module (the global manager inherits the test's
	// cwd, which would put these files outside the workspace).
	m := NewManager(Config{Enabled: true, Servers: map[string]string{"go": "gopls"}})
	m.dir = dir
	defer m.KillAll()

	// gopls needs both files open (didOpen) before rename — Diagnostics does that.
	_ = m.Diagnostics(aPath, aContent)
	_ = m.Diagnostics(bPath, bContent)

	// Rename oldName at a.go line 2 (0-based: line 0=package, line 1=blank,
	// line 2=func), col 5 (the identifier).
	deadline := time.Now().Add(45 * time.Second)
	var edits []TextEdit
	for time.Now().Before(deadline) {
		edits = m.Rename(aPath, 2, 5, "newName")
		if len(edits) > 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if len(edits) == 0 {
		t.Fatal("gopls rename returned no edits")
	}
	// Expect at least 2 edits: the definition in a.go and the call in b.go.
	if len(edits) < 2 {
		t.Fatalf("expected >=2 rename edits (definition + call site), got %d: %+v", len(edits), edits)
	}
	for _, e := range edits {
		if e.NewText != "newName" {
			t.Fatalf("unexpected newText %q in %+v", e.NewText, e)
		}
	}
	t.Logf("rename edits: %d across %d files", len(edits), func() int {
		seen := map[string]bool{}
		for _, e := range edits {
			seen[e.Path] = true
		}
		return len(seen)
	}())
}

// Real gopls integration: only runs if gopls is on PATH.
// Polls up to 12s for diagnostics so it is robust under CI/full-suite load.
func TestRealGoplsDiagnostics(t *testing.T) {
	if _, err := execLookPath("gopls"); err != nil {
		t.Skip("gopls not installed")
	}
	Configure(Config{Enabled: true, Servers: map[string]string{"go": "gopls"}})
	defer KillAll()

	dir := t.TempDir()
	// gopls needs module context
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module smoke\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "broken.go")
	// syntax error: missing closing paren
	content := "package main\nfunc main() {\n\tprintln(\"hi\"\n}\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// cold start: initialize handshake + first analysis can take seconds.
	// On slow devices (e.g. phones) gopls cold start is ~11s, and under
	// full-suite CPU load it can take far longer — use a generous deadline
	// to avoid flaky failures unrelated to the LSP client itself.
	_ = Diagnostics(path, content)

	deadline := time.Now().Add(45 * time.Second)
	var diags []Diagnostic
	for time.Now().Before(deadline) {
		diags = Diagnostics(path, content)
		if len(diags) > 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if len(diags) == 0 {
		t.Fatal("expected >=1 diagnostic from gopls on broken.go, got none")
	}
	found := false
	for _, d := range diags {
		sev := d.SeverityText()
		if sev == "ERR" || sev == "WARN" {
			found = true
			t.Logf("diag: %s:%d:%d %s: %s", filepath.Base(path), d.Line, d.Col, sev, d.Message)
		}
	}
	if !found {
		t.Fatalf("no ERR/WARN diagnostics, got: %+v", diags)
	}
}
