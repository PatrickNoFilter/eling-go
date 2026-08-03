package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// fileLocks serializes read-modify-write cycles per absolute file path.
// Both editExecute and writeExecute take the lock for their whole
// read → backup → write sequence so two concurrent tool calls (parallel
// turns, hooks, or dynamic tools) can never interleave and corrupt a file.
// Keyed by absolute path to avoid alias collisions (./a vs a).
var fileLocks sync.Map // absPath -> *sync.Mutex

// lockFile returns an unlock func for the given path's per-file mutex.
func lockFile(path string) func() {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	v, _ := fileLocks.LoadOrStore(abs, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// sha256Hex returns the lowercase hex SHA-256 of data. Used as the
// hash-anchor for source_hash verification on edits.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func init() {
	// Register read tool
	DefaultRegistry.Register(Tool{
		Name:        "read",
		Description: "Read the contents of a file. Supports text files, JSON, and source code.",
		Version:     "1.1.0", // timeout-aware + 64 MiB size guard
		Category:    "system",
		Execute:     readExecute,
		ExecuteCtx:  readExecuteCtx,
		Timeout:     readToolTimeout,
	})

	// Register write tool
	DefaultRegistry.Register(Tool{
		Name:        "write",
		Description: "Write content to a file, creating directories if needed. Overwrites existing files. Automatically creates a timestamped .bak backup of the existing file before overwriting (rotation keeps the last 5 backups per file).",
		Version:     "1.1.0",
		Category:    "system",
		Execute:     writeExecute,
		Timeout:     fileToolTimeout,
	})

	// Register edit tool (like jcode's edit)
	DefaultRegistry.Register(Tool{
		Name:        "edit",
		Description: "Replace specific text in a file with new text. Uses exact string matching (not regex). Specify old_string and new_string. Optionally pass occurrence (1-based, default 1) to pick which match to replace when old_string appears multiple times, and source_hash (the 'hash' value returned by the read tool) to abort the edit if the file changed since it was read — a mismatch returns both hashes so you can re-read and retry. Automatically creates a timestamped .bak backup before applying the edit (rotation keeps the last 5 backups per file).",
		Version:     "1.2.0", // hash-anchored edits + occurrence targeting + per-file lock
		Category:    "system",
		Execute:     editExecute,
		Timeout:     fileToolTimeout,
	})

	// Register ls tool (like jcode's ls)
	DefaultRegistry.Register(Tool{
		Name:        "ls",
		Description: "List directory contents with file sizes and metadata.",
		Version:     "1.0.0",
		Category:    "system",
		Execute:     lsExecute,
		Timeout:     fileToolTimeout,
	})

	// Register the preferred ugrep search tool. The legacy "grep" tool below
	// is a deprecated alias for the same engine — the agent prompt enforces
	// calling `ugrep` for all text searches.
	DefaultRegistry.Register(Tool{
		Name:        "ugrep",
		Description: "Search for text patterns in files using ugrep 7.5.0 (preferred text-search tool — executes ugrep directly; the legacy 'grep' tool is a deprecated alias for this same engine). Supports recursive directory search, regex, file-type filtering (-t), fuzzy search (-Z), word boundary (-w), fixed strings (-F), smart case (-S), multiline (-U), JSON/CSV output (--json/--csv), and boolean operators (--bool). Standard grep flags (-rn, -I, -m, --exclude-dir, --include) work unchanged. Prefer this tool over bash grep for all code search.",
		Version:     "1.1.0",
		Category:    "system",
		Execute:     grepExecute,
		Timeout:     grepToolTimeout,
	})

	// Register grep tool — DEPRECATED alias for the ugrep tool (same engine).
	DefaultRegistry.Register(Tool{
		Name:        "grep",
		Description: "DEPRECATED alias for the `ugrep` tool (executes the same ugrep 7.5.0 engine via the /usr/local/bin/grep wrapper). Use the `ugrep` tool instead — this name is kept only for backward compatibility.",
		Version:     "1.1.0",
		Category:    "system",
		Execute:     grepExecute,
		Timeout:     grepToolTimeout,
	})
}

// Tool timeout budgets for file operations. These are enforced by the
// registry's ExecuteContext; the read tool additionally cancels mid-read via
// its ExecuteCtx variant (turn deadline / Ctrl+C abort immediately).
const (
	fileToolTimeout = 15 * time.Second // write / edit / ls — local disk ops
	readToolTimeout = 20 * time.Second // read — allows large-file streaming
	grepToolTimeout = 20 * time.Second // grep — already has 10s internal ctx
)

// maxReadBytes caps a single read to 64 MiB. Reading a multi-GB log into RAM
// is both slow (the user's complaint) and OOM-risky on 8 GB phones. Files
// larger than this get an actionable error instead of a hang.
const maxReadBytes = 64 << 20 // 64 MiB

func readExecute(args map[string]interface{}) (interface{}, error) {
	return readExecuteCtx(context.Background(), args)
}

// readExecuteCtx is the context-aware read. It enforces the same behaviour as
// readExecute but can be aborted the moment the caller's context is cancelled
// (turn deadline, Ctrl+C, or the registry's readToolTimeout budget) — even
// when os.ReadFile is blocked on a slow filesystem or special file.
func readExecuteCtx(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	path, _ := args["file_path"].(string)
	if path == "" {
		path, _ = args["path"].(string)
	}
	if path == "" {
		return Err("path is required"), nil
	}

	// Timeout strategy part 1: refuse oversized files BEFORE reading them so
	// we never spend minutes slurping a multi-GB log into RAM. The cap is
	// generous (64 MiB) — plenty for source/config/docs.
	if fi, err := os.Stat(path); err == nil {
		if fi.Mode().IsRegular() && fi.Size() > maxReadBytes {
			return nil, fmt.Errorf("read %s: file is %d bytes (%.1f MiB), exceeding the %d MiB safety cap; "+
				"use grep/ls or a smaller max_lines instead", path, fi.Size(),
				float64(fi.Size())/(1<<20), maxReadBytes>>20)
		}
	}

	type readResult struct {
		result interface{}
		err    error
	}
	done := make(chan readResult, 1)
	go func() {
		res, e := readExecuteInner(args)
		done <- readResult{res, e}
	}()

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("read %s aborted: %v", path, ctx.Err())
	case d := <-done:
		return d.result, d.err
	}
}

