// Package layers implements an 8-layer memory architecture for the ELING agent.
//
// Verify-on-stop — verification nudge for agents that lack built-in verification.
// Adapted from Python eling's verify_on_stop.py by PatrickNoFilter.
//
// When an AI agent (OpenCode, OpenClaw, etc.) does not have its own
// verify-on-stop, eling fills the gap:
//
//  1. Tracks file edits via hooks or explicit MCP calls
//  2. Detects whether the host agent already has built-in verification (skip)
//  3. Runs spec-kit conformance check (if spec-kit artifacts exist)
//  4. Produces a verification nudge message when code was edited but not verified
//  5. Exposes status via MCP tool so any agent can query it
//
// Detection logic:
//   - ELING_ADAPTER=hermes → skip (Hermes has built-in verification)
//   - ELING_ADAPTER=opencode|openclaw|openclaude|claude_cli → enable
//   - ELING_ADAPTER=auto → auto-detect from environment variables
package layers

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ── Agent signatures ───────────────────────────────────────────────────────

// Agents that have built-in verify-on-stop — eling is a no-op for these
var agentsWithVerify = map[string]bool{
	"hermes": true,
}

// Agents that do NOT have built-in verify-on-stop — eling provides it
var agentsWithoutVerify = map[string]bool{
	"opencode":    true,
	"openclaw":    true,
	"openclaude":  true,
	"claude_cli":  true,
	"cursor":      true,
	"windsurf":    true,
	"generic":     true,
}

// Env-var → agent name mapping for auto-detection
var agentSignatures = map[string]string{
	"HERMES_SESSION_SOURCE": "hermes",
	"HERMES_PLATFORM":       "hermes",
	"OPENCODE_HOME":         "opencode",
}

// ── Verification ledger (session-scoped) ───────────────────────────────────

var (
	ledgerMu     sync.RWMutex
	verifyLedger = &VerificationLedger{
		ChangedPaths:     []string{},
		VerificationEvents: []VerificationEvent{},
		Verified:         false,
		LastEditTime:     0,
		LastVerifyTime:   0,
		VerifyAttempts:   0,
	}
)

// VerificationLedger tracks file edits and verification events for a session.
type VerificationLedger struct {
	ChangedPaths       []string            `json:"changed_paths"`
	VerificationEvents []VerificationEvent `json:"verification_events"`
	Verified           bool                `json:"verified"`
	LastEditTime       float64             `json:"last_edit_time"`
	LastVerifyTime     float64             `json:"last_verify_time"`
	VerifyAttempts     int                 `json:"verify_attempts"`
}

// VerificationEvent records a single verification event.
type VerificationEvent struct {
	Time          float64 `json:"time"`
	Status        string  `json:"status"` // "passed", "failed", "skipped"
	Command       string  `json:"command"`
	OutputSummary string  `json:"output_summary"`
}

// ── Public API: detection ──────────────────────────────────────────────────

// DetectHostAgent detects which AI agent is running by inspecting environment variables.
// Returns one of: "hermes", "opencode", or "generic".
func DetectHostAgent() string {
	for envVar, agent := range agentSignatures {
		val := os.Getenv(envVar)
		if val != "" {
			return agent
		}
	}
	return "generic"
}

// HostHasVerifyOnStop returns true if the host agent already has verify-on-stop built-in.
// adapter: "auto" (default) → auto-detect from environment.
// Any other string is checked against agentsWithVerify.
//
// Set ELING_VERIFY_ALL_AGENTS=1 to force eling's verify-on-stop to be
// active for every agent, including Hermes.
func HostHasVerifyOnStop(adapter string) bool {
	// Check universal override
	override := os.Getenv("ELING_VERIFY_ALL_AGENTS")
	if override == "1" || strings.EqualFold(override, "true") ||
		strings.EqualFold(override, "yes") || strings.EqualFold(override, "on") {
		return false
	}

	if adapter != "auto" {
		return agentsWithVerify[adapter]
	}
	agent := DetectHostAgent()
	return agentsWithVerify[agent]
}

// ── Public API: ledger operations ──────────────────────────────────────────

