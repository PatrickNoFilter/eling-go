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