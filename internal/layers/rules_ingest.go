package layers

// Project rules ingestion (DeepCode heist — Part III, phase D1).
//
// Reads the project's OWN rules file (AGENTS.md / DEEPCODE.md / CLAUDE.md /
// .cursor/rules/*.mdc) so repo-specific engineering conventions steer every
// agent turn — mirroring DeepCode's DeepContext. This is strictly read-only:
// we never modify the user's rules file (that's what WriteRules / init-rules
// already do for OTHER agents).
//
// Concurrency note: the agent field that holds the ingested rules is written
// once at boot (Agent.New) and never mutated afterwards, so it is immutable
// after construction. buildMessages reads it while the caller already holds
// a.mu.RLock() — matching the a.learnings read (A10).

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// projectRulesMaxChars caps how much of a rules file we inject, protecting
	// small local-model token budgets (same rationale as summaryMaxChars).
	projectRulesMaxChars = 4096
	// projectRulesMaxLines caps the number of lines injected.
	projectRulesMaxLines = 40
)

// projectRulesFileTable lists candidate file names in priority order
// (first match wins), mirroring DeepCode's AGENTS.md > DEEPCODE.md probing.
var projectRulesFileTable = []string{
	"AGENTS.md",
	"DEEPCODE.md",
	"CLAUDE.md",
	".cursor/rules",
}

// FindProjectRulesFile returns the highest-priority rules file that exists in
// dir, or "" if none. Probe order: AGENTS.md, DEEPCODE.md, CLAUDE.md, then the
// first *.mdc inside .cursor/rules.
func FindProjectRulesFile(dir string) string {
	if dir == "" {
		return ""
	}
	for _, name := range projectRulesFileTable {
		if name == ".cursor/rules" {
			// Cursor-style rules are a directory of *.mdc files.
			entries, err := os.ReadDir(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			var mdc []string
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".mdc") {
					mdc = append(mdc, filepath.Join(dir, name, e.Name()))
				}
			}
			if len(mdc) == 0 {
				continue
			}
			sort.Strings(mdc) // deterministic first-match
			return mdc[0]
		}
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// LoadProjectRules reads and caps a project's rules file. Returns the file
// path (if found) and the trimmed, capped content. Any I/O error or an
// absent file returns both empty — callers treat that as "no rules".
func LoadProjectRules(dir string) (file string, content string) {
	p := FindProjectRulesFile(dir)
	if p == "" {
		return "", ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", ""
	}
	content = truncateRules(string(data))
	if content == "" {
		return "", ""
	}
	return p, content
}

// truncateRules trims and caps rules content to keep the injected block small.
func truncateRules(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) > projectRulesMaxChars {
		s = s[:projectRulesMaxChars] +
			fmt.Sprintf("\n... [rules truncated at %d chars]", projectRulesMaxChars)
	}
	if lines := strings.Split(s, "\n"); len(lines) > projectRulesMaxLines {
		s = strings.Join(lines[:projectRulesMaxLines], "\n") +
			fmt.Sprintf("\n... [rules truncated at %d lines]", projectRulesMaxLines)
	}
	return s
}