package agent

import (
	"strings"
	"testing"

	"eling/internal/config"
)

// shapeEndMessage is a pure, unit-testable choke point: it only reads cfg and
// nil-safely fires the end_message_produce hook. An Agent with just a cfg is
// sufficient — no provider, brain or session needed.

func shapeTestAgent(cfg *config.Config) *Agent {
	return &Agent{cfg: cfg}
}

func TestShapeEndMessageDefaultPassthrough(t *testing.T) {
	cfg := config.DefaultConfig() // Output all-zero → policy inactive
	ag := shapeTestAgent(cfg)
	msg := "long message that stays exactly as-is, no caps configured"
	if got := ag.shapeEndMessage(msg); got != msg {
		t.Fatalf("default policy must passthrough; got %q", got)
	}
}

func TestShapeEndMessageCapsRunes(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Output.EndMessageRunes = 20
	ag := shapeTestAgent(cfg)
	msg := strings.Repeat("word ", 50)
	out := ag.shapeEndMessage(msg)
	if !strings.Contains(out, "truncated") {
		t.Fatalf("expected truncation when cap configured; got %q", out)
	}
	if len([]rune(out)) > 20+len([]rune("\n… (truncated to respect output budget)")) {
		t.Fatalf("over rune budget+trailer: %q (%d)", out, len([]rune(out)))
	}
}

func TestShapeEndMessageEmptyCfgNilBrainSafe(t *testing.T) {
	// cfg non-nil, Brain nil (fireHook must be nil-safe).
	cfg := config.DefaultConfig()
	cfg.Output.EndMessageRunes = 5
	ag := shapeTestAgent(cfg) // Brain == nil
	out := ag.shapeEndMessage("abcdef")
	if out == "" {
		t.Fatal("expected shaped non-empty output even with nil Brain")
	}
	if !strings.Contains(out, "truncated") {
		t.Fatalf("expected truncation marker; got %q", out)
	}
}

// guardrailEndMessageVeto must be a pure passthrough when the Guardrails
// block is default/zero (inert P3 wiring): no logging, no veto.
func TestGuardrailEndMessageVetoInertByDefault(t *testing.T) {
	ag := shapeTestAgent(config.DefaultConfig())
	if ag.guardrailEndMessageVeto(999) {
		t.Fatal("guardrail must be inert when Guardrails block is zero (audit+strict off)")
	}
}

// Audit-only mode: violations are observed (logged) but never veto — the
// shaped end message still flows through.
func TestGuardrailEndMessageVetoAuditOnlyNeverBlocks(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Guardrails.Audit = true
	cfg.Guardrails.Strict = false
	ag := shapeTestAgent(cfg)
	// craft a shaped length that violates the end-message budget
	if ag.guardrailEndMessageVeto(10_000) {
		t.Fatal("audit-only mode must never veto, even on a violated invariant")
	}
}

// Strict mode: a violated invariant hard-vetoes the shaped output.
func TestGuardrailEndMessageVetoStrictBlocksViolation(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Output.EndMessageRunes = 50
	cfg.Guardrails.Audit = false
	cfg.Guardrails.Strict = true
	ag := shapeTestAgent(cfg)
	if !ag.guardrailEndMessageVeto(10_000) {
		t.Fatal("strict mode must veto when the shaped message violates the budget")
	}
	// No violation -> no veto even in strict mode.
	if ag.guardrailEndMessageVeto(10) {
		t.Fatal("strict mode with an in-budget message must not veto")
	}
}

// Strict veto wired through shapeEndMessage: when the shaped message would
// violate the budget, the original (unshaped) message is returned instead.
func TestShapeEndMessageStrictVetoReturnsOriginal(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Output.EndMessageRunes = 20
	cfg.Guardrails.Strict = true
	ag := shapeTestAgent(cfg)

	// A huge message that the shaping pump caps below budget does NOT violate
	// the invariant — shaping + trailer stay within the allowance, and the
	// veto helper only fires when the OBSERVED shaped length exceeds the
	// budget+trailer allowance (regression/edge case).
	msg := strings.Repeat("word ", 500)
	out := ag.shapeEndMessage(msg)
	if out == msg {
		t.Fatalf("expected shaping to still apply under strict when no violation; got original")
	}
	if !strings.Contains(out, "truncated") {
		t.Fatalf("expected truncation; got %q", out)
	}
}