// readExecuteInner holds the original read implementation (arg parsing, line
// slicing, header building). It is invoked under the context guard so it can
// be aborted if the turn deadline or tool budget expires mid-read.
func readExecuteInner(args map[string]interface{}) (interface{}, error) {
	path, _ := args["file_path"].(string)
	if path == "" {
		path, _ = args["path"].(string)
	}
	if path == "" {
		return Err("path is required"), nil
	}

	// Parse max_lines
	maxLines := 5000
	if n, ok := args["max_lines"].(float64); ok {
		maxLines = int(n)
	} else if s, ok := args["max_lines"].(string); ok {
		fmt.Sscanf(s, "%d", &maxLines)
	}
	if maxLines < 1 {
		maxLines = 1
	}

	// Parse start_line (0-indexed)
	startLine := 0
	if n, ok := args["start_line"].(float64); ok {
		startLine = int(n)
	} else if s, ok := args["start_line"].(string); ok {
		fmt.Sscanf(s, "%d", &startLine)
	}
	if startLine < 0 {
		startLine = 0
	}

	// Read entire file
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file %s: %w", path, err)
	}

	// Split into lines
	allLines := strings.Split(string(data), "\n")
	totalFileLines := len(allLines)

	// Clamp startLine
	if startLine >= totalFileLines {
		startLine = totalFileLines - 1
		if startLine < 0 {
			startLine = 0
		}
	}

	// Calculate range
	fromLine := startLine
	toLine := fromLine + maxLines
	if toLine > totalFileLines {
		toLine = totalFileLines
	}

	// Extract the requested lines
	var contentLines []string
	if fromLine < totalFileLines {
		contentLines = allLines[fromLine:toLine]
	} else {
		contentLines = []string{}
	}

	contentStr := strings.Join(contentLines, "\n")
	linesReturned := len(contentLines)

	// Build header
	startDisplay := startLine + 1 // convert to 1-indexed for display
	if startDisplay < 1 {
		startDisplay = 1
	}
	endDisplay := startLine + linesReturned
	if endDisplay > totalFileLines {
		endDisplay = totalFileLines
	}

	header := fmt.Sprintf("[Showing lines %d-%d of %d total lines]", startDisplay, endDisplay, totalFileLines)
	if startLine > 0 {
		header += " (skipped first " + fmt.Sprintf("%d lines", startLine) + ")"
	}
	if totalFileLines == 0 {
		header = "[File is empty]"
	} else if startLine >= totalFileLines {
		// This shouldn't happen due to clamping above, but just in case
		header = fmt.Sprintf("[File has %d total lines, start_line=%d is beyond end]", totalFileLines, startLine)
		contentStr = ""
	} else if totalFileLines-startLine > maxLines {
		contentStr += fmt.Sprintf("\n... [truncated at %d lines, %d lines after start_line]", maxLines, totalFileLines-startLine)
	}
	contentStr = header + "\n" + contentStr

	return OK(map[string]interface{}{
		"path":    path,
		"content": contentStr,
		"lines":   linesReturned,
		"size":    len(data),
		// hash anchors the file content as seen by the model. Pass it back as
		// edit's source_hash to abort the edit if the file drifted in between
		// (parallel edit, hook, external process) — see editExecute.
		"hash": sha256Hex(data),
	}), nil
}

