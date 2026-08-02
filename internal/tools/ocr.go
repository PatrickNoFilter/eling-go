package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Tool timeout budgets for OCR. The OCR CLI (Alibaba OpenCodeReview) can take
// minutes per file when the configured LLM is slow — historically it hung the
// agent until manually killed. These budgets make it fail fast instead:
//   - ocr_review / ocr_scan: 5 minutes by default (override via
//     tool_timeout_sec arg). The per-file --timeout arg is a separate knob.
//   - ocr_health: 60 seconds (just version + LLM ping).
const (
	ocrReviewTimeout = 5 * time.Minute
	ocrHealthTimeout = 60 * time.Second
)

func init() {
	DefaultRegistry.Register(Tool{
		Name: "ocr_review",
		Description: "Run OpenCodeReview (OCR) on workspace changes, one commit, or a ref range. " +
			"Returns structured line-level findings as JSON. Use preview=true to inspect scope without LLM usage. " +
			"Requires 'ocr' CLI installed (npm install -g @alibaba-group/open-code-review). " +
			"Hard timeout: 5 min default (override with tool_timeout_sec).",
		Version:    "1.1.0", // ctx-aware + hard timeout budget
		Category:   "system",
		Execute:    ocrReviewExecute,
		ExecuteCtx: ocrReviewExecuteCtx,
		Timeout:    ocrReviewTimeout,
	})

	DefaultRegistry.Register(Tool{
		Name: "ocr_scan",
		Description: "Run OpenCodeReview (OCR) full-file scan on whole files instead of a diff. " +
			"Reviews entire files for auditing unfamiliar codebases or directories that have no meaningful diff. " +
			"Requires 'ocr' CLI installed (npm install -g @alibaba-group/open-code-review). " +
			"Hard timeout: 5 min default (override with tool_timeout_sec).",
		Version:    "1.1.0", // ctx-aware + hard timeout budget
		Category:   "system",
		Execute:    ocrScanExecute,
		ExecuteCtx: ocrScanExecuteCtx,
		Timeout:    ocrReviewTimeout,
	})

	DefaultRegistry.Register(Tool{
		Name: "ocr_health",
		Description: "Check the installed OpenCodeReview (OCR) CLI version and verify its configured LLM connection. " +
			"Requires 'ocr' CLI installed (npm install -g @alibaba-group/open-code-review).",
		Version:    "1.1.0", // ctx-aware + hard timeout budget
		Category:   "system",
		Execute:    ocrHealthExecute,
		ExecuteCtx: ocrHealthExecuteCtx,
		Timeout:    ocrHealthTimeout,
	})
}

// toolBudget extracts the optional tool_timeout_sec arg, falling back to the
// given default. Returns a ctx-with-deadline so the whole OCR run (which can
// spawn many subprocesses) cannot exceed the budget.
func toolBudgetCtx(ctx context.Context, args map[string]interface{}, def time.Duration) (context.Context, context.CancelFunc) {
	timeout := def
	if n, ok := args["tool_timeout_sec"].(float64); ok && n > 0 {
		// float64 → Duration directly (time.Duration(n) would truncate
		// fractional seconds to 0).
		timeout = time.Duration(n * float64(time.Second))
	} else if s, ok := args["tool_timeout_sec"].(string); ok && s != "" {
		var secs int
		if _, err := fmt.Sscanf(s, "%d", &secs); err == nil && secs > 0 {
			timeout = time.Duration(secs) * time.Second
		}
	}
	// An earlier caller deadline (turn max_duration) wins over the tool budget.
	if d, ok := ctx.Deadline(); ok {
		if remaining := time.Until(d); remaining < timeout {
			timeout = remaining
		}
	}
	return context.WithTimeout(ctx, timeout)
}

// runOcr runs the ocr CLI with context support. The command is killed the
// moment the context expires, so a slow LLM cannot hang the turn.
func runOcr(ctx context.Context, args []string) (interface{}, error) {
	cmd := exec.CommandContext(ctx, "ocr", args...)
	stdout, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(stdout))
	if ctx.Err() != nil {
		return nil, fmt.Errorf("OCR command aborted: %v (hard timeout reached)", ctx.Err())
	}
	if err != nil {
		return OK(map[string]interface{}{
			"stdout":   output,
			"error":    fmt.Sprintf("OCR command failed: %v", err),
			"exit_err": err.Error(),
		}), fmt.Errorf("OCR command failed: %w\n%s", err, output)
	}
	return OK(map[string]interface{}{
		"stdout": output,
	}), nil
}

func ocrReviewExecute(args map[string]interface{}) (interface{}, error) {
	return ocrReviewExecuteCtx(context.Background(), args)
}

