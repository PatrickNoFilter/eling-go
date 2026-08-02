package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runCmd runs a command in dir and fails the test on error.
func runCmd(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return cmd.Run()
}

// resultMap unwraps the Result wrapper returned by tool Execute functions
// into the underlying data map. Tools return Result{Success, Data} so
// callers must unwrap .Data before asserting on fields.
func resultMap(t *testing.T, res interface{}) map[string]interface{} {
	t.Helper()
	r, ok := res.(Result)
	if !ok {
		t.Fatalf("expected tools.Result, got %T", res)
	}
	if !r.Success {
		t.Fatalf("expected success, got error: %s", r.Error)
	}
	m, ok := r.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected data map, got %T", r.Data)
	}
	return m
}

func TestSandboxDefaultsOff(t *testing.T) {
	// Without SetSandbox, sandbox must be off (safe default) so existing
	// tests that call bashExecute directly are unaffected.
	if SandboxEnabled() {
		t.Fatal("sandbox should default to disabled before SetSandbox")
	}
}

func TestSandboxDestructiveBlock(t *testing.T) {
	dir := t.TempDir()
	SetSandbox(SandboxSettings{Enabled: true, Root: dir, GuardMode: "block"})
	t.Cleanup(func() { SetSandbox(SandboxSettings{}) })

	res, err := bashExecute(map[string]interface{}{"command": "rm -rf /root/eling"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := resultMap(t, res)
	if m["blocked"] != true {
		t.Fatalf("expected blocked=true, got %v", m)
	}
	if _, err := os.Stat("/root/eling"); err != nil {
		t.Fatalf("real tree should be untouched after block: %v", err)
	}
}

func TestSandboxAllowHost(t *testing.T) {
	dir := t.TempDir()
	SetSandbox(SandboxSettings{Enabled: true, Root: dir, GuardMode: "block"})
	t.Cleanup(func() { SetSandbox(SandboxSettings{}) })

	// allow_host: true should bypass the guard (still runs, but not blocked).
	res, err := bashExecute(map[string]interface{}{
		"command":    "echo allowed-host-run > /tmp/eling-sandbox-test-$$.txt; rm -f /tmp/eling-sandbox-test-$$.txt; echo ok",
		"allow_host": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := resultMap(t, res)
	if m["sandbox"] == true {
		t.Fatalf("allow_host run should not be sandboxed: %v", m)
	}
	if code, _ := m["exit_code"].(int); code != 0 {
		t.Fatalf("expected exit 0, got %v (%v)", code, m)
	}
}

func TestSandboxRunsIsolated(t *testing.T) {
	dir := t.TempDir()
	SetSandbox(SandboxSettings{Enabled: true, Root: dir, GuardMode: "block"})
	t.Cleanup(func() { SetSandbox(SandboxSettings{}) })

	// Command runs in the sandbox dir, not the host PWD.
	res, err := bashExecute(map[string]interface{}{
		"command": "pwd; echo \"$ELING_SANDBOX\"; echo \"$HOME\"",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := resultMap(t, res)
	stdout, _ := m["stdout"].(string)
	if !strings.Contains(stdout, dir) {
		t.Fatalf("expected sandbox dir in stdout, got: %q", stdout)
	}
	if !strings.Contains(stdout, "1") {
		t.Fatalf("expected ELING_SANDBOX=1, got: %q", stdout)
	}
	// HOME must point inside the configured sandbox root (dir), not the
	// real home. The default sandbox lives at ~/.eling/sandbox, but tests
	// redirect the root to t.TempDir() — assert against that configured root.
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/root") && !strings.HasPrefix(line, dir) {
			t.Fatalf("HOME leaked to real home: %q", line)
		}
	}
}

func TestSandboxScrubsSecrets(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEEPSEEK_API_KEY", "sk-supersecret123")
	SetSandbox(SandboxSettings{Enabled: true, Root: dir, GuardMode: "block"})
	t.Cleanup(func() { SetSandbox(SandboxSettings{}) })

	res, err := bashExecute(map[string]interface{}{
		"command": "env | grep -i key || echo NO_SECRET",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := resultMap(t, res)
	stdout, _ := m["stdout"].(string)
	if strings.Contains(stdout, "sk-supersecret123") {
		t.Fatalf("API key leaked into sandbox env: %q", stdout)
	}
	if !strings.Contains(stdout, "NO_SECRET") && !strings.Contains(stdout, "ELING_SANDBOX") {
		t.Fatalf("unexpected env output: %q", stdout)
	}
}

func TestWorktreeRoundTrip(t *testing.T) {
	// Build a tiny throwaway repo to avoid touching /root/eling.
	repo := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(runCmd(repo, "git", "init", "-q", "-b", "main"))
	must(os.WriteFile(filepath.Join(repo, "a.txt"), []byte("hello\n"), 0o644))
	must(runCmd(repo, "git", "add", "a.txt"))
	must(runCmd(repo, "git", "commit", "-q", "-m", "init"))

	// Redirect worktree root to a temp dir so tests don't pollute ~/.eling.
	oldRoot := worktreeRoot()
	t.Cleanup(func() { worktreeRootOverride = oldRoot })
	worktreeRootOverride = filepath.Join(t.TempDir(), "wts")
	os.MkdirAll(worktreeRootOverride, 0o755)

	// Point detectRepoDir at our test repo.
	oldDetect := detectRepoDirFn
	t.Cleanup(func() { detectRepoDirFn = oldDetect })
	detectRepoDirFn = func() string { return repo }

	// create
	res, err := worktreeCreate(map[string]interface{}{"name": "exp1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	m := resultMap(t, res)
	if m["branch"] != "eling/exp/exp1" {
		t.Fatalf("unexpected branch: %v", m)
	}
	wtPath, _ := m["path"].(string)
	if _, err := os.Stat(filepath.Join(wtPath, "a.txt")); err != nil {
		t.Fatalf("worktree missing file: %v", err)
	}

	// Make a change in the worktree and commit it.
	must(runCmd(wtPath, "bash", "-c", "echo change >> a.txt && git add a.txt && git commit -q -m exp"))

	// list
	res, err = worktreeList(map[string]interface{}{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	m = resultMap(t, res)
	if !strings.Contains(m["worktrees"].(string), "eling/exp/exp1") {
		t.Fatalf("worktree not listed: %v", m)
	}

	// merge back into main
	res, err = worktreeMerge(map[string]interface{}{"name": "exp1", "target": "main"})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	m = resultMap(t, res)
	if m["merged"] != "eling/exp/exp1" {
		t.Fatalf("unexpected merge result: %v", m)
	}

	// main should now contain the change
	mainFile, err := os.ReadFile(filepath.Join(repo, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mainFile), "change") {
		t.Fatalf("merge did not land in main: %q", string(mainFile))
	}

	// worktree dir should be gone after merge
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("worktree dir should be removed after merge, stat err=%v", err)
	}
}

func TestValidWorktreeName(t *testing.T) {
	for _, bad := range []string{"", "../evil", "a b", "a/b", strings.Repeat("x", 65)} {
		if ok, _ := validWorktreeName(bad); ok {
			t.Fatalf("expected invalid name: %q", bad)
		}
	}
	for _, good := range []string{"exp1", "feature.x", "fix-bug_2", "A1"} {
		if ok, _ := validWorktreeName(good); !ok {
			t.Fatalf("expected valid name: %q", good)
		}
	}
}