func writeExecute(args map[string]interface{}) (interface{}, error) {
	path, _ := args["file_path"].(string)
	if path == "" {
		path, _ = args["path"].(string)
	}
	if path == "" {
		return Err("path is required"), nil
	}
	// Serialize with edits on the same file (read → backup → write must be atomic).
	unlock := lockFile(path)
	defer unlock()

	content, _ := args["content"].(string)

	// Create directories if needed
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create directories: %w", err)
	}

	// No-op guard: if the file already has identical content, skip backup + write
	if existing, err := os.ReadFile(path); err == nil && string(existing) == content {
		return OK(map[string]interface{}{
			"path":      path,
			"written":   0,
			"unchanged": true,
			"backup":    "",
		}), nil
	}

	// Automatic backup-before-write: snapshot the existing file
	backupPath, err := backupFile(path)
	if err != nil {
		return nil, fmt.Errorf("backup before write: %w", err)
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}

	return OK(map[string]interface{}{
		"path":    path,
		"written": len(content),
		"backup":  backupPath,
	}), nil
}

// isTextFile checks whether data looks like a text file by testing if it's
// valid UTF-8 and contains no null bytes. This prevents the edit tool from
// corrupting binary files (images, compiled binaries, etc.).
func isTextFile(data []byte) bool {
	// If it contains null bytes, it's definitely binary
	if bytes.IndexByte(data, 0) != -1 {
		return false
	}
	// Valid UTF-8 is required for string replacement to work safely
	return utf8.Valid(data)
}