// RecordEdit records a file edit in the verification ledger.
// Resets the verified flag so a new verification is required.
func RecordEdit(filePath string) {
	ledgerMu.Lock()
	defer ledgerMu.Unlock()

	// Check if already tracked
	found := false
	for _, p := range verifyLedger.ChangedPaths {
		if p == filePath {
			found = true
			break
		}
	}
	if !found {
		verifyLedger.ChangedPaths = append(verifyLedger.ChangedPaths, filePath)
	}
	verifyLedger.LastEditTime = float64(time.Now().Unix())
	verifyLedger.Verified = false
}

// RecordVerification records a verification event (test run, lint, build, etc.).
func RecordVerification(status, command, output string) {
	ledgerMu.Lock()
	defer ledgerMu.Unlock()

	outputSummary := output
	if len(outputSummary) > 500 {
		outputSummary = outputSummary[:500]
	}

	verifyLedger.VerificationEvents = append(verifyLedger.VerificationEvents, VerificationEvent{
		Time:          float64(time.Now().Unix()),
		Status:        status,
		Command:       command,
		OutputSummary: outputSummary,
	})

	if status == "passed" {
		verifyLedger.Verified = true
		verifyLedger.LastVerifyTime = float64(time.Now().Unix())
	}
	verifyLedger.VerifyAttempts++
}

// ResetLedger resets the verification ledger (e.g. at session start).
func ResetLedger() {
	ledgerMu.Lock()
	defer ledgerMu.Unlock()
	verifyLedger = &VerificationLedger{
		ChangedPaths:       []string{},
		VerificationEvents: []VerificationEvent{},
		Verified:           false,
		LastEditTime:       0,
		LastVerifyTime:     0,
		VerifyAttempts:     0,
	}
}

// GetChangedPaths returns the list of changed paths (thread-safe).
func GetChangedPaths() []string {
	ledgerMu.RLock()
	defer ledgerMu.RUnlock()
	paths := make([]string, len(verifyLedger.ChangedPaths))
	copy(paths, verifyLedger.ChangedPaths)
	return paths
}

// ── Non-code path filter ───────────────────────────────────────────────────

var nonCodeExtensions = map[string]bool{
	".md": true, ".markdown": true, ".mdx": true,
	".rst": true, ".txt": true, ".text": true,
	".adoc": true, ".asciidoc": true, ".org": true,
	".log": true, ".csv": true, ".tsv": true,
}

var nonCodeFilenames = map[string]bool{
	"license": true, "licence": true, "notice": true,
	"authors": true, "contributors": true, "changelog": true,
	"codeowners": true,
}

func isNonCodePath(raw string) bool {
	ext := strings.ToLower(filepath.Ext(raw))
	if nonCodeExtensions[ext] {
		return true
	}
	name := strings.ToLower(filepath.Base(raw))
	if ext == "" && nonCodeFilenames[name] {
		return true
	}
	return false
}

func filterVerifiablePaths(paths []string) []string {
	var result []string
	for _, p := range paths {
		if p != "" && !isNonCodePath(p) {
			result = append(result, p)
		}
	}
	return result
}

// ── Nudge builder ──────────────────────────────────────────────────────────

const maxChangedPathsShown = 8
const maxVerifyAttempts = 2

func formatPaths(paths []string) string {
	var lines []string
	shown := paths
	if len(shown) > maxChangedPathsShown {
		shown = shown[:maxChangedPathsShown]
	}
	for _, p := range shown {
		lines = append(lines, "- `"+p+"`")
	}
	remaining := len(paths) - len(shown)
	if remaining > 0 {
		lines = append(lines, "- ... and "+itoa(remaining)+" more")
	}
	return strings.Join(lines, "\n")
}

