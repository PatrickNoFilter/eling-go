package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "anchor.txt")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// asInt normalizes the numeric value the tool put in its result map
// (int in-process; would be float64 if it had round-tripped through JSON).
func asInt(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return -1
}

func TestEditOccurrenceTargetsNthMatch(t *testing.T) {
	path := writeTemp(t, "alpha beta alpha gamma alpha\n")

	res, err := editExecute(map[string]interface{}{
		"file_path":  path,
		"old_string": "alpha",
		"new_string": "ALPHA",
		"occurrence": float64(2),
	})
	if err != nil {
		t.Fatalf("editExecute error: %v", err)
	}
	ok := res.(Result).Data.(map[string]interface{})
	if occ := asInt(ok["occurrence"]); occ != 2 {
		t.Errorf("expected occurrence=2, got %v", ok["occurrence"])
	}
	if total := asInt(ok["total_occurrences"]); total != 3 {
		t.Errorf("expected total_occurrences=3, got %v", ok["total_occurrences"])
	}

	got := readFile(t, path)
	want := "alpha beta ALPHA gamma alpha\n"
	if got != want {
		t.Fatalf("file after occurrence=2 edit wrong:\n got: %q\nwant: %q", got, want)
	}
}

func TestEditDefaultOccurrenceIsFirst(t *testing.T) {
	path := writeTemp(t, "one two one\n")

	res, err := editExecute(map[string]interface{}{
		"file_path":  path,
		"old_string": "one",
		"new_string": "1",
	})
	if err != nil {
		t.Fatalf("editExecute error: %v", err)
	}
	ok := res.(Result).Data.(map[string]interface{})
	if occ := asInt(ok["occurrence"]); occ != 1 {
		t.Errorf("expected default occurrence=1, got %v", ok["occurrence"])
	}
	if got := readFile(t, path); got != "1 two one\n" {
		t.Fatalf("default edit replaced wrong occurrence: %q", got)
	}
}

func TestEditOccurrenceOutOfRange(t *testing.T) {
	path := writeTemp(t, "dup dup\n")

	res, err := editExecute(map[string]interface{}{
		"file_path":  path,
		"old_string": "dup",
		"new_string": "DUP",
		"occurrence": float64(5),
	})
	if err != nil {
		t.Fatalf("editExecute should not error: %v", err)
	}
	r := res.(Result)
	if r.Success {
		t.Fatal("expected failure for out-of-range occurrence")
	}
	if !strings.Contains(r.Error, "occurs 2 time(s)") || !strings.Contains(r.Error, "occurrence=5") {
		t.Fatalf("unexpected error: %s", r.Error)
	}
	// File must be untouched.
	if got := readFile(t, path); got != "dup dup\n" {
		t.Fatalf("file changed on failed edit: %q", got)
	}
}

func TestEditSourceHashMismatchAborts(t *testing.T) {
	path := writeTemp(t, "hello world\n")

	res, err := editExecute(map[string]interface{}{
		"file_path":   path,
		"old_string":  "hello",
		"new_string":  "goodbye",
		"source_hash": "0000000000000000000000000000000000000000000000000000000000000000", // wrong on purpose
	})
	if err != nil {
		t.Fatalf("editExecute should not error: %v", err)
	}
	r := res.(Result)
	if r.Success {
		t.Fatal("expected failure on source_hash mismatch")
	}
	if !strings.Contains(r.Error, "source_hash mismatch") ||
		!strings.Contains(r.Error, "expected 00000000") ||
		!strings.Contains(r.Error, "computed ") {
		t.Fatalf("mismatch error missing hash details: %s", r.Error)
	}
	if got := readFile(t, path); got != "hello world\n" {
		t.Fatalf("file changed despite hash mismatch: %q", got)
	}
}

func TestEditSourceHashMatchSucceeds(t *testing.T) {
	path := writeTemp(t, "hello world\n")

	// Correct hash of the file as it currently exists.
	correct := sha256Hex([]byte("hello world\n"))

	res, err := editExecute(map[string]interface{}{
		"file_path":   path,
		"old_string":  "hello",
		"new_string":  "goodbye",
		"source_hash": correct,
	})
	if err != nil {
		t.Fatalf("editExecute error: %v", err)
	}
	if !res.(Result).Success {
		t.Fatalf("expected success with matching source_hash: %s", res.(Result).Error)
	}
	if got := readFile(t, path); got != "goodbye world\n" {
		t.Fatalf("file after hashed edit wrong: %q", got)
	}
}