func editExecute(args map[string]interface{}) (interface{}, error) {
	path, _ := args["file_path"].(string)
	if path == "" {
		path, _ = args["path"].(string)
	}
	if path == "" {
		return Err("path is required"), nil
	}

	oldStr, _ := args["old_string"].(string)
	newStr, _ := args["new_string"].(string)
	sourceHash, _ := args["source_hash"].(string)

	if oldStr == "" {
		return Err("old_string is required"), nil
	}

	// occurrence: 1-based index of which match to replace (default 1).
	// Kills the old strings.Replace(...,1) first-match ambiguity: when
	// old_string appears N times the model can target exactly the one it
	// means instead of gambling on position.
	occurrence := 1
	switch v := args["occurrence"].(type) {
	case float64:
		occurrence = int(v)
	case string:
		fmt.Sscanf(v, "%d", &occurrence)
	}
	if occurrence < 1 {
		occurrence = 1
	}

	// Serialize with other edits/writes on the same file so the whole
	// read → verify → replace → backup → write cycle is atomic.
	unlock := lockFile(path)
	defer unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	if !isTextFile(data) {
		return Err(fmt.Sprintf("refusing to edit binary file: %s", path)), nil
	}

	content := string(data)

	// Hash-anchor verification (optional source_hash): the model passes the
	// "hash" it got from read. If the file changed since then (parallel edit,
	// hook, external process), abort with both hashes so the model can
	// re-read and self-correct instantly instead of silently editing stale
	// content or burning retry loops.
	if sourceHash != "" {
		actual := sha256Hex(data)
		if !strings.EqualFold(actual, sourceHash) {
			return Err(fmt.Sprintf(
				"source_hash mismatch for %s: expected %s, computed %s (file changed since it was read). Re-read the file and retry with the new hash.",
				path, sourceHash, actual)), nil
		}
	}

	total := strings.Count(content, oldStr)
	if total == 0 {
		// Drift hint: if only whitespace/line-endings differ, say so — the
		// model re-copies exact bytes instead of retrying the same string.
		if hint := whitespaceNormalizedHint(content, oldStr); hint != "" {
			return Err(fmt.Sprintf("old_string not found in %s. %s", path, hint)), nil
		}
		return Err(fmt.Sprintf("old_string not found in %s", path)), nil
	}
	if occurrence > total {
		return Err(fmt.Sprintf(
			"old_string occurs %d time(s) in %s but occurrence=%d was requested (max %d). Use occurrence 1..%d or a more specific old_string.",
			total, path, occurrence, total, total)), nil
	}

	newContent, ok := replaceNth(content, oldStr, newStr, occurrence)
	if !ok {
		return Err(fmt.Sprintf("occurrence %d of old_string not found in %s", occurrence, path)), nil
	}

	// Generate unified diff
	diff := buildDiff(path, content, newContent, oldStr, newStr)

	// Automatic backup-before-edit: snapshot the original file
	backupPath, err := backupFile(path)
	if err != nil {
		return nil, fmt.Errorf("backup before edit: %w", err)
	}

	if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}

	return OK(map[string]interface{}{
		"path":             path,
		"edited":           true,
		"changes":          1,
		"occurrence":       occurrence,
		"total_occurrences": total,
		"diff":             diff,
		"backup":           backupPath,
		// New file hash — pass it as source_hash on the next edit to chain
		// edits without a re-read round-trip.
		"hash": sha256Hex([]byte(newContent)),
	}), nil
}

// replaceNth replaces only the n-th (1-based) occurrence of old in s.
// Returns ok=false if the n-th occurrence does not exist.
func replaceNth(s, old, new string, n int) (string, bool) {
	if n < 1 {
		n = 1
	}
	start := 0
	found := 0
	for {
		i := strings.Index(s[start:], old)
		if i < 0 {
			return s, false
		}
		abs := start + i
		found++
		if found == n {
			return s[:abs] + new + s[abs+len(old):], true
		}
		start = abs + len(old)
	}
}

// whitespaceNormalizedHint returns an actionable hint when oldStr is absent
// from content but present after stripping all whitespace (the most common
// drift cause: tabs vs spaces, CRLF vs LF, wrapped lines). Empty string
// means no normalized match was found.
func whitespaceNormalizedHint(content, oldStr string) string {
	strip := func(s string) string {
		var b strings.Builder
		b.Grow(len(s))
		for _, r := range s {
			switch r {
			case ' ', '\t', '\n', '\r':
			default:
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	if strip(oldStr) == "" {
		return ""
	}
	if strings.Contains(strip(content), strip(oldStr)) {
		return "old_string differs from the file only in whitespace/line endings. Re-copy the exact bytes from the read tool output (watch tabs vs spaces and CRLF vs LF) and retry."
	}
	return ""
}

// backupFile creates a timestamped snapshot of path before it is modified.
// Returns the backup path, or "" if the file does not exist (nothing to back up).
//
// Default location: same directory as the source file, named <path>.bak.<timestamp>
// (e.g. files.go.bak.20260801_120000).
//
// If the ELING_BACKUP_DIR environment variable is set, backups are mirrored under
// that central directory using the absolute path of the source file.
//
// Rotation: only the most recent ELING_BACKUP_KEEP backups (default 5) per source
// file are retained; older ones are deleted automatically.
func backupFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil // nothing to back up
		}
		return "", fmt.Errorf("read file for backup: %w", err)
	}

	ts := time.Now().Format("20060102_150405")
	backupPath := fmt.Sprintf("%s.bak.%s", path, ts)

	if dir := os.Getenv("ELING_BACKUP_DIR"); dir != "" {
		abs, err := filepath.Abs(path)
		if err != nil {
			abs = path
		}
		rel := strings.TrimPrefix(abs, string(filepath.Separator))
		backupPath = filepath.Join(dir, rel+".bak."+ts)
		if err := os.MkdirAll(filepath.Dir(backupPath), 0755); err != nil {
			return "", fmt.Errorf("create backup dir: %w", err)
		}
	}

	if err := os.WriteFile(backupPath, data, 0644); err != nil {
		return "", fmt.Errorf("write backup: %w", err)
	}

	rotateBackups(path, backupPath)
	return backupPath, nil
}

