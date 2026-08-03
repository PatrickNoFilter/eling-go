// A6 (stealing.md): tests for the lsp_rename tool registration and the
// applyLSPEdits safety-net path (per-file lock + timestamped backup + binary
// guard + UTF-16 column mapping).
package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eling/internal/lsp"
)

func TestLSPRenameToolRegistered(t *testing.T) {
	tool, ok := DefaultRegistry.Get("lsp_rename")
	if !ok {
		t.Fatal("lsp_rename tool not registered")
	}
	if tool.Version != "1.0.0" {
		t.Fatalf("lsp_rename version = %s, want 1.0.0", tool.Version)
	}
	if tool.Category != "system" {
		t.Fatalf("lsp_rename category = %s, want system", tool.Category)
	}
}

// applyLSPEdits must route through the same safety net as edit/write:
// per-file lock + timestamped backup + binary guard.
func TestApplyLSPEditsBasic(t *testing.T) {
	t.Setenv("ELING_BACKUP_DIR", "") // keep backups next to the file, deterministically
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	original := "package main\n\nfunc oldName() {\n\toldName()\n}\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	// Declaration edit (line 2 col 5..12) and call-site edit (line 3 col
	// 1..8) — supplied out of document order to prove reverse-order sorting.
	edits := []lsp.TextEdit{
		{Path: path, Line: 3, Col: 1, EndLine: 3, EndCol: 8, NewText: "newName"},
		{Path: path, Line: 2, Col: 5, EndLine: 2, EndCol: 12, NewText: "newName"},
	}
	n, err := applyLSPEdits(edits)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected 2 edits applied, got %d", n)
	}

	data, _ := os.ReadFile(path)
	got := string(data)
	if !strings.Contains(got, "func newName()") || !strings.Contains(got, "\tnewName()") {
		t.Fatalf("rename not applied correctly:\n%s", got)
	}
	if strings.Contains(got, "oldName") {
		t.Fatalf("oldName still present:\n%s", got)
	}

	// A timestamped backup must exist next to the file.
	entries, _ := os.ReadDir(dir)
	found := false
	for _, e := range entries {
		if strings.Contains(e.Name(), ".bak.") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected a timestamped .bak backup to be created")
	}
}

// UTF-16 columns: gopls positions count emoji/CJK as 2 code units. An edit
// after an emoji must still land on the right byte.
func TestApplyLSPEditsUTF16Columns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	// Line 1: "var s = \"😀 old\"" — 😀 is 2 UTF-16 units (cols 9-10), 4 bytes.
	original := "package x\n\nvar s = \"😀 old\"\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	// Columns on line 1: v0 a1 r2 ' '3 s4 ' '5 =6 ' '7 "8 😀9-10 ' '11
	// o12 l13 d14 "15 — so "old" spans UTF-16 cols 12..15.
	edits := []lsp.TextEdit{
		{Path: path, Line: 2, Col: 12, EndLine: 2, EndCol: 15, NewText: "new"},
	}
	n, err := applyLSPEdits(edits)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 edit applied, got %d", n)
	}
	data, _ := os.ReadFile(path)
	got := string(data)
	if !strings.Contains(got, `"😀 new"`) {
		t.Fatalf("UTF-16 column edit landed wrong:\n%s", got)
	}
}

// Out-of-bounds / malformed edits are skipped, never corrupt the file, and
// return 0 applied — matching the best-effort LSP design.
func TestApplyLSPEditsSkipsOutOfBounds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	if err := os.WriteFile(path, []byte("package x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	edits := []lsp.TextEdit{
		{Path: path, Line: 99, Col: 0, EndLine: 99, EndCol: 1, NewText: "z"},
		{Path: path, Line: 0, Col: 5, EndLine: 0, EndCol: 1, NewText: "z"}, // end < start
	}
	n, err := applyLSPEdits(edits)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected 0 applied for invalid edits, got %d", n)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "package x\n" {
		t.Fatalf("file was corrupted by invalid edits: %q", data)
	}
}

// Binary files are refused — same guard as the edit tool.
func TestApplyLSPEditsRefusesBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blob.bin")
	if err := os.WriteFile(path, []byte{0x00, 0x01, 0x02, 0xff}, 0644); err != nil {
		t.Fatal(err)
	}
	_, err := applyLSPEdits([]lsp.TextEdit{
		{Path: path, Line: 0, Col: 0, EndLine: 0, EndCol: 1, NewText: "x"},
	})
	if err == nil {
		t.Fatal("expected error for binary file, got nil")
	}
}

// Multiple files in one batch are all handled (used by cross-file renames).
func TestApplyLSPEditsMultiFile(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.go")
	b := filepath.Join(dir, "b.go")
	if err := os.WriteFile(a, []byte("package x\n\nfunc oldName() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("package x\n\nfunc oldName() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	edits := []lsp.TextEdit{
		{Path: a, Line: 2, Col: 5, EndLine: 2, EndCol: 12, NewText: "newName"},
		{Path: b, Line: 2, Col: 5, EndLine: 2, EndCol: 12, NewText: "newName"},
	}
	n, err := applyLSPEdits(edits)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected 2 edits applied across 2 files, got %d", n)
	}
	for _, p := range []string{a, b} {
		data, _ := os.ReadFile(p)
		if !strings.Contains(string(data), "func newName()") {
			t.Fatalf("rename missing in %s", p)
		}
	}
}
