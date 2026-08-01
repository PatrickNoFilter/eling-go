package agent

import (
	"testing"

	"eling/internal/config"
)

// TestAugmentLSPDisabled verifies the augment helper is a no-op when LSP is
// disabled in config (the default for CI environments without servers).
func TestAugmentLSPDisabled(t *testing.T) {
	a := &Agent{cfg: &config.Config{LSP: config.LSPConfig{Enabled: false}}}
	got := a.augmentToolResultWithLSP("write", map[string]interface{}{
		"file_path": "/tmp/x.go", "content": "package main\n",
	}, `{"path":"/tmp/x.go","written":13}`)
	if got != `{"path":"/tmp/x.go","written":13}` {
		t.Fatalf("disabled LSP should return result unchanged, got: %s", got)
	}
}

// TestAugmentLSPNonEditingTool verifies only write/edit tools are augmented.
func TestAugmentLSPNonEditingTool(t *testing.T) {
	a := &Agent{cfg: &config.Config{LSP: config.LSPConfig{
		Enabled: true,
		Servers: map[string]string{"go": "gopls"},
	}}}
	got := a.augmentToolResultWithLSP("bash", map[string]interface{}{
		"command": "echo hi",
	}, `{"exit_code":0}`)
	if got != `{"exit_code":0}` {
		t.Fatalf("bash tool should not be augmented, got: %s", got)
	}
}

// TestAugmentLSPMissingBinary verifies missing server binary → silent skip.
func TestAugmentLSPMissingBinary(t *testing.T) {
	a := &Agent{cfg: &config.Config{LSP: config.LSPConfig{
		Enabled: true,
		Servers: map[string]string{"go": "no-such-lsp-binary-xyz"},
	}}}
	result := `{"path":"/tmp/x.go","written":13}`
	got := a.augmentToolResultWithLSP("write", map[string]interface{}{
		"file_path": "/tmp/x.go", "content": "package main\n",
	}, result)
	if got != result {
		t.Fatalf("missing binary should skip silently, got: %s", got)
	}
}

// TestAugmentLSPUnsupportedExtension verifies non-source files are skipped.
func TestAugmentLSPUnsupportedExtension(t *testing.T) {
	a := &Agent{cfg: &config.Config{LSP: config.LSPConfig{
		Enabled: true,
		Servers: map[string]string{"go": "gopls"},
	}}}
	result := `{"path":"/tmp/notes.md","written":5}`
	got := a.augmentToolResultWithLSP("write", map[string]interface{}{
		"file_path": "/tmp/notes.md", "content": "hello",
	}, result)
	if got != result {
		t.Fatalf("unsupported extension should skip, got: %s", got)
	}
}

// TestAugmentLSPMissingPathArg verifies tools without a path are skipped.
func TestAugmentLSPMissingPathArg(t *testing.T) {
	a := &Agent{cfg: &config.Config{LSP: config.LSPConfig{
		Enabled: true,
		Servers: map[string]string{"go": "gopls"},
	}}}
	result := `{"written":13}`
	got := a.augmentToolResultWithLSP("write", map[string]interface{}{"content": "x"}, result)
	if got != result {
		t.Fatalf("missing path should skip, got: %s", got)
	}
}
