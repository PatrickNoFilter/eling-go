// Package tools — Phase 1 Git Worktrees.
//
// worktree_create / worktree_list / worktree_remove / worktree_merge let the
// agent experiment on isolated branch worktrees outside the real repo tree,
// then merge them back explicitly. Worktrees live under ~/.eling/worktrees/
// (never inside the project), so experiments can never corrupt the main
// working tree by accident.
package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"
	"strings"
)

// worktreeRoot returns the directory where eling-managed worktrees live.
var worktreeRootOverride string // test hook

func worktreeRoot() string {
	if worktreeRootOverride != "" {
		return worktreeRootOverride
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".eling", "worktrees")
}

// safeName validates a worktree/branch name: lowercase alnum, dash, dot,
// underscore only. Prevents path traversal via names like "../evil".
var safeNameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

func validWorktreeName(name string) (bool, string) {
	if name == "" {
		return false, "name is required"
	}
	if len(name) > 64 {
		return false, "name too long (max 64 chars)"
	}
	if !safeNameRe.MatchString(name) {
		return false, "name may only contain letters, digits, '.', '_', '-'"
	}
	return true, ""
}

// runGit runs a git command in the given repo dir and returns trimmed output.
func runGit(repoDir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if repoDir != "" {
		cmd.Dir = repoDir
	}
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return s, fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, s)
	}
	return s, nil
}

// detectRepoDir finds the git repo root containing cwd (or current dir).
var detectRepoDirFn func() string // test hook

func detectRepoDir() string {
	if detectRepoDirFn != nil {
		return detectRepoDirFn()
	}
	for _, cand := range []string{".", os.Getenv("PWD"), "/root/eling"} {
		if out, err := runGit(cand, "rev-parse", "--show-toplevel"); err == nil && out != "" {
			return out
		}
	}
	return "."
}

// init registers the worktree tools with the default registry.
func init() {
	create := Tool{
		Name:        "worktree_create",
		Description: "Create an isolated git worktree for experimentation. Worktrees live under ~/.eling/worktrees/ so experiments never touch the main tree. Args: name (required), base_branch (optional, defaults to current branch).",
		Version:     "1.1.0", // git ops with registry timeout
		Category:    "system",
		Execute:     worktreeCreate,
		Timeout:     60 * time.Second,
	}
	DefaultRegistry.Register(create)

	list := Tool{
		Name:        "worktree_list",
		Description: "List all git worktrees with their branch, path, and HEAD.",
		Version:     "1.1.0", // git ops with registry timeout
		Category:    "system",
		Execute:     worktreeList,
		Timeout:     60 * time.Second,
	}
	DefaultRegistry.Register(list)

	remove := Tool{
		Name:        "worktree_remove",
		Description: "Remove a worktree by name (cleans up the branch and directory). Args: name (required).",
		Version:     "1.1.0", // git ops with registry timeout
		Category:    "system",
		Execute:     worktreeRemove,
		Timeout:     60 * time.Second,
	}
	DefaultRegistry.Register(remove)

	merge := Tool{
		Name:        "worktree_merge",
		Description: "Merge a worktree's branch back into the main tree and remove the worktree. Args: name (required), target (optional branch, default current).",
		Version:     "1.1.0", // git ops with registry timeout
		Category:    "system",
		Execute:     worktreeMerge,
		Timeout:     60 * time.Second,
	}
	DefaultRegistry.Register(merge)
}

