package lsp

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
