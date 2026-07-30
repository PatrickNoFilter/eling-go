// Package layers implements an 8-layer memory architecture for the ELING agent.
//
// Steering rules generator — writes agent-specific rules files.
// Adapted from Python eling's rules.py by PatrickNoFilter.
//
// Detects which AI agent is in use (OpenCode, Cursor, Claude Code, Kiro, Gemini)
// and writes steering rules that teach the agent when/how to use eling's tools.
package layers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ── Rule content templates ──────────────────────────────────────────────────

const rulesMemory = `# Eling Memory — Steering Rules
#
# Eling is your long-term memory layer.
# Use these tools to store and retrieve persistent information
# across conversations.

## When to STORE memories
- User states a preference or habit → eling_remember
- User corrects a previous answer → eling_remember
- You discover a project fact (language, framework, pattern) → eling_remember
- A decision is made about architecture or design → eling_remember
- Content < 500 chars → auto-routes to facts layer
- Content > 500 chars or has markdown headings → auto-routes to KB

## When to RETRIEVE memories
- At conversation start → eling_recall with the user's question
- When topic shifts → eling_recall with new topic keywords
- When asked "do you remember..." → eling_recall
- Before suggesting a solution → eling_recall for prior context

## When to PROBE
- User asks "what do you know about X" → eling_probe with entity name
- Before contradicting a statement → eling_probe to check existing facts

## Snapshot & Rollback
- Before destructive operations → eling_snapshot with reason
- After a mistake → eling_list_snapshots + eling_rollback
`

const rulesSessionLifecycle = `# Session Lifecycle — Eling Memory
#
# Bootstrap memory at conversation start, persist at end.

## Conversation Start
1. eling_recall(query="<user's first message>") — load relevant context
2. eling_recall(query="session context") — check for active session state

## Conversation End
1. eling_remember(content="<session summary>", category="general") — persist key info
2. If Notion is configured, eling_sync(direction="push") — push high-trust facts
`

const rulesMemoryHygiene = `# Memory Hygiene — Eling
#
# Proactive governance to keep memory healthy.

- Periodically call eling_evolve to merge near-duplicate facts
- Before running evolution, call eling_snapshot(reason="pre_evolution")
- Check eling_stats for pending contradictions
- Resolve contradictions with eling_remember(correction) or evolution
- If facts grow stale, run evolution with lower threshold
`

// RulesContent maps rule key to content.
var RulesContent = map[string]string{
	"memory":           rulesMemory,
	"session-lifecycle": rulesSessionLifecycle,
	"memory-hygiene":   rulesMemoryHygiene,
}

// RuleNames is the ordered list of rule keys.
var RuleNames = []string{
	"memory",
	"session-lifecycle",
	"memory-hygiene",
}

// ── Templates ──────────────────────────────────────────────────────────────

const cursorRulesTemplate = `---
description: Eling Memory — {title}
globs: 
---
{content}
`

const claudeRulesTemplate = `# Eling Memory — {title}

{content}
`

const opcodeAgentsTemplate = `## Eling Memory — {title}

{content}
`

// ── Public API ─────────────────────────────────────────────────────────────

// WriteRulesResult describes one written/updated rule file.
type WriteRulesResult struct {
	Agent  string `json:"agent"`
	File   string `json:"file"`
	Action string `json:"action"` // "write", "update", "skipped"
}

// DetectAgents detects which AI agents are configured in the project.
func DetectAgents(projectPath string) []string {
	var agents []string

	// Check for agent-specific directories
	if info, err := os.Stat(filepath.Join(projectPath, ".cursor", "rules")); err == nil && info.IsDir() {
		agents = append(agents, "cursor")
	}
	if info, err := os.Stat(filepath.Join(projectPath, ".claude", "rules")); err == nil && info.IsDir() {
		agents = append(agents, "claude_code")
	}
	if info, err := os.Stat(filepath.Join(projectPath, "AGENTS.md")); err == nil && !info.IsDir() {
		agents = append(agents, "opencode")
	}
	if info, err := os.Stat(filepath.Join(projectPath, ".kiro")); err == nil && info.IsDir() {
		agents = append(agents, "kiro")
	}
	if info, err := os.Stat(filepath.Join(projectPath, ".gemini")); err == nil && !info.IsDir() {
		agents = append(agents, "gemini")
	}
	if info, err := os.Stat(filepath.Join(projectPath, "GEMINI.md")); err == nil && !info.IsDir() {
		agents = append(agents, "gemini")
	}

	// Also check env vars
	if os.Getenv("CURSOR_HOME") != "" || os.Getenv("CURSOR_AGENT") != "" {
		if !contains(agents, "cursor") {
			agents = append(agents, "cursor")
		}
	}
	if os.Getenv("OPENCODE_HOME") != "" {
		if !contains(agents, "opencode") {
			agents = append(agents, "opencode")
		}
	}

	if len(agents) == 0 {
		agents = append(agents, "generic")
	}

	return agents
}

// WriteRules writes steering rules for detected agents.
// projectRoot: project root directory.
// agents: agent types to write rules for. Auto-detected if nil.
// dryRun: if true, only show what would be written.
func WriteRules(projectRoot string, agents []string, dryRun bool) []WriteRulesResult {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		root = projectRoot
	}

	if agents == nil {
		agents = DetectAgents(root)
	}

	var results []WriteRulesResult

	for _, agent := range agents {
		switch agent {
		case "cursor":
			results = append(results, writeCursorRules(root, dryRun)...)
		case "claude_code":
			results = append(results, writeClaudeRules(root, dryRun)...)
		case "opencode":
			results = append(results, writeOpenCodeRules(root, dryRun)...)
		case "generic", "kiro", "gemini":
			results = append(results, writeGenericRules(root, agent, dryRun)...)
		}
	}

	return results
}

