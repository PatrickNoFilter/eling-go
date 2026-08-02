package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

func init() {
	// Register web_search tool
	webSearchTool := Tool{
		Name:        "web_search",
		Description: "Search the web using DuckDuckGo or Bing. Returns a list of results with titles, URLs, and snippets.",
		Version:     "2.4.0", // dns cache + --resolve skip-dns + adaptive max-time + ctx-cancel + Ctrl+C kill + registry budget
		Category:    "system",
		Execute:     webSearchExecute,
		ExecuteCtx:  webSearchExecuteCtx,
		// Registry-level budget: adaptive per-host max-time is 4-8s; the 30s
		// cap bounds the full fallback chain (html -> lite) so a slow search
		// engine cannot stall the turn.
		Timeout: 30 * time.Second,
	}
	DefaultRegistry.Register(webSearchTool)

	// Register web_fetch tool
	webFetchTool := Tool{
		Name:        "web_fetch",
		Description: "Fetch the content of a URL and return it as text. Supports http/https URLs.",
		Version:     "2.4.0", // dns cache + --resolve skip-dns + adaptive max-time + ctx-cancel + Ctrl+C kill + registry budget
		Category:    "system",
		Execute:     webFetchExecute,
		ExecuteCtx:  webFetchExecuteCtx,
		Timeout:     30 * time.Second,
	}
	DefaultRegistry.Register(webFetchTool)
}

// webSearchExecute searches the web.
func webSearchExecute(args map[string]interface{}) (interface{}, error) {
	return webSearchExecuteCtx(context.Background(), args)
}

// webSearchExecuteCtx is the context-aware web search implementation.
func webSearchExecuteCtx(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return Err("query is required"), nil
	}

	numResults := 5
	if n, ok := args["num_results"].(float64); ok {
		numResults = int(n)
	}

	// Use DuckDuckGo via curl (bypasses Go's broken DNS on Android)
	results, err := duckDuckGoSearchCurlCtx(ctx, query, numResults)
	if err != nil {
		return nil, fmt.Errorf("web search failed: %w", err)
	}

	return OK(map[string]interface{}{
		"query":              query,
		"results":            results,
		"timeout_prediction": predictor.predictionInfo("duckduckgo.com"),
	}), nil
}

// curlGet performs an HTTP GET via bash curl (reliable DNS resolution).
// It uses the timeout prediction mechanism: fast DNS+TCP preflight, adaptive
// per-host --max-time, and outcome recording so slow/dead hosts fail fast.
func curlGet(targetURL string, headers ...string) (string, error) {
	return curlGetCtx(context.Background(), targetURL, headers...)
}

