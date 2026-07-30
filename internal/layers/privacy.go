// Package layers implements privacy pipeline for the ELING agent.
// Provides PII/secret stripping, SHA-256 dedup, and content sanitization.
// Adapted from Python eling's privacy.py and compress.py.
package layers

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// ── Privacy Patterns (19 patterns from Python eling) ───────────────────────

// PrivacyPattern defines a regex-based PII/secret detection pattern.
type PrivacyPattern struct {
	Name    string
	Pattern *regexp.Regexp
	Replace string // replacement string (empty = redact with name)
}

var privacyPatterns = []PrivacyPattern{
	// API Keys & Tokens
	{`GitHub Token`, regexp.MustCompile(`ghp_[a-zA-Z0-9]{36}`), `[REDACTED_GITHUB_TOKEN]`},
	{`GitHub Classic Token`, regexp.MustCompile(`gho_[a-zA-Z0-9]{36}`), `[REDACTED_GITHUB_TOKEN]`},
	{`GitLab Token`, regexp.MustCompile(`glpat-[a-zA-Z0-9\-_]{20,}`), `[REDACTED_GITLAB_TOKEN]`},
	{`OpenAI API Key`, regexp.MustCompile(`sk-[a-zA-Z0-9]{20,}`), `[REDACTED_OPENAI_KEY]`},
	{`OpenAI Project Key`, regexp.MustCompile(`sk-proj-[a-zA-Z0-9\-_]{20,}`), `[REDACTED_OPENAI_PROJECT_KEY]`},
	{`Anthropic API Key`, regexp.MustCompile(`sk-ant-[a-zA-Z0-9]{20,}`), `[REDACTED_ANTHROPIC_KEY]`},
	{`Notion API Key`, regexp.MustCompile(`ntn_[a-zA-Z0-9]{20,}`), `[REDACTED_NOTION_KEY]`},
	{`AWS Access Key`, regexp.MustCompile(`AKIA[0-9A-Z]{16}`), `[REDACTED_AWS_KEY]`},
	{`AWS Secret Key`, regexp.MustCompile(`(?i)aws[_-]?secret[_-]?access[_-]?key\s*[:=]\s*['"]?\S+`), `[REDACTED_AWS_SECRET]`},
	{`Google API Key`, regexp.MustCompile(`AIza[0-9A-Za-z\-_]{35}`), `[REDACTED_GOOGLE_KEY]`},
	{`Slack Token`, regexp.MustCompile(`xox[bprsa]-[0-9A-Za-z\-_]{20,}`), `[REDACTED_SLACK_TOKEN]`},
	{`JWT Token`, regexp.MustCompile(`eyJ[a-zA-Z0-9\-_]+\.eyJ[a-zA-Z0-9\-_]+\.[a-zA-Z0-9\-_]+`), `[REDACTED_JWT]`},
	{`Generic Bearer Token`, regexp.MustCompile(`(?i)bearer\s+[a-zA-Z0-9\-_.]{20,}`), `[REDACTED_BEARER_TOKEN]`},
	{`Generic API Key Header`, regexp.MustCompile(`(?i)(x-api-key|api-key)\s*:\s*\S+`), `$1: [REDACTED]`},
	// Connection strings
	{`Database URL`, regexp.MustCompile(`(postgresql?|mysql|mongodb|redis|rediss)://[^@\s]+@`), `$1://[REDACTED]@`},
	{`Private Key`, regexp.MustCompile(`-----BEGIN\s*(?:RSA\s*)?PRIVATE\s*KEY-----[\s\S]*?-----END\s*(?:RSA\s*)?PRIVATE\s*KEY-----`), `[REDACTED_PRIVATE_KEY]`},
	// Secrets in config
	{`Password in Config`, regexp.MustCompile(`(?i)(password|passwd|pwd|secret)\s*[:=]\s*['"]?\S+`), `$1: [REDACTED]`},
	// IP addresses (private/internal only)
	{`Email`, regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`), `[REDACTED_EMAIL]`},
	{`Private IP`, regexp.MustCompile(`\b(127\.\d{1,3}\.\d{1,3}\.\d{1,3}|10\.\d{1,3}\.\d{1,3}\.\d{1,3}|172\.(1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3}|192\.168\.\d{1,3}\.\d{1,3})\b`), `[REDACTED_PRIVATE_IP]`},
}

// PrivacyPipeline handles PII detection, redaction, and deduplication.
type PrivacyPipeline struct {
	mu        sync.Mutex
	seenHashes map[string]bool // SHA-256 hashes of previously seen content
}

// PrivacyResult contains the output of processing content.
type PrivacyResult struct {
	Original    string `json:"original"`
	Clean       string `json:"clean"`
	Redacted    bool   `json:"redacted"`
	IsDuplicate bool   `json:"is_duplicate"`
	PatternsHit []string `json:"patterns_hit,omitempty"`
}

// NewPrivacyPipeline creates a new PrivacyPipeline.
func NewPrivacyPipeline() *PrivacyPipeline {
	return &PrivacyPipeline{
		seenHashes: make(map[string]bool),
	}
}

// Process runs the full privacy and dedup pipeline on the given content.
// Returns cleaned content and metadata.
func (p *PrivacyPipeline) Process(content string) *PrivacyResult {
	result := &PrivacyResult{
		Original: content,
	}

	// 1. SHA-256 dedup check
	hash := sha256Hex(content)
	p.mu.Lock()
	if p.seenHashes[hash] {
		p.mu.Unlock()
		result.IsDuplicate = true
		result.Clean = content
		return result
	}
	p.seenHashes[hash] = true
	p.mu.Unlock()

	// 2. PII/secret stripping (no mutex needed — reads only)
	clean, patternsHit := p.strip(content)
	result.Clean = clean
	result.Redacted = len(patternsHit) > 0
	result.PatternsHit = patternsHit

	return result
}

// Strip performs PII/secret redaction on the given content.
func (p *PrivacyPipeline) Strip(content string) (string, []string) {
	return p.strip(content)
}

// Stats returns pipeline statistics.
func (p *PrivacyPipeline) Stats() map[string]interface{} {
	p.mu.Lock()
	defer p.mu.Unlock()

	return map[string]interface{}{
		"total_unique": len(p.seenHashes),
		"patterns":     len(privacyPatterns),
	}
}

// ── Internal ───────────────────────────────────────────────────────────────

func (p *PrivacyPipeline) strip(content string) (string, []string) {
	var hits []string
	cleaned := content

	for _, pp := range privacyPatterns {
		if pp.Pattern.MatchString(cleaned) {
			cleaned = pp.Pattern.ReplaceAllString(cleaned, pp.Replace)
			hits = append(hits, pp.Name)
		}
	}

	// Also redact any line that looks like "KEY=VALUE" where KEY is uppercase
	if regexp.MustCompile(`^[A-Z][A-Z0-9_]+\s*=\s*\S`).MatchString(cleaned) {
		cleaned = regexp.MustCompile(`(?m)^([A-Z][A-Z0-9_]+)\s*=\s*\S+.*$`).ReplaceAllString(cleaned, `$1=[REDACTED]`)
	}

	return cleaned, hits
}

// ── Compression (SHA-256 dedup + length compression) ───────────────────────

// CompressContent performs length-based compression on content.
// If content is longer than 2000 chars, it truncates with a note.
// This is a simpler version of Python's compress.py.
func CompressContent(content string) string {
	if len(content) <= 2000 {
		return content
	}

	// Try to keep structure: first 1000 chars + last 500 chars
	lines := strings.Split(content, "\n")
	if len(lines) < 10 {
		return content[:1500] + fmt.Sprintf("\n\n[... truncated, original length: %d ...]\n\n", len(content)) + content[len(content)-500:]
	}

	// Keep first and last sections
	var result strings.Builder
	mid := len(lines) / 2

	// First section
	for _, line := range lines[:mid/2] {
		result.WriteString(line)
		result.WriteString("\n")
	}

	result.WriteString(fmt.Sprintf("\n[... truncated %d lines, original length: %d ...]\n\n", len(lines)-mid, len(content)))

	// Last section
	for _, line := range lines[len(lines)-mid/2:] {
		result.WriteString(line)
		result.WriteString("\n")
	}

	return strings.TrimSpace(result.String())
}

// ── Helpers ────────────────────────────────────────────────────────────────

func sha256Hex(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h)
}

// Brain methods for privacy

// ProcessPrivacy runs the privacy pipeline on the given content.
func (b *Brain) ProcessPrivacy(content string) *PrivacyResult {
	// Check if any layer has a privacy pipeline
	// For now, create a default one
	pp := NewPrivacyPipeline()
	return pp.Process(content)
}

// StripPII performs PII/secret redaction on the given content.
func (b *Brain) StripPII(content string) (string, []string) {
	pp := NewPrivacyPipeline()
	return pp.Strip(content)
}
