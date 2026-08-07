// Package layers - Rust-style guardrails scaffolding (P3, ECCADAption).
//
// Like Rust's compiler-builtin runtime checks (Miri, bounds checks), this
// module encodes the invariants that P1/P2 must enforce *by construction* as
// white-box checks. Each check is a pure function of a read-only witness
// snapshot; AssertAll runs every check and returns only the violated
// invariants, so a caller can audit (soft) or veto (hard) the emitting
// persist path. All wiring is INERT by default — nothing here changes
// behavior unless a caller opts in via GuardrailsConfig.
package layers

import (
	"fmt"
	"sort"
	"strings"
)

// GuardrailID enumerates the white-box invariants checked by this module.
type GuardrailID int

const (
	// GuardrailEndMessageUnderBudget — witness: internal/layers/shaping.go.
	// The shaped final assistant message must never exceed the configured
	// rune budget (cap + truncation trailer allowance).
	GuardrailEndMessageUnderBudget GuardrailID = iota
	// GuardrailSessionTokenMonotonic — witness: internal/session/session.go
	// (verifyTotals). The persisted total_tokens must be monotonic: it may
	// never decrease between successive saves.
	GuardrailSessionTokenMonotonic
	// GuardrailOpenersMatchPerms — witness: internal/tools/permissions.go
	// (PermPolicy.ModeFor). Every tool that was actually opened must be
	// present in the configured permission projection (or the projection is
	// inactive, in which case nothing is constrained).
	GuardrailOpenersMatchPerms
	// GuardrailMCPserverMatchesConfig — witness: internal/mcp/mcp.go
	// (Manager.Reset). The set of live MCP servers must equal the set of
	// configured servers.
	GuardrailMCPserverMatchesConfig
)

// String returns the stable identifier of the guardrail.
func (g GuardrailID) String() string {
	switch g {
	case GuardrailEndMessageUnderBudget:
		return "end_message_under_budget"
	case GuardrailSessionTokenMonotonic:
		return "session_token_monotonic"
	case GuardrailOpenersMatchPerms:
		return "openers_match_perms"
	case GuardrailMCPserverMatchesConfig:
		return "mcp_server_matches_config"
	default:
		return fmt.Sprintf("guardrail_%d", int(g))
	}
}

// GuardrailsAssert is a single violated white-box invariant. Witness is a
// file:line-provenance hint pointing at the code that produced the data.
type GuardrailsAssert struct {
	ID        GuardrailID
	Violation string
	Witness   string
}

// GuardrailsAssertType returns one durable human line for the assert.
func (a GuardrailsAssert) String() string {
	return fmt.Sprintf("[%s] %s (witness: %s)", a.ID, a.Violation, a.Witness)
}

// GuardWitness is the read-only snapshot of the four guarded areas at the
// moment the check runs. The caller (agent/persist boundary) builds it from
// its live state; a zero-valued field means "not observed" and skips that
// check (inert by default).
type GuardWitness struct {
	// End-message budget (P1): actual shaped length vs the policy applied.
	EndMsgPolicy EndMessagePolicy
	EndMsgLen    int // runes of the final message after shaping

	// Session tokens (P2.1): monotonicity across two saves.
	SessionPrev int // total_tokens at the previous save (0 if unset)
	SessionNext int // total_tokens at the next save (0 if unset)

	// Permissions projection (P2.3): openers actually permitted vs the
	// configured projection. Empty Permitted = inactive policy = no check.
	Openers   []string // tool names actually opened
	Permitted []string // tool names the permission projection allows

	// MCP (P2.2): configured vs live server sets after a Reset.
	MCPConfigured []string
	MCPLive       []string
}

// AssertAll runs every guardrail check over the witness and returns only the
// violated invariants. A witness field left at its zero value is skipped, so
// an all-zero witness produces zero asserts (inert scaffolding).
func AssertAll(w GuardWitness) []GuardrailsAssert {
	var out []GuardrailsAssert
	if a := checkEndMessageBudget(w); a != nil {
		out = append(out, *a)
	}
	if a := checkSessionTokenMonotonic(w); a != nil {
		out = append(out, *a)
	}
	if a := checkOpenersMatchPerms(w); a != nil {
		out = append(out, *a)
	}
	if a := checkMCPserverMatchesConfig(w); a != nil {
		out = append(out, *a)
	}
	return out
}

