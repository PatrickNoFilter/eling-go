package autorepair

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// commitGuardCheck is the working-tree dirty check used by Repair(). It is a
// variable so tests can stub it deterministically.
var commitGuardCheck = gitTreeDirty

// gitTreeDirty reports whether the ELING working tree (the directory the
// binary runs from, falling back to $HOME/eling) has uncommitted changes.
// It is used by the Phase 3 commit guard: code-mutation fixes are refused
// while work is in progress. Returns (false, nil) when the directory is not
// a git repo (nothing to guard) or git is unavailable.
func gitTreeDirty() (bool, error) {
	dir := workingTreeDir()
	if dir == "" {
		return false, nil
	}
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		// Not a git repo (or git missing) → nothing to guard.
		return false, nil
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// workingTreeDir locates the ELING source tree for the commit guard by
// walking up from the current working directory to find go.mod, then falling
// back to $HOME/eling.
func workingTreeDir() string {
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		cand := filepath.Join(home, "eling")
		if _, err := os.Stat(filepath.Join(cand, "go.mod")); err == nil {
			return cand
		}
	}
	return ""
}

// SanitizeUTF8 replaces invalid UTF-8 byte sequences with U+FFFD so error
// strings from tools (which may contain arbitrary bytes) never corrupt the
// dashboard, TUI, or persisted state. This is the Phase 3 "advisory logging
// for un-UTF8" hardening: detection/classification must not choke on (or
// propagate) malformed encodings.
func SanitizeUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == utf8.RuneError {
			b.WriteRune('\uFFFD')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
