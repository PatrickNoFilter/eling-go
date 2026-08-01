// Package tools — Phase 1 sandbox engine.
//
// SandboxSettings is configured once at startup (main.go) from
// config.SandboxConfig. When enabled, the bash tool:
//
//  1. runs every command in a fresh, per-invocation directory under
//     ~/.eling/sandbox/run-<ts>-<rand>/ (never the real project tree),
//  2. scrubs the environment: locked PATH, HOME pointed at the sandbox,
//     ELING_SANDBOX=1, and secrets (API keys) removed,
//  3. blocks destructive host commands unless the caller explicitly opts
//     out with `allow_host: true` on the bash tool args,
//  4. best-effort network isolation via `unshare -n` when the binary exists.
package tools

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// SandboxSettings mirrors config.SandboxConfig for the tools package.
type SandboxSettings struct {
	Enabled    bool
	Root       string
	MaxOutput  int
	TimeoutSec int
	GuardMode  string // "block" (default) or "warn"
}

var (
	sandboxMu   sync.RWMutex
	sandboxCfg  SandboxSettings
	sandboxInit bool
)

// SetSandbox configures bash sandboxing. Called once at startup from main.
func SetSandbox(s SandboxSettings) {
	sandboxMu.Lock()
	defer sandboxMu.Unlock()
	sandboxCfg = s
	sandboxInit = true
}

// SandboxEnabled reports whether the bash sandbox is active.
func SandboxEnabled() bool {
	sandboxMu.RLock()
	defer sandboxMu.RUnlock()
	if !sandboxInit {
		return false // not configured → sandbox off (safe default for tests)
	}
	return sandboxCfg.Enabled
}

// sandboxRoot returns the configured sandbox root (defaults under home).
func sandboxRoot() string {
	sandboxMu.RLock()
	defer sandboxMu.RUnlock()
	if sandboxCfg.Root != "" {
		return sandboxCfg.Root
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".eling", "sandbox")
}

func sandboxGuardMode() string {
	sandboxMu.RLock()
	defer sandboxMu.RUnlock()
	if sandboxCfg.GuardMode == "" {
		return "block"
	}
	return sandboxCfg.GuardMode
}

// newSandboxDir creates a fresh per-invocation sandbox directory and returns
// its absolute path. Callers are responsible for creating it (MkdirAll) —
// this only computes the unique name. On failure (entropy unavailable) it
// falls back to a nanosecond timestamp.
func newSandboxDir() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return filepath.Join(sandboxRoot(), fmt.Sprintf("run-%d", time.Now().UnixNano()))
	}
	return filepath.Join(sandboxRoot(), fmt.Sprintf("run-%d-%s", time.Now().Unix(), hex.EncodeToString(b)))
}

// maxSandboxDirs caps how many per-invocation sandbox dirs are kept before
// cleanup prunes the oldest ones. Prevents unbounded accumulation of
// throwaway dirs under ~/.eling/sandbox (which slows du/ls/backups).
const maxSandboxDirs = 25

// cleanupSandbox prunes old run-* sandbox dirs, keeping only the most
// recent maxSandboxDirs. Called periodically when creating a new dir.
func cleanupSandbox() {
	root := sandboxRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	type dirInfo struct {
		name string
		mod  time.Time
	}
	var dirs []dirInfo
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "run-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		dirs = append(dirs, dirInfo{name: e.Name(), mod: info.ModTime()})
	}
	if len(dirs) <= maxSandboxDirs {
		return
	}
	// Sort oldest-first and remove the excess oldest entries.
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].mod.Before(dirs[j].mod) })
	excess := dirs[:len(dirs)-maxSandboxDirs]
	for _, d := range excess {
		_ = os.RemoveAll(filepath.Join(root, d.name))
	}
}

