package layers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// BuiltinLayer is Layer 1: the always-on, zero-setup memory layer.
// It reads MEMORY.md and USER.md files from the eling state directory
// and provides their content as stable, persistent context.
//
// Adapted from Python eling's layers/builtin.py
type BuiltinLayer struct {
	mu       sync.RWMutex
	stateDir string
	memoryMD string // cached content of MEMORY.md
	userMD   string // cached content of USER.md
}

// NewBuiltinLayer creates a new BuiltinLayer.
// stateDir is the ELING state directory (e.g. ~/.eling).
func NewBuiltinLayer(stateDir string) *BuiltinLayer {
	l := &BuiltinLayer{
		stateDir: stateDir,
	}
	l.load()
	return l
}

// Name returns "builtin".
func (l *BuiltinLayer) Name() string { return "builtin" }

// Priority returns 1 (highest).
func (l *BuiltinLayer) Priority() int { return 1 }

// Query returns builtin content if the query matches known patterns.
func (l *BuiltinLayer) Query(ctx context.Context, q string, limit int) ([]Result, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var results []Result
	lower := strings.ToLower(q)

	// Always include identity content on identity queries
	if containsAny(lower, "who are you", "your name", "identity", "your role") {
		if l.memoryMD != "" {
			results = append(results, Result{
				Content:  l.memoryMD,
				Category: "identity",
				Source:   "MEMORY.md",
				Score:    1.0,
			})
		}
	}

	// Include user profile on user queries
	if containsAny(lower, "user", "profile", "who am i", "my name", "preferences") {
		if l.userMD != "" {
			results = append(results, Result{
				Content:  l.userMD,
				Category: "profile",
				Source:   "USER.md",
				Score:    1.0,
			})
		}
	}

	// Always include if no specific match — provide concise identity summary
	if len(results) == 0 {
		if l.memoryMD != "" {
			summary := truncateStr(l.memoryMD, 200)
			results = append(results, Result{
				Content:  summary,
				Category: "identity",
				Source:   "MEMORY.md",
				Score:    0.7,
			})
		}
	}

	return results, nil
}

// Store saves content to MEMORY.md (identity) or as tagged files.
func (l *BuiltinLayer) Store(ctx context.Context, item Item) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	dir := l.stateDir
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	switch item.Category {
	case "identity":
		path := filepath.Join(dir, "MEMORY.md")
		if err := os.WriteFile(path, []byte(item.Content), 0644); err != nil {
			return err
		}
		l.memoryMD = item.Content
	case "profile":
		path := filepath.Join(dir, "USER.md")
		if err := os.WriteFile(path, []byte(item.Content), 0644); err != nil {
			return err
		}
		l.userMD = item.Content
	default:
		// Store as a tagged markdown file
		filename := fmt.Sprintf("note_%s.md", sanitizeFilename(item.Category))
		path := filepath.Join(dir, filename)
		content := fmt.Sprintf("# %s\n\n%s\n\nTags: %s\n",
			item.Category, item.Content, strings.Join(item.Tags, ", "))
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return err
		}
	}
	return nil
}

// Close is a no-op for builtin layer.
func (l *BuiltinLayer) Close() error { return nil }

// load reads MEMORY.md and USER.md from disk.
func (l *BuiltinLayer) load() {
	l.mu.Lock()
	defer l.mu.Unlock()

	dir := l.stateDir
	memPath := filepath.Join(dir, "MEMORY.md")
	userPath := filepath.Join(dir, "USER.md")

	if data, err := os.ReadFile(memPath); err == nil {
		l.memoryMD = strings.TrimSpace(string(data))
	}
	if data, err := os.ReadFile(userPath); err == nil {
		l.userMD = strings.TrimSpace(string(data))
	}
}

// GetContext returns the combined builtin context for system prompts.
func (l *BuiltinLayer) GetContext() string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var parts []string
	if l.memoryMD != "" {
		parts = append(parts, "[Memory]", l.memoryMD)
	}
	if l.userMD != "" {
		parts = append(parts, "[User Profile]", l.userMD)
	}
	return strings.Join(parts, "\n\n")
}

// Internal helpers

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func truncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

func sanitizeFilename(s string) string {
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s)
	return strings.Trim(s, "_")
}
