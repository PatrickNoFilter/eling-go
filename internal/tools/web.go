package tools

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
)

func init() {
	// Register web_search tool
	webSearchTool := Tool{
		Name:        "web_search",
		Description: "Search the web using DuckDuckGo or Bing. Returns a list of results with titles, URLs, and snippets.",
		Version:     "2.0.0", // DNS-fixed: uses curl for reliable resolution on Android
		Category:    "system",
		Execute:     webSearchExecute,
	}
	DefaultRegistry.Register(webSearchTool)

	// Register web_fetch tool
	webFetchTool := Tool{
		Name:        "web_fetch",
		Description: "Fetch the content of a URL and return it as text. Supports http/https URLs.",
		Version:     "2.0.0", // DNS-fixed: uses curl for reliable resolution on Android
		Category:    "system",
		Execute:     webFetchExecute,
	}
	DefaultRegistry.Register(webFetchTool)
}

// webSearchExecute searches the web.
func webSearchExecute(args map[string]interface{}) (interface{}, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return Err("query is required"), nil
	}

	numResults := 5
	if n, ok := args["num_results"].(float64); ok {
		numResults = int(n)
	}

	// Use DuckDuckGo via curl (bypasses Go's broken DNS on Android)
	results, err := duckDuckGoSearchCurl(query, numResults)
	if err != nil {
		return nil, fmt.Errorf("web search failed: %w", err)
	}

	return OK(map[string]interface{}{
		"query":   query,
		"results": results,
	}), nil
}

// curlGet performs an HTTP GET via bash curl (reliable DNS resolution)
func curlGet(targetURL string, headers ...string) (string, error) {
	args := []string{"-sL", "--connect-timeout", "5", "--max-time", "10", "--max-filesize", "1M", "--speed-limit", "100", "--speed-time", "5"}

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

	cmd := exec.Command("curl", args...)
	// Limit output to prevent OOM from huge pages
	stdout := newLimitedBuffer(maxBashOutputBytes)
	stderr := newLimitedBuffer(maxBashOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	if err != nil {
		errStr := strings.TrimSpace(stderr.String())
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("curl failed (exit %d): %s", exitErr.ExitCode(), errStr)
		}
		return "", fmt.Errorf("curl failed: %w", err)
	}

	output := stdout.String()
	if stdout.Len() >= maxBashOutputBytes {
		output += "\n... [response truncated at 512 KiB]"
	}
	return output, nil
}

// duckDuckGoSearchCurl performs a search via DuckDuckGo using curl
func duckDuckGoSearchCurl(query string, numResults int) ([]map[string]string, error) {
	searchURL := fmt.Sprintf("https://html.duckduckgo.com/html/?q=%s", url.QueryEscape(query))

	body, err := curlGet(searchURL,
		"Accept: text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language: en-US,en;q=0.5",
		"DNT: 1",
	)
	if err != nil {
		// Fallback: try Lite endpoint
		return duckDuckGoLiteSearchCurl(query, numResults)
	}

	// Parse HTML results
	results, _ := parseDuckDuckGoHTML(body, numResults)
	if len(results) > 0 {
		return results, nil
	}

	// Fallback to Lite
	return duckDuckGoLiteSearchCurl(query, numResults)
}

// duckDuckGoLiteSearchCurl is a fallback using the Lite endpoint
func duckDuckGoLiteSearchCurl(query string, numResults int) ([]map[string]string, error) {
	searchURL := fmt.Sprintf("https://lite.duckduckgo.com/lite/?q=%s", url.QueryEscape(query))

	body, err := curlGet(searchURL,
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
	targetURL, _ := args["url"].(string)
	if targetURL == "" {
		return Err("url is required"), nil
	}

	format, _ := args["format"].(string)
	if format == "" {
		format = "text"
	}

	body, err := curlGet(targetURL)
	if err != nil {
		return nil, fmt.Errorf("fetch failed: %w", err)
	}

	if format == "json" {
		var data interface{}
		if err := json.Unmarshal([]byte(body), &data); err == nil {
			return OK(map[string]interface{}{
				"url":     targetURL,
				"content": data,
			}), nil
		}
	}

	return OK(map[string]interface{}{
		"url":     targetURL,
		"content": body,
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
