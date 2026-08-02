package tools

import (
	"context"
	"errors"
	"testing"
)

// TestStatsRecordsCallsAndFailures verifies the A5 runtime metrics: successful
// and failing tool calls are recorded per-tool, totals aggregate correctly,
// and pre-dispatch "tool not found" errors do NOT pollute metrics (they return
// before the defer that records).
func TestStatsRecordsCallsAndFailures(t *testing.T) {
	DefaultRegistry.Unregister("test_stats_ok")
	DefaultRegistry.Unregister("test_stats_fail")
	defer func() {
		DefaultRegistry.Unregister("test_stats_ok")
		DefaultRegistry.Unregister("test_stats_fail")
	}()

	DefaultRegistry.Register(Tool{
		Name: "test_stats_ok", Description: "succeeds", Version: "1.0.0",
		Category: "test",
		Execute:  func(map[string]interface{}) (interface{}, error) { return "ok", nil },
	})
	DefaultRegistry.Register(Tool{
		Name: "test_stats_fail", Description: "fails", Version: "1.0.0",
		Category: "test",
		Execute:  func(map[string]interface{}) (interface{}, error) { return nil, errors.New("boom") },
	})

	// Successful call.
	if _, err := DefaultRegistry.ExecuteContext(context.Background(), "test_stats_ok", nil); err != nil {
		t.Fatalf("ok tool failed unexpectedly: %v", err)
	}
	// Failing call.
	if _, err := DefaultRegistry.ExecuteContext(context.Background(), "test_stats_fail", nil); err == nil {
		t.Fatal("fail tool unexpectedly succeeded")
	}
	// Unknown tool — must NOT appear in metrics.
	_, _ = DefaultRegistry.ExecuteContext(context.Background(), "test_stats_nonexistent", nil)

	stats := DefaultRegistry.Stats()

	if calls := stats["tool_calls"].(int64); calls != 2 {
		t.Fatalf("tool_calls = %d, want 2", calls)
	}
	if fails := stats["tool_failures"].(int64); fails != 1 {
		t.Fatalf("tool_failures = %d, want 1", fails)
	}

	perTool, ok := stats["per_tool"].(map[string]map[string]interface{})
	if !ok {
		t.Fatalf("per_tool has wrong type: %T", stats["per_tool"])
	}
	okEntry, exists := perTool["test_stats_ok"]
	if !exists {
		t.Fatal("per_tool missing test_stats_ok")
	}
	if okEntry["calls"].(int64) != 1 || okEntry["failures"].(int64) != 0 {
		t.Fatalf("ok tool metrics wrong: %+v", okEntry)
	}
	if okEntry["success_rate"].(float64) != 1.0 {
		t.Fatalf("ok tool success_rate = %v, want 1.0", okEntry["success_rate"])
	}
	if okEntry["avg_latency_ms"].(float64) < 0 {
		t.Fatalf("ok tool avg_latency_ms negative: %v", okEntry["avg_latency_ms"])
	}

	failEntry, exists := perTool["test_stats_fail"]
	if !exists {
		t.Fatal("per_tool missing test_stats_fail")
	}
	if failEntry["calls"].(int64) != 1 || failEntry["failures"].(int64) != 1 {
		t.Fatalf("fail tool metrics wrong: %+v", failEntry)
	}
	if failEntry["success_rate"].(float64) != 0.0 {
		t.Fatalf("fail tool success_rate = %v, want 0.0", failEntry["success_rate"])
	}

	if _, exists := perTool["test_stats_nonexistent"]; exists {
		t.Fatal("unknown tool should not appear in metrics")
	}

	if rate := stats["tool_success_rate"].(float64); rate != 0.5 {
		t.Fatalf("tool_success_rate = %v, want 0.5", rate)
	}
}
