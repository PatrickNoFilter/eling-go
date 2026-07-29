package tools

import (
	"fmt"
	"os/exec"
	"strings"
)

func init() {
	DefaultRegistry.Register(Tool{
		Name: "ocr_review",
		Description: "Run OpenCodeReview (OCR) on workspace changes, one commit, or a ref range. " +
			"Returns structured line-level findings as JSON. Use preview=true to inspect scope without LLM usage. " +
			"Requires 'ocr' CLI installed (npm install -g @alibaba-group/open-code-review).",
		Version:  "1.0.0",
		Category: "system",
		Execute:  ocrReviewExecute,
	})

	DefaultRegistry.Register(Tool{
		Name: "ocr_scan",
		Description: "Run OpenCodeReview (OCR) full-file scan on whole files instead of a diff. " +
			"Reviews entire files for auditing unfamiliar codebases or directories that have no meaningful diff. " +
			"Requires 'ocr' CLI installed (npm install -g @alibaba-group/open-code-review).",
		Version:  "1.0.0",
		Category: "system",
		Execute:  ocrScanExecute,
	})

	DefaultRegistry.Register(Tool{
		Name: "ocr_health",
		Description: "Check the installed OpenCodeReview (OCR) CLI version and verify its configured LLM connection. " +
			"Requires 'ocr' CLI installed (npm install -g @alibaba-group/open-code-review).",
		Version:  "1.0.0",
		Category: "system",
		Execute:  ocrHealthExecute,
	})
}

func runOcr(args []string) (interface{}, error) {
	cmd := exec.Command("ocr", args...)
	stdout, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(stdout))
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

	return runOcr(ocrArgs)
}

func ocrScanExecute(args map[string]interface{}) (interface{}, error) {
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

	return runOcr(ocrArgs)
}

func ocrHealthExecute(args map[string]interface{}) (interface{}, error) {
	versionArgs := []string{"version"}
	versionCmd := exec.Command("ocr", versionArgs...)
	versionOut, versionErr := versionCmd.CombinedOutput()

	llmArgs := []string{"llm", "test"}
	llmCmd := exec.Command("ocr", llmArgs...)
	llmOut, llmErr := llmCmd.CombinedOutput()

	var parts []string
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
