package benchmark

import (
	"context"
	"fmt"
	"log"
	"time"

	"eling/internal/agent"
)

// ──────────────────────────────────────────────────────────────────────
// OnlineExecutor — LLM-based test executor (full agent).
// ──────────────────────────────────────────────────────────────────────

// OnlineExecutor runs benchmark tests using the full ELING agent.
// Requires an LLM provider to be configured.
type OnlineExecutor struct {
	ag *agent.Agent
}

// NewOnlineExecutor creates a new online executor.
func NewOnlineExecutor(ag *agent.Agent) *OnlineExecutor {
	return &OnlineExecutor{ag: ag}
}

// Name returns the executor name.
func (e *OnlineExecutor) Name() string { return "online" }

// Close cleans up resources.
func (e *OnlineExecutor) Close() error { return nil }

// Execute runs a single test case using the full agent.
func (e *OnlineExecutor) Execute(ctx context.Context, tc *TestCase) (*TestResult, error) {
	start := time.Now()
	result := &TestResult{
		TestCaseID: tc.ID,
	}

	switch tc.Suite {
	case SuiteMemory, SuiteSession:
		log.Printf("[benchmark] running agent test: %s", tc.ID)
		response, err := e.ag.Ask(ctx, tc.Input)
		if err != nil {
			result.Passed = false
			result.Error = fmt.Sprintf("agent error: %v", err)
			result.Duration = time.Since(start)
			return result, nil
		}

		result.ActualOutput = response
		result.Passed = true

		if tc.ExpectedOutput != "" {
			passed, detail := AssertContains(response, tc.ExpectedOutput)
			if !passed {
				result.Passed = false
				result.Error = detail
			}
		}

		result.Duration = time.Since(start)
		return result, nil

	default:
		result.Passed = false
		result.Error = fmt.Sprintf("unknown suite: %s", tc.Suite)
		result.Duration = time.Since(start)
		return result, nil
	}
}

// ──────────────────────────────────────────────────────────────────────
// NoopExecutor — records test cases without executing them.
// ──────────────────────────────────────────────────────────────────────

// NoopExecutor records test cases but does not run them.
// Useful for listing what tests would be run.
type NoopExecutor struct{}

// NewNoopExecutor creates a new no-op executor.
func NewNoopExecutor() *NoopExecutor { return &NoopExecutor{} }

// Name returns the executor name.
func (e *NoopExecutor) Name() string { return "noop" }

// Close cleans up resources.
func (e *NoopExecutor) Close() error { return nil }

// Execute records the test case but doesn't actually run it.
func (e *NoopExecutor) Execute(_ context.Context, tc *TestCase) (*TestResult, error) {
	return &TestResult{
		TestCaseID:   tc.ID,
		Passed:       true,
		ActualOutput: fmt.Sprintf("noop: would test %s (%s)", tc.Suite, tc.Description),
		Duration:     0,
	}, nil
}
