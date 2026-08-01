package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ── Unit: URI / path round-trip ──────────────────────────────────────────────

func TestPathURIRoundTrip(t *testing.T) {
	cases := []string{
		"/root/eling/main.go",
		"/home/user/my project/file.ts",
		"/tmp/x/y/z.js",
	}
	for _, p := range cases {
		uri := pathToURI(p)
		if !strings.HasPrefix(uri, "file://") {
			t.Fatalf("pathToURI(%q) = %q, want file:// prefix", p, uri)
		}
		back := uriToPath(uri)
		if !strings.HasSuffix(back, p) {
			t.Fatalf("uriToPath(pathToURI(%q)) = %q, want suffix %q", p, back, p)
		}
	}
}

func TestURIToPathNonFile(t *testing.T) {
	if got := uriToPath("http://example.com/x.go"); got != "" {
		t.Fatalf("uriToPath(http) = %q, want empty", got)
	}
}

// ── Unit: framing ────────────────────────────────────────────────────────────

func TestMessageFraming(t *testing.T) {
	var buf strings.Builder
	payload := []byte(`{"jsonrpc":"2.0","method":"test"}`)
	if err := writeMessage(&buf, payload); err != nil {
		t.Fatal(err)
	}
	got, err := readMessage(bufio.NewReader(strings.NewReader(buf.String())))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("framing round-trip mismatch: %s", got)
	}
}

// ── Unit: langForPath / Supports ─────────────────────────────────────────────

func TestLangForPath(t *testing.T) {
	cases := map[string]bool{
		"a.go":     true,
		"b.py":     true,
		"c.ts":     true,
		"d.tsx":    true,
		"e.js":     true,
		"f.md":     false,
		"g.txt":    false,
		"h.go.bak": false, // extension is .bak
	}
	for path, want := range cases {
		if got := Supports(path); got != want {
			t.Errorf("Supports(%q) = %v, want %v", path, got, want)
		}
	}
}

// ── Unit: disabled / missing binary → silent skip ───────────────────────────

func TestDisabledManagerSkips(t *testing.T) {
	m := NewManager(Config{Enabled: false, Servers: map[string]string{"go": "gopls"}})
	diags := m.Diagnostics("/tmp/x.go", "package main\n")
	if diags != nil {
		t.Fatalf("disabled manager returned diagnostics: %+v", diags)
	}
}

func TestMissingBinarySkips(t *testing.T) {
	m := NewManager(Config{Enabled: true, Servers: map[string]string{
		"go": "definitely-not-a-real-lsp-binary-xyz",
	}})
	diags := m.Diagnostics("/tmp/x.go", "package main\n")
	if diags != nil {
		t.Fatalf("missing binary should skip silently, got: %+v", diags)
	}
}

func TestUnsupportedExtensionSkips(t *testing.T) {
	m := NewManager(Config{Enabled: true, Servers: map[string]string{"go": "gopls"}})
	if diags := m.Diagnostics("/tmp/README.md", "# hi"); diags != nil {
		t.Fatalf("unsupported extension should skip, got: %+v", diags)
	}
}

// ── Integration: fake LSP server end-to-end ──────────────────────────────────

// TestFakeLSPHelperProcess is the helper-mode entrypoint. When spawned with
// GO_WANT_LSP_HELPER=1 it acts as a tiny LSP server over stdio.
func TestFakeLSPHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_LSP_HELPER") != "1" {
		return
	}
	fakeLSPMain()
	os.Exit(0)
}

// fakeLSPMain answers initialize and publishes one error diagnostic on didOpen.
func fakeLSPMain() {
	r := bufio.NewReader(os.Stdin)
	for {
		msg, err := readMessage(r)
		if err != nil {
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(msg, &req); err != nil {
			continue
		}
		if req.ID != nil {
			// Respond to initialize with minimal capabilities.
			resp, _ := json.Marshal(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      json.RawMessage(req.ID),
				"result": map[string]interface{}{
					"capabilities": map[string]interface{}{
						"textDocumentSync": 1,
					},
				},
			})
			_ = writeMessage(os.Stdout, resp)
			continue
		}
		switch req.Method {
		case "textDocument/didOpen":
			var p struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(req.Params, &p)
			diag, _ := json.Marshal(map[string]interface{}{
				"jsonrpc": "2.0",
				"method":  "textDocument/publishDiagnostics",
				"params": map[string]interface{}{
					"uri": p.TextDocument.URI,
					"diagnostics": []map[string]interface{}{
						{
							"severity": 1,
							"message":  "boom: syntax error",
							"source":   "fake-lsp",
							"range": map[string]interface{}{
								"start": map[string]interface{}{"line": 2, "character": 3},
								"end":   map[string]interface{}{"line": 2, "character": 7},
							},
						},
					},
				},
			})
			_ = writeMessage(os.Stdout, diag)
		}
	}
}

