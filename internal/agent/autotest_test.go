package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"eling/internal/config"
	"eling/internal/provider"
)

// helperAgent builds an Agent with autoTest enabled and a tiny cooldown so
// tests can exercise the memoization logic without sleeping.
func helperAgent(t *testing.T, cooldownSec, timeoutSec int) *Agent {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agent.AutoTest = true
	cfg.Agent.AutoTestCooldownSec = cooldownSec
	cfg.Agent.AutoTestTimeoutSec = timeoutSec
	a, err := New(cfg)
	if err != nil {
		t.Fatalf("New(cfg): %v", err)
	}
	return a
}

// makeModule creates a throwaway Go module in a temp dir with a passing test
// file, so autoTest can actually run `go test` against it.
func makeModule(t *testing.T, pkgName string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+pkgName+"\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testFile := filepath.Join(dir, pkgName+"_test.go")
	// NOTE: the test file MUST import "testing" — without it `go test` fails
	// with `undefined: testing` and the autoTest memoization tests all break.
	if err := os.WriteFile(testFile, []byte("package "+pkgName+"\n\nimport \"testing\"\n\nfunc TestTrue(t *testing.T){}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestAutoTestDisabled returns empty when auto_test is off.
func TestAutoTestDisabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agent.AutoTest = false
	a, err := New(cfg)
	if err != nil {
		t.Fatalf("New(cfg): %v", err)
	}
	if got := a.autoTest(nil); got != "" {
		t.Fatalf("autoTest with AutoTest=false = %q, want empty", got)
	}
}

// TestAutoTestNoFiles returns empty when no .go files appear in results.
func TestAutoTestNoFiles(t *testing.T) {
	a := helperAgent(t, 0, 5)
	msgs := []provider.Message{{Role: "tool", Content: "hello world, nothing to see"}}
	if got := a.autoTest(msgs); got != "" {
		t.Fatalf("autoTest on non-go output = %q, want empty", got)
	}
}

// TestAutoTestCacheSkipsRepeatedPass: after a passing run, a second call for
// the same package inside the cooldown with unchanged files returns "" without
// re-running go test (proves the memoization fast-path).
func TestAutoTestCacheSkipsRepeatedPass(t *testing.T) {
	dir := makeModule(t, "sample")

	// Point the agent's working dir at the temp module so pkgArgs resolve.
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)

	a := helperAgent(t, 3600, 300) // 1h cooldown → second call must be cached
	testFile := filepath.Join(dir, "sample_test.go")
	msgs := []provider.Message{{Role: "tool", Content: testFile}}

	if got := a.autoTest(msgs); got != "" {
		t.Fatalf("first autoTest returned failure: %q", got)
	}
	// Second call within cooldown: must be skipped (cache hit).
	start := time.Now()
	if got := a.autoTest(msgs); got != "" {
		t.Fatalf("cached autoTest returned failure: %q", got)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("cached autoTest took %v — go test re-ran instead of using cache", elapsed)
	}
}

// TestAutoTestReRunsOnFileChange: touching the test file after a cached pass
// invalidates the memo (mtime newer than cache timestamp), so go test re-runs.
func TestAutoTestReRunsOnFileChange(t *testing.T) {
	dir := makeModule(t, "changed")
	oldWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)

	a := helperAgent(t, 3600, 300) // 1h cooldown — only file change can invalidate
	testFile := filepath.Join(dir, "changed_test.go")
	msgs := []provider.Message{{Role: "tool", Content: testFile}}

	if got := a.autoTest(msgs); got != "" {
		t.Fatalf("first autoTest returned failure: %q", got)
	}

	// Modify the test file (mtime bump) → cache must be invalidated.
	time.Sleep(5 * time.Millisecond)
	os.WriteFile(testFile, []byte("package changed\n\nimport \"testing\"\n\nfunc TestTrue(t *testing.T){}\n// touched\n"), 0o644)

	// Push the global-cooldown timestamp into the past so it can't mask the
	// file-change invalidation we're trying to prove: with an old global
	// timestamp, the ONLY thing that can skip the run is the per-package
	// memo, and the newer file mtime must have invalidated it.
	a.autoTestMu.Lock()
	a.autoTestLast = time.Now().Add(-time.Hour)
	a.autoTestMu.Unlock()

	// Even with a long cooldown, the file change forces a real re-run.
	if got := a.autoTest(msgs); got != "" {
		t.Fatalf("re-run after file change returned failure: %q", got)
	}
}

