package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func init() {
	DefaultRegistry.Register(Tool{
		Name:        "create_backup",
		Description: "Create a timestamped ZIP backup of the entire eling project, excluding the compiled binary, any existing backup zips, and .bak files (rotation keeps the last 2 ZIPs).",
		Version:     "1.2.0", // registry timeout budget
		Category:    "system",
		Execute:     backupExecute,
		Timeout:     2 * time.Minute, // zipping a large tree can take a while
	})

	DefaultRegistry.Register(Tool{
		Name:        "codebase-intelligence",
		Description: "Advanced codebase analysis and intelligence skill. Leverages codebase-memory-mcp knowledge graph for: architecture discovery, call graph tracing, impact analysis, dead code detection, cross-service HTTP linking, semantic code search, and architecture decision records. Use this skill when you need deep understanding of a codebase - its structure, dependencies, data flow, and architectural patterns. The skill orchestrates multiple graph tools: search_graph, trace_path, query_graph, get_architecture, detect_changes, and more.",
		Version:     "1.1.0", // registry timeout budget
		Category:    "system",
		Execute:     codebaseIntelligenceExecute,
		Timeout:     2 * time.Minute, // multi-step graph queries
	})
}

// resolveProjectRoot determines the directory to back up.
// Priority: explicit project_dir arg > ELING_PROJECT_DIR env > walk up from
// CWD for a go.mod or .git marker. Returns an error when no project root can
// be determined, so we never zip an entire home directory by accident.
func resolveProjectRoot(args map[string]interface{}) (string, error) {
	if d, ok := args["project_dir"].(string); ok && d != "" {
		return d, nil
	}
	if d := os.Getenv("ELING_PROJECT_DIR"); d != "" {
		return d, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get cwd: %w", err)
	}
	dir := cwd
	for {
		if isProjectRoot(dir) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached filesystem root
		}
		dir = parent
	}
	return "", fmt.Errorf(
		"cannot determine project root: no go.mod or .git found from %q (pass project_dir explicitly)", cwd)
}

// isProjectRoot reports whether dir contains a go.mod or .git marker.
func isProjectRoot(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		return true
	}
	if fi, err := os.Stat(filepath.Join(dir, ".git")); err == nil && fi.IsDir() {
		return true
	}
	return false
}

// backupExecute creates a timestamped ZIP backup of the project.
// It excludes: compiled binaries (eling, eling_new, eling_rebuild),
// any existing backup zips, and common cache/vendor directories.
func backupExecute(args map[string]interface{}) (interface{}, error) {
	// Determine project root. Never default to the raw CWD: the agent
	// process may run from a home directory (e.g. /root) whose tree contains
	// hundreds of MB of unrelated data (archives, caches, other projects),
	// which makes zip crawl for hours. Resolve the actual project root by
	// walking up for go.mod/.git, and error out if none is found.
	projectDir, err := resolveProjectRoot(args)
	if err != nil {
		return nil, err
	}

	// Get absolute path for better messages
	absDir, err := filepath.Abs(projectDir)
	if err != nil {
		absDir = projectDir
	}

	backupDir := "."
	if d, ok := args["backup_dir"].(string); ok && d != "" {
		backupDir = d
	}
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return nil, fmt.Errorf("create backup dir: %w", err)
	}

	timestamp := time.Now().Format("20060102_150405")
	backupName := fmt.Sprintf("eling_backup_%s.zip", timestamp)
	backupPath := filepath.Join(backupDir, backupName)

	// Build zip command with exclusions using exec.Command with proper
	// argument separation (no shell expansion needed).
	zipArgs := []string{
		"-r", backupPath,
		".",
		"-x", "eling",
		"-x", "eling_new",
		"-x", "eling_rebuild",
		"-x", "eling-linux-*",
		"-x", "*.zip",
		"-x", "*.tar.gz",
		"-x", "*.tgz",
		"-x", "*.bak.*",
		"-x", ".git/*",
		"-x", "node_modules/*",
		"-x", "vendor/*",
		"-x", ".cache/*",
		"-x", "cache/*",
		"-x", "__pycache__/*",
		"-x", "*.pyc",
		"-x", ".DS_Store",
	}
	cmd := exec.Command("zip", zipArgs...)
	cmd.Dir = absDir
	// Start in its own process group so killProcessGroup (used by
	// KillRunningTools on timeout) can SIGKILL it even mid-crawl.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Track the subprocess so the registry's timeout guard (KillRunningTools)
	// can SIGKILL it on expiry. Without this, a zip crawling a huge tree
	// orphans and keeps running for hours after the tool call times out.
	trackCmd(cmd)
	defer untrackCmd(cmd)

	stdout := newLimitedBuffer(maxBashOutputBytes)
	stderr := newLimitedBuffer(maxBashOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		errStr := strings.TrimSpace(stderr.String())
		return nil, fmt.Errorf("backup failed: %s (exit: %v)", errStr, err)
	}

	// Get backup file size
	info, err := os.Stat(backupPath)
	size := int64(0)
	if err == nil {
		size = info.Size()
	}

	// Rotate old backup ZIPs, keeping only the most recent 2.
	rotateZipBackups(backupDir)

	return OK(map[string]interface{}{
		"backup_path": backupPath,
		"size_bytes":  size,
		"size_human":  fmtSize(size),
		"timestamp":   timestamp,
		"message":     fmt.Sprintf("Backup created: %s (%s)", backupPath, fmtSize(size)),
	}), nil
}