// DescribeAll renders an empty-friendly, one-line-per-assert table of the
// violations (used by the soft-audit and hard-veto call sites).
func DescribeAll(asserts []GuardrailsAssert) string {
	if len(asserts) == 0 {
		return "guardrails: 0 violations"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "guardrails: %d violation(s)\n", len(asserts))
	for _, a := range asserts {
		fmt.Fprintf(&b, "  - %s\n", a.String())
	}
	return b.String()
}

// checkEndMessageBudget enforces the P1 invariant that the final assistant
// message never exceeds the configured rune budget. The budget allowance is
// the cap PLUS the truncation trailer (which is appended after cutting), so a
// properly shaped message still fits — mirroring the established behavior in
// the shaping pump tests.
func checkEndMessageBudget(w GuardWitness) *GuardrailsAssert {
	if w.EndMsgPolicy.MaxRunes <= 0 {
		return nil // no cap configured → nothing to violate
	}
	allowed := w.EndMsgPolicy.MaxRunes + len([]rune(truncationTrailer))
	if w.EndMsgLen > allowed {
		return &GuardrailsAssert{
			ID:        GuardrailEndMessageUnderBudget,
			Violation: fmt.Sprintf("final message %d runes exceeds budget %d (cap %d)", w.EndMsgLen, allowed, w.EndMsgPolicy.MaxRunes),
			Witness:   "internal/layers/shaping.go (NewEndMessage)",
		}
	}
	return nil
}

// checkSessionTokenMonotonic enforces the P2.1 invariant: total_tokens must
// never decrease between saves. A next token of 0 (unset) is treated as
// "not tracked" and skipped.
func checkSessionTokenMonotonic(w GuardWitness) *GuardrailsAssert {
	if w.SessionNext == 0 {
		return nil // no data for the next save
	}
	if w.SessionPrev > w.SessionNext {
		return &GuardrailsAssert{
			ID:        GuardrailSessionTokenMonotonic,
			Violation: fmt.Sprintf("total_tokens decreased %d -> %d between saves", w.SessionPrev, w.SessionNext),
			Witness:   "internal/session/session.go (verifyTotals)",
		}
	}
	return nil
}

// checkOpenersMatchPerms enforces the P2.3 invariant: a tool that was opened
// must be explainable by the active permission projection. When the
// projection is empty the policy is inactive and nothing is constrained.
func checkOpenersMatchPerms(w GuardWitness) *GuardrailsAssert {
	if len(w.Permitted) == 0 {
		return nil // inactive projection: nothing constrained
	}
	ok := make(map[string]bool, len(w.Permitted))
	for _, t := range w.Permitted {
		ok[t] = true
	}
	var missing []string
	for _, t := range w.Openers {
		if !ok[t] {
			missing = append(missing, t)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return &GuardrailsAssert{
			ID:        GuardrailOpenersMatchPerms,
			Violation: fmt.Sprintf("opened tools not in permission projection: %s", strings.Join(missing, ", ")),
			Witness:   "internal/tools/permissions.go (PermPolicy.ModeFor)",
		}
	}
	return nil
}

// checkMCPserverMatchesConfig enforces the P2.2 invariant: after a Reset the
// live server set must equal the configured server set exactly.
func checkMCPserverMatchesConfig(w GuardWitness) *GuardrailsAssert {
	if len(w.MCPConfigured) == 0 && len(w.MCPLive) == 0 {
		return nil // both empty: nothing to compare
	}
	cfg := append([]string(nil), w.MCPConfigured...)
	live := append([]string(nil), w.MCPLive...)
	sort.Strings(cfg)
	sort.Strings(live)
	if strings.Join(cfg, ",") != strings.Join(live, ",") {
		return &GuardrailsAssert{
			ID:        GuardrailMCPserverMatchesConfig,
			Violation: fmt.Sprintf("live servers %v do not equal configured %v", w.MCPLive, w.MCPConfigured),
			Witness:   "internal/mcp/mcp.go (Manager.Reset)",
		}
	}
	return nil
}