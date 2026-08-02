package learnings

import (
	"strings"
	"testing"
)

func TestAppendLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := Append("hash-anchored edits prevent silent corruption"); err != nil {
		t.Fatal(err)
	}
	if err := Append("timeout budgets must always win over tool hangs"); err != nil {
		t.Fatal(err)
	}

	ls, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 2 {
		t.Fatalf("want 2 learnings, got %d: %v", len(ls), ls)
	}
	if !strings.Contains(ls[0], "hash-anchored") {
		t.Fatalf("first learning mismatch: %q", ls[0])
	}
	if !strings.Contains(ls[1], "timeout budgets") {
		t.Fatalf("second learning mismatch: %q", ls[1])
	}
	if Count() != 2 {
		t.Fatalf("Count() = %d, want 2", Count())
	}
}

func TestLoadMissingFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	ls, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 0 {
		t.Fatalf("want empty learnings, got %v", ls)
	}
	if Count() != 0 {
		t.Fatalf("Count() = %d, want 0", Count())
	}
}

func TestAppendEmptyRejected(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := Append("   \n  "); err == nil {
		t.Fatal("want error for empty entry, got nil")
	}
}

func TestMultiLineFlattened(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := Append("line one\nline two"); err != nil {
		t.Fatal(err)
	}
	ls, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 1 {
		t.Fatalf("want 1 learning, got %d", len(ls))
	}
	if !strings.HasSuffix(ls[0], "line one line two") {
		t.Fatalf("entry not flattened: %q", ls[0])
	}
	if !strings.HasPrefix(ls[0], "[") {
		t.Fatalf("entry missing timestamp: %q", ls[0])
	}
}
