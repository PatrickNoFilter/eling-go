package agents

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newFixtureRepo creates a throwaway git repo with an initial commit and
// returns its root path. Shared fixtures keeps the real working tree clean.
func newFixtureRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "eling-test")
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "b.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-q", "-m", "initial")
	return repo
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v (%s)", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestDisabledByDefault is the D3 gate: with Enabled false (the default), Run
// is a no-op and produces zero results, so enabling requires explicit config.
func TestDisabledByDefault(t *testing.T) {
	repo := newFixtureRepo(t)
	o := New(Config{Enabled: false, WorktreeRoot: t.TempDir()}, repo)
	rep, err := o.Run(context.Background(), []Unit{{Name: "alpha", Role: "implementer"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.Results) != 0 {
		t.Fatalf("disabled orchestrator must produce no results, got %d", len(rep.Results))
	}
}

// TestTwoAgentSeparateFiles checks the clean split: two implementers each edit
// a different file in their own worktrees; after Merge both changes land in the
// repo and no conflict is surfaced.
func TestTwoAgentSeparateFiles(t *testing.T) {
	repo := newFixtureRepo(t)
	o := New(Config{Enabled: true, Max: 2, WorktreeRoot: t.TempDir()}, repo)

	units := []Unit{
		{
			Name: "alpha", Role: "implementer", Files: []string{"a.go"},
			OnRun: func(wt string) error {
				return os.WriteFile(filepath.Join(wt, "a.go"), []byte("package main\n// alpha edit\n"), 0o644)
			},
		},
		{
			Name: "beta", Role: "implementer", Files: []string{"b.go"},
			OnRun: func(wt string) error {
				return os.WriteFile(filepath.Join(wt, "b.go"), []byte("package main\n// beta edit\n"), 0o644)
			},
		},
	}

	rep, err := o.Run(context.Background(), units)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.Results) != 2 {
		t.Fatalf("want 2 results, got %d", len(rep.Results))
	}
	for _, r := range rep.Results {
		if !r.OK {
			t.Fatalf("unit %s failed: %s", r.Name, r.Err)
		}
	}
	if !rep.ConflictFree() {
		t.Fatalf("separate files must be conflict-free: %+v", rep.Conflicts)
	}

	mr, err := o.Merge(context.Background(), rep)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if len(mr.Conflicts) != 0 {
		t.Fatalf("surfaced unexpected conflicts: %+v", mr.Conflicts)
	}
	if len(mr.CleanUnits) != 2 {
		t.Fatalf("want 2 clean units merged, got %v", mr.CleanUnits)
	}
	if !strings.Contains(readFile(t, filepath.Join(repo, "a.go")), "alpha edit") {
		t.Fatal("a.go should contain alpha's edit after merge")
	}
	if !strings.Contains(readFile(t, filepath.Join(repo, "b.go")), "beta edit") {
		t.Fatal("b.go should contain beta's edit after merge")
	}
	if !strings.Contains(mr.Review, "alpha") || !strings.Contains(strings.ToLower(mr.Review), "diff") {
		t.Fatal("review report should carry a diff review for merged units")
	}
}

// TestSameFileConflict is the "surfaced, never overwritten" criterion: two
// units both touch the same file; the merge must NOT silently pick a winner —
// it reports a Conflict and leaves the worktree untouched.
func TestSameFileConflict(t *testing.T) {
	repo := newFixtureRepo(t)
	o := New(Config{Enabled: true, Max: 2, WorktreeRoot: t.TempDir()}, repo)

	units := []Unit{
		{
			Name: "red", Role: "implementer", Files: []string{"shared.go"},
			OnRun: func(wt string) error {
				return os.WriteFile(filepath.Join(wt, "shared.go"), []byte("// red version\n"), 0o644)
			},
		},
		{
			Name: "blue", Role: "implementer", Files: []string{"shared.go"},
			OnRun: func(wt string) error {
				return os.WriteFile(filepath.Join(wt, "shared.go"), []byte("// blue version\n"), 0o644)
			},
		},
	}

	rep, err := o.Run(context.Background(), units)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.Conflicts) != 1 {
		t.Fatalf("want exactly 1 conflict, got %+v", rep.Conflicts)
	}
	if rep.Conflicts[0].File != "shared.go" {
		t.Fatalf("conflict should be on shared.go, got %s", rep.Conflicts[0].File)
	}
	if len(rep.Conflicts[0].Units) != 2 {
		t.Fatalf("conflict should name both units, got %v", rep.Conflicts[0].Units)
	}

	mr, err := o.Merge(context.Background(), rep)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	// Conflict must be surfaced, never auto-resolved.
	if len(mr.Conflicts) == 0 {
		t.Fatal("conflict must be surfaced in merge result (no silent merge)")
	}
	if len(mr.CleanUnits) != 0 {
		t.Fatalf("conflicted units must NOT be merged; got clean=%v", mr.CleanUnits)
	}
	if !strings.Contains(strings.ToLower(mr.Review), "conflict") {
		t.Fatalf("review should mention the conflict, got:\n%s", mr.Review)
	}
}

func newFixture(t *testing.T) string { return newFixtureRepo(t) }