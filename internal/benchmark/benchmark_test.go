package benchmark

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ──────────────────────────────────────────────────────────────────────
// Benchmark Test Suite
// ──────────────────────────────────────────────────────────────────────

// TestSuite runs the complete offline benchmark suite and generates a report.
// This is the primary entry point for CI/CD integration.
func TestSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping benchmark suite in short mode")
	}

	executor := &NoopExecutor{}
	runner := NewRunner(executor)

	// Load default test cases
	cases := DefaultTestCases()
	runner.AddCases(cases...)

	t.Logf("Running %d benchmark test cases", len(cases))

	// Run all tests
	ctx := context.Background()
	report, err := runner.RunAll(ctx, "eling-benchmark")
	if err != nil {
		t.Fatalf("benchmark failed: %v", err)
	}

	// Print summary
	t.Logf("\n%s", formatTestOutput(report))

	// Save report to file
	saveDir := filepath.Join(os.TempDir(), "eling-benchmarks")
	if err := os.MkdirAll(saveDir, 0755); err == nil {
		filename := fmt.Sprintf("benchmark-%s.json",
			time.Now().Format("20060102-150405"))
		path := filepath.Join(saveDir, filename)
		f, err := os.Create(path)
		if err == nil {
			defer f.Close()
			formatter := NewFormatter(FormatJSON)
			_ = formatter.Format(f, report)
			t.Logf("Report saved to: %s", path)
		}
	}

	// Assert no critical failures
	for _, suite := range SortedSuiteNames(report.Suites) {
		sm := report.Suites[suite]
		if sm.Failed > 0 && sm.TotalTests > 0 {
			t.Errorf("Suite %q: %d/%d tests failed (%.1f%%)",
				suite, sm.Failed, sm.TotalTests, sm.PassRate*100)
		}
	}
}

// TestMemoryOperations runs only memory benchmarks.
func TestMemoryOperations(t *testing.T) {
	executor := &NoopExecutor{}
	runner := NewRunner(executor)
	cases := MemoryCases()
	runner.AddCases(cases...)

	ctx := context.Background()
	report, err := runner.RunSuite(ctx, "memory", SuiteMemory)
	if err != nil {
		t.Fatalf("memory benchmark failed: %v", err)
	}

	sm := report.Suites[SuiteMemory]
	if sm == nil {
		t.Fatal("no memory results")
	}

	t.Logf("Memory: %d/%d passed (%.1f%%)",
		sm.Passed, sm.TotalTests, sm.PassRate*100)

	if sm.PassRate < 0.80 {
		t.Errorf("Memory pass rate < 80%%: %.1f%%", sm.PassRate*100)
	}
}

// ──────────────────────────────────────────────────────────────────────
// Benchmark Runner (integration point for main.go)
// ──────────────────────────────────────────────────────────────────────

// BenchmarkRunner provides a convenient integration point for main.go.
type BenchmarkRunner struct {
	runner  *Runner
	reports []*Report
}

// NewBenchmarkRunner creates a benchmark runner with the noop executor.
func NewBenchmarkRunner() *BenchmarkRunner {
	executor := &NoopExecutor{}
	runner := NewRunner(executor)
	runner.AddCases(DefaultTestCases()...)
	return &BenchmarkRunner{
		runner:  runner,
		reports: make([]*Report, 0),
	}
}

// RunAll runs all benchmark suites and returns the report.
func (br *BenchmarkRunner) RunAll(ctx context.Context) (*Report, error) {
	report, err := br.runner.RunAll(ctx, "eling-benchmark")
	if err != nil {
		return nil, err
	}
	br.reports = append(br.reports, report)
	return report, nil
}

// RunSuite runs a specific suite.
func (br *BenchmarkRunner) RunSuite(ctx context.Context, suite SuiteID) (*Report, error) {
	report, err := br.runner.RunSuite(ctx, fmt.Sprintf("eling-%s", suite), suite)
	if err != nil {
		return nil, err
	}
	br.reports = append(br.reports, report)
	return report, nil
}

// LastReport returns the most recent report.
func (br *BenchmarkRunner) LastReport() *Report {
	if len(br.reports) == 0 {
		return nil
	}
	return br.reports[len(br.reports)-1]
}

// Reports returns all reports.
func (br *BenchmarkRunner) Reports() []*Report {
	return br.reports
}

// ExportJSON saves a report as JSON to the given path.
func (br *BenchmarkRunner) ExportJSON(report *Report, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return NewFormatter(FormatJSON).Format(f, report)
}

// ExportMarkdown saves a report as Markdown to the given path.
func (br *BenchmarkRunner) ExportMarkdown(report *Report, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return NewFormatter(FormatMarkdown).Format(f, report)
}

// ──────────────────────────────────────────────────────────────────────
// Helper: format test output
// ──────────────────────────────────────────────────────────────────────

func formatTestOutput(report *Report) string {
	var b strings.Builder
	b.WriteString(strings.Repeat("━", 60))
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("  BENCHMARK REPORT: %s\n", report.Name))
	b.WriteString(fmt.Sprintf("  Date: %s\n", report.CreatedAt.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("  Duration: %s\n", report.TotalDuration))
	b.WriteString(strings.Repeat("━", 60))
	b.WriteString("\n\n")

	for _, suite := range SortedSuiteNames(report.Suites) {
		sm := report.Suites[suite]
		status := "✅"
		if sm.Failed > 0 {
			status = "⚠️ "
		}
		b.WriteString(fmt.Sprintf("  %s %s:\n", status, suite))
		b.WriteString(fmt.Sprintf("     %d tests: %d passed, %d failed, %d skipped\n",
			sm.TotalTests, sm.Passed, sm.Failed, sm.Skipped))
		b.WriteString(fmt.Sprintf("     Pass Rate: %.1f%% | F1: %.3f | Precision: %.3f | Recall: %.3f\n",
			sm.PassRate*100, sm.F1Score, sm.Precision, sm.Recall))
		b.WriteString(fmt.Sprintf("     Avg Duration: %.1fms | Total Tokens: %d\n",
			sm.AvgDuration, sm.TotalTokens))
		b.WriteString("\n")
	}

	b.WriteString(strings.Repeat("━", 60))
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("  Overall: %d/%d passed (%.1f%%) | F1: %.3f | Tokens: %d | Time: %s\n",
		report.TotalPassed, report.TotalTests-report.TotalSkipped,
		report.OverallPassRate*100, report.OverallF1,
		report.TotalTokens, report.TotalDuration))
	b.WriteString(strings.Repeat("━", 60))
	b.WriteString("\n")

	return b.String()
}