// curlGetCtx is the context-aware variant of curlGet. The caller's context can
// cancel the underlying curl process immediately (exec.CommandContext), so a
// parent deadline or user interrupt aborts the fetch instead of blocking.
func curlGetCtx(ctx context.Context, targetURL string, headers ...string) (string, error) {
	host := hostOf(targetURL)
	u, err := url.Parse(targetURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL %q: %w", targetURL, err)
	}
	hostname := u.Hostname()

	// 1) Timeout prediction: fast DNS + TCP preflight so dead hosts fail in
	//    ~1s instead of hanging until curl's --max-time. The returned IPs are
	//    handed to curl via --resolve so curl skips its own DNS lookup.
	ips, err := predictor.preflightReachable(targetURL, preflightTimeout)
	if err != nil {
		return "", err
	}

	// 2) Adaptive timeout budget based on this host's history.
	maxTime := predictor.adaptiveMaxTime(host)
	start := time.Now()

	args := []string{"-sL", "--connect-timeout", "2",
		"--max-time", fmt.Sprintf("%d", int(maxTime.Seconds())),
		"--max-filesize", "1M", "--speed-limit", "100", "--speed-time", "3"}

	// 3) Skip curl's DNS lookup using the preflight-resolved IPs. This halves
	//    the network round trips per fetch (one DNS instead of two).
	if len(ips) > 0 {
		port := u.Port()
		if port == "" {
			if u.Scheme == "https" {
				port = "443"
			} else {
				port = "80"
			}
		}
		parts := make([]string, 0, len(ips))
		for _, ip := range ips {
			if strings.Contains(ip, ":") { // IPv6 needs brackets in --resolve
				ip = "[" + ip + "]"
			}
			parts = append(parts, ip)
		}
		args = append(args, "--resolve", hostname+":"+port+":"+strings.Join(parts, ","))
	}

	// Add headers
	for _, h := range headers {
		args = append(args, "-H", h)
	}

	// Use a browser-like User-Agent unless overridden
	hasUA := false
	for _, h := range headers {
		if strings.HasPrefix(strings.ToLower(h), "user-agent:") {
			hasUA = true
			break
		}
	}
	if !hasUA {
		args = append(args, "-A", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	}

	// Append "--" before URL to prevent curl from interpreting the URL
	// as a flag if it starts with a dash (e.g., a malicious or malformed URL).
	args = append(args, "--", targetURL)

	// CommandContext kills curl the moment ctx is cancelled.
	cmd := exec.CommandContext(ctx, "curl", args...)
	// Give curl its own process group and track it so Ctrl+C /
	// KillRunningTools() also aborts in-flight web fetches, not just bash.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	trackCmd(cmd)
	defer untrackCmd(cmd)
	// Limit output to prevent OOM from huge pages
	stdout := newLimitedBuffer(maxBashOutputBytes)
	stderr := newLimitedBuffer(maxBashOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err = cmd.Run()
	elapsed := time.Since(start)
	predictor.recordResult(host, elapsed, err)

	if err != nil {
		// Distinguish cancellation from genuine fetch errors.
		if ctx.Err() != nil {
			return "", fmt.Errorf("fetch aborted (context cancelled after %v): %w", elapsed.Round(time.Millisecond), ctx.Err())
		}
		errStr := strings.TrimSpace(stderr.String())
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("curl failed (exit %d) after %v: %s", exitErr.ExitCode(), elapsed.Round(time.Millisecond), errStr)
		}
		return "", fmt.Errorf("curl failed after %v: %w", elapsed.Round(time.Millisecond), err)
	}

	output := stdout.String()
	if stdout.Len() >= maxBashOutputBytes {
		output += "\n... [response truncated at 512 KiB]"
	}
	return output, nil
}

// duckDuckGoSearchCurl performs a search via DuckDuckGo using curl
func duckDuckGoSearchCurl(query string, numResults int) ([]map[string]string, error) {
	return duckDuckGoSearchCurlCtx(context.Background(), query, numResults)
}

// duckDuckGoSearchCurlCtx is the context-aware DuckDuckGo search.
func duckDuckGoSearchCurlCtx(ctx context.Context, query string, numResults int) ([]map[string]string, error) {
	searchURL := fmt.Sprintf("https://html.duckduckgo.com/html/?q=%s", url.QueryEscape(query))

	body, err := curlGetCtx(ctx, searchURL,
		"Accept: text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language: en-US,en;q=0.5",
		"DNT: 1",
	)
	if err != nil {
		// Fallback: try Lite endpoint
		return duckDuckGoLiteSearchCurlCtx(ctx, query, numResults)
	}

	// Parse HTML results
	results, _ := parseDuckDuckGoHTML(body, numResults)
	if len(results) > 0 {
		return results, nil
	}

	// Fallback to Lite
	return duckDuckGoLiteSearchCurlCtx(ctx, query, numResults)
}

// duckDuckGoLiteSearchCurl is a fallback using the Lite endpoint
func duckDuckGoLiteSearchCurl(query string, numResults int) ([]map[string]string, error) {
	return duckDuckGoLiteSearchCurlCtx(context.Background(), query, numResults)
}

// duckDuckGoLiteSearchCurlCtx is the context-aware Lite fallback.
func duckDuckGoLiteSearchCurlCtx(ctx context.Context, query string, numResults int) ([]map[string]string, error) {
	searchURL := fmt.Sprintf("https://lite.duckduckgo.com/lite/?q=%s", url.QueryEscape(query))

	body, err := curlGetCtx(ctx, searchURL,
		"Accept: text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language: en-US,en;q=0.5",
		"DNT: 1",
	)
	if err != nil {
		return nil, fmt.Errorf("all search endpoints failed: %w", err)
	}

	return parseDuckDuckGoLiteHTML(body, numResults)
}

// parseDuckDuckGoHTML extracts results from DuckDuckGo's HTML search page
func parseDuckDuckGoHTML(body string, numResults int) ([]map[string]string, error) {
	var results []map[string]string
	lines := strings.Split(body, "\n")
	inResult := false
	var current map[string]string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.Contains(trimmed, `class="result__a"`) {
			current = make(map[string]string)
			inResult = true
			// Extract URL
			if start := strings.Index(trimmed, `href="`); start >= 0 {
				start += 6
				if end := strings.Index(trimmed[start:], `"`); end >= 0 {
					urlStr := trimmed[start : start+end]
					// Unescape HTML entities
					urlStr = strings.ReplaceAll(urlStr, "&amp;", "&")
					urlStr = strings.ReplaceAll(urlStr, "&lt;", "<")
					urlStr = strings.ReplaceAll(urlStr, "&gt;", ">")
					current["url"] = urlStr
				}
			}
			// Extract title text
			if start := strings.Index(trimmed, ">"); start >= 0 {
				title := trimmed[start+1:]
				title = stripHTMLTags(title)
				title = strings.TrimSpace(title)
				// Clean up DuckDuckGo's title format
				title = strings.ReplaceAll(title, "&amp;", "&")
				title = strings.ReplaceAll(title, "&#x27;", "'")
				title = strings.ReplaceAll(title, "&quot;", `"`)
				current["title"] = title
			}
		}

		if inResult && strings.Contains(trimmed, `class="result__snippet"`) {
			snippet := stripHTMLTags(trimmed)
			snippet = strings.TrimSpace(snippet)
			snippet = strings.ReplaceAll(snippet, "&amp;", "&")
			snippet = strings.ReplaceAll(snippet, "&#x27;", "'")
			current["snippet"] = snippet
		}

		if inResult && strings.Contains(trimmed, "</a>") && len(current) > 0 {
			if current["title"] != "" {
				results = append(results, current)
			}
			current = nil
			inResult = false
			if len(results) >= numResults {
				break
			}
		}
	}

	return results, nil
}