// worktreeCreate implements worktree_create.
func worktreeCreate(args map[string]interface{}) (interface{}, error) {
	name, _ := args["name"].(string)
	base, _ := args["base_branch"].(string)

	if ok, why := validWorktreeName(name); !ok {
		return Err("worktree_create: " + why), nil
	}

	repo := detectRepoDir()
	if base == "" {
		out, err := runGit(repo, "branch", "--show-current")
		if err != nil || out == "" {
			out = "main"
		}
		base = out
	}

	root := worktreeRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return Err("worktree_create: mkdir: " + err.Error()), nil
	}
	wtPath := filepath.Join(root, name)
	branch := "eling/exp/" + name

	if _, err := os.Stat(wtPath); err == nil {
		return Err("worktree_create: worktree '" + name + "' already exists at " + wtPath), nil
	}

	if _, err := runGit(repo, "worktree", "add", "-b", branch, wtPath, base); err != nil {
		return Err("worktree_create: " + err.Error()), nil
	}

	head, _ := runGit(wtPath, "rev-parse", "--short", "HEAD")
	return OK(map[string]interface{}{
		"name":        name,
		"path":        wtPath,
		"branch":      branch,
		"base_branch": base,
		"head":        head,
	}), nil
}

// worktreeList implements worktree_list.
func worktreeList(args map[string]interface{}) (interface{}, error) {
	repo := detectRepoDir()
	out, err := runGit(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return Err("worktree_list: " + err.Error()), nil
	}
	return OK(map[string]interface{}{
		"worktrees": out,
	}), nil
}

// worktreeRemove implements worktree_remove.
func worktreeRemove(args map[string]interface{}) (interface{}, error) {
	name, _ := args["name"].(string)
	if ok, why := validWorktreeName(name); !ok {
		return Err("worktree_remove: " + why), nil
	}

	repo := detectRepoDir()
	wtPath := filepath.Join(worktreeRoot(), name)
	if _, err := os.Stat(wtPath); err != nil {
		return Err("worktree_remove: worktree '" + name + "' not found"), nil
	}

	if _, err := runGit(repo, "worktree", "remove", "--force", wtPath); err != nil {
		return Err("worktree_remove: " + err.Error()), nil
	}
	// Delete the experiment branch too (safe: branch only existed in this worktree).
	branch := "eling/exp/" + name
	if _, err := runGit(repo, "branch", "-D", branch); err != nil {
		// non-fatal — branch may already be merged/deleted
	}
	return OK(map[string]interface{}{
		"removed": name,
		"path":    wtPath,
	}), nil
}

// worktreeMerge implements worktree_merge — merges the worktree's branch
// into the target branch, then removes the worktree.
func worktreeMerge(args map[string]interface{}) (interface{}, error) {
	name, _ := args["name"].(string)
	target, _ := args["target"].(string)

	if ok, why := validWorktreeName(name); !ok {
		return Err("worktree_merge: " + why), nil
	}

	repo := detectRepoDir()
	if target == "" {
		out, err := runGit(repo, "branch", "--show-current")
		if err != nil || out == "" {
			out = "main"
		}
		target = out
	}

	branch := "eling/exp/" + name
	// Confirm the branch exists.
	if _, err := runGit(repo, "rev-parse", "--verify", branch); err != nil {
		return Err("worktree_merge: branch '" + branch + "' not found"), nil
	}

	// Merge the experiment branch into target (fast-forward preferred).
	if _, err := runGit(repo, "merge", "--no-edit", branch); err != nil {
		// Try explicit target checkout path
		cur, _ := runGit(repo, "branch", "--show-current")
		if cur != target {
			if _, err2 := runGit(repo, "checkout", target); err2 != nil {
				return Err("worktree_merge: checkout " + target + ": " + err2.Error()), nil
			}
			if _, err2 := runGit(repo, "merge", "--no-edit", branch); err2 != nil {
				return Err("worktree_merge: " + err2.Error()), nil
			}
		} else {
			return Err("worktree_merge: " + err.Error()), nil
		}
	}

	// Clean up: remove worktree + branch.
	wtPath := filepath.Join(worktreeRoot(), name)
	_, _ = runGit(repo, "worktree", "remove", "--force", wtPath)
	_, _ = runGit(repo, "branch", "-D", branch)

	return OK(map[string]interface{}{
		"merged":      branch,
		"into":        target,
		"worktree":    name,
		"worktree_removed": true,
	}), nil
}
