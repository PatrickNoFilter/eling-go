// Package benchmark provides a comprehensive benchmark suite for ELING,
// inspired by Alibaba's Open Code Review benchmark methodology.
// It measures precision, recall, F1, accuracy, latency, and token usage
// across multiple capability dimensions.
package benchmark

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ──────────────────────────────────────────────────────────────────────
// Core Types
// ──────────────────────────────────────────────────────────────────────

// SuiteID uniquely identifies a benchmark suite dimension.
type SuiteID string

const (
	SuiteMemory  SuiteID = "memory"  // Memory operations
	SuiteSession SuiteID = "session" // Session management
)

// Severity indicates how important a test case is.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
)

// TestCase defines a single benchmark test case.
type TestCase struct {
	// ID is a unique identifier for this test case.
	ID string `json:"id"`
	// Description explains what this test case validates.
	Description string `json:"description"`
	// Suite is the capability dimension this test belongs to.
	Suite SuiteID `json:"suite"`
	// Severity indicates importance.
	Severity Severity `json:"severity"`
	// Input is the test input (task, query, prompt, etc.).
	Input string `json:"input"`
	// ExpectedOutput is the ground-truth expected result.
	ExpectedOutput string `json:"expected_output,omitempty"`
	// ExpectedSuccess is whether execution should succeed.
	ExpectedSuccess bool `json:"expected_success,omitempty"`
	// Tags for filtering test cases.
	Tags []string `json:"tags,omitempty"`
	// Skip marks this test as skipped with a reason.
	Skip string `json:"skip,omitempty"`
	// TimeoutSec is an optional per-test timeout.
	TimeoutSec int `json:"timeout_sec,omitempty"`
}

// TestResult captures the outcome of executing one test case.
type TestResult struct {
	TestCaseID   string        `json:"test_case_id"`
	Passed       bool          `json:"passed"`
	ActualOutput string        `json:"actual_output,omitempty"`
	Error        string        `json:"error,omitempty"`
	Duration     time.Duration `json:"duration"`
	TokensUsed   int           `json:"tokens_used,omitempty"`
	LLMCalls     int           `json:"llm_calls,omitempty"`
}

// SuiteMetrics holds aggregated metrics for one benchmark suite.
type SuiteMetrics struct {
	Suite          SuiteID `json:"suite"`
	TotalTests     int     `json:"total_tests"`
	Passed         int     `json:"passed"`
	Failed         int     `json:"failed"`
	Skipped        int     `json:"skipped"`
	PassRate       float64 `json:"pass_rate"`       // 0.0-1.0
	Precision      float64 `json:"precision"`       // 0.0-1.0
	Recall         float64 `json:"recall"`          // 0.0-1.0
	F1Score        float64 `json:"f1_score"`        // 0.0-1.0
	Accuracy       float64 `json:"accuracy"`        // 0.0-1.0
	AvgDuration    float64 `json:"avg_duration_ms"` // milliseconds
	TotalTokens    int     `json:"total_tokens"`
	AvgTokens      float64 `json:"avg_tokens"`
	TotalLLMCalls  int     `json:"total_llm_calls"`
	TruePositives  int     `json:"true_positives"`
	FalsePositives int     `json:"false_positives"`
	TrueNegatives  int     `json:"true_negatives"`
	FalseNegatives int     `json:"false_negatives"`
}

// Report aggregates all benchmark suite metrics into a single report.
type Report struct {
	// Name is the benchmark report name.
	Name string `json:"name"`
	// CreatedAt is when the benchmark was run.
	CreatedAt time.Time `json:"created_at"`
	// Duration is total benchmark runtime.
	Duration time.Duration `json:"duration"`
	// Suites maps suite IDs to their metrics.
	Suites map[SuiteID]*SuiteMetrics `json:"suites"`
	// Total metrics across all suites.
	TotalTests      int     `json:"total_tests"`
	TotalPassed     int     `json:"total_passed"`
	TotalFailed     int     `json:"total_failed"`
	TotalSkipped    int     `json:"total_skipped"`
	OverallPassRate float64 `json:"overall_pass_rate"`
	OverallF1       float64 `json:"overall_f1"`
	TotalTokens     int     `json:"total_tokens"`
	TotalDuration   string  `json:"total_duration"`
}

// ──────────────────────────────────────────────────────────────────────
// Test Executor Interface
// ──────────────────────────────────────────────────────────────────────

// TestExecutor runs a single test case and returns the result.
// Implementations can be offline (deterministic) or online (LLM-based).
type TestExecutor interface {
	// Execute runs a single test case.
	Execute(ctx context.Context, tc *TestCase) (*TestResult, error)
	// Name returns a human-readable name for this executor.
	Name() string
	// Close cleans up any resources.
	Close() error
}

// ──────────────────────────────────────────────────────────────────────
// Benchmark Suite Runner
// ──────────────────────────────────────────────────────────────────────

