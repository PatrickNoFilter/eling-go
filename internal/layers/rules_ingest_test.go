package layers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRulesFile creates a temp rules file and returns the project dir.
func writeRulesFile(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestFindProjectRulesFileProbeOrder verifies the first-match priority order:
// AGENTS.md > DEEPCODE.md > CLAUDE.md > .cursor/rules.
func TestFindProjectRulesFileProbeOrder(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("agents"), 0o644)
	os.WriteFile(filepath.Join(dir, "DEEPCODE.md"), []byte("deepcode"), 0o644)
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("claude"), 0o644)

	if got := FindProjectRulesFile(dir); got != filepath.Join(dir, "AGENTS.md") {
		t.Fatalf("want AGENTS.md first, got %s", got)
	}
}

// TestFindProjectRulesFileCursorRules verifies .cursor/rules/*.mdc is probed
// when no top-level rules file exists.
func TestFindProjectRulesFileCursorRules(t *testing.T) {
	dir := t.TempDir()
	rulesDir := filepath.Join(dir, ".cursor", "rules")
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(rulesDir, "b.mdc"), []byte("b"), 0o644)
	os.WriteFile(filepath.Join(rulesDir, "a.mdc"), []byte("a"), 0o644)
	if got := FindProjectRulesFile(dir); got != filepath.Join(rulesDir, "a.mdc") {
		t.Fatalf("want lexically-first .mdc, got %s", got)
	}
}

// TestFindProjectRulesFileMissing verifies a silent "" when nothing exists.
func TestFindProjectRulesFileMissing(t *testing.T) {
	if got := FindProjectRulesFile(t.TempDir()); got != "" {
		t.Fatalf("want empty for missing rules, got %s", got)
	}
}

// TestLoadProjectRulesEmptyDir verifies LoadProjectRules returns empty for a
// directory with no rules file (the silent-skip acceptance case).
func TestLoadProjectRulesEmptyDir(t *testing.T) {
	file, content := LoadProjectRules(t.TempDir())
	if file != "" || content != "" {
		t.Fatalf("want empty load for empty dir, got file=%q content=%q", file, content)
	}
}

// TestLoadProjectRulesCapsContent proves oversized files are truncated so
// small local-model token budgets are protected.
func TestLoadProjectRulesCapsContent(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("rule line\n", 1000)
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(big), 0o644)
	_, content := LoadProjectRules(dir)
	if content == "" {
		t.Fatal("expected content for AGENTS.md")
	}
	// 1000+ lines must be capped to projectRulesMaxLines.
	if n := len(strings.Split(content, "\n")); n > projectRulesMaxLines+1 {
		t.Fatalf("rules not line-capped: %d lines", n)
	}
	if !strings.Contains(content, "truncated") {
		t.Errorf("expected truncation marker in capped content")
	}
}

// TestLoadProjectRulesReturnsSource verifies the returned file path matches.
func TestLoadProjectRulesReturnsSource(t *testing.T) {
	dir := writeRulesFile(t, "DEEPCODE.md", "always run go vet")
	file, content := LoadProjectRules(dir)
	if !strings.HasSuffix(file, "DEEPCODE.md") {
		t.Errorf("unexpected source file: %s", file)
	}
	if !strings.Contains(content, "go vet") {
		t.Errorf("content missing rules text: %q", content)
	}
}