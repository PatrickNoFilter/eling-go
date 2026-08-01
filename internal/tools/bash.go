package tools

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// maxBashOutputBytes caps the total stdout+stderr captured from a single
// bash command to prevent OOM from runaway output.
const maxBashOutputBytes = 512 * 1024 // 512 KiB

// limitedBuffer wraps bytes.Buffer with a hard size limit. Writes beyond
// the limit are silently discarded to prevent OOM.
type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func newLimitedBuffer(limit int) *limitedBuffer {
	return &limitedBuffer{limit: limit}
}

func (lb *limitedBuffer) Write(p []byte) (int, error) {
	// If already at or over limit, discard
	if lb.buf.Len() >= lb.limit {
		return len(p), nil
	}
	// Only write up to the limit
	remaining := lb.limit - lb.buf.Len()
	if len(p) > remaining {
		p = p[:remaining]
	}
	return lb.buf.Write(p)
}

func (lb *limitedBuffer) String() string {
	return lb.buf.String()
}

func (lb *limitedBuffer) Len() int {
	return lb.buf.Len()
}

// runningCmds tracks currently-executing bash commands so they can be killed
// externally (e.g., on Ctrl+C interrupt).
var (
	runningCmdsMu sync.Mutex
	runningCmds   []*exec.Cmd
)

// KillRunningTools kills all currently-running bash subprocesses and their
// entire process groups (grandchildren included — a plain Process.Kill only
// kills the direct child, orphaning `du`/`find`/`go` subprocesses).
func KillRunningTools() {
	runningCmdsMu.Lock()
	defer runningCmdsMu.Unlock()
	for _, cmd := range runningCmds {
		if cmd != nil && cmd.Process != nil {
			killProcessGroup(cmd)
		}
	}
	runningCmds = nil
}

// killProcessGroup sends SIGKILL to the process group rooted at cmd.
// cmd is started with Setpgid: true, so the group id equals its PID.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	// Negative PID targets the whole process group (including descendants).
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	// Fallback: kill the direct child too (in case group kill failed).
	_ = cmd.Process.Kill()
}

func trackCmd(cmd *exec.Cmd) {
	runningCmdsMu.Lock()
	defer runningCmdsMu.Unlock()
	runningCmds = append(runningCmds, cmd)
}

func untrackCmd(cmd *exec.Cmd) {
	runningCmdsMu.Lock()
	defer runningCmdsMu.Unlock()
	for i, c := range runningCmds {
		if c == cmd {
			runningCmds = append(runningCmds[:i], runningCmds[i+1:]...)
			return
		}
	}
}

func init() {
	bashTool := Tool{
		Name:        "bash",
		Description: "Execute a bash command and return its output. Use for running scripts, compilation, file operations, git, etc.",
		Version:     "1.0.0",
		Category:    "system",
		Execute:     bashExecute,
	}
	DefaultRegistry.Register(bashTool)
}