// Runner manages the execution of benchmark suites.
type Runner struct {
	mu           sync.Mutex
	executor     TestExecutor
	cases        []TestCase
	results      map[string]*TestResult // TestCaseID -> result
	reports      []*Report
	suiteMetrics map[SuiteID]*SuiteMetrics
}

// NewRunner creates a new benchmark runner with the given executor.
func NewRunner(executor TestExecutor) *Runner {
	return &Runner{
		executor:     executor,
		cases:        make([]TestCase, 0),
		results:      make(map[string]*TestResult),
		reports:      make([]*Report, 0),
		suiteMetrics: make(map[SuiteID]*SuiteMetrics),
	}
}

// AddCases adds test cases to the runner.
func (r *Runner) AddCases(cases ...TestCase) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cases = append(r.cases, cases...)
}

// AddSuite adds a complete suite of test cases.
func (r *Runner) AddSuite(suite SuiteID, cases []TestCase) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range cases {
		cases[i].Suite = suite
	}
	r.cases = append(r.cases, cases...)
}

// Cases returns all registered test cases.
func (r *Runner) Cases() []TestCase {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]TestCase, len(r.cases))
	copy(result, r.cases)
	return result
}

// FilterCases returns test cases matching the given suite.
func (r *Runner) FilterCases(suite SuiteID) []TestCase {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []TestCase
	for _, tc := range r.cases {
		if tc.Suite == suite {
			result = append(result, tc)
		}
	}
	return result
}

// RunAll executes all registered test cases.
func (r *Runner) RunAll(ctx context.Context, name string) (*Report, error) {
	r.mu.Lock()
	cases := make([]TestCase, len(r.cases))
	copy(cases, r.cases)
	r.mu.Unlock()

	return r.RunCases(ctx, name, cases)
}

// RunSuite executes all test cases in a specific suite.
func (r *Runner) RunSuite(ctx context.Context, name string, suite SuiteID) (*Report, error) {
	cases := r.FilterCases(suite)
	return r.RunCases(ctx, fmt.Sprintf("%s/%s", name, suite), cases)
}

// RunCases executes a specific set of test cases.
func (r *Runner) RunCases(ctx context.Context, name string, cases []TestCase) (*Report, error) {
	start := time.Now()

	// Filter out skipped cases
	var toRun []TestCase
	for _, tc := range cases {
		if tc.Skip != "" {
			r.recordResult(&tc, &TestResult{
				TestCaseID: tc.ID,
				Passed:     false,
				Error:      fmt.Sprintf("skipped: %s", tc.Skip),
			})
			continue
		}
		toRun = append(toRun, tc)
	}

	// Execute each test case sequentially (parallel is future work)
	for _, tc := range toRun {
		select {
		case <-ctx.Done():
			return r.buildReport(name, start), ctx.Err()
		default:
		}

		// Run with optional timeout
		var result *TestResult
		var err error
		if tc.TimeoutSec > 0 {
			tCtx, cancel := context.WithTimeout(ctx, time.Duration(tc.TimeoutSec)*time.Second)
			result, err = r.executor.Execute(tCtx, &tc)
			cancel()
		} else {
			result, err = r.executor.Execute(ctx, &tc)
		}

		if err != nil {
			result = &TestResult{
				TestCaseID: tc.ID,
				Passed:     false,
				Error:      err.Error(),
			}
		}

		r.recordResult(&tc, result)
	}

	return r.buildReport(name, start), nil
}

// recordResult stores a test result and updates suite metrics.
func (r *Runner) recordResult(tc *TestCase, tr *TestResult) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.results[tc.ID] = tr

	// Update suite metrics lazily (aggregated in buildReport)
	_ = tc.Suite // will be used in buildReport
}