// codebaseIntelligenceExecute provides guidance on available code analysis
// tools that are actually registered in the system.
func codebaseIntelligenceExecute(args map[string]interface{}) (interface{}, error) {
	query, _ := args["query"].(string)
	return OK(map[string]interface{}{
		"note": "codebase-intelligence is a meta-skill that orchestrates the available code analysis tools. Use the following tools for codebase understanding:",
		"tools_available": []string{
			"grep - search for text patterns in files with regex support (uses GNU grep)",
			"read - read file contents with line limits",
			"ls - list directory contents",
			"bash - execute shell commands (git log, find, etc.)",
			"semantic_search - meaning-based search over indexed content",
			"semantic_index - add content to semantic search index",
			"web_search - search the web for external documentation",
		},
		"query_received": query,
		"hint":           "For deep codebase exploration: use grep (GNU grep) for pattern matching, semantic_search for concept discovery, and bash for git history or structural analysis.",
	}), nil
}

// escapeShellArg wraps a string in single quotes for safe shell passing.
// Single quotes inside are replaced with '\” (end quote, escaped quote, restart).
func escapeShellArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// fmtSize formats bytes to human-readable string.
func fmtSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024.0)
	}
	if bytes < 1024*1024*1024 {
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024.0*1024.0))
	}
	return fmt.Sprintf("%.1f GB", float64(bytes)/(1024.0*1024.0*1024.0))
}

// rotateZipBackups removes older eling_backup_*.zip files from backupDir,
// keeping only the most recent ELING_BACKUP_KEEP (default 2) archives.
// The newest archive (including the one just created) is always kept.
func rotateZipBackups(backupDir string) {
	keep := 2
	if s := os.Getenv("ELING_BACKUP_KEEP"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			keep = n
		}
	}

	pattern := filepath.Join(backupDir, "eling_backup_*.zip")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) <= keep {
		return
	}

	// Sort by modification time ascending (oldest first)
	sort.Slice(matches, func(i, j int) bool {
		fi, errI := os.Stat(matches[i])
		fj, errJ := os.Stat(matches[j])
		if errI != nil || errJ != nil {
			return matches[i] < matches[j]
		}
		return fi.ModTime().Before(fj.ModTime())
	})

	for _, m := range matches[:len(matches)-keep] {
		_ = os.Remove(m)
	}
}
