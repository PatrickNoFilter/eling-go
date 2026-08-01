package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"eling/internal/layers"
)

// writeScript creates an executable shell script in a temp dir and returns
// its path. The body is wrapped in `#!/bin/sh` + `set -e` + the given lines.
func writeScript(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	content := "#!/bin/sh\nset -e\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

// TestRegisterUserHooksFiresPostToolUse verifies a script attached to
// post_tool_use actually runs and receives the hook context on stdin.
func TestRegisterUserHooksFiresPostToolUse(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	script := writeScript(t, "post.sh", `
cat > /dev/null
echo "ran" > `+marker+`
`)

	brain := layers.NewBrain()
	registered := RegisterUserHooks(brain, map[string][]string{
		layers.HookPostToolUse: {script},
	})
	if registered != 1 {
		t.Fatalf("expected 1 registered script, got %d", registered)
	}

	results := brain.FireHook(layers.HookPostToolUse, map[string]interface{}{
		"tool_name": "bash",
		"result":    "ok",
	})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("post_tool_use script did not run (marker missing): %v", err)
	}
}

// TestPreToolUseVeto verifies a script emitting {"block":true} blocks the call.
func TestPreToolUseVeto(t *testing.T) {
	script := writeScript(t, "veto.sh", `
cat > /dev/null
echo '{"block":true,"reason":"no rm -rf"}'
`)
	brain := layers.NewBrain()
	RegisterUserHooks(brain, map[string][]string{
		layers.HookPreToolUse: {script},
	})

	results := brain.FireHook(layers.HookPreToolUse, map[string]interface{}{
		"tool_name": "bash",
		"arguments": `{"cmd":"rm -rf /"}`,
	})
	blocked, reason := CheckVeto(results)
	if !blocked {
		t.Fatalf("expected veto, got results=%+v", results)
	}
	if reason != "no rm -rf" {
		t.Fatalf("expected reason %q, got %q", "no rm -rf", reason)
	}
}

// TestPreToolUseNoVeto verifies scripts that don't emit block don't veto.
func TestPreToolUseNoVeto(t *testing.T) {
	script := writeScript(t, "allow.sh", `
cat > /dev/null
echo '{"block":false}'
`)
	brain := layers.NewBrain()
	RegisterUserHooks(brain, map[string][]string{
		layers.HookPreToolUse: {script},
	})

	results := brain.FireHook(layers.HookPreToolUse, map[string]interface{}{
		"tool_name": "bash",
	})
	if blocked, reason := CheckVeto(results); blocked {
		t.Fatalf("expected no veto, got blocked=%v reason=%q", blocked, reason)
	}
}

// TestScriptContextJSON verifies the hook context is piped to stdin as JSON.
func TestScriptContextJSON(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "ctx.json")
	script := writeScript(t, "ctx.sh", `
cat > `+outFile+`
`)
	brain := layers.NewBrain()
	RegisterUserHooks(brain, map[string][]string{
		layers.HookErrorOccurred: {script},
	})

	brain.FireHook(layers.HookErrorOccurred, map[string]interface{}{
		"error":  "boom",
		"source": "test",
	})

	raw, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read ctx file: %v", err)
	}
	var ctx map[string]interface{}
	if err := json.Unmarshal(raw, &ctx); err != nil {
		t.Fatalf("ctx is not valid JSON: %v\nraw: %s", err, raw)
	}
	if ctx["error"] != "boom" {
		t.Fatalf("expected ctx.error=boom, got %v", ctx["error"])
	}
}

// TestUnknownHookIgnored verifies unknown hook names are skipped with warning.
func TestUnknownHookIgnored(t *testing.T) {
	brain := layers.NewBrain()
	script := writeScript(t, "x.sh", `echo hi`)
	registered := RegisterUserHooks(brain, map[string][]string{
		"not_a_real_hook": {script},
	})
	if registered != 0 {
		t.Fatalf("expected 0 registered for unknown hook, got %d", registered)
	}
}

// TestMissingScriptDoesNotCrash verifies a missing script path logs and
// returns, never panics.
func TestMissingScriptDoesNotCrash(t *testing.T) {
	brain := layers.NewBrain()
	RegisterUserHooks(brain, map[string][]string{
		layers.HookPostToolUse: {filepath.Join(t.TempDir(), "does-not-exist.sh")},
	})

	// Must not panic, must not return a veto.
	results := brain.FireHook(layers.HookPostToolUse, map[string]interface{}{
		"tool_name": "bash",
	})
	if blocked, _ := CheckVeto(results); blocked {
		t.Fatalf("missing script must not veto, results=%+v", results)
	}
}

// TestCheckVetoNilResults is a defensive test: nil/empty results → no veto.
func TestCheckVetoNilResults(t *testing.T) {
	if blocked, reason := CheckVeto(nil); blocked || reason != "" {
		t.Fatalf("nil results must not veto, got blocked=%v reason=%q", blocked, reason)
	}
}
