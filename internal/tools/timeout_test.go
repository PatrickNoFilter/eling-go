package tools

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// A plain (non-ctx) tool that sleeps past its budget. The registry guard must
// cut it off and return a timeout error instead of blocking forever.
func TestExecuteContextEnforcesPlainToolTimeout(t *testing.T) {
	name := "test_slow_tool"
	DefaultRegistry.Register(Tool{
		Name:     name,
		Category: "system",
		Timeout:  200 * time.Millisecond, // tiny budget for the test
		Execute: func(args map[string]interface{}) (interface{}, error) {
			time.Sleep(5 * time.Second) // would hang without the guard
			return "done", nil
		},
	})
	defer DefaultRegistry.Unregister(name)

	start := time.Now()
	_, err := DefaultRegistry.ExecuteContext(context.Background(), name, nil)
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

// A ctx-aware tool must be cancelled when the caller's context deadline fires
// (this is what the turn max_duration path relies on).
func TestExecuteContextCancelsCtxAwareTool(t *testing.T) {
	name := "test_ctx_slow_tool"
	DefaultRegistry.Register(Tool{
		Name:     name,
		Category: "system",
		Timeout:  5 * time.Minute, // generous budget; caller deadline should win
		ExecuteCtx: func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(5 * time.Second):
				return "done", nil
			}
		},
	})
	defer DefaultRegistry.Unregister(name)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := DefaultRegistry.ExecuteContext(ctx, name, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("ctx-aware tool not cancelled promptly: took %v", elapsed)
	}
}

// A fast tool with a budget must complete normally and promptly.
func TestExecuteContextFastToolSucceeds(t *testing.T) {
	name := "test_fast_tool"
	DefaultRegistry.Register(Tool{
		Name:     name,
		Category: "system",
		Timeout:  5 * time.Second,
		Execute: func(args map[string]interface{}) (interface{}, error) {
			return "fast", nil
		},
	})
	defer DefaultRegistry.Unregister(name)

	result, err := DefaultRegistry.ExecuteContext(context.Background(), name, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "fast" {
		t.Fatalf("expected 'fast', got %v", result)
	}
}

// read must refuse oversized files with an actionable error instead of
// attempting to slurp a multi-GB file (the "takes too long" complaint).
func TestReadRefusesOversizedFile(t *testing.T) {
	dir := t.TempDir()
	path := fmt.Sprintf("%s/big.bin", dir)
	// Create a sparse file larger than the 64 MiB cap without writing 64 MiB.
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	f.Close()
	if err := os.Truncate(path, maxReadBytes+1); err != nil {
		t.Fatalf("create sparse file: %v", err)
	}
	t.Cleanup(func() { os.Remove(path) })

	_, err = readExecute(map[string]interface{}{"path": path})
	if err == nil {
		t.Fatal("expected oversized-file error, got nil")
	}
	if !strings.Contains(err.Error(), "safety cap") {
		t.Fatalf("expected safety-cap error, got: %v", err)
	}
}

// read must abort promptly when the caller's context is cancelled (Ctrl+C /
// turn deadline while blocked on a slow file).
func TestReadAbortsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	_, err := readExecuteCtx(ctx, map[string]interface{}{"path": "nonexistent"})
	if err == nil {
		t.Fatal("expected abort error on cancelled ctx")
	}
}

// Regression: the tool's OWN budget must apply even when the caller's context
// already carries a deadline that is LONGER than the tool's Timeout. Before the
// fix, ExecuteContext skipped context.WithTimeout whenever ctx.Deadline()
// returned a deadline, so a 30s tool called inside a 300s turn would inherit
// only the 300s cap — the "timeout doesn't kick in" bug. Go takes the min of
// parent deadline and budget, so budget must hold regardless of the parent.
func TestExecuteContextToolBudgetHoldsUnderLongerParentDeadline(t *testing.T) {
	name := "test_budget_vs_longer_parent"
	DefaultRegistry.Register(Tool{
		Name:     name,
		Category: "system",
		Timeout:  200 * time.Millisecond, // tool cap is the tighter constraint
		ExecuteCtx: func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(5 * time.Second): // would exceed budget if uncapped
				return "done", nil
			}
		},
	})
	defer DefaultRegistry.Unregister(name)

	// Parent deadline is far longer than the tool's 200ms budget.
	parent, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	start := time.Now()
	_, err := DefaultRegistry.ExecuteContext(parent, name, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected the tool's own budget to fire, got nil")
	}
	if !strings.Contains(err.Error(), "deadline exceeded") && !strings.Contains(err.Error(), "context") {
		t.Fatalf("expected ctx deadline error, got: %v", err)
	}
	// The tool budget (200ms) must fire long before the 5-minute parent: the
	// executor should return in well under the uncapped window.
	if elapsed > 2*time.Second {
		t.Fatalf("tool budget was not enforced: returned after %v (parent was 5min)", elapsed)
	}
}
