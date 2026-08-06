package agent

import (
	"context"
	"net/http/httptest"
	"testing"

	"eling/internal/config"
	"eling/internal/provider"
)

// These tests verify the D2 (DeepCode heist) wiring: that the verify→repair
// verifier is (a) commissioned on by default, (b) stays off when --no-verify /
// verify.enabled:false commissioned it off — even across a full Ask turn that
// would normally re-enable it — and (c) plan mode opts out per turn but restores
// on the next non-plan turn. The verifier's own loop semantics are covered by
// internal/verify/verify_test.go.

func verifyTestAgent(t *testing.T, ts *httptest.Server, verifyOn bool) *Agent {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Verify.Enabled = verifyOn
	cfg.Agent.Providers = []config.ProviderConfig{
		{Name: "mock", Model: "mock-model", BaseURL: ts.URL, APIKey: "test-key"},
	}
	cfg.Session.SaveDir = t.TempDir()
	cfg.Agent.MaxTurnRounds = 4
	ag, err := New(cfg)
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	return ag
}

func TestVerifyWiringCommissionedOnByDefault(t *testing.T) {
	ts, _ := mockPlanServer(t)
	ag := verifyTestAgent(t, ts, true) // verify.enabled default true
	if ag.verifyCtl == nil {
		t.Fatal("expected verifyCtl to be constructed")
	}
	if !ag.verifyCtl.Enabled() {
		t.Fatal("verifyCtl should be enabled with the default config")
	}
}

func TestVerifyWiringNoVerifyStaysOffEvenAfterAsk(t *testing.T) {
	ts, _ := mockPlanServer(t)
	ag := verifyTestAgent(t, ts, false) // --no-verify / verify.enabled:false
	if !(ag.verifyCtl != nil && !ag.verifyCtl.Enabled()) {
		t.Fatal("verifyCtl should be commissioned disabled from birth")
	}
	// A full non-plan Ask runs Enable() at turn start; because the verifier
	// was commissioned disabled, Enable() must be a no-op and it stays off.
	if _, err := ag.Ask(context.Background(), "hello"); err != nil {
		t.Fatalf("Ask error: %v", err)
	}
	if ag.verifyCtl.Enabled() {
		t.Fatal("--no-verify must never be resurrected by per-turn plan-mode logic")
	}
}

func TestVerifyWiringPlanModeOptsOutThenRestores(t *testing.T) {
	ts, _ := mockPlanServer(t)
	ag := verifyTestAgent(t, ts, true)
	ag.PlanApprover = func(string) PlanVerdict { return PlanApprove }

	// Plan mode turn: verification is an explicit user gate, so auto-verify
	// opts out for this turn.
	ag.PlanEnabled.Store(true)
	if _, err := ag.Ask(context.Background(), "plan task"); err != nil {
		t.Fatalf("Ask(plan) error: %v", err)
	}
	if ag.verifyCtl.Enabled() {
		t.Fatal("plan mode should opt out of auto-verification for the turn")
	}

	// Next non-plan turn: verification is restored for the commissioned agent.
	ag.PlanEnabled.Store(false)
	if _, err := ag.Ask(context.Background(), "regular task"); err != nil {
		t.Fatalf("Ask(regular) error: %v", err)
	}
	if !ag.verifyCtl.Enabled() {
		t.Fatal("non-plan turn should restore verification for a commissioned agent")
	}
}

func TestVerifyToolCallsReducesProviderCalls(t *testing.T) {
	in := []provider.ToolCall{
		{Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "read", Arguments: `{"file_path":"/a/b.go"}`}},
		{Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "edit", Arguments: `{"file_path":"/a/b.go"}`}},
	}
	out := verifyToolCalls(in)
	if len(out) != 2 {
		t.Fatalf("expected 2 reduced calls, got %d", len(out))
	}
	if out[0].Name != "read" || out[0].Args != `{"file_path":"/a/b.go"}` {
		t.Errorf("call 0 not reduced correctly: %+v", out[0])
	}
	if out[1].Name != "edit" {
		t.Errorf("call 1 name not preserved: %+v", out[1])
	}
}