func ocrReviewExecuteCtx(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	ctx, cancel := toolBudgetCtx(ctx, args, ocrReviewTimeout)
	defer cancel()

	ocrArgs := []string{"review", "--audience", "agent", "--format", "json"}

	if preview, ok := args["preview"].(bool); ok && preview {
		ocrArgs = append(ocrArgs, "--preview")
	}
	if v, ok := args["repo"].(string); ok && v != "" {
		ocrArgs = append(ocrArgs, "--repo", v)
	}
	if v, ok := args["commit"].(string); ok && v != "" {
		ocrArgs = append(ocrArgs, "--commit", v)
	}
	if v, ok := args["from"].(string); ok && v != "" {
		ocrArgs = append(ocrArgs, "--from", v)
	}
	if v, ok := args["to"].(string); ok && v != "" {
		ocrArgs = append(ocrArgs, "--to", v)
	}
	if v, ok := args["resume"].(string); ok && v != "" {
		ocrArgs = append(ocrArgs, "--resume", v)
	}
	if v, ok := args["background"].(string); ok && v != "" {
		ocrArgs = append(ocrArgs, "--background", v)
	}
	if v, ok := args["exclude"].(string); ok && v != "" {
		ocrArgs = append(ocrArgs, "--exclude", v)
	}
	if v, ok := args["model"].(string); ok && v != "" {
		ocrArgs = append(ocrArgs, "--model", v)
	}
	if v, ok := args["concurrency"].(float64); ok && v > 0 {
		ocrArgs = append(ocrArgs, "--concurrency", fmt.Sprintf("%.0f", v))
	}
	if v, ok := args["timeout_minutes"].(float64); ok && v > 0 {
		ocrArgs = append(ocrArgs, "--timeout", fmt.Sprintf("%.0f", v))
	}
	if v, ok := args["max_tools"].(float64); ok && v > 0 {
		ocrArgs = append(ocrArgs, "--max-tools", fmt.Sprintf("%.0f", v))
	}
	if v, ok := args["max_git_procs"].(float64); ok && v > 0 {
		ocrArgs = append(ocrArgs, "--max-git-procs", fmt.Sprintf("%.0f", v))
	}

	return runOcr(ctx, ocrArgs)
}

func ocrScanExecute(args map[string]interface{}) (interface{}, error) {
	return ocrScanExecuteCtx(context.Background(), args)
}

func ocrScanExecuteCtx(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	ctx, cancel := toolBudgetCtx(ctx, args, ocrReviewTimeout)
	defer cancel()

	ocrArgs := []string{"scan", "--format", "json"}

	if v, ok := args["path"].(string); ok && v != "" {
		ocrArgs = append(ocrArgs, "--path", v)
	}
	if v, ok := args["model"].(string); ok && v != "" {
		ocrArgs = append(ocrArgs, "--model", v)
	}
	if v, ok := args["exclude"].(string); ok && v != "" {
		ocrArgs = append(ocrArgs, "--exclude", v)
	}
	if v, ok := args["concurrency"].(float64); ok && v > 0 {
		ocrArgs = append(ocrArgs, "--concurrency", fmt.Sprintf("%.0f", v))
	}
	if v, ok := args["timeout_minutes"].(float64); ok && v > 0 {
		ocrArgs = append(ocrArgs, "--timeout", fmt.Sprintf("%.0f", v))
	}

	return runOcr(ctx, ocrArgs)
}

func ocrHealthExecute(args map[string]interface{}) (interface{}, error) {
	return ocrHealthExecuteCtx(context.Background(), args)
}

func ocrHealthExecuteCtx(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	ctx, cancel := toolBudgetCtx(ctx, args, ocrHealthTimeout)
	defer cancel()

	versionCmd := exec.CommandContext(ctx, "ocr", "version")
	versionOut, versionErr := versionCmd.CombinedOutput()

	llmCmd := exec.CommandContext(ctx, "ocr", "llm", "test")
	llmOut, llmErr := llmCmd.CombinedOutput()

	var parts []string
	if ctx.Err() != nil {
		parts = append(parts, fmt.Sprintf("OCR health check timed out after %v", ocrHealthTimeout))
	}
	if versionErr == nil {
		parts = append(parts, strings.TrimSpace(string(versionOut)))
	} else {
		parts = append(parts, fmt.Sprintf("Version check failed: %v", versionErr))
	}
	if llmErr == nil {
		parts = append(parts, strings.TrimSpace(string(llmOut)))
	} else {
		parts = append(parts, fmt.Sprintf("LLM connection check failed: %v", llmErr))
	}

	result := strings.Join(parts, "\n")
	return OK(map[string]interface{}{
		"stdout": result,
	}), nil
}