// TestAutoTestCooldownGlobal: even for a DIFFERENT package, the global cooldown
// prevents a second go test run within the window.
func TestAutoTestCooldownGlobal(t *testing.T) {
	dir1 := makeModule(t, "pkgone")
	dir2 := makeModule(t, "pkgtwo")

	a := helperAgent(t, 3600, 300) // 1h cooldown

	oldWd, _ := os.Getwd()

	// First run in package one.
	os.Chdir(dir1)
	f1 := filepath.Join(dir1, "pkgone_test.go")
	msgs := []provider.Message{{Role: "tool", Content: f1}}
	if got := a.autoTest(msgs); got != "" {
		t.Fatalf("first autoTest returned failure: %q", got)
	}

	a.autoTestMu.Lock()
	last := a.autoTestLast
	a.autoTestMu.Unlock()
	if last.IsZero() {
		t.Fatal("autoTestLast not set after first run")
	}

	// Second run with a different package: global cooldown must skip it.
	os.Chdir(dir2)
	f2 := filepath.Join(dir2, "pkgtwo_test.go")
	msgs2 := []provider.Message{{Role: "tool", Content: f2}}
	start := time.Now()
	if got := a.autoTest(msgs2); got != "" {
		t.Fatalf("second autoTest returned failure: %q", got)
	}
	os.Chdir(oldWd)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("cooldown-guarded autoTest took %v — should have skipped", elapsed)
	}
}

// TestFilesUnchangedSince: modified files invalidate the cache.
func TestFilesUnchangedSince(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "x.go")
	os.WriteFile(f, []byte("package x\n"), 0o644)

	// Set mtime to 1h ago — counts as unchanged.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(f, old, old); err != nil {
		t.Fatal(err)
	}
	if !filesUnchangedSince([]string{f}, time.Now().Add(-30*time.Minute)) {
		t.Fatal("file with old mtime should count as unchanged")
	}

	// Bump mtime to now — counts as changed.
	now := time.Now()
	if err := os.Chtimes(f, now, now); err != nil {
		t.Fatal(err)
	}
	if filesUnchangedSince([]string{f}, now.Add(-time.Minute)) {
		t.Fatal("file modified after cache timestamp should count as changed")
	}
}

// TestFindGoModuleRoot: the helper locates the module root by walking up
// from a nested directory, and returns "" when no go.mod exists anywhere
// above the given dir.
func TestFindGoModuleRoot(t *testing.T) {
	dir := makeModule(t, "rootmod")
	nested := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findGoModuleRoot(nested); got != dir {
		t.Fatalf("findGoModuleRoot(%q) = %q, want %q", nested, got, dir)
	}
	// A directory with no go.mod anywhere above it must return "".
	nowhere := t.TempDir()
	if got := findGoModuleRoot(nowhere); got != "" {
		t.Fatalf("findGoModuleRoot(%q) = %q, want \"\"", nowhere, got)
	}
}

// TestAutoTestRunsFromModuleRoot: regression for "go.mod file not found".
// ELING is frequently started from a directory without go.mod (e.g. /root
// while the project lives in /root/eling). autoTest must locate the module
// from the touched file's path and run `go test` from the module root via
// cmd.Dir — NOT from the process CWD.
func TestAutoTestRunsFromModuleRoot(t *testing.T) {
	dir := makeModule(t, "rootrun")
	// Move the process CWD to a directory with no go.mod — the exact
	// scenario that used to break autoTest.
	nowhere := t.TempDir()
	oldWd, _ := os.Getwd()
	if err := os.Chdir(nowhere); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()

	a := helperAgent(t, 0, 300)
	testFile := filepath.Join(dir, "rootrun_test.go")
	msgs := []provider.Message{{Role: "tool", Content: testFile}}
	if got := a.autoTest(msgs); got != "" {
		t.Fatalf("autoTest from non-module CWD returned failure: %q", got)
	}
}
