// Package hooks bridges ELING's internal lifecycle-hook system to
// user-defined shell scripts (Phase 5 of the qwen feature steal).
//
// Users configure scripts in config.yaml under:
//
//	hooks:
//	  scripts:
//	    post_tool_use: ["/path/to/script.sh"]
//	    error_occurred: ["/root/notify-error.sh"]
//
// For every registered script, this package registers a layers.HookHandler
// that executes the script with the hook context as JSON on stdin:
//
//	{"tool_name":"bash","arguments":"{...}","result":"...","duration_ms":123}
//
// Scripts run with a 5s timeout; stderr is captured and logged. Failures
// NEVER crash the agent (mirrors the recover pattern in fireHook).
//
// Pre-tool gate: for HookPreToolUse, a script may emit on stdout:
//
//	{"block": true, "reason": "policy: no rm -rf"}
//
// and the tool call is vetoed before execution (see CheckVeto).
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os/exec"
	"strings"
	"time"

	"eling/internal/layers"
)

// scriptTimeout bounds each user hook script execution.
const scriptTimeout = 5 * time.Second

// maxScriptOutput caps captured stdout (avoid unbounded memory).
const maxScriptOutput = 64 * 1024

// VetoResult is the shape a pre_tool_use script may emit to block a call.
type VetoResult struct {
	Block  bool   `json:"block"`
	Reason string `json:"reason"`
}

// RegisterUserHooks wires user-configured shell scripts into the Brain's
// hook registry. Each script gets its own HookHandler closure so a slow or
// failing script only affects itself. Unknown hook names log a warning.
// Returns the number of scripts registered.
func RegisterUserHooks(brain *layers.Brain, scripts map[string][]string) int {
	if brain == nil || len(scripts) == 0 {
		return 0
	}
	registered := 0
	for hookName, paths := range scripts {
		for _, path := range paths {
			path := strings.TrimSpace(path)
			if path == "" {
				continue
			}
			// Validate hook name against the canonical list so a typo in
			// config.yaml logs a warning instead of silently never firing.
			if !isKnownHook(hookName) {
				log.Printf("[hooks] warning: unknown hook %q in config (script %s) — ignored", hookName, path)
				continue
			}
			scriptPath := path // capture for closure
			brain.RegisterHook(hookName, func(name string, ctx map[string]interface{}) interface{} {
				return runScript(name, scriptPath, ctx)
			})
			registered++
			log.Printf("[hooks] registered user script %q for hook %q", scriptPath, hookName)
		}
	}
	return registered
}

// CheckVeto inspects the results returned by Brain.FireHook for a
// pre_tool_use event. If any user script emitted {"block":true}, returns
// blocked=true with the first non-empty reason. Used by the agent to gate
// tool execution (Phase 5 pre-tool gate).
func CheckVeto(results []interface{}) (blocked bool, reason string) {
	for _, r := range results {
		m, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		b, _ := m["block"].(bool)
		if !b {
			continue
		}
		rs, _ := m["reason"].(string)
		if rs != "" {
			return true, rs
		}
		return true, "blocked by user pre_tool_use hook"
	}
	return false, ""
}

// isKnownHook reports whether name is one of the canonical lifecycle hooks.
func isKnownHook(name string) bool {
	for _, h := range layers.AllHooks {
		if h == name {
			return true
		}
	}
	return false
}

// runScript executes one user hook script with the hook context JSON piped
// to stdin. stdout (if any) is parsed as a VetoResult when the hook is
// pre_tool_use; otherwise it is logged at debug level and ignored. All
// failures are logged and swallowed — a hook must never crash the agent.
func runScript(hookName, scriptPath string, ctx map[string]interface{}) interface{} {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[hooks] user script %q for %q panicked: %v (caught)", scriptPath, hookName, r)
		}
	}()

	payload, err := json.Marshal(ctx)
	if err != nil {
		payload = []byte("{}")
	}

	cctx, cancel := context.WithTimeout(context.Background(), scriptTimeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, scriptPath)
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Timeout and non-zero exits are logged, not fatal.
		log.Printf("[hooks] script %q for %q error: %v (stderr: %s)",
			scriptPath, hookName, err, strings.TrimSpace(truncate(stderr.String())))
		return map[string]interface{}{"ran": false, "error": err.Error()}
	}

	out := truncate(stdout.String())
	if hookName == layers.HookPreToolUse && strings.TrimSpace(out) != "" {
		var vr VetoResult
		if json.Unmarshal([]byte(out), &vr) == nil && vr.Block {
			return map[string]interface{}{"block": true, "reason": vr.Reason}
		}
		// Non-JSON output from a pre_tool_use script: treat as informational.
		if len(out) > 0 {
			log.Printf("[hooks] pre_tool_use script %q stdout (ignored): %s", scriptPath, strings.TrimSpace(out))
		}
	}
	return map[string]interface{}{"ran": true, "hook": hookName}
}

// truncate caps s to maxScriptOutput bytes without splitting UTF-8 runes.
func truncate(s string) string {
	if len(s) <= maxScriptOutput {
		return s
	}
	b := []byte(s)
	cut := maxScriptOutput
	for cut > 0 && b[cut]&0xC0 == 0x80 {
		cut--
	}
	return string(b[:cut]) + "\n... [truncated]"
}