// rotateBackups removes the oldest backups for a source file, keeping only the
// most recent ELING_BACKUP_KEEP (default 5) snapshots.
func rotateBackups(originalPath, backupPath string) {
	keep := 5
	if s := os.Getenv("ELING_BACKUP_KEEP"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			keep = n
		}
	}

	// Build a glob pattern matching all backups of this source file
	var pattern string
	if dir := os.Getenv("ELING_BACKUP_DIR"); dir != "" {
		// Central dir mode: backupPath = <dir>/<rel>.bak.<ts>
		base := filepath.Base(backupPath)
		if idx := strings.LastIndex(base, ".bak."); idx >= 0 {
			base = base[:idx]
		}
		pattern = filepath.Join(filepath.Dir(backupPath), base+".bak.*")
	} else {
		pattern = originalPath + ".bak.*"
	}

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

// buildDiff uses diff -u to create a unified diff between old and new content.
// Falls back to a simple inline diff if the diff command is not available.
func buildDiff(path, before, after, _, _ string) string {
	// Check if diff command is available
	diffPath, err := exec.LookPath("diff")
	if err != nil {
		return fallbackDiff(before, after)
	}

	// Write before to temp file
	tmpDir, err := os.MkdirTemp("", "eling-diff-*")
	if err != nil {
		return fmt.Sprintf("error creating temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	oldPath := filepath.Join(tmpDir, "old")
	newPath := filepath.Join(tmpDir, "new")

	if err := os.WriteFile(oldPath, []byte(before), 0644); err != nil {
		return fmt.Sprintf("error writing temp: %v", err)
	}
	if err := os.WriteFile(newPath, []byte(after), 0644); err != nil {
		return fmt.Sprintf("error writing temp: %v", err)
	}

	out, err := exec.Command(diffPath, "-u", oldPath, newPath).CombinedOutput()
	if err != nil {
		// diff returns exit 1 when files differ, that's expected
		if len(out) == 0 {
			return "no changes"
		}
	}
	return strings.TrimSpace(string(out))
}

// fallbackDiff generates a simple line-based diff without an external diff command.
func fallbackDiff(before, after string) string {
	beforeLines := strings.Split(before, "\n")
	afterLines := strings.Split(after, "\n")

	var sb strings.Builder
	sb.WriteString("--- a\n+++ b\n")

	maxLen := len(beforeLines)
	if len(afterLines) > maxLen {
		maxLen = len(afterLines)
	}

	lineStart := -1
	for i := 0; i < maxLen; i++ {
		var bLine, aLine string
		if i < len(beforeLines) {
			bLine = beforeLines[i]
		}
		if i < len(afterLines) {
			aLine = afterLines[i]
		}
		if bLine != aLine {
			if lineStart == -1 {
				lineStart = i
			}
			// Continue until we find matching lines again
		} else if lineStart >= 0 {
			// Print the diff chunk
			sb.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", lineStart+1, i-lineStart, lineStart+1, i-lineStart))
			for j := lineStart; j < i; j++ {
				if j < len(beforeLines) {
					sb.WriteString("-" + beforeLines[j] + "\n")
				}
				if j < len(afterLines) {
					sb.WriteString("+" + afterLines[j] + "\n")
				}
			}
			lineStart = -1
		}
	}

	// Handle trailing diff chunk
	if lineStart >= 0 {
		sb.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", lineStart+1, maxLen-lineStart, lineStart+1, maxLen-lineStart))
		for j := lineStart; j < maxLen; j++ {
			if j < len(beforeLines) {
				sb.WriteString("-" + beforeLines[j] + "\n")
			}
			if j < len(afterLines) {
				sb.WriteString("+" + afterLines[j] + "\n")
			}
		}
	}

	result := sb.String()
	if result == "--- a\n+++ b\n" {
		return "no changes"
	}
	return strings.TrimSpace(result)
}

func lsExecute(args map[string]interface{}) (interface{}, error) {
	path, _ := args["path"].(string)
	if path == "" {
		path = "."
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("list directory: %w", err)
	}

	var files []map[string]interface{}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, map[string]interface{}{
			"name":    entry.Name(),
			"dir":     entry.IsDir(),
			"size":    info.Size(),
			"mode":    info.Mode().String(),
			"modtime": info.ModTime().Format("2006-01-02 15:04:05"),
		})
	}

	return OK(map[string]interface{}{
		"path":  path,
		"files": files,
		"count": len(files),
	}), nil
}

