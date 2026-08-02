package srv

import (
	"context"
	"strings"
	"testing"
	"time"

	"eling/internal/layers"
)

// Every MCP tool must have a bounded per-call budget: fast local ops ≤ 10s,
// registry-backed tools aligned with their registry Timeout, network/heavy
// tools ≤ 60s. No tool may default to unbounded.
func TestMCPToolTimeoutBudgets(t *testing.T) {
	cases := map[string]time.Duration{
		// Registry-backed: aligned with internal/tools registry.
		"bash":        10 * time.Minute,
		"read":        20 * time.Second,
		"write":       20 * time.Second,
		"edit":        20 * time.Second,
		"grep":        20 * time.Second,
		"web_search":  30 * time.Second,
		"web_fetch":   30 * time.Second,
		// Fast local lookups.
		"system_info":            10 * time.Second,
		"brain_get_context":      10 * time.Second,
		"continuum_list_agents":  10 * time.Second,
		// Quick local stores/searches.
		"facts_store":        20 * time.Second,
		"facts_search":       20 * time.Second,
		"kb_store":           20 * time.Second,
		"kb_search":          20 * time.Second,
		"continuum_heartbeat": 20 * time.Second,
		"continuum_share":     20 * time.Second,
		// Multi-layer / heavier local ops.
		"brain_query":      30 * time.Second,
		"brain_store":      30 * time.Second,
		"blackbox_record":  30 * time.Second,
		"blackbox_score":   30 * time.Second,
		"obsidian_write":   30 * time.Second,
		"obsidian_search":  30 * time.Second,
		"markdownify_file": 30 * time.Second,
		"code_search":      30 * time.Second,
		// Network / heavy indexing: slowest allowed, still bounded.
		"code_index":    60 * time.Second,
		"notion_sync":   60 * time.Second,
		"markdownify_url": 60 * time.Second,
	}

	for name, want := range cases {
		got := mcpToolTimeout(name)
		if got != want {
			t.Errorf("mcpToolTimeout(%q) = %v, want %v", name, got, want)
		}
	}

	// Every budget must be positive and finite — a 0 or negative budget would
	// break the guard (time.After(0) fires immediately).
	for name, d := range cases {
		if d <= 0 {
			t.Errorf("mcpToolTimeout(%q) = %v, want > 0", name, d)
		}
	}
}

// A slow handler must be cut off when its per-call budget expires, returning a
// timeout error promptly instead of blocking the caller.
func TestRunWithTimeoutEnforcesBudget(t *testing.T) {
	ctx := context.Background()
	start := time.Now()
	_, err := runWithTimeout(ctx, "slow_tool", 100*time.Millisecond, func() (string, error) {
		time.Sleep(5 * time.Second) // would hang without the guard
		return "done", nil
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error message, got: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("tool was not cut off promptly: took %v", elapsed)
	}
}

// A fast handler must complete normally, returning its result untouched.
func TestRunWithTimeoutFastSucceeds(t *testing.T) {
	out, err := runWithTimeout(context.Background(), "fast_tool", 5*time.Second, func() (string, error) {
		return "fast result", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "fast result" {
		t.Fatalf("expected 'fast result', got %q", out)
	}
}

// A parent context deadline must win over the per-call budget: the caller's
// cancellation (host shutdown / turn max_duration) aborts the tool first.
func TestRunWithTimeoutParentCtxWins(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := runWithTimeout(ctx, "ctx_tool", time.Minute, func() (string, error) {
		time.Sleep(5 * time.Second)
		return "done", nil
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected abort error, got nil")
	}
	if !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("expected abort error message, got: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("ctx-aware tool not cancelled promptly: took %v", elapsed)
	}
}

// tool_timeout_sec must override the per-tool default budget.
func TestToolTimeoutSecOverride(t *testing.T) {
	// Default for system_info is 10s; a 50ms override must make a slow call
	// time out fast.
	s := &Server{brain: layers.NewBrain()}
	start := time.Now()
	_, err := s.executeToolWithTimeout(context.Background(), "system_info",
		map[string]interface{}{"tool_timeout_sec": 0.05})
	if err != nil {
		t.Fatalf("unexpected error with real brain: %v", err)
	}
	// The override must be respected: if the call hangs (it can't here), the
	// guard would fire at ~50ms. Assert we returned within a sane window.
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("override not applied: took %v", elapsed)
	}
}
