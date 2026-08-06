package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupFileCreatesSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("original content"), 0644); err != nil {
		t.Fatal(err)
	}

	backupPath, err := backupFile(path)
	if err != nil {
		t.Fatalf("backupFile returned error: %v", err)
	}
	if backupPath == "" {
		t.Fatal("backupFile returned empty path for existing file")
	}
	if !strings.Contains(backupPath, "test.txt.bak.") {
		t.Fatalf("unexpected backup path: %s", backupPath)
	}

	data, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("backup file unreadable: %v", err)
	}
	if string(data) != "original content" {
		t.Fatalf("backup content mismatch: %q", string(data))
	}
}

func TestBackupFileMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.txt")

	backupPath, err := backupFile(path)
	if err != nil {
		t.Fatalf("backupFile returned error for missing file: %v", err)
	}
	if backupPath != "" {
		t.Fatalf("expected empty backup path for missing file, got %q", backupPath)
	}
}

func TestBackupFileCentralDir(t *testing.T) {
	dir := t.TempDir()
	central := t.TempDir()
	t.Setenv("ELING_BACKUP_DIR", central)

	src := filepath.Join(dir, "sub", "app.go")
	if err := os.MkdirAll(filepath.Dir(src), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}

	backupPath, err := backupFile(src)
	if err != nil {
		t.Fatalf("backupFile returned error: %v", err)
	}
	if !strings.HasPrefix(backupPath, central) {
		t.Fatalf("backup not under central dir: %s", backupPath)
	}
	if !strings.Contains(backupPath, "app.go.bak.") {
		t.Fatalf("backup path missing expected suffix: %s", backupPath)
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("backup file missing: %v", err)
	}
}

func TestRotateBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rotate.txt")

	// Create 7 backups with different timestamps (simulate by writing then calling rotate)
	for i := 0; i < 7; i++ {
		ts := "20260801_12000" + string(rune('0'+i))
		bp := path + ".bak." + ts
		if err := os.WriteFile(bp, []byte("v"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	rotateBackups(path, path+".bak.20260801_120006")

	matches, _ := filepath.Glob(path + ".bak.*")
	if len(matches) != 2 {
		t.Fatalf("expected 2 backups after rotation, got %d: %v", len(matches), matches)
	}
}

func TestRotateZipBackups(t *testing.T) {
	dir := t.TempDir()

	// Create 5 fake backup zips with distinct mtimes.
	for i := 0; i < 5; i++ {
		bp := filepath.Join(dir, fmt.Sprintf("eling_backup_2026080%d_12000%d.zip", i, i))
		if err := os.WriteFile(bp, []byte("zip"), 0644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond) // ensure distinct modtimes
	}

	rotateZipBackups(dir)

	matches, _ := filepath.Glob(filepath.Join(dir, "eling_backup_*.zip"))
	if len(matches) != 2 {
		t.Fatalf("expected 2 zips after rotation, got %d: %v", len(matches), matches)
	}
	// The two newest (last written) must survive.
	keep1 := filepath.Join(dir, "eling_backup_20260803_120003.zip")
	keep2 := filepath.Join(dir, "eling_backup_20260804_120004.zip")
	for _, m := range matches {
		if m != keep1 && m != keep2 {
			t.Fatalf("unexpected surviving zip: %s", m)
		}
	}
}

func TestEditExecuteCreatesBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "editme.txt")
	if err := os.WriteFile(path, []byte("hello world\nsecond line\n"), 0644); err != nil {
		t.Fatal(err)
	}

	res, err := editExecute(map[string]interface{}{
		"file_path":  path,
		"old_string": "hello",
		"new_string": "goodbye",
	})
	if err != nil {
		t.Fatalf("editExecute error: %v", err)
	}
	ok := res.(Result).Data.(map[string]interface{})
	backupPath, _ := ok["backup"].(string)
	if backupPath == "" {
		t.Fatal("editExecute did not return a backup path")
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("backup file not created: %v", err)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "goodbye world\nsecond line\n" {
		t.Fatalf("file content after edit wrong: %q", string(data))
	}
}

func TestWriteExecuteCreatesBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "writeme.txt")
	if err := os.WriteFile(path, []byte("old content"), 0644); err != nil {
		t.Fatal(err)
	}

	res, err := writeExecute(map[string]interface{}{
		"file_path": path,
		"content":   "new content",
	})
	if err != nil {
		t.Fatalf("writeExecute error: %v", err)
	}
	ok := res.(Result).Data.(map[string]interface{})
	backupPath, _ := ok["backup"].(string)
	if backupPath == "" {
		t.Fatal("writeExecute did not return a backup path")
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("backup file not created: %v", err)
	}

	data, _ := os.ReadFile(backupPath)
	if string(data) != "old content" {
		t.Fatalf("backup content mismatch: %q", string(data))
	}
}

func TestWriteExecuteSkipsIdenticalContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "same.txt")
	if err := os.WriteFile(path, []byte("identical"), 0644); err != nil {
		t.Fatal(err)
	}

	res, err := writeExecute(map[string]interface{}{
		"file_path": path,
		"content":   "identical",
	})
	if err != nil {
		t.Fatalf("writeExecute error: %v", err)
	}
	ok := res.(Result).Data.(map[string]interface{})
	if unchanged, _ := ok["unchanged"].(bool); !unchanged {
		t.Fatal("expected unchanged=true for identical content")
	}
	backupPath, _ := ok["backup"].(string)
	if backupPath != "" {
		t.Fatalf("expected no backup for identical content, got %q", backupPath)
	}
	// No .bak files should exist
	matches, _ := filepath.Glob(path + ".bak.*")
	if len(matches) != 0 {
		t.Fatalf("unexpected backups created: %v", matches)
	}
}
