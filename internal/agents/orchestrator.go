// Package agents implements the D3 multi-agent parallelism subsystem
// (package heist feature note, Phase 3 milestone).
//
// The core idea (mirroring DeepCode): instead of one agent doing everything
// inline, a job can be SPLIT into focused sub-agents (an investigator that
// only search-read-plans, and an implementer that mutates code), each running
// in its OWN isolated git worktree. Because the worktrees are separate, their
// edits can never clobber the real working tree or each other by accident.
//
// Two hard rules are enforced here:
//
//  1. NO SILENT MERGE. A sub-agent's work is only ever merged back through an
//     explicit review report (a diff). The caller (the main agent) decides.
//     We never auto-resolve conflicts and never blindly fast-forward.
//
//  2. CONFLICTS SURFACE, THEY DO NOT OVERWRITE. If two sub-agents touch the
//     same file, the merge does NOT silently pick a winner; the overlap is
//     recorded and returned to the caller as a Conflict so it is surfaced
//     explicitly.
//
// This subsystem is GATED and DEFAULT OFF (config.AgentsConfig.Enabled=false).
// It only engages when explicitly switched on, so a default installation is
// unaffected until it has been battle-tested.
package agents

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Config is the runtime configuration for the orchestrator.
type Config struct {
	Enabled      bool   // master gate (default false)
	Max          int    // max concurrent sub-agents (0 = default 2)
	Token        int    // per-sub-agent token budget cap (0 = unlimited)
	WorktreeRoot string // override for the worktree directory (test/CI)
}

// MaxWorkers returns the concurrency bound (at least 1).
func (c Config) MaxWorkers() int {
	if c.Max < 1 {
		return 2
	}
	return c.Max
}

// Budget returns the per-sub-agent token cap (0 = unlimited).
func (c Config) Budget() int { return c.Token }

// Unit is one sub-agent task in a split. Role is "investigator" (read-only)
// or "implementer" (edits code). OnRun is executed inside the unit's isolated
// worktree; the worktree path is passed to it.
type Unit struct {
	Name string
	Role string // "investigator" | "implementer"
	// OnRun runs the sub-agent's work inside the given isolated worktree
	// directory. For an investigator this is typically a no-op that only
	// reports (read/search results are its output); for an implementer it
	// performs edits.
	OnRun func(worktree string) error
	// Files lists the repository-relative paths this unit intends to touch
	// (used for conflict detection across units). May be empty.
	Files []string
}

// UnitResult is the outcome of a single sub-agent unit.
type UnitResult struct {
	Name string
	Role string
	OK   bool
	Err  string
}

// Conflict records two or more units overlapping on the same file. It is
// surfaced explicitly rather than being silently resolved.
type Conflict struct {
	File  string
	Units []string
}

// Report is the aggregate outcome of a split orchestration.
type Report struct {
	Results   []UnitResult
	Conflicts []Conflict
}

// ConflictFree reports whether no two units overlapped on any file.
func (r *Report) ConflictFree() bool { return len(r.Conflicts) == 0 }

// Orchestrator runs a split of parallel sub-agents in isolated git worktrees.
type Orchestrator struct {
	cfg  Config
	repo string // git repo root that the worktrees are created from

	mu        sync.Mutex
	worktrees map[string]string // unit name -> worktree path
	branches  map[string]string // unit name -> branch name
}

// New returns an Orchestrator bound to the given config and repo root.
func New(cfg Config, repo string) *Orchestrator {
	return &Orchestrator{
		cfg:       cfg,
		repo:      repo,
		worktrees: map[string]string{},
		branches:  map[string]string{},
	}
}

// Enabled reports whether parallel orchestration is switched on.
func (o *Orchestrator) Enabled() bool { return o.cfg.Enabled }

func (o *Orchestrator) worktreeRoot() string {
	if o.cfg.WorktreeRoot != "" {
		return o.cfg.WorktreeRoot
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".eling", "worktrees")
}

// validName mirrors tools.validWorktreeName: safe names only (alnum, dot,
// underscore, hyphen) to prevent path traversal.
func validName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		ok := r == '-' || r == '_' || r == '.' || (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return true
}