// destructivePatterns are regular expressions matched against the raw command
// string. A match blocks execution (unless GuardMode == "warn", which only
// annotates the result). These guard the real host tree: /root, /etc, /usr,
// /home, device nodes, and fork-bombs.
var destructivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\brm\s+(-[a-zA-Z]*[rf][a-zA-Z]*\s+)*/\s*(\*\s*)?$`),                 // rm -rf /
	regexp.MustCompile(`\brm\s+-[a-zA-Z]*rf[a-zA-Z]*\s+/root\b`),                             // rm -rf /root
	regexp.MustCompile(`\brm\s+-[a-zA-Z]*rf[a-zA-Z]*\s+/home\b`),                             // rm -rf /home
	regexp.MustCompile(`\brm\s+-[a-zA-Z]*rf[a-zA-Z]*\s+/etc\b`),                              // rm -rf /etc
	regexp.MustCompile(`\brm\s+-[a-zA-Z]*rf[a-zA-Z]*\s+/usr\b`),                              // rm -rf /usr
	regexp.MustCompile(`\brm\s+-[a-zA-Z]*rf[a-zA-Z]*\s+/var\b`),                              // rm -rf /var
	regexp.MustCompile(`\brm\s+-[a-zA-Z]*rf[a-zA-Z]*\s+/s?bin\b`),                            // rm -rf /bin /sbin
	regexp.MustCompile(`\bmkfs\.\w+`),                                                        // mkfs.*
	regexp.MustCompile(`\bdd\s+[^|]*of=/dev/`),                                               // dd of=/dev/*
	regexp.MustCompile(`\bchmod\s+-R\s+[0-7]{3,4}\s+/\s*$`),                                   // chmod -R 777 /
	regexp.MustCompile(`:\(\)\s*\{\s*:\|:&\s*\}\s*;`),                                         // fork bomb
	regexp.MustCompile(`>\s*/dev/sd`),                                                        // write to raw disk
	regexp.MustCompile(`\bhalt\b|\bpoweroff\b|\breboot\b|\bshutdown\b`),                      // system shutdown
	regexp.MustCompile(`\bwget\s+[^|]*\s*\|\s*(ba)?sh\b`),                                     // curl|sh style pipes
	regexp.MustCompile(`\bcurl\s+[^|]*\s*\|\s*(ba)?sh\b`),                                     // wget|sh style pipes
}

// destructiveCommand reports whether the command matches a destructive
// pattern. Returns the matched description for messaging.
func destructiveCommand(command string) (bool, string) {
	for _, re := range destructivePatterns {
		if re.MatchString(command) {
			return true, re.String()
		}
	}
	return false, ""
}

// realHome returns the host user's home directory (used to share tool
// caches with the sandbox so Go/npm/pip don't re-download on every call).
func realHome() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return "/root"
}

// toolCacheEnv returns a map of cache env vars that must point at the REAL
// home (not the throwaway sandbox dir). Without this, every sandboxed
// `go build`, `go test`, `npm install`, or `pip install` re-downloads and
// recompiles the entire dependency tree — the #1 cause of "takes forever".
func toolCacheEnv() map[string]string {
	home := realHome()
	return map[string]string{
		"GOCACHE":         home + "/.cache/go-build",
		"GOPATH":          home + "/go",
		"GOMODCACHE":      home + "/go/pkg/mod",
		"GOTMPDIR":        home + "/.cache/go-tmp",
		"GOLANGCI_LINT_CACHE": home + "/.cache/golangci-lint",
		"npm_config_cache": home + "/.npm",
		"XDG_CACHE_HOME":  home + "/.cache",
		"PIP_CACHE_DIR":   home + "/.cache/pip",
		"CARGO_HOME":      home + "/.cargo",
		"RUSTUP_HOME":     home + "/.rustup",
	}
}

// scrubEnv builds a sandboxed environment from the current process env.
// It keeps a locked PATH (dropping user-writable entries), points HOME at
// the sandbox dir, sets ELING_SANDBOX=1, and strips API keys. Tool caches
// (GOCACHE, GOPATH, npm, pip…) are redirected to the real home so repeated
// builds stay fast instead of re-downloading dependencies each invocation.
func scrubEnv(sandboxDir string) []string {
	// Locked PATH: system dirs only — safe for a Termux/root host.
	path := "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	home := sandboxDir
	caches := toolCacheEnv()
	// Sandbox-controlled vars — these MUST win, so we both set them first
	// and skip them when copying the host env (last duplicate wins in execve).
	controlled := map[string]bool{
		"HOME":          true,
		"PATH":          true,
		"PWD":           true,
		"LANG":          true,
		"ELING_SANDBOX": true,
	}
	for k := range caches {
		controlled[k] = true
	}
	env := []string{
		"PATH=" + path,
		"HOME=" + home,
		"ELING_SANDBOX=1",
		"PWD=" + sandboxDir,
		"LANG=C.UTF-8",
	}
	for k, v := range caches {
		env = append(env, k+"="+v)
	}
	for _, kv := range os.Environ() {
		key := kv
		if i := strings.Index(kv, "="); i > 0 {
			key = kv[:i]
		}
		upper := strings.ToUpper(key)
		// Skip vars we already control — a duplicate HOME/PATH later in the
		// list would override our sandbox values (execve: last wins).
		if controlled[upper] {
			continue
		}
		// Drop secrets and anything that would leak credentials or break isolation.
		if strings.Contains(upper, "KEY") || strings.Contains(upper, "TOKEN") ||
			strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD") ||
			strings.Contains(upper, "AUTH") || strings.Contains(upper, "COOKIE") ||
			upper == "SSH_AUTH_SOCK" || strings.HasPrefix(upper, "DEEPSEEK") {
			continue
		}
		// Keep existing vars otherwise (TERM, EDITOR, etc.)
		env = append(env, kv)
	}
	return env
}

// wrapNetworkIsolation rewrites the command to run inside a network
// namespace when `unshare` is available; otherwise returns it unchanged
// (best-effort, ignore failure — the working-dir + env + guard layers still
// apply).
func wrapNetworkIsolation(command string) string {
	if _, err := exec.LookPath("unshare"); err != nil {
		return command
	}
	// unshare -n requires privileges; if it fails the command simply errors
	// and the user can retry with allow_host. Never silently drop the guard.
	return fmt.Sprintf("unshare -n bash -c %q", command)
}
