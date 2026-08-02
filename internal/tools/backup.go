package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func init() {
	DefaultRegistry.Register(Tool{
		Name:        "create_backup",
		Description: "Create a timestamped ZIP backup of the entire eling project, excluding the compiled binary and any existing backup zips.",
		Version:     "1.1.0", // registry timeout budget
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

// backupExecute creates a timestamped ZIP backup of the project.
// It excludes: compiled binaries (eling, eling_new, eling_rebuild),
// any existing backup zips, and common cache/vendor directories.
func backupExecute(args map[string]interface{}) (interface{}, error) {
	// Determine project root: default to current directory
	projectDir := "."
	if d, ok := args["project_dir"].(string); ok && d != "" {
		projectDir = d
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
		"-x", "*.zip",
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