// ── Internal writers ───────────────────────────────────────────────────────

func writeCursorRules(root string, dryRun bool) []WriteRulesResult {
	rulesDir := filepath.Join(root, ".cursor", "rules")
	if !dryRun {
		if err := os.MkdirAll(rulesDir, 0755); err != nil {
			return []WriteRulesResult{{Agent: "cursor", File: rulesDir, Action: "error: " + err.Error()}}
		}
	}

	var results []WriteRulesResult
	for _, key := range RuleNames {
		content := RulesContent[key]
		filename := "eling-memory-" + key + ".mdc"
		filepath := filepath.Join(rulesDir, filename)

		title := strings.ReplaceAll(key, "-", " ")
		title = strings.Title(title)

		text := strings.NewReplacer("{title}", title, "{content}", content).Replace(cursorRulesTemplate)

		action := "write"
		if _, err := os.Stat(filepath); err == nil {
			action = "update"
		}

		if !dryRun {
			if err := os.WriteFile(filepath, []byte(strings.TrimSpace(text)+"\n"), 0644); err != nil {
				action = "error: " + err.Error()
			}
		}

		results = append(results, WriteRulesResult{
			Agent:  "cursor",
			File:   filepath,
			Action: action,
		})
	}

	return results
}

func writeClaudeRules(root string, dryRun bool) []WriteRulesResult {
	rulesDir := filepath.Join(root, ".claude", "rules")
	if !dryRun {
		if err := os.MkdirAll(rulesDir, 0755); err != nil {
			return []WriteRulesResult{{Agent: "claude_code", File: rulesDir, Action: "error: " + err.Error()}}
		}
	}

	var results []WriteRulesResult
	for _, key := range RuleNames {
		content := RulesContent[key]
		filename := "eling-memory-" + key + ".md"
		filepath := filepath.Join(rulesDir, filename)

		title := strings.ReplaceAll(key, "-", " ")
		title = strings.Title(title)

		text := strings.NewReplacer("{title}", title, "{content}", content).Replace(claudeRulesTemplate)

		action := "write"
		if _, err := os.Stat(filepath); err == nil {
			action = "update"
		}

		if !dryRun {
			if err := os.WriteFile(filepath, []byte(strings.TrimSpace(text)+"\n"), 0644); err != nil {
				action = "error: " + err.Error()
			}
		}

		results = append(results, WriteRulesResult{
			Agent:  "claude_code",
			File:   filepath,
			Action: action,
		})
	}

	return results
}

func writeOpenCodeRules(root string, dryRun bool) []WriteRulesResult {
	agentsFile := filepath.Join(root, "AGENTS.md")

	action := "create"
	existing := ""
	if data, err := os.ReadFile(agentsFile); err == nil {
		existing = string(data)
		action = "update"
	}

	// Gather eling rules section
	var sections []string
	for _, key := range RuleNames {
		title := strings.ReplaceAll(key, "-", " ")
		title = strings.Title(title)
		tmpl := strings.NewReplacer("{title}", title, "{content}", RulesContent[key]).Replace(opcodeAgentsTemplate)
		sections = append(sections, strings.TrimSpace(tmpl))
	}
	newSection := strings.Join(sections, "\n\n")

	if dryRun {
		return []WriteRulesResult{{
			Agent:  "opencode",
			File:   agentsFile,
			Action: action,
		}}
	}

	if strings.Contains(existing, "## Eling Memory") {
		return []WriteRulesResult{{
			Agent:  "opencode",
			File:   agentsFile,
			Action: "skipped",
		}}
	}

	text := ""
	if existing != "" {
		text = strings.TrimRight(existing, "\n") + "\n\n" + newSection + "\n"
	} else {
		text = newSection + "\n"
	}

	if err := os.WriteFile(agentsFile, []byte(text), 0644); err != nil {
		return []WriteRulesResult{{
			Agent:  "opencode",
			File:   agentsFile,
			Action: "error: " + err.Error(),
		}}
	}

	return []WriteRulesResult{{
		Agent:  "opencode",
		File:   agentsFile,
		Action: action,
	}}
}

func writeGenericRules(root string, agent string, dryRun bool) []WriteRulesResult {
	filename := "ELING_MEMORY.md"
	filepath := filepath.Join(root, filename)

	action := "write"
	if _, err := os.Stat(filepath); err == nil {
		action = "update"
	}

	if !dryRun {
		var sections []string
		for _, key := range RuleNames {
			title := strings.ReplaceAll(key, "-", " ")
			title = strings.Title(title)
			sections = append(sections, fmt.Sprintf("# Eling Memory — %s\n\n%s", title, strings.TrimSpace(RulesContent[key])))
		}
		text := strings.Join(sections, "\n\n") + "\n"
		if err := os.WriteFile(filepath, []byte(text), 0644); err != nil {
			return []WriteRulesResult{{
				Agent:  agent,
				File:   filepath,
				Action: "error: " + err.Error(),
			}}
		}
	}

	return []WriteRulesResult{{
		Agent:  agent,
		File:   filepath,
		Action: action,
	}}
}

// ── Utility ────────────────────────────────────────────────────────────────

func contains(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}
