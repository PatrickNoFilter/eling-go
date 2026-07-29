package benchmark

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// ──────────────────────────────────────────────────────────────────────
// Report Formatters
// ──────────────────────────────────────────────────────────────────────

// ReportFormat defines how a report is serialized.
type ReportFormat string

const (
	FormatText     ReportFormat = "text"
	FormatJSON     ReportFormat = "json"
	FormatMarkdown ReportFormat = "markdown"
	FormatCSV      ReportFormat = "csv"
	FormatCompact  ReportFormat = "compact"
)

// Formatter writes a report in a specific format.
type Formatter struct {
	format ReportFormat
}

// NewFormatter creates a formatter for the given format.
func NewFormatter(format ReportFormat) *Formatter {
	return &Formatter{format: format}
}

// Format writes the report to the given writer.
func (f *Formatter) Format(w io.Writer, report *Report) error {
	switch f.format {
	case FormatJSON:
		return f.formatJSON(w, report)
	case FormatMarkdown:
		return f.formatMarkdown(w, report)
	case FormatCSV:
		return f.formatCSV(w, report)
	case FormatCompact:
		return f.formatCompact(w, report)
	default:
		return f.formatText(w, report)
	}
}

// formatJSON writes the report as indented JSON.
func (f *Formatter) formatJSON(w io.Writer, report *Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

// formatText writes the report as a human-readable text table.
func (f *Formatter) formatText(w io.Writer, report *Report) error {
	title := fmt.Sprintf("Benchmark Report: %s", report.Name)
	sep := strings.Repeat("═", len(title)+4)

	fmt.Fprintf(w, "╔%s╗\n", sep)
	fmt.Fprintf(w, "║  %s  ║\n", title)
	fmt.Fprintf(w, "╚%s╝\n", sep)
	fmt.Fprintf(w, "  Date:  %s\n", report.CreatedAt.Format(time.RFC3339))
	fmt.Fprintf(w, "  Duration: %s\n", report.TotalDuration)
	fmt.Fprintf(w, "\n")

	// Summary bar
	passRate := report.OverallPassRate * 100
	fmt.Fprintf(w, "  Overall: %d/%d passed (%.1f%%) | F1: %.3f | Tokens: %d\n",
		report.TotalPassed, report.TotalTests-report.TotalSkipped, passRate,
		report.OverallF1, report.TotalTokens)
	fmt.Fprintf(w, "\n")

	// Per-suite table
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	fmt.Fprintf(tw, "  Suite\tTests\tPassed\tFailed\tSkipped\tPass Rate\tPrecision\tRecall\tF1\tAccuracy\tAvg (ms)\tTokens\n")
	fmt.Fprintf(tw, "  -----\t-----\t------\t------\t-------\t---------\t---------\t-----\t--\t--------\t--------\t------\n")

	for _, suite := range SortedSuiteNames(report.Suites) {
		sm := report.Suites[suite]
		fmt.Fprintf(tw, "  %s\t%d\t%d\t%d\t%d\t%.1f%%\t%.3f\t%.3f\t%.3f\t%.3f\t%.1f\t%d\n",
			suite, sm.TotalTests, sm.Passed, sm.Failed, sm.Skipped,
			sm.PassRate*100, sm.Precision, sm.Recall, sm.F1Score, sm.Accuracy,
			sm.AvgDuration, sm.TotalTokens)
	}
	tw.Flush()

	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "  Legend:\n")
	fmt.Fprintf(w, "    Pass Rate = Passed / (Total - Skipped)\n")
	fmt.Fprintf(w, "    Precision = TP / (TP + FP)\n")
	fmt.Fprintf(w, "    Recall    = TP / (TP + FN)\n")
	fmt.Fprintf(w, "    F1        = 2 * (Precision * Recall) / (Precision + Recall)\n")
	fmt.Fprintf(w, "    Accuracy  = (TP + TN) / (TP + TN + FP + FN)\n")

	return nil
}

