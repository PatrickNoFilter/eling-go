package layers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ObsidianLayer is Layer 6: the local Markdown vault access layer.
// It reads and writes Markdown files in an Obsidian vault directory,
// giving the agent access to project notes, daily logs, and research.
//
// Adapted from Python eling's layers/obsidian.py
type ObsidianLayer struct {
	mu        sync.RWMutex
	vaultPath string
}

// NewObsidianLayer creates an ObsidianLayer pointing to an Obsidian vault.
// If vaultPath is empty, the layer is disabled.
// Returns nil (disabled) if vaultPath is empty.
func NewObsidianLayer(vaultPath string) *ObsidianLayer {
	if vaultPath == "" {
		return nil
	}
	// Ensure vault directory exists
	os.MkdirAll(vaultPath, 0755)
	return &ObsidianLayer{vaultPath: vaultPath}
}

// Name returns "obsidian".
func (l *ObsidianLayer) Name() string { return "obsidian" }

// Priority returns 6.
func (l *ObsidianLayer) Priority() int { return 6 }

// Query searches notes in the vault by filename and content.
func (l *ObsidianLayer) Query(ctx context.Context, q string, limit int) ([]Result, error) {
	if l == nil || l.vaultPath == "" {
		return nil, nil
	}

	l.mu.RLock()
	defer l.mu.RUnlock()

	var results []Result
	lower := strings.ToLower(q)

	_ = filepath.Walk(l.vaultPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}

		// Check if filename matches
		relPath, _ := filepath.Rel(l.vaultPath, path)
		if strings.Contains(strings.ToLower(info.Name()), lower) ||
			strings.Contains(strings.ToLower(relPath), lower) {
			data, _ := os.ReadFile(path)
			content := string(data)
			results = append(results, Result{
				Content:  fmt.Sprintf("# %s\n\n%s", info.Name(), truncateStr(content, 300)),
				Category: "note",
				Source:   relPath,
				Score:    0.7,
			})
			if len(results) >= limit {
				return filepath.SkipAll
			}
		}

		// Check if content matches
		if info.Size() > 0 && info.Size() < 100*1024 { // only search small files
			data, _ := os.ReadFile(path)
			if strings.Contains(strings.ToLower(string(data)), lower) {
				content := string(data)
				results = append(results, Result{
					Content:  fmt.Sprintf("# %s\n\n%s", info.Name(), truncateStr(content, 300)),
					Category: "note",
					Source:   relPath,
					Score:    0.5,
				})
				if len(results) >= limit {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})

	return results, nil
}

// Store writes a note to the Obsidian vault.
func (l *ObsidianLayer) Store(ctx context.Context, item Item) error {
	if l == nil || l.vaultPath == "" {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Generate filename from category
	filename := sanitizeFilename(item.Category) + ".md"
	if filename == ".md" {
		filename = fmt.Sprintf("note_%d.md", len(item.Content)%1000)
	}

	path := filepath.Join(l.vaultPath, filename)

	// Build frontmatter
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("category: %s\n", item.Category))
	if len(item.Tags) > 0 {
		sb.WriteString(fmt.Sprintf("tags:\n"))
		for _, tag := range item.Tags {
			sb.WriteString(fmt.Sprintf("  - %s\n", tag))
		}
	}
	if item.Source != "" {
		sb.WriteString(fmt.Sprintf("source: %s\n", item.Source))
	}
	sb.WriteString("---\n\n")
	sb.WriteString(item.Content)

	return os.WriteFile(path, []byte(sb.String()), 0644)
}

// Close is a no-op.
func (l *ObsidianLayer) Close() error { return nil }