// git runs a git command in the given directory and returns trimmed combined
// output fused with the error.
func (o *Orchestrator) git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return s, fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, s)
	}
	return s, nil
}

// createWorktree provisions an isolated worktree for a unit. It returns the
// worktree path and branch name.
func (o *Orchestrator) createWorktree(u Unit) (path, branch string, err error) {
	if !validName(u.Name) {
		return "", "", fmt.Errorf("agents: invalid unit name %q", u.Name)
	}
	root := o.worktreeRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", "", fmt.Errorf("agents: mkdir worktree root: %w", err)
	}
	path = filepath.Join(root, "eling-exp-"+u.Name)
	branch = "eling/exp/" + u.Name

	if _, err := os.Stat(path); err == nil {
		return "", "", fmt.Errorf("agents: worktree %q already exists", u.Name)
	}
	base := "main"
	if out, herr := o.git(o.repo, "branch", "--show-current"); herr == nil && out != "" {
		base = out
	}
	if _, gerr := o.git(o.repo, "worktree", "add", "-b", branch, path, base); gerr != nil {
		return "", "", fmt.Errorf("agents: create worktree for %q: %w", u.Name, gerr)
	}
	o.mu.Lock()
	o.worktrees[u.Name] = path
	o.branches[u.Name] = branch
	o.mu.Unlock()
	return path, branch, nil
}

// commitWorktree stages and commits the sub-agent's finished work in its own
// worktree branch. If nothing changed (investigator with no edits, or an
// already-clean tree), the empty-commit error is swallowed.
func (o *Orchestrator) commitWorktree(wt, unit string) {
	if _, err := o.git(wt, "add", "-A"); err != nil {
		return
	}
	msg := fmt.Sprintf("sub-agent %s", unit)
	if _, err := o.git(wt, "commit", "-q", "-m", msg); err != nil {
		// "nothing to commit" (or user identity missing) — leave branch as-is.
		return
	}
}

// removeWorktree tears a sub-agent's worktree and its branch down.
func (o *Orchestrator) removeWorktree(u Unit) {
	o.mu.Lock()
	path := o.worktrees[u.Name]
	branch := o.branches[u.Name]
	delete(o.worktrees, u.Name)
	delete(o.branches, u.Name)
	o.mu.Unlock()
	if path == "" {
		return
	}
	_, _ = o.git(o.repo, "worktree", "remove", "--force", path)
	if branch != "" {
		_, _ = o.git(o.repo, "branch", "-D", branch)
	}
}

// branchOf returns the branch created for a unit ("" if unknown).
func (o *Orchestrator) branchOf(name string) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.branches[name]
}

// Run splits the given units into their own isolated worktrees, executes each
// OnRun in its worktree (bounded by MaxWorkers concurrency), and builds a
// Report with results + crossing-file conflict detection. It always returns a
// Report; the returned error is non-nil only for setup failures that leave
// worktrees unprovisioned.
func (o *Orchestrator) Run(ctx context.Context, units []Unit) (*Report, error) {
	rep := &Report{Results: []UnitResult{}, Conflicts: []Conflict{}}
	if !o.Enabled() {
		return rep, nil
	}
	if len(units) == 0 {
		return rep, nil
	}

	type job struct {
		u    Unit
		path string
	}
	jobs := make([]job, 0, len(units))
	for _, u := range units {
		path, _, err := o.createWorktree(u)
		if err != nil {
			// Provisioning failed for this unit — record it, don't abort all.
			rep.Results = append(rep.Results, UnitResult{Name: u.Name, Role: u.Role, Err: err.Error()})
			continue
		}
		jobs = append(jobs, job{u: u, path: path})
	}

	sem := make(chan struct{}, o.cfg.MaxWorkers())
	var wg sync.WaitGroup
	mu := &sync.Mutex{}
	for _, j := range jobs {
		j := j
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			res := UnitResult{Name: j.u.Name, Role: j.u.Role, OK: true}
			if j.u.OnRun != nil {
				if err := j.u.OnRun(j.path); err != nil {
					res.OK = false
					res.Err = err.Error()
				} else {
					// Commit the sub-agent's finished work in ITS OWN branch so
					// the review diff and merge act on a real commit. If the
					// agent produced no changes, "nothing to commit" is ignored.
					o.commitWorktree(j.path, j.u.Name)
				}
			}
			mu.Lock()
			rep.Results = append(rep.Results, res)
			mu.Unlock()
		}()
	}
	wg.Wait()

	// Conflict detection: same file touched by >= 2 units.
	byFile := map[string][]string{}
	for _, u := range units {
		for _, f := range u.Files {
			byFile[f] = append(byFile[f], u.Name)
		}
	}
	var files []string
	for f := range byFile {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		names := byFile[f]
		if len(names) > 1 {
			sort.Strings(names)
			rep.Conflicts = append(rep.Conflicts, Conflict{File: f, Units: names})
		}
	}
	return rep, nil
}