func grepExecute(args map[string]interface{}) (interface{}, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return Err("query is required"), nil
	}

	path, _ := args["path"].(string)
	if path == "" {
		path = "."
	}

	fileType, _ := args["type"].(string)
	isRegex := false
	if b, ok := args["regex"].(bool); ok {
		isRegex = b
	}

	maxResults := 50
	if n, ok := args["max_results"].(float64); ok {
		maxResults = int(n)
	}

	// Prefer the ugrep binary directly — the /usr/local/bin/grep wrapper also
	// prefers ugrep, but resolving ugrep explicitly guarantees every search
	// runs through ugrep 7.5.0 (fallback: wrapper -> GNU grep).
	grepBin := "grep"
	if p, err := exec.LookPath("ugrep"); err == nil {
		grepBin = p
	}

	// Build grep command arguments
	grepArgs := []string{"-rn", "-I"}
	if !isRegex {
		grepArgs = append(grepArgs, "-F")
	}
	// Limit results via max-count to prevent OOM — cap at 5000 matches per file
	grepArgs = append(grepArgs, "-m", "5000")
	grepArgs = append(grepArgs, "--exclude-dir=.git", "--exclude-dir=node_modules", "--exclude-dir=vendor", "--exclude-dir=.cache", "--exclude-dir=cache")
	if fileType != "" {
		grepArgs = append(grepArgs, "--include=*."+fileType)
	}
	grepArgs = append(grepArgs, query, path)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, grepBin, grepArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("search timed out after 10s")
		}
		// Check if this is an actual error (exit code 2) vs no-match (exit code 1)
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() == 2 {
				// Real error — return it
				return nil, fmt.Errorf("grep error: %s", strings.TrimSpace(string(out)))
			}
		}
		// grep exits 1 when no match; that's not an error for us
		if len(out) == 0 {
			return OK(map[string]interface{}{
				"query":   query,
				"matches": []map[string]interface{}{},
				"files":   0,
				"total":   0,
			}), nil
		}
	}

	// Cap at 1 MB to protect against huge output
	if len(out) > 1*1024*1024 {
		truncated := string(out[:1*1024*1024])
		out = []byte(truncated + "\n... [output truncated at 1 MB]")
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var results []map[string]interface{}
	fileSet := make(map[string]bool)

	for _, line := range lines {
		if line == "" {
			continue
		}
		// Format: file:line:content
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 2 {
			continue
		}
		filePath := parts[0]
		lineNum := 0
		fmt.Sscanf(parts[1], "%d", &lineNum)
		matchContent := ""
		if len(parts) >= 3 {
			matchContent = strings.TrimSpace(parts[2])
		}

		fileSet[filePath] = true
		results = append(results, map[string]interface{}{
			"file":  filePath,
			"line":  lineNum,
			"match": matchContent,
		})

		if len(results) >= maxResults {
			break
		}
	}

	return OK(map[string]interface{}{
		"query":     query,
		"matches":   results,
		"files":     len(fileSet),
		"total":     len(results),
		"truncated": len(results) >= maxResults,
	}), nil
}