// formatMarkdown writes the report as GitHub-flavored Markdown.
func (f *Formatter) formatMarkdown(w io.Writer, report *Report) error {
	fmt.Fprintf(w, "# Benchmark Report: %s\n\n", report.Name)
	fmt.Fprintf(w, "- **Date:** %s\n", report.CreatedAt.Format(time.RFC3339))
	fmt.Fprintf(w, "- **Duration:** %s\n", report.TotalDuration)
	fmt.Fprintf(w, "- **Overall Pass Rate:** %.1f%% (%d/%d)\n",
		report.OverallPassRate*100, report.TotalPassed, report.TotalTests-report.TotalSkipped)
	fmt.Fprintf(w, "- **Overall F1 Score:** %.3f\n", report.OverallF1)
	fmt.Fprintf(w, "- **Total Tokens:** %d\n", report.TotalTokens)
	fmt.Fprintf(w, "\n")

	// Suite summary table
	fmt.Fprintf(w, "## Suite Results\n\n")
	fmt.Fprintf(w, "| Suite | Tests | ✅ Passed | ❌ Failed | ⏭️ Skipped | Pass Rate | Precision | Recall | F1 | Accuracy | Avg Time | Tokens |\n")
	fmt.Fprintf(w, "|-------|-------|----------|----------|----------|----------|-----------|--------|-----|----------|----------|--------|\n")

	for _, suite := range SortedSuiteNames(report.Suites) {
		sm := report.Suites[suite]
		fmt.Fprintf(w, "| %s | %d | %d | %d | %d | %.1f%% | %.3f | %.3f | %.3f | %.3f | %.1fms | %d |\n",
			suite, sm.TotalTests, sm.Passed, sm.Failed, sm.Skipped,
			sm.PassRate*100, sm.Precision, sm.Recall, sm.F1Score, sm.Accuracy,
			sm.AvgDuration, sm.TotalTokens)
	}

	fmt.Fprintf(w, "\n## Legend\n\n")
	fmt.Fprintf(w, "- **Precision:** TP / (TP + FP) — how many selected items are relevant\n")
	fmt.Fprintf(w, "- **Recall:** TP / (TP + FN) — how many relevant items are selected\n")
	fmt.Fprintf(w, "- **F1 Score:** Harmonic mean of precision and recall\n")
	fmt.Fprintf(w, "- **Accuracy:** (TP + TN) / (total) — overall correctness\n\n")

	// Add historical comparison section if multiple reports exist
	fmt.Fprintf(w, "## Interpretation\n\n")
	fmt.Fprintf(w, "- **Pass Rate ≥ 95%%:** Excellent — the system is reliable for this dimension.\n")
	fmt.Fprintf(w, "- **Pass Rate ≥ 80%%:** Good — acceptable for most use cases.\n")
	fmt.Fprintf(w, "- **Pass Rate ≥ 60%%:** Needs improvement — review failing cases.\n")
	fmt.Fprintf(w, "- **Pass Rate < 60%%:** Critical — requires immediate attention.\n")

	return nil
}

