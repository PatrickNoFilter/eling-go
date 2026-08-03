// A6 (stealing.md): language-server rename support.
//
// Registers the lsp_rename tool and installs the workspace/applyEdit hook so
// edits pushed by the language server (gopls rename refactors, code actions)
// land through the exact same safety net as edit/write: per-file lock +
// timestamped backup + binary guard (see files.go).
//
// Positions are LSP 0-based (line + UTF-16 code-unit column) — the encoding
// gopls uses by default. lspOffset converts them to byte offsets so edits
// stay correct on non-ASCII files (emoji/CJK are multi-code-unit).
package tools

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"eling/internal/lsp"
)

func init() {
	// lsp_rename — safe whole-project symbol rename via the language server.
	DefaultRegistry.Register(Tool{
		Name: "lsp_rename",
		Description: "Rename a symbol (function, variable, type, method) across the whole project using the language server " +
			"(gopls / pyright-langserver / typescript-language-server). Provide file_path (or path) plus line and col " +
			"(0-based — the read tool's output is 1-based, subtract 1) and new_name. " +
			"Every edit is applied through the same backup + per-file-lock path as edit/write. " +
			"Best-effort: returns an error when no language server is configured for the file, the server is missing, " +
			"or the symbol cannot be renamed.",
		Version:  "1.0.0",
		Category: "system",
		Execute:  lspRenameExecute,
		Timeout:  30 * time.Second, // gopls cold start can take seconds on slow devices
	})

	// Route server-initiated workspace/applyEdit pushes (rename code actions,
	// multi-file refactors) through the same backup + lock path as edit/write
	// so nothing can silently mutate files without a snapshot.
	lsp.SetApplyEditHandler(func(edits []lsp.TextEdit) error {
		_, err := applyLSPEdits(edits)
		return err
	})
}

// lspRenameExecute implements the lsp_rename tool.
func lspRenameExecute(args map[string]interface{}) (interface{}, error) {
	path, _ := args["file_path"].(string)
	if path == "" {
		path, _ = args["path"].(string)
	}
	if path == "" {
		return Err("path is required"), nil
	}
	newName, _ := args["new_name"].(string)
	if newName == "" {
		return Err("new_name is required"), nil
	}
	line := intArg(args, "line")
	col := intArg(args, "col")

	if !lsp.Supports(path) {
		return Err(fmt.Sprintf("no language server configured for %s (supported: .go, .py, .ts/.tsx/.js)", path)), nil
	}
	// gopls (and most servers) refuse textDocument/rename on documents they
	// have never seen. Diagnostics opens the file (didOpen) first — the same
	// path the agent's post-edit [lsp] section uses, so no new machinery.
	if content, err := os.ReadFile(path); err == nil {
		_ = lsp.Diagnostics(path, string(content))
	}
	edits := lsp.Rename(path, line, col, newName)
	if len(edits) == 0 {
		return Err(fmt.Sprintf(
			"rename returned no edits for %s:%d:%d -> %q (language server unavailable, symbol not found at that position, or rename unsupported)",
			path, line, col, newName)), nil
	}
	applied, err := applyLSPEdits(edits)
	if err != nil {
		return nil, err
	}
	files := map[string]int{}
	for _, e := range edits {
		files[e.Path]++
	}
	return OK(map[string]interface{}{
		"renamed":       newName,
		"path":          path,
		"position":      fmt.Sprintf("%d:%d", line, col),
		"edits_applied": applied,
		"files":         files,
	}), nil
}

// intArg reads an int tool argument, tolerating JSON float64 (the registry's
// arg decoding) and native int.
func intArg(args map[string]interface{}, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return 0
}

// applyLSPEdits applies a batch of LSP TextEdits to disk. Edits are grouped
// per file; each file is processed under its per-file lock with a timestamped
// .bak backup first — the same safety net edit/write use. Returns the number
// of edits actually applied.
func applyLSPEdits(edits []lsp.TextEdit) (int, error) {
	if len(edits) == 0 {
		return 0, nil
	}
	byFile := map[string][]lsp.TextEdit{}
	var order []string
	for _, e := range edits {
		if _, ok := byFile[e.Path]; !ok {
			order = append(order, e.Path)
		}
		byFile[e.Path] = append(byFile[e.Path], e)
	}
	applied := 0
	for _, path := range order {
		n, err := applyLSPEditsToFile(path, byFile[path])
		if err != nil {
			return applied, err
		}
		applied += n
	}
	return applied, nil
}

// applyLSPEditsToFile applies one file's edits. Edits are sorted in reverse
// document order (end of file first) so earlier replacements never shift the
// offsets of later ones.
func applyLSPEditsToFile(path string, edits []lsp.TextEdit) (int, error) {
	unlock := lockFile(path)
	defer unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("lsp apply read %s: %w", path, err)
	}
	if !isTextFile(data) {
		return 0, fmt.Errorf("lsp apply refused binary file: %s", path)
	}
	content := string(data)

	sort.Slice(edits, func(i, j int) bool {
		if edits[i].Line != edits[j].Line {
			return edits[i].Line > edits[j].Line
		}
		if edits[i].Col != edits[j].Col {
			return edits[i].Col > edits[j].Col
		}
		return edits[i].EndLine > edits[j].EndLine
	})

	changed := 0
	for _, e := range edits {
		start := lspOffset(content, e.Line, e.Col)
		end := lspOffset(content, e.EndLine, e.EndCol)
		if start < 0 || end < 0 || end < start {
			continue // out of bounds — skip rather than corrupt
		}
		content = content[:start] + e.NewText + content[end:]
		changed++
	}
	if changed == 0 {
		return 0, nil
	}
	if _, err := backupFile(path); err != nil {
		return 0, fmt.Errorf("lsp apply backup %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return 0, fmt.Errorf("lsp apply write %s: %w", path, err)
	}
	return changed, nil
}

// lspOffset converts a 0-based LSP position (line + UTF-16 code-unit column,
// the encoding gopls uses by default) into a byte offset within content.
// Returns -1 when the position is out of bounds; clamps to the end of the
// line when the column exceeds the line's length.
func lspOffset(content string, line, col int) int {
	if line < 0 || col < 0 {
		return -1
	}
	start := 0
	for i := 0; i < line; i++ {
		idx := strings.IndexByte(content[start:], '\n')
		if idx < 0 {
			return -1
		}
		start += idx + 1
	}
	lineText := content[start:]
	if idx := strings.IndexByte(lineText, '\n'); idx >= 0 {
		lineText = lineText[:idx]
	}
	byteOff := 0
	units := 0
	for _, r := range lineText {
		if units >= col {
			break
		}
		u := utf16.RuneLen(r)
		if u < 0 {
			u = 1
		}
		if units+u > col {
			break // mid-rune — clamp to rune start
		}
		units += u
		byteOff += utf8.RuneLen(r)
	}
	if units < col {
		byteOff = len(lineText) // clamp to end of line
	}
	return start + byteOff
}
