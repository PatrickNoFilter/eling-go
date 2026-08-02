package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"eling/internal/config"
)

// mockPlanServer returns a plain assistant content response (no tool calls)
// for every /chat/completions request and counts them. Other endpoints
// (e.g. embedding probes) are answered but not counted.
func mockPlanServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	calls := &atomic.Int64{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			calls.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"id":      "mock-completion",
			"object":  "chat.completion",
			"created": 1234567890,
			"model":   "mock-model",
			"choices": []interface{}{
				map[string]interface{}{
					"index": 0,
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "1. Inspect the request\n2. Execute the change\n3. Verify results",
					},
				},
			},
			"usage": map[string]interface{}{"total_tokens": 42},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(ts.Close)
	return ts, calls
}

// planTestAgent builds an agent wired to the mock server with a throwaway
// session dir so tests never touch the real ~/.eling state.
func planTestAgent(t *testing.T, ts *httptest.Server) *Agent {
	t.Helper()
	cfg := config.DefaultConfig()
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

// TestPlanModeRejectAbortsBeforeTools verifies that when the user rejects a
// drafted plan, Ask returns the rejection message and no tool-execution
// request is made — only the plan draft call hits the provider.
func TestPlanModeRejectAbortsBeforeTools(t *testing.T) {
	ts, calls := mockPlanServer(t)
	ag := planTestAgent(t, ts)
	ag.PlanEnabled.Store(true)
	ag.PlanApprover = func(string) PlanVerdict { return PlanReject }

	out, err := ag.Ask(context.Background(), "deploy the service")
	if err != nil {
		t.Fatalf("Ask error: %v", err)
	}
	if !strings.Contains(out, "Plan rejected") {
		t.Errorf("expected rejection message, got %q", out)
	}
	if calls.Load() != 1 {
		t.Errorf("expected exactly 1 provider call (plan draft only), got %d", calls.Load())
	}
}

// TestPlanModeApprovePersistsAndExecutes verifies that approving the plan
// persists it on the session and continues execution with the provider.
func TestPlanModeApprovePersistsAndExecutes(t *testing.T) {
	ts, calls := mockPlanServer(t)
	ag := planTestAgent(t, ts)
	ag.PlanEnabled.Store(true)
	ag.PlanApprover = func(string) PlanVerdict { return PlanApprove }

	out, err := ag.Ask(context.Background(), "deploy the service")
	if err != nil {
		t.Fatalf("Ask error: %v", err)
	}
	if out == "" {
		t.Fatal("expected a response after approving the plan")
	}
	if calls.Load() < 2 {
		t.Errorf("expected >=2 provider calls (draft + execution), got %d", calls.Load())
	}
	s, ok := ag.Sessions.Get(ag.sessionName)
	if !ok {
		t.Fatal("session not found")
	}
	if s.Plan == "" {
		t.Error("expected approved plan to be persisted on the session")
	}
}

// TestPlanModeSkipContinuesWithoutPlan verifies that skipping plan mode for a
// turn still executes normally and does NOT persist a plan on the session.
func TestPlanModeSkipContinuesWithoutPlan(t *testing.T) {
	ts, calls := mockPlanServer(t)
	ag := planTestAgent(t, ts)
	ag.PlanEnabled.Store(true)
	ag.PlanApprover = func(string) PlanVerdict { return PlanSkip }

	out, err := ag.Ask(context.Background(), "deploy the service")
	if err != nil {
		t.Fatalf("Ask error: %v", err)
	}
	if out == "" {
		t.Fatal("expected a response after skipping plan mode")
	}
	if calls.Load() < 2 {
		t.Errorf("expected >=2 provider calls (draft + execution), got %d", calls.Load())
	}
	s, _ := ag.Sessions.Get(ag.sessionName)
	if s.Plan != "" {
		t.Errorf("expected no plan persisted on skip, got %q", s.Plan)
	}
}

// TestBuildMessagesInjectsApprovedPlan verifies the approved plan is injected
// as a system message so the model follows it during execution.
func TestBuildMessagesInjectsApprovedPlan(t *testing.T) {
	ts, _ := mockPlanServer(t)
	ag := planTestAgent(t, ts)
	s, _ := ag.Sessions.Get(ag.sessionName)
	s.Plan = "1. Step one\n2. Step two"

	msgs := ag.buildMessages("test prompt")
	found := false
	for _, m := range msgs {
		if m.Role == "system" && strings.Contains(m.Content, "Approved execution plan") && strings.Contains(m.Content, "1. Step one") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected buildMessages to inject the approved plan as a system message")
	}
}

// TestSessionPlanPersistence verifies the plan survives save/load, so it is
// visible in the saved session JSON.
func TestSessionPlanPersistence(t *testing.T) {
	ts, _ := mockPlanServer(t)
	ag := planTestAgent(t, ts)
	s, _ := ag.Sessions.Get(ag.sessionName)
	s.Plan = "1. Draft plan for persistence"

	if err := ag.Sessions.Save(ag.sessionName); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := ag.Sessions.Load(ag.sessionName)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Plan != "1. Draft plan for persistence" {
		t.Errorf("plan not persisted: got %q", loaded.Plan)
	}
}