// formatCSV writes the report in CSV format (one row per suite).
func (f *Formatter) formatCSV(w io.Writer, report *Report) error {
	fmt.Fprintf(w, "report_name,date,duration,total_tests,passed,failed,skipped,pass_rate,overall_f1,total_tokens\n")
	fmt.Fprintf(w, "%s,%s,%s,%d,%d,%d,%d,%.4f,%.4f,%d\n",
		report.Name,
		report.CreatedAt.Format(time.RFC3339),
		report.TotalDuration,
		report.TotalTests,
		report.TotalPassed,
		report.TotalFailed,
		report.TotalSkipped,
		report.OverallPassRate,
		report.OverallF1,
		report.TotalTokens,
	)
	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "suite,total,passed,failed,skipped,pass_rate,precision,recall,f1,accuracy,avg_duration_ms,total_tokens,total_llm_calls,tp,fp,tn,fn\n")

	for _, suite := range SortedSuiteNames(report.Suites) {
		sm := report.Suites[suite]
		fmt.Fprintf(w, "%s,%d,%d,%d,%d,%.4f,%.4f,%.4f,%.4f,%.4f,%.1f,%d,%d,%d,%d,%d,%d\n",
			suite,
			sm.TotalTests, sm.Passed, sm.Failed, sm.Skipped,
			sm.PassRate, sm.Precision, sm.Recall, sm.F1Score, sm.Accuracy,
			sm.AvgDuration, sm.TotalTokens, sm.TotalLLMCalls,
			sm.TruePositives, sm.FalsePositives, sm.TrueNegatives, sm.FalseNegatives,
		)
	}
	return nil
}

// formatCompact writes a one-line summary of the report.
func (f *Formatter) formatCompact(w io.Writer, report *Report) error {
	fmt.Fprintf(w, "[%s] %s: %d/%d passed (%.1f%%), F1=%.3f, tokens=%d, time=%s",
		report.CreatedAt.Format(time.RFC3339),
		report.Name,
		report.TotalPassed, report.TotalTests-report.TotalSkipped,
		report.OverallPassRate*100,
		report.OverallF1,
		report.TotalTokens,
		report.TotalDuration,
	)
	return nil
}

// ──────────────────────────────────────────────────────────────────────
// Historical Tracking
// ──────────────────────────────────────────────────────────────────────

// History tracks benchmark reports over time for trend analysis.
type History struct {
	reports []*Report
}

// NewHistory creates a new history tracker.
func NewHistory() *History {
	return &History{
		reports: make([]*Report, 0),
	}
}

// Add adds a report to the history.
func (h *History) Add(report *Report) {
	h.reports = append(h.reports, report)
}

// All returns all tracked reports.
func (h *History) All() []*Report {
	result := make([]*Report, len(h.reports))
	copy(result, h.reports)
	return result
}

// Trend returns the trend for a given metric across all reports.
type TrendPoint struct {
	Index int       `json:"index"`
	Date  time.Time `json:"date"`
	Value float64   `json:"value"`
	Label string    `json:"label,omitempty"`
}

// PassRateTrend returns the overall pass rate over time.
func (h *History) PassRateTrend() []TrendPoint {
	points := make([]TrendPoint, 0, len(h.reports))
	for i, r := range h.reports {
		points = append(points, TrendPoint{
			Index: i,
			Date:  r.CreatedAt,
			Value: r.OverallPassRate,
		})
	}
	return points
}

// F1Trend returns the overall F1 score over time.
func (h *History) F1Trend() []TrendPoint {
	points := make([]TrendPoint, 0, len(h.reports))
	for i, r := range h.reports {
		points = append(points, TrendPoint{
			Index: i,
			Date:  r.CreatedAt,
			Value: r.OverallF1,
		})
	}
	return points
}

// TokenTrend returns the total tokens used over time.
func (h *History) TokenTrend() []TrendPoint {
	points := make([]TrendPoint, 0, len(h.reports))
	for i, r := range h.reports {
		points = append(points, TrendPoint{
			Index: i,
			Date:  r.CreatedAt,
			Value: float64(r.TotalTokens),
			Label: fmt.Sprintf("%d tokens", r.TotalTokens),
		})
	}
	return points
}

// SuiteTrend returns the pass rate for a specific suite over time.
func (h *History) SuiteTrend(suite SuiteID) []TrendPoint {
	points := make([]TrendPoint, 0, len(h.reports))
	for i, r := range h.reports {
		if sm, ok := r.Suites[suite]; ok {
			points = append(points, TrendPoint{
				Index: i,
				Date:  r.CreatedAt,
				Value: sm.PassRate,
			})
		}
	}
	return points
}