// DiffOf returns the review diff (git diff base...branch --stat + patch) for a
// unit's branch. Used to build the explicit review report that must precede
// any merge — merges are never silent.
func (o *Orchestrator) DiffOf(name string) string {
	branch := o.branchOf(name)
	if branch == "" {
		return ""
	}
	base := "main"
	if out, err := o.git(o.repo, "branch", "--show-current"); err == nil && out != "" {
		base = out
	}
	stat, _ := o.git(o.repo, "diff", "--stat", base+"..."+branch)
	patch, _ := o.git(o.repo, "diff", base+"..."+branch)
	if stat == "" && patch == "" {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("### Review diff — " + name + " (" + branch + ")\n")
	if stat != "" {
		sb.WriteString(stat + "\n")
	}
	if patch != "" {
		sb.WriteString(patch + "\n")
	}
	return sb.String()
}

// MergeResult describes the outcome of reconciling a finished Report back
// into the working tree. Conflicted units are never auto-merged; they are
// reported as Conflicts and left untouched (surfaced, not overwritten).
type MergeResult struct {
	CleanUnits []string   // units merged via review (each had a diff)
	Conflicts  []Conflict // overlapping units — surfaced, not merged
	Review     string     // human-readable review / diff summary
}

// Merge reconciles a finished Report into the working tree, honoring
// "no silent merge": only conflict-free units are merged and each is
// accompanied by a diff review excerpt. Conflicted units are left in their
// worktrees and surfaced in MergeResult.Conflicts.
func (o *Orchestrator) Merge(ctx context.Context, rep *Report) (*MergeResult, error) {
	mr := &MergeResult{CleanUnits: []string{}, Conflicts: []Conflict{}}
	if rep == nil {
		return mr, nil
	}
	mr.Conflicts = rep.Conflicts

	for _, c := range rep.Conflicts {
		mr.Review += fmt.Sprintf("CONFLICT on %s between units %s — surfaced, not auto-resolved.\n",
			c.File, strings.Join(c.Units, ", "))
	}

	for _, r := range rep.Results {
		if !r.OK {
			continue // failed unit: never merge
		}
		if unitInConflict(rep, r.Name) {
			continue // surfaced conflict — leave its worktree untouched
		}
		branch := o.branchOf(r.Name)
		if branch == "" {
			continue
		}
		review := o.DiffOf(r.Name)
		if review == "" {
			continue // no changes produced — nothing to merge
		}
		// Merge the branch (conflict-free by construction; --no-edit keeps it
		// non-interactive). Each clean unit is merged through its review diff.
		if _, err := o.git(o.repo, "merge", "--no-edit", branch); err != nil {
			// The merge itself hit an unexpected conflict — surface it.
			mr.Conflicts = append(mr.Conflicts, Conflict{File: "<merge>", Units: []string{r.Name}})
			mr.Review += "Merge failed for " + r.Name + ": surfaced explicitly.\n"
			continue
		}
		mr.CleanUnits = append(mr.CleanUnits, r.Name)
		mr.Review += review + "\n"
		o.removeWorktree(Unit{Name: r.Name})
	}
	return mr, nil
}

// unitInConflict reports whether the named unit appears in any recorded
// conflict. Files are not retained on UnitResult, so membership in a conflict
// is the authoritative collision signal.
func unitInConflict(rep *Report, name string) bool {
	for _, c := range rep.Conflicts {
		for _, un := range c.Units {
			if un == name {
				return true
			}
		}
	}
	return false
}
