package layers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// NotionLayer is Layer 7: the online brain persistence layer.
// It optionally syncs high-trust facts to a Notion database as
// well-formatted pages, providing recoverable storage.
//
// Adapted from Python eling's layers/notion.py
type NotionLayer struct {
	mu       sync.RWMutex
	apiKey   string
	parentID string
	client   *http.Client
	enabled  bool
}

// NewNotionLayer creates a NotionLayer.
// Requires NOTION_API_KEY and NOTION_PARENT_PAGE_ID env vars.
// Returns nil (disabled) if either is missing.
func NewNotionLayer() *NotionLayer {
	apiKey := getEnv("NOTION_API_KEY", "")
	parentID := getEnv("NOTION_PARENT_PAGE_ID", "")
	if apiKey == "" || parentID == "" {
		return nil
	}
	return &NotionLayer{
		apiKey:   apiKey,
		parentID: parentID,
		client:   &http.Client{Timeout: 30 * time.Second},
		enabled:  true,
	}
}

// Name returns "notion".
func (l *NotionLayer) Name() string { return "notion" }

// Priority returns 7.
func (l *NotionLayer) Priority() int { return 7 }

// Query is not supported for Notion (write-only in this implementation).
func (l *NotionLayer) Query(ctx context.Context, q string, limit int) ([]Result, error) {
	return nil, nil
}

// Store creates a Notion page from the item content.
func (l *NotionLayer) Store(ctx context.Context, item Item) error {
	if l == nil || !l.enabled {
		return nil
	}
	return l.createPage(ctx, item.Category, item.Content, item.Tags)
}

// Close is a no-op.
func (l *NotionLayer) Close() error { return nil }

// createPage creates a Notion page under the parent.
func (l *NotionLayer) createPage(ctx context.Context, title, content string, tags []string) error {
	if !l.enabled {
		return nil
	}

	// Determine icon and category routing
	icon := "📋"
	lowerTitle := strings.ToLower(title)

	switch {
	case strings.Contains(lowerTitle, "project") || strings.Contains(lowerTitle, "complete") ||
		strings.Contains(lowerTitle, "deploy") || strings.Contains(lowerTitle, "summary"):
		icon = "🎯"
	case strings.Contains(lowerTitle, "api_key") || strings.Contains(lowerTitle, "password") ||
		strings.Contains(lowerTitle, "secret") || strings.Contains(lowerTitle, "token") ||
		strings.Contains(lowerTitle, "credential"):
		icon = "🔑"
	case strings.Contains(lowerTitle, "config") || strings.Contains(lowerTitle, "setup") ||
		strings.Contains(lowerTitle, "setting") || strings.Contains(lowerTitle, "environment"):
		icon = "⚙️"
	}

	// Build Notion page blocks
	blocks := []map[string]interface{}{
		{
			"object": "block",
			"type":   "heading_2",
			"heading_2": map[string]interface{}{
				"rich_text": []map[string]interface{}{
					{"type": "text", "text": map[string]interface{}{"content": title}},
				},
			},
		},
		{
			"object": "block",
			"type":   "paragraph",
			"paragraph": map[string]interface{}{
				"rich_text": []map[string]interface{}{
					{"type": "text", "text": map[string]interface{}{"content": content}},
				},
			},
		},
	}

	// Add tags as a callout
	if len(tags) > 0 {
		tagText := "Tags: " + strings.Join(tags, ", ")
		blocks = append(blocks, map[string]interface{}{
			"object": "block",
			"type":   "callout",
			"callout": map[string]interface{}{
				"rich_text": []map[string]interface{}{
					{"type": "text", "text": map[string]interface{}{"content": tagText}},
				},
				"icon": map[string]interface{}{"emoji": "🏷️"},
			},
		})
	}

	// Build the page request
	body := map[string]interface{}{
		"parent": map[string]interface{}{
			"type":   "page_id",
			"page_id": l.parentID,
		},
		"icon": map[string]interface{}{
			"type":  "emoji",
			"emoji": icon,
		},
		"properties": map[string]interface{}{
			"title": map[string]interface{}{
				"title": []map[string]interface{}{
					{"type": "text", "text": map[string]interface{}{"content": title}},
				},
			},
		},
		"children": blocks,
	}

	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://api.notion.com/v1/pages", bytes.NewReader(data))
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+l.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Notion-Version", "2022-06-28")

	resp, err := l.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respData, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("notion API error %d: %s", resp.StatusCode, string(respData))
	}

	return nil
}

func getEnv(key, fallback string) string {
	if v := strings.TrimSpace(strings.ToLower(os.Getenv(key))); v != "" {
		return os.Getenv(key)
	}
	return fallback
}