func TestEditChainedHashesWithoutReread(t *testing.T) {
	path := writeTemp(t, "func a() {}\nfunc b() {}\n")

	// First edit — capture returned hash.
	res, err := editExecute(map[string]interface{}{
		"file_path":  path,
		"old_string": "func a() {}",
		"new_string": "func a2() {}",
	})
	if err != nil {
		t.Fatalf("first edit error: %v", err)
	}
	hash, _ := res.(Result).Data.(map[string]interface{})["hash"].(string)
	if hash == "" {
		t.Fatal("first edit did not return a hash")
	}

	// Second edit uses the returned hash — no re-read needed.
	res2, err := editExecute(map[string]interface{}{
		"file_path":   path,
		"old_string":  "func b() {}",
		"new_string":  "func b2() {}",
		"source_hash": hash,
	})
	if err != nil {
		t.Fatalf("second edit error: %v", err)
	}
	if !res2.(Result).Success {
		t.Fatalf("chained edit failed: %s", res2.(Result).Error)
	}
	if got := readFile(t, path); got != "func a2() {}\nfunc b2() {}\n" {
		t.Fatalf("chained edits wrong: %q", got)
	}
}

func TestEditWhitespaceDriftHint(t *testing.T) {
	// File uses tabs; model sends spaces.
	path := writeTemp(t, "\titem one\n\titem two\n")

	res, err := editExecute(map[string]interface{}{
		"file_path":  path,
		"old_string": "    item one", // 4 spaces instead of tab
		"new_string": "    item ONE",
	})
	if err != nil {
		t.Fatalf("editExecute should not error: %v", err)
	}
	r := res.(Result)
	if r.Success {
		t.Fatal("expected failure for whitespace-drifted old_string")
	}
	if !strings.Contains(r.Error, "whitespace/line endings") {
		t.Fatalf("expected whitespace hint in error, got: %s", r.Error)
	}
	if got := readFile(t, path); got != "\titem one\n\titem two\n" {
		t.Fatalf("file changed despite drift failure: %q", got)
	}
}

func TestReadReturnsHashAnchor(t *testing.T) {
	path := writeTemp(t, "anchor me\n")

	res, err := readExecute(map[string]interface{}{"file_path": path})
	if err != nil {
		t.Fatalf("readExecute error: %v", err)
	}
	data := res.(Result).Data.(map[string]interface{})
	hash, _ := data["hash"].(string)
	if hash == "" {
		t.Fatal("read result missing hash anchor")
	}
	want := sha256Hex([]byte("anchor me\n"))
	if hash != want {
		t.Fatalf("read hash wrong: got %s want %s", hash, want)
	}
}

// TestEditConcurrentSerialization proves the per-file lock: N goroutines
// each replace a distinct token; with serialization all N replacements land.
// Without the lock this is a classic lost-update race.
func TestEditConcurrentSerialization(t *testing.T) {
	const n = 20
	var sb strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, "token%d\n", i)
	}
	path := writeTemp(t, sb.String())

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := editExecute(map[string]interface{}{
				"file_path":  path,
				"old_string": fmt.Sprintf("token%d", i),
				"new_string": fmt.Sprintf("DONE%d", i),
			})
			if err != nil {
				errs <- err
				return
			}
			if !res.(Result).Success {
				errs <- fmt.Errorf("edit %d failed: %s", i, res.(Result).Error)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	got := readFile(t, path)
	for i := 0; i < n; i++ {
		if !strings.Contains(got, fmt.Sprintf("DONE%d", i)) {
			t.Fatalf("lost update for token%d — file:\n%s", i, got)
		}
	}
	if strings.Contains(got, "token") {
		t.Fatalf("unreplaced tokens remain:\n%s", got)
	}
}

// TestEditRejectsBinaryFile ensures the binary guard still fires (regression).
func TestEditRejectsBinaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bin.dat")
	if err := os.WriteFile(path, []byte{0x00, 0x01, 0x02, 0xff}, 0644); err != nil {
		t.Fatal(err)
	}

	res, err := editExecute(map[string]interface{}{
		"file_path":  path,
		"old_string": "\x00",
		"new_string": "x",
	})
	if err != nil {
		t.Fatalf("editExecute should not error: %v", err)
	}
	r := res.(Result)
	if r.Success || !strings.Contains(r.Error, "binary") {
		t.Fatalf("expected binary refusal, got success=%v error=%q", r.Success, r.Error)
	}
}