// bashExecute runs a bash command with timeout protection.
// Phase 1: when the sandbox is enabled (default), commands run in an
// isolated per-invocation directory with a scrubbed environment and a
// destructive-command guard. Passing `allow_host: true` opts out of the
// sandbox for commands that must touch the real tree (git add, rebuild.sh).
func bashExecute(args map[string]interface{}) (interface{}, error) {
	command, _ := args["command"].(string)
	if command == "" {
		return Err("command is required"), nil
	}

	allowHost, _ := args["allow_host"].(bool)
	if v, ok := args["allow_host"].(string); ok {
		allowHost = v == "true" || v == "1" || v == "yes"
	}

	timeoutSec := 30
	if n, ok := args["timeout"].(float64); ok {
		timeoutSec = int(n)
	}
	if n, ok := args["timeout_sec"].(float64); ok {
		timeoutSec = int(n)
	}

	dir, _ := args["working_dir"].(string)
	if dir == "" {
		dir, _ = args["dir"].(string)
	}

	// ── Phase 1 sandbox ────────────────────────────────────────────────────
	sandboxed := false
	sandboxDir := ""
	var env []string
	if SandboxEnabled() && !allowHost {
		// Destructive-command guard: block (or warn) before anything runs.
		if bad, why := destructiveCommand(command); bad {
			if sandboxGuardMode() == "warn" {
				command = fmt.Sprintf("echo '[sandbox-warn] potentially destructive command matched: %s'; %s", why, command)
			} else {
				return OK(map[string]interface{}{
					"exit_code": -1,
					"stdout":    "",
					"stderr":    fmt.Sprintf("[sandbox] blocked: command matches destructive pattern %s\nUse allow_host: true to run against the real tree.", why),
					"command":   command,
					"blocked":   true,
					"sandbox":   true,
				}), nil
			}
		}
		// Fresh per-invocation directory.
		sandboxDir = newSandboxDir()
		if err := os.MkdirAll(sandboxDir, 0o755); err != nil {
			return Err("sandbox: create sandbox dir: " + err.Error()), nil
		}
		// Opportunistically prune stale sandbox dirs so they don't pile up.
		cleanupSandbox()
		if dir == "" {
			dir = sandboxDir // commands default into the sandbox
		}
		env = scrubEnv(sandboxDir)
		command = wrapNetworkIsolation(command)
		sandboxed = true
	}
	// ───────────────────────────────────────────────────────────────────────

	// Create command
	cmd := exec.Command("bash", "-c", command)
	if dir != "" {
		cmd.Dir = dir
	}
	if env != nil {
		cmd.Env = env
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	trackCmd(cmd)
	defer untrackCmd(cmd)

	// Capture stdout and stderr with size limits
	stdout := newLimitedBuffer(maxBashOutputBytes)
	stderr := newLimitedBuffer(maxBashOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	// Run with timeout
	done := make(chan error, 1)
	go func() {
		done <- cmd.Run()
	}()

	timer := time.NewTimer(time.Duration(timeoutSec) * time.Second)
	defer timer.Stop()

	select {
	case err := <-done:
		exitCode := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				return nil, fmt.Errorf("bash execution failed: %w", err)
			}
		}

		stdoutStr := strings.TrimSpace(stdout.String())
		stderrStr := strings.TrimSpace(stderr.String())
		if stdout.Len() >= maxBashOutputBytes {
			stdoutStr += "\n... [stdout truncated at 512 KiB]"
		}
		if stderr.Len() >= maxBashOutputBytes {
			stderrStr += "\n... [stderr truncated at 512 KiB]"
		}

		result := map[string]interface{}{
			"exit_code": exitCode,
			"stdout":    stdoutStr,
			"stderr":    stderrStr,
			"command":   command,
			"sandbox":   sandboxed,
		}
		if sandboxed {
			result["sandbox_dir"] = sandboxDir
		}

		return OK(result), nil

	case <-timer.C:
		// Kill the entire process group (not just the direct child) so
		// grandchildren like `du`, `find`, `go` don't keep running.
		killProcessGroup(cmd)
		stdoutStr := strings.TrimSpace(stdout.String())
		stderrStr := strings.TrimSpace(stderr.String())
		if stdout.Len() >= maxBashOutputBytes {
			stdoutStr += "\n... [stdout truncated at 512 KiB]"
		}
		if stderr.Len() >= maxBashOutputBytes {
			stderrStr += "\n... [stderr truncated at 512 KiB]"
		}
		result := map[string]interface{}{
			"exit_code": -1,
			"stdout":    stdoutStr,
			"stderr":    stderrStr + fmt.Sprintf("\n[command timed out after %d seconds]", timeoutSec),
			"command":   command,
			"timed_out": true,
			"sandbox":   sandboxed,
		}
		if sandboxed {
			result["sandbox_dir"] = sandboxDir
		}
		return OK(result), nil
	}
}