// writeFakeLSPHelper writes a shell wrapper that re-execs the test binary in
// helper mode, so Manager can spawn it as a "language server".
func writeFakeLSPHelper(t *testing.T) string {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-lsp.sh")
	content := fmt.Sprintf("#!/bin/sh\nGO_WANT_LSP_HELPER=1 exec %s -test.run=TestFakeLSPHelperProcess\n", bin)
	if err := os.WriteFile(script, []byte(content), 0755); err != nil {
		t.Fatalf("write fake-lsp.sh: %v", err)
	}
	return script
}

func TestFakeServerEndToEnd(t *testing.T) {
	if os.Getenv("GO_WANT_LSP_HELPER") == "1" {
		t.Skip("helper mode")
	}
	script := writeFakeLSPHelper(t)
	m := NewManager(Config{Enabled: true, Servers: map[string]string{"go": script}})
	defer m.KillAll()

	path := filepath.Join(t.TempDir(), "main.go")
	content := "package main\n\nfunc main() {\n\tundefinedFunc()\n}\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	diags := m.Diagnostics(path, content)
	if len(diags) != 1 {
		t.Fatalf("expected 1 diagnostic from fake server, got %d: %+v", len(diags), diags)
	}
	d := diags[0]
	if d.Severity != 1 || d.SeverityText() != "ERR" {
		t.Fatalf("expected severity 1 (ERR), got %d (%s)", d.Severity, d.SeverityText())
	}
	if !strings.Contains(d.Message, "syntax error") {
		t.Fatalf("message = %q, want containing 'syntax error'", d.Message)
	}
	if d.Line != 2 || d.Col != 3 {
		t.Fatalf("expected line 2 col 3, got line %d col %d", d.Line, d.Col)
	}

	// Second call → didChange path, still works, no crash.
	if diags := m.Diagnostics(path, content); len(diags) != 1 {
		t.Fatalf("didChange round failed: %+v", diags)
	}
}

// ── Unit: severity label ─────────────────────────────────────────────────────

func TestSeverityText(t *testing.T) {
	cases := map[int]string{1: "ERR", 2: "WARN", 3: "INFO", 4: "HINT", 99: "HINT"}
	for sev, want := range cases {
		if got := (Diagnostic{Severity: sev}).SeverityText(); got != want {
			t.Errorf("SeverityText(%d) = %s, want %s", sev, got, want)
		}
	}
}

// ── Integration: fake server via exec.Command directly (framing sanity) ──────

func TestReadMessageAfterWriteMessage(t *testing.T) {
	// Pipe a framed message through an actual OS pipe.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	payload := []byte(`{"jsonrpc":"2.0","method":"x","params":{}}`)
	go func() {
		_ = writeMessage(w, payload)
		_ = w.Close()
	}()
	got, err := readMessage(bufio.NewReader(r))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("pipe framing mismatch: %s", got)
	}
}

// ── Integration: exec-based fake server (no shell wrapper) ───────────────────

func TestFakeServerViaExec(t *testing.T) {
	if os.Getenv("GO_WANT_LSP_HELPER") == "1" {
		t.Skip("helper mode")
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-test.run=TestFakeLSPHelperProcess")
	cmd.Env = append(os.Environ(), "GO_WANT_LSP_HELPER=1")
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	// Handshake.
	if _, err := fmt.Fprintf(stdin, "Content-Length: %d\r\n\r\n%s", len(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`), `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`); err != nil {
		t.Fatal(err)
	}
	// Read response.
	if _, err := readMessage(bufio.NewReader(stdout)); err != nil {
		t.Fatalf("initialize response: %v", err)
	}
}