// buildReport aggregates all results into a Report.
func (r *Runner) buildReport(name string, start time.Time) *Report {
	r.mu.Lock()
	defer r.mu.Unlock()

	report := &Report{
		Name:      name,
		CreatedAt: time.Now(),
		Suites:    make(map[SuiteID]*SuiteMetrics),
	}

	totalTests := 0
	totalPassed := 0
	totalFailed := 0
	totalSkipped := 0
	totalTokens := 0
	totalTP := 0
	totalFP := 0
	totalTN := 0
	totalFN := 0

	// Group results by suite
	suiteResults := make(map[SuiteID][]*TestResult)
	suiteCaseMap := make(map[SuiteID]*TestCase)
	for _, tc := range r.cases {
		if tr, ok := r.results[tc.ID]; ok {
			suiteResults[tc.Suite] = append(suiteResults[tc.Suite], tr)
			suiteCaseMap[tc.Suite] = &tc
		}
	}

	for suite, results := range suiteResults {
		sm := &SuiteMetrics{Suite: suite}
		sm.TotalTests = len(results)

		for _, tr := range results {
			if tr.Error != "" && strings.HasPrefix(tr.Error, "skipped:") {
				sm.Skipped++
				continue
			}
			if tr.Passed {
				sm.Passed++
			} else {
				sm.Failed++
			}
			sm.TotalTokens += tr.TokensUsed
			sm.TotalLLMCalls += tr.LLMCalls
			sm.AvgDuration += tr.Duration.Seconds() * 1000 // accumulate ms

			// Determine TP/FP/TN/FN based on test type
			if tr.Passed {
				sm.TruePositives++
			} else {
				sm.FalseNegatives++
			}
		}

		// Calculate metrics
		if len(results) > 0 {
			sm.PassRate = float64(sm.Passed) / float64(len(results)-sm.Skipped)
		}
		if len(results) > 0 {
			sm.AvgDuration /= float64(len(results) - sm.Skipped)
		}
		if sm.TotalTests-sm.Skipped > 0 {
			sm.AvgTokens = float64(sm.TotalTokens) / float64(sm.TotalTests-sm.Skipped)
		}

		// Precision, Recall, F1
		sm.Precision = calcPrecision(sm.TruePositives, sm.FalsePositives)
		sm.Recall = calcRecall(sm.TruePositives, sm.FalseNegatives)
		sm.F1Score = calcF1(sm.Precision, sm.Recall)
		sm.Accuracy = calcAccuracy(sm.TruePositives, sm.TrueNegatives, sm.FalsePositives, sm.FalseNegatives)

		report.Suites[suite] = sm
		totalTests += sm.TotalTests
		totalPassed += sm.Passed
		totalFailed += sm.Failed
		totalSkipped += sm.Skipped
		totalTokens += sm.TotalTokens
		totalTP += sm.TruePositives
		totalFP += sm.FalsePositives
		totalTN += sm.TrueNegatives
		totalFN += sm.FalseNegatives
	}

	report.TotalTests = totalTests
	report.TotalPassed = totalPassed
	report.TotalFailed = totalFailed
	report.TotalSkipped = totalSkipped
	report.TotalTokens = totalTokens
	report.Duration = time.Since(start)
	report.TotalDuration = report.Duration.String()

	if totalTests-totalSkipped > 0 {
		report.OverallPassRate = float64(totalPassed) / float64(totalTests-totalSkipped)
	}

	overallPrecision := calcPrecision(totalTP, totalFP)
	overallRecall := calcRecall(totalTP, totalFN)
	report.OverallF1 = calcF1(overallPrecision, overallRecall)

	r.reports = append(r.reports, report)
	return report
}

// Results returns all test results.
func (r *Runner) Results() map[string]*TestResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	results := make(map[string]*TestResult, len(r.results))
	for k, v := range r.results {
		results[k] = v
	}
	return results
}

// Reports returns all generated reports.
func (r *Runner) Reports() []*Report {
	r.mu.Lock()
	defer r.mu.Unlock()
	reports := make([]*Report, len(r.reports))
	copy(reports, r.reports)
	return reports
}

// LastReport returns the most recent report, if any.
func (r *Runner) LastReport() *Report {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.reports) == 0 {
		return nil
	}
	return r.reports[len(r.reports)-1]
}

// Executor returns the configured test executor.
func (r *Runner) Executor() TestExecutor {
	return r.executor
}

// ──────────────────────────────────────────────────────────────────────
// Metric Calculation Helpers
// ──────────────────────────────────────────────────────────────────────

func calcPrecision(tp, fp int) float64 {
	denom := tp + fp
	if denom == 0 {
		return 0.0
	}
	return float64(tp) / float64(denom)
}

func calcRecall(tp, fn int) float64 {
	denom := tp + fn
	if denom == 0 {
		return 0.0
	}
	return float64(tp) / float64(denom)
}

func calcF1(precision, recall float64) float64 {
	denom := precision + recall
	if denom == 0 {
		return 0.0
	}
	return 2 * precision * recall / denom
}

func calcAccuracy(tp, tn, fp, fn int) float64 {
	denom := tp + tn + fp + fn
	if denom == 0 {
		return 0.0
	}
	return float64(tp+tn) / float64(denom)
}

// ──────────────────────────────────────────────────────────────────────
// Assertion Helpers
// ──────────────────────────────────────────────────────────────────────

// AssertEqual checks if two strings are equal.
func AssertEqual(expected, actual string) (bool, string) {
	if expected == actual {
		return true, ""
	}
	return false, fmt.Sprintf("expected %q, got %q", expected, truncateStr(actual, 100))
}

// AssertContains checks if a string contains a substring.
func AssertContains(s, substr string) (bool, string) {
	if strings.Contains(s, substr) {
		return true, ""
	}
	return false, fmt.Sprintf("expected %q to contain %q", truncateStr(s, 100), substr)
}

// truncateStr truncates a string for display.
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// ──────────────────────────────────────────────────────────────────────
// Suite Name Sort
// ──────────────────────────────────────────────────────────────────────

// SortedSuiteNames returns suite names sorted alphabetically.
func SortedSuiteNames(suites map[SuiteID]*SuiteMetrics) []SuiteID {
	names := make([]SuiteID, 0, len(suites))
	for n := range suites {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		return string(names[i]) < string(names[j])
	})
	return names
}