// parseDuckDuckGoLiteHTML extracts results from DuckDuckGo's Lite HTML
func parseDuckDuckGoLiteHTML(body string, numResults int) ([]map[string]string, error) {
	var results []map[string]string
	lines := strings.Split(body, "\n")
	var current map[string]string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Lite uses <a rel="nofollow" class="result-link">...</a>
		if strings.Contains(trimmed, `class="result-link"`) {
			current = make(map[string]string)
			// Extract URL
			if start := strings.Index(trimmed, `href="`); start >= 0 {
				start += 6
				if end := strings.Index(trimmed[start:], `"`); end >= 0 {
					urlStr := trimmed[start : start+end]
					// DuckDuckGo wraps URLs in redirect
					if strings.Contains(urlStr, "duckduckgo.com/l/") {
						if uStart := strings.Index(urlStr, "uddg="); uStart >= 0 {
							uStart += 5
							if uEnd := strings.Index(urlStr[uStart:], "&"); uEnd >= 0 {
								if decoded, err := url.QueryUnescape(urlStr[uStart : uStart+uEnd]); err == nil {
									urlStr = decoded
								}
							}
						}
					}
					urlStr = strings.ReplaceAll(urlStr, "&amp;", "&")
					current["url"] = urlStr
				}
			}
			// Extract title text between <a ...> and </a>
			if aEnd := strings.Index(trimmed, "</a>"); aEnd >= 0 {
				titlePart := trimmed[:aEnd]
				if gt := strings.LastIndex(titlePart, ">"); gt >= 0 {
					title := titlePart[gt+1:]
					title = strings.TrimSpace(title)
					title = strings.ReplaceAll(title, "&amp;", "&")
					title = strings.ReplaceAll(title, "&#x27;", "'")
					current["title"] = title
				}
			}
			if current["title"] == "" {
				title := stripHTMLTags(trimmed)
				title = strings.TrimSpace(title)
				if len(title) > 0 && len(title) < 200 {
					current["title"] = title
				}
			}
			results = append(results, current)
			current = nil
			if len(results) >= numResults {
				break
			}
		}

		// Also try snippet extraction
		if strings.Contains(trimmed, `class="result-snippet"`) {
			snippet := stripHTMLTags(trimmed)
			snippet = strings.TrimSpace(snippet)
			snippet = strings.ReplaceAll(snippet, "&amp;", "&")
			if len(results) > 0 {
				results[len(results)-1]["snippet"] = snippet
			}
		}
	}

	return results, nil
}

// webFetchExecute fetches a URL using curl for reliable DNS resolution.
func webFetchExecute(args map[string]interface{}) (interface{}, error) {
	return webFetchExecuteCtx(context.Background(), args)
}

// webFetchExecuteCtx is the context-aware web fetch implementation.
// It embeds the timeout prediction state so callers can see the decision.
func webFetchExecuteCtx(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	targetURL, _ := args["url"].(string)
	if targetURL == "" {
		return Err("url is required"), nil
	}

	format, _ := args["format"].(string)
	if format == "" {
		format = "text"
	}

	body, err := curlGetCtx(ctx, targetURL)
	if err != nil {
		return nil, fmt.Errorf("fetch failed: %w", err)
	}

	if format == "json" {
		var data interface{}
		if err := json.Unmarshal([]byte(body), &data); err == nil {
			return OK(map[string]interface{}{
				"url":                targetURL,
				"content":            data,
				"timeout_prediction": predictor.predictionInfo(hostOf(targetURL)),
			}), nil
		}
	}

	return OK(map[string]interface{}{
		"url":                targetURL,
		"content":            body,
		"timeout_prediction": predictor.predictionInfo(hostOf(targetURL)),
	}), nil
}

// stripHTMLTags removes HTML tags from a string.
func stripHTMLTags(s string) string {
	var result strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			result.WriteRune(' ')
			continue
		}
		if !inTag {
			result.WriteRune(r)
		}
	}
	return result.String()
}