// BuildVerifyNudge builds a verification nudge message if code edits need fresh verification.
// Returns the nudge text, or empty string when no nudge is needed.
func BuildVerifyNudge() string {
	ledgerMu.RLock()
	defer ledgerMu.RUnlock()

	// Deduplicate and filter
	pathSet := make(map[string]bool)
	for _, p := range verifyLedger.ChangedPaths {
		if !isNonCodePath(p) {
			pathSet[p] = true
		}
	}
	paths := make([]string, 0, len(pathSet))
	for p := range pathSet {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	if len(paths) == 0 {
		return ""
	}

	if verifyLedger.VerifyAttempts >= maxVerifyAttempts {
		return ""
	}

	if verifyLedger.Verified && verifyLedger.LastVerifyTime >= verifyLedger.LastEditTime {
		return ""
	}

	// Build status summary
	var detailParts []string
	if len(verifyLedger.VerificationEvents) > 0 {
		last := verifyLedger.VerificationEvents[len(verifyLedger.VerificationEvents)-1]
		detailParts = append(detailParts, last.Status)
		if last.Command != "" {
			detailParts = append(detailParts, "last command `"+last.Command+"`")
		}
		if last.OutputSummary != "" {
			output := last.OutputSummary
			if len(output) > 1200 {
				output = output[:1200] + "\n... [truncated]"
			}
			detailParts = append(detailParts, "last output:\n"+output)
		}
	} else {
		detailParts = append(detailParts, "unverified")
	}

	return "[System: You edited code in this turn, but the workspace does not have " +
		"fresh passing verification evidence yet.\n\n" +
		"Verification status: " + strings.Join(detailParts, " | ") + "\n\n" +
		"Changed paths:\n" + formatPaths(paths) + "\n\n" +
		"Run the relevant verification command now (test, lint, build), " +
		"read any failure, repair the code, and summarize what passed. " +
		"If verification is not possible, explain the concrete blocker " +
		"instead of claiming the work is fully verified.]"
}

// VerifyStatus returns the current verification status as a map.
func VerifyStatus() map[string]interface{} {
	ledgerMu.RLock()
	defer ledgerMu.RUnlock()

	pathSet := make(map[string]bool)
	for _, p := range verifyLedger.ChangedPaths {
		if !isNonCodePath(p) {
			pathSet[p] = true
		}
	}
	paths := make([]string, 0, len(pathSet))
	for p := range pathSet {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	// Get last 3 events
	events := verifyLedger.VerificationEvents
	if len(events) > 3 {
		events = events[len(events)-3:]
	}

	return map[string]interface{}{
		"changed_paths":      paths,
		"verification_events": events,
		"verified":           verifyLedger.Verified,
		"attempts":           verifyLedger.VerifyAttempts,
		"needs_verification": len(paths) > 0 && !verifyLedger.Verified,
		"nudge":              BuildVerifyNudge(),
	}
}

// ── Spec-kit integration ───────────────────────────────────────────────────

var (
	specKitMu         sync.RWMutex
	specKitProjectPath string
	specKitVerifier   *SpecKitVerifier
)

// SetProjectPath sets the project path for spec-kit artifact discovery.
// Call this once at session start so spec-kit artifacts can be loaded.
// Pass "" to disable spec-kit checking.
func SetProjectPath(path string) {
	specKitMu.Lock()
	defer specKitMu.Unlock()
	specKitProjectPath = path
	specKitVerifier = nil // force re-init on next check
}

// specKitCheck runs spec-kit verification against the current changed paths.
func specKitCheck() map[string]interface{} {
	specKitMu.Lock()
	defer specKitMu.Unlock()

	if specKitProjectPath == "" {
		return map[string]interface{}{
			"detected": false,
			"reason":   "no project path set",
		}
	}

	if specKitVerifier == nil {
		specKitVerifier = NewSpecKitVerifier(specKitProjectPath)
	}

	changed := GetChangedPaths()
	result := specKitVerifier.Verify(changed, nil)
	return result
}

// BuildVerifyNudgeSpecKit builds a spec-kit verification nudge fragment, or empty string.
func BuildVerifyNudgeSpecKit() string {
	sk := specKitCheck()
	detected, _ := sk["detected"].(bool)
	if !detected {
		return ""
	}

	coverage, ok := sk["coverage"].(map[string]interface{})
	if !ok {
		return ""
	}

	uncovered, _ := coverage["uncovered"].(int)
	total, _ := coverage["total"].(int)
	covered, _ := coverage["covered"].(int)

	if uncovered == 0 {
		return ""
	}

	return "\n\n[Spec-kit coverage: " + itoa(covered) + "/" + itoa(total) +
		" requirements covered (" + itoa(uncovered) + " uncovered).\n" +
		"Review the spec requirements flagged above and ensure the implementation " +
		"addresses each one.]"
}

// itoa is a simple integer to string converter (no import needed).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	negative := false
	if n < 0 {
		negative = true
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if negative {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}
