package tools

import (
	"path/filepath"
	"strings"
)

// Permission modes for per-tool permission profiles (D6). Same strings as the
// config YAML so the policy is directly serializable and introspectable.
const (
	PermAllow = "allow"
	PermAsk   = "ask"
	PermDeny  = "deny"
)

// ValidPermMode reports whether m is a recognised permission mode.
func ValidPermMode(m string) bool {
	switch m {
	case "allow", "ask", "deny":
		return true
	}
	return false
}

// PermPolicy is the runtime enforcement model behind the D6 per-tool
// permission profiles. It mirrors config.PermissionsConfig but is independent
// of the config package (avoids any config<->tools import cycle).
//
// Resolution order when a tool is executed:
//
//	1. an exact tool rule (Rules[tool]) — highest priority
//	2. the active project's trust level (Projects), if the project is listed
//	3. DefaultMode for any unlisted tool
//	4. "allow" fallback when the policy is fully empty (inactive) — preserves
//	   the historical allow-everything behavior of a fresh install.
type PermPolicy struct {
	DefaultMode string            // "allow" | "ask" | "deny"
	Rules       map[string]string // tool -> mode
	Projects    map[string]string // abs project path -> "full" | "ask" | "deny"
	Active      bool              // any rule / default / project configured
}

// NewPermPolicy builds a policy from raw config values. mode defaults to
// "ask" (the D6 plan default) when empty, so unlisted tools prompt rather than
// silently running once the user opts into permissions. Active is derived from
// whether anything was configured.
func NewPermPolicy(defaultMode string, rules map[string]string, projects map[string]string) PermPolicy {
	if defaultMode == "" {
		defaultMode = "ask"
	}
	if rules == nil {
		rules = map[string]string{}
	}
	if projects == nil {
		projects = map[string]string{}
	}
	active := defaultMode != "" || len(rules) > 0 || len(projects) > 0
	return PermPolicy{
		DefaultMode: defaultMode,
		Rules:       rules,
		Projects:    projects,
		Active:      active,
	}
}

// ModeFor resolves the permission mode for a tool executed from projectDir.
// projectDir may be "" (no project context). Returns the resolved mode and a
// human-readable reason describing which rule produced it (for deny blocks).
func (p PermPolicy) ModeFor(tool, projectDir string) (mode, reason string) {
	if !p.Active {
		return "allow", "permissions not configured (inactive policy)"
	}
	// 1. Exact tool rule wins.
	if m, ok := p.Rules[tool]; ok {
		return m, fmtReason("rule", tool, m)
	}
	// 2. Project trust, longest-prefix match.
	if trust, ok := p.projectTrust(projectDir); ok {
		switch trust {
		case "full", "allow":
			return "allow", fmtReason("project trust", projectDir, "full")
		case "deny":
			return "deny", fmtReason("project trust", projectDir, "deny")
		default: // "ask" or anything else => prompt
			return "ask", fmtReason("project trust", projectDir, "ask")
		}
	}
	// 3. Default mode.
	return p.DefaultMode, fmtReason("default", "", p.DefaultMode)
}

// projectTrust looks up the longest matching configured project path prefix.
// Project keys may be a directory or a file inside it; matching is by path
// prefix, so "/root/eling" also covers "/root/eling/sub/dir".
func (p PermPolicy) projectTrust(projectDir string) (string, bool) {
	if projectDir == "" || len(p.Projects) == 0 {
		return "", false
	}
	best := ""
	bestLen := -1
	for key := range p.Projects {
		if pathWithin(projectDir, key) && len(key) > bestLen {
			best = key
			bestLen = len(key)
		}
	}
	if bestLen < 0 {
		return "", false
	}
	return p.Projects[best], true
}

// pathWithin reports whether dir is inside base (prefix match on clean abs
// paths). Both are cleaned and made absolute before comparison.
func pathWithin(dir, base string) bool {
	dirAbs, err1 := filepath.Abs(dir)
	baseAbs, err2 := filepath.Abs(base)
	if err1 != nil || err2 != nil {
		return false
	}
	dir = filepath.Clean(dirAbs)
	base = filepath.Clean(baseAbs)
	if base == string(filepath.Separator) {
		return strings.HasPrefix(dir, base)
	}
	return dir == base || strings.HasPrefix(dir, base+string(filepath.Separator))
}

func fmtReason(kind, name, mode string) string {
	switch kind {
	case "rule":
		return "tool rule: " + name + " -> " + mode
	case "project trust":
		return "project trust: " + name + " -> " + mode
	default:
		return "default mode: " + mode
	}
}
