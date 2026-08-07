package layers

import (
	"strings"
	"testing"
)

// TestGuardrailsAllZeroWitnessInert proves the scaffolding is inert by
// default: a witness with no tracked value yields zero asserts.
func TestGuardrailsAllZeroWitnessInert(t *testing.T) {
	if got := AssertAll(GuardWitness{}); len(got) != 0 {
		t.Fatalf("zero witness must be inert, got %d asserts: %s", len(got), DescribeAll(got))
	}
}

// TestGuardrailEndMessageBudget checks the P1 invariant: only a message that
// exceeds cap+trailer is reported; within budget passes clean.
func TestGuardrailEndMessageBudgetCheck(t *testing.T) {
	policy := EndMessagePolicy{MaxRunes: 50}

	// Within budget (message smaller than cap) → no violation.
	if got := AssertAll(GuardWitness{EndMsgPolicy: policy, EndMsgLen: 40}); len(got) != 0 {
		t.Fatalf("in-budget message flagged: %s", DescribeAll(got))
	}

	// Exactly at cap + trailer allowance → still clean (matches shaping pump).
	if got := AssertAll(GuardWitness{EndMsgPolicy: policy, EndMsgLen: 50 + len([]rune(truncationTrailer))}); len(got) != 0 {
		t.Fatalf("at-budget+trailer flagged: %s", DescribeAll(got))
	}

	// Over budget → exactly one guardrail asserted with correct ID.
	got := AssertAll(GuardWitness{EndMsgPolicy: policy, EndMsgLen: 50 + len([]rune(truncationTrailer)) + 1})
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 assert, got %d: %s", len(got), DescribeAll(got))
	}
	if got[0].ID != GuardrailEndMessageUnderBudget {
		t.Fatalf("wrong ID: got %s", got[0].ID)
	}
	if !strings.Contains(got[0].String(), "internal/layers/shaping.go") {
		t.Fatalf("missing provenance witness: %s", got[0].String())
	}
}

// TestGuardrailSessionTokenMonotonic — P2.1 drift check.
func TestGuardrailSessionTokenMonotonic(t *testing.T) {
	t.Run("unset next is not tracked", func(t *testing.T) {
		if got := AssertAll(GuardWitness{SessionPrev: 100}); len(got) != 0 {
			t.Fatalf("unset next flagged: %s", DescribeAll(got))
		}
	})
	t.Run("increase passes", func(t *testing.T) {
		if got := AssertAll(GuardWitness{SessionPrev: 10, SessionNext: 42}); len(got) != 0 {
			t.Fatalf("monotonic increase flagged: %s", DescribeAll(got))
		}
	})
	t.Run("decrease is a violation", func(t *testing.T) {
		got := AssertAll(GuardWitness{SessionPrev: 42, SessionNext: 10})
		if len(got) != 1 || got[0].ID != GuardrailSessionTokenMonotonic {
			t.Fatalf("expected monotonic violation, got %s", DescribeAll(got))
		}
	})
}

// TestGuardrailOpenersMatchPerms — active projection constrains openers,
// inactive projection (empty) constrains nothing.
func TestGuardrailOpenersMatchPerms(t *testing.T) {
	t.Run("inactive_projection_no_constraints", func(t *testing.T) {
		if got := AssertAll(GuardWitness{Openers: []string{"bash"}}); len(got) != 0 {
			t.Fatalf("inactive projection flagged: %s", DescribeAll(got))
		}
	})
	t.Run("all_openers_allowed", func(t *testing.T) {
		w := GuardWitness{
			Openers:     []string{"bash", "read"},
			Permitted:   []string{"read", "bash", "write"},
		}
		if got := AssertAll(w); len(got) != 0 {
			t.Fatalf("allowed openers flagged: %s", DescribeAll(got))
		}
	})
	t.Run("unknown_opener_flagged", func(t *testing.T) {
		w := GuardWitness{
			Openers:     []string{"bash", "rm_rf_unlisted"},
			Permitted:   []string{"read", "bash"},
		}
		got := AssertAll(w)
		if len(got) != 1 || got[0].ID != GuardrailOpenersMatchPerms {
			t.Fatalf("expected openers violation, got %s", DescribeAll(got))
		}
		if !strings.Contains(got[0].Violation, "rm_rf_unlisted") {
			t.Fatalf("missing opener in violation text: %s", got[0].Violation)
		}
	})
}

// TestGuardrailMCPserverMatchesConfig — live set must equal config set.
func TestGuardrailMCPserverMatchesConfig(t *testing.T) {
	t.Run("both_empty_passes", func(t *testing.T) {
		if got := AssertAll(GuardWitness{}); len(got) != 0 {
			t.Fatalf("empty sets flagged: %s", DescribeAll(got))
		}
	})
	t.Run("matching_sets_pass", func(t *testing.T) {
		w := GuardWitness{MCPConfigured: []string{"a", "b"}, MCPLive: []string{"b", "a"}}
		if got := AssertAll(w); len(got) != 0 {
			t.Fatalf("matching sets flagged: %s", DescribeAll(got))
		}
	})
	t.Run("stale_live_server_flagged", func(t *testing.T) {
		w := GuardWitness{MCPConfigured: []string{"a"}, MCPLive: []string{"a", "stale"}}
		got := AssertAll(w)
		if len(got) != 1 || got[0].ID != GuardrailMCPserverMatchesConfig {
			t.Fatalf("expected mcp mismatch, got %s", DescribeAll(got))
		}
	})
}

// TestGuardrailsDescribeAll — human table renders 0 and n violations.
func TestGuardrailsDescribeAll(t *testing.T) {
	if s := DescribeAll(nil); !strings.Contains(s, "0 violations") {
		t.Fatalf("clean describe wrong: %q", s)
	}
	got := AssertAll(GuardWitness{SessionPrev: 5, SessionNext: 1})
	s := DescribeAll(got)
	if !strings.Contains(s, "1 violation") || !strings.Contains(s, "session_token_monotonic") {
		t.Fatalf("describe table malformed: %q", s)
	}
}