// Package provider provides multi-provider LLM communication.
// Supports any OpenAI-compatible API, configurable per provider.
// Includes automatic retry with exponential backoff for transient API errors.
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Default retry configuration.
const (
	defaultMaxRetries    = 5
	defaultBaseDelay     = 2 * time.Second
	defaultMaxDelay      = 60 * time.Second
	defaultRequestBudget = 120 * 3 // 6 minutes total retry budget (3 minutes per budget, doubled)
)

// DefaultOuterRetries is the default number of additional retry attempts at
// the agent/application level, applied after the provider-level retries are
// exhausted. This handles longer-lived transient failures.
const DefaultOuterRetries = 2

// RetryConfig controls automatic retry behaviour for API calls.
type RetryConfig struct {
	// MaxRetries is the maximum number of retry attempts (0 = no retries).
	MaxRetries int `json:"max_retries" yaml:"max_retries"`
	// BaseDelay is the initial backoff delay.
	BaseDelay time.Duration `json:"base_delay" yaml:"base_delay"`
	// MaxDelay is the upper bound for backoff delay.
	MaxDelay time.Duration `json:"max_delay" yaml:"max_delay"`
	// MaxBudget is the total wall-clock time allowed for all retries combined
	// (prevents indefinite retrying on persistent failures).
	MaxBudget time.Duration `json:"max_budget" yaml:"max_budget"`
	// OnRetry, if set, is called before each retry with the attempt number,
	// the error that caused the retry, and the delay that will be applied.
	OnRetry func(attempt int, err error, delay time.Duration)
}

// DefaultRetryConfig returns a sensible default retry configuration.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries: defaultMaxRetries,
		BaseDelay:  defaultBaseDelay,
		MaxDelay:   defaultMaxDelay,
		MaxBudget:  defaultRequestBudget * time.Second,
	}
}

// isRetryableHTTPError returns true if the HTTP status code indicates a
// transient error that can be safely retried.
//
// Retryable status codes:
//   - 408 Request Timeout — server timed out waiting for client
//   - 429 Too Many Requests (rate limit)
//   - 500 Internal Server Error
//   - 502 Bad Gateway
//   - 503 Service Unavailable
//   - 504 Gateway Timeout
//
// All other 4xx codes (400, 401, 403, 404, 422, etc.) are treated as
// non-retryable because retrying won't change the result.
func isRetryableHTTPError(statusCode int) bool {
	switch statusCode {
	case http.StatusRequestTimeout, // 408
		http.StatusTooManyRequests,     // 429
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout:      // 504
		return true
	default:
		return false
	}
}

// parseRetryAfter parses the Retry-After header value from an HTTP response.
// Returns 0 if the header is missing or unparseable.
// Supports both HTTP-date and seconds formats as defined in RFC 9110.
func parseRetryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	header := resp.Header.Get("Retry-After")
	if header == "" {
		return 0
	}

	// Try seconds format first: "Retry-After: 120"
	if seconds, err := parseRetryAfterSeconds(header); err == nil {
		return time.Duration(seconds) * time.Second
	}

	// Try HTTP-date format: "Retry-After: Wed, 21 Oct 2015 07:28:00 GMT"
	if t, err := http.ParseTime(header); err == nil {
		delay := time.Until(t)
		if delay > 0 {
			return delay
		}
		return 0 // already past
	}

	return 0
}

// isUpstreamError returns true if the response body indicates a transient
// upstream provider error (e.g. "Upstream request failed"). These errors
// typically come back as HTTP 400 but are actually transient server-side
// failures, not client errors.
func isUpstreamError(body []byte) bool {
	// Convert to lowercase for case-insensitive matching
	lower := strings.ToLower(string(body))
	upstreamPatterns := []string{
		"upstream request failed",
		"upstream request timeout",
		"upstream connect error",
		"upstream service unavailable",
		"upstream failure",
		"upstream unavailable",
		"upstream reset",
		"upstream service",
		"upstream is unavailable",
		"upstream temporarily",
	}
	for _, p := range upstreamPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// IsUpstreamError returns true if the error indicates an upstream provider
// failure that may warrant trying a different provider.
// Public helper so callers (e.g. agent) can detect this condition.
func IsUpstreamError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "upstream") ||
		strings.Contains(strings.ToLower(err.Error()), "transient upstream")
}

// parseRetryAfterSeconds attempts to parse a Retry-After value as a decimal
// number of seconds. Returns an error if the value is missing, negative, or
// not a valid integer. Note: "0" is valid (means retry immediately).
func parseRetryAfterSeconds(header string) (int, error) {
	// Trim any whitespace
	header = strings.TrimSpace(header)
	if header == "" {
		return 0, fmt.Errorf("empty header")
	}
	var seconds int
	_, err := fmt.Sscanf(header, "%d", &seconds)
	if err != nil {
		return 0, err
	}
	if seconds < 0 {
		return 0, fmt.Errorf("negative seconds: %d", seconds)
	}
	// seconds == 0 is valid — means "retry immediately"
	return seconds, nil
}

// isRetryableHTTPErrorCode classifies an error from an HTTP API call as
// retryable or not.  Returns true for network errors, timeouts, and
// retryable status codes.
func isRetryableHTTPErrorCode(statusCode int, err error) bool {
	if err != nil {
		// Network errors, DNS failures, timeouts, connection refused, etc.
		// are all worth retrying.
		return true
	}
	return isRetryableHTTPError(statusCode)
}

// retryWithBackoff executes fn up to maxRetries times, backing off
// exponentially with jitter between attempts.  It respects context
// cancellation and a total time budget.
//
// If the error returned by fn is a *RetryDelayError, the backoff delay
// from the error is used instead of the computed exponential backoff.
// This allows callers to propagate Retry-After headers from 429 responses.
func retryWithBackoff(ctx context.Context, cfg RetryConfig, fn func() error) error {
	if cfg.MaxRetries <= 0 {
		// No retry configured — just call once.
		return fn()
	}

	start := time.Now()
	var lastErr error

	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		// Check context before each attempt (including the first).
		select {
		case <-ctx.Done():
			return fmt.Errorf("retry aborted: %w", ctx.Err())
		default:
		}

		if attempt > 0 && cfg.MaxBudget > 0 {
			// Check total budget before retrying.
			if time.Since(start) >= cfg.MaxBudget {
				return fmt.Errorf("retry budget exhausted after %d attempts (%s): %w",
					attempt, friendlyDuration(cfg.MaxBudget), lastErr)
			}
		}

		err := fn()
		if err == nil {
			return nil // success
		}
		lastErr = err

		// If we've exhausted retries, return the error.
		if attempt >= cfg.MaxRetries {
			break
		}

		// Determine if this error is retryable.
		if !isRetryableError(err) {
			return err // non-retryable; bail immediately
		}

		// Compute backoff delay.
		// If the error carries a RetryDelayError with a suggested delay,
		// use that instead of the exponential backoff.
		var delay time.Duration
		if suggestedDelay := GetRetryDelay(err); suggestedDelay > 0 {
			// Use suggested delay, but cap at MaxDelay to avoid infinite waits
			if suggestedDelay > cfg.MaxDelay {
				delay = cfg.MaxDelay
			} else {
				delay = suggestedDelay
			}
		} else {
			// Exponential backoff with full jitter.
			// delay = min(maxDelay, baseDelay * 2^attempt)  -- capped exponential
			// actual = random(0, delay)                      -- full jitter
			d := float64(cfg.BaseDelay) * math.Pow(2, float64(attempt))
			if d > float64(cfg.MaxDelay) {
				d = float64(cfg.MaxDelay)
			}
			// Full jitter: random between 0 and delay.
			jittered := time.Duration(rand.Int63n(int64(d)))
			if jittered < 10*time.Millisecond {
				jittered = 10 * time.Millisecond // floor so we don't hammer
			}
			delay = jittered
		}

		// Notify caller if they provided a callback.
		if cfg.OnRetry != nil {
			cfg.OnRetry(attempt, lastErr, delay)
		}

		// Wait for the delay or context cancellation.
		select {
		case <-ctx.Done():
			return fmt.Errorf("retry aborted during backoff: %w", ctx.Err())
		case <-time.After(delay):
		}
	}

	return fmt.Errorf("retry failed after %d attempts: %w", cfg.MaxRetries+1, lastErr)
}

// isRetryableError returns true if the error is likely transient.
// All matching is case-insensitive (error string is lowercased before matching).
// If the error is a *NonRetryableError, it's definitively not retryable.
// If the error is a *RetryDelayError, it IS retryable (the wrapper carries
// a suggested delay from a Retry-After header).
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	// If it's a NonRetryableError, definitely not retryable.
	if _, ok := err.(*NonRetryableError); ok {
		return false
	}
	// RetryDelayError wraps a retryable error — it IS retryable.
	if _, ok := err.(*RetryDelayError); ok {
		return true
	}
	errStr := err.Error()

	// Check for known retryable error substrings from various providers.
	// NOTE: all patterns MUST be lowercase — errStr is lowercased before matching.
	retryablePatterns := []string{
		// Network-level errors
		"timeout",
		"timed out",
		"connection refused",
		"connection reset",
		"connection closed",
		"broken pipe",
		"no such host",
		"tls handshake",
		"i/o timeout",
		"eof",
		"stream error",
		"http2",
		"reset by peer",
		"host unreachable",
		"network unreachable",
		"no route to host",

		// HTTP-level retryable status codes
		"408",
		"429",
		"500",
		"502",
		"503",
		"504",

		// Rate limiting
		"rate limit",
		"rate_limit",
		"too many requests",
		"throttl", // matches "throttle", "throttling"
		"capacity",
		"quota exceeded",
		"quota_exceeded",

		// Server-side transient errors
		"internal server error",
		"server error",
		"server is busy",
		"service unavailable",
		"service is unavailable",
		"bad gateway",
		"gateway timeout",
		"upstream connect error",
		"upstream service",
		"upstream failure",
		"upstream unavailable",
		"upstream request timeout",
		"upstream reset",
		"cluster not ready",
		"cluster unavailable",
		"backend timeout",

		// Generic transient signals
		"temporary",
		"temporarily unavailable",
		"try again",
		"please try again",
		"retry",
		"unavailable",
		"overloaded",
		"busy",
		"backpressure",
		"down for maintenance",
		"under maintenance",

		// Context / deadline
		"deadline exceeded",
		"context deadline",
		"no available",

		// OpenAI / provider-specific error codes
		"insufficient_quota",
		"engine_overloaded",
		"server_has_error",
		"provider_error",
		"model_overloaded",
		"token_limit",
	}
	errLower := strings.ToLower(errStr)
	for _, p := range retryablePatterns {
		if strings.Contains(errLower, p) {
			return true
		}
	}
	return false
}

// NonRetryableError wraps errors that should never be retried (e.g. 400 Bad
// Request, 401 Unauthorized, 403 Forbidden, 404 Not Found, 422 Unprocessable).
// Callers outside the provider package can use NewNonRetryableError to wrap
// their own errors so the retry loop knows they must not be retried.
type NonRetryableError struct {
	msg string
}

func (e *NonRetryableError) Error() string {
	return e.msg
}

// NewNonRetryableError wraps an error or string as a non-retryable error.
// If err is nil, nil is returned.  Use this to mark errors that should never
// be automatically retried (e.g. invalid input, authentication failures).
func NewNonRetryableError(msg string) *NonRetryableError {
	return &NonRetryableError{msg: msg}
}

// RetryDelayError wraps a retryable error with a specific suggested delay
// (e.g. from a Retry-After header). The retry loop will use this delay
// instead of the exponential backoff calculation.
type RetryDelayError struct {
	Err   error
	Delay time.Duration
}

func (e *RetryDelayError) Error() string {
	return fmt.Sprintf("retry after %s: %v", friendlyDuration(e.Delay), e.Err)
}

// Unwrap returns the wrapped error for errors.Is/As compatibility.
func (e *RetryDelayError) Unwrap() error {
	return e.Err
}

// NewRetryDelayError wraps an error with a suggested retry delay.
// If delay is <= 0, the normal exponential backoff is used.
// Use this to propagate Retry-After headers from rate-limited responses.
func NewRetryDelayError(err error, delay time.Duration) *RetryDelayError {
	return &RetryDelayError{Err: err, Delay: delay}
}

// GetRetryDelay extracts a suggested delay from an error chain.
// Returns 0 if no RetryDelayError is found in the chain.
func GetRetryDelay(err error) time.Duration {
	if err == nil {
		return 0
	}
	var rde *RetryDelayError
	if errors.As(err, &rde) {
		return rde.Delay
	}
	return 0
}

// IsRetryable returns true if the given error is considered transient.
// Public helper so callers (e.g. agent/tui) can inspect errors after retries
// are exhausted and decide whether to show a "retry" button or just bail.
func IsRetryable(err error) bool {
	return isRetryableError(err)
}

// RetryBudgetExceeded returns true if the error indicates that the retry
// budget was exhausted (i.e. the operation kept failing transiently for too
// long).  Callers can use this to distinguish \"gave up after N retries\" from
// \"non-retryable error.\"
func RetryBudgetExceeded(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "retry budget exhausted")
}

// Message represents a chat message.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall is a function call requested by the model.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// ToolDef mirrors the OpenAI-style function tool definition. Kept generic
// (Function as interface{}) so this package doesn't need to import
// internal/tools and create an import cycle.
type ToolDef struct {
	Type     string      `json:"type"`
	Function interface{} `json:"function"`
}

// ChatResponse represents the API response.
type ChatResponse struct {
	Content   string     `json:"content"`
	Reasoning string     `json:"reasoning,omitempty"` // model's internal reasoning (e.g. reasoning_content)
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Tokens    int        `json:"tokens"`
}

// ProviderConfig configures an LLM provider.
type ProviderConfig struct {
	Name    string
	Model   string
	BaseURL string
	APIKey  string
	// BackupKeys are additional API keys (excluding the primary) used in
	// round-robin rotation when the current key fails with an auth/403 error.
	BackupKeys []string
}

// RetryStats holds cumulative retry statistics for a provider.
type RetryStats struct {
	mu            sync.Mutex
	TotalAttempts int           `json:"total_attempts"`
	RetriedCalls  int           `json:"retried_calls"`
	RetrySuccess  int           `json:"retry_success"` // calls that succeeded after at least one retry
	FailedCalls   int           `json:"failed_calls"`
	TotalBackoff  time.Duration `json:"total_backoff"`
	LastError     string        `json:"last_error"`
}

// recordRetry records a single retry attempt.
func (s *RetryStats) recordRetry(backoff time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TotalAttempts++
	s.RetriedCalls++
	s.TotalBackoff += backoff
}

// recordRetrySuccess records that a call succeeded after one or more retries.
func (s *RetryStats) recordRetrySuccess() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RetrySuccess++
}

// recordCall records the outcome of a call (not counting retries).
func (s *RetryStats) recordCall(success bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TotalAttempts++
	if !success {
		s.FailedCalls++
		if err != nil {
			s.LastError = err.Error()
		}
	}
}

// Snapshot returns a copy of the current stats.
func (s *RetryStats) Snapshot() RetryStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return RetryStats{
		TotalAttempts: s.TotalAttempts,
		RetriedCalls:  s.RetriedCalls,
		RetrySuccess:  s.RetrySuccess,
		FailedCalls:   s.FailedCalls,
		TotalBackoff:  s.TotalBackoff,
		LastError:     s.LastError,
	}
}

// Provider handles communication with an LLM API.
// It includes automatic retry with exponential backoff for transient errors,
// and automatic key rotation for auth/permission errors.
type Provider struct {
	config ProviderConfig
	client *http.Client
	retry  RetryConfig
	stats  RetryStats

	// Key rotation state
	keyRing   []string     // all keys: [primary] + backup keys
	keyIdx    atomic.Int64 // current key index (atomic for thread safety)
	keyRotErr atomic.Bool  // set when rotation has been triggered
}

// New creates a new provider with the given config and default retry settings.
func New(cfg ProviderConfig) *Provider {
	// Build the key ring: primary key first, then backup keys
	keyRing := []string{cfg.APIKey}
	if len(cfg.BackupKeys) > 0 {
		// Deduplicate: only add backup keys that aren't the same as primary
		for _, bk := range cfg.BackupKeys {
			if bk != cfg.APIKey && bk != "" {
				keyRing = append(keyRing, bk)
			}
		}
	}

	return &Provider{
		config: cfg,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
		retry:   DefaultRetryConfig(),
		keyRing: keyRing,
	}
}

// SetRetryConfig updates the retry configuration on the provider.
func (p *Provider) SetRetryConfig(rc RetryConfig) {
	p.retry = rc
}

// GetRetryConfig returns a copy of the current retry configuration.
func (p *Provider) GetRetryConfig() RetryConfig {
	return p.retry
}

// GetRetryStats returns a snapshot of the provider's retry statistics.
func (p *Provider) GetRetryStats() RetryStats {
	return p.stats.Snapshot()
}

// ResetRetryStats resets the cumulative retry statistics.
func (p *Provider) ResetRetryStats() {
	p.stats = RetryStats{}
}

// currentKey returns the currently active API key.
func (p *Provider) currentKey() string {
	idx := p.keyIdx.Load()
	if idx < 0 || idx >= int64(len(p.keyRing)) {
		idx = 0
		p.keyIdx.Store(0)
	}
	return p.keyRing[idx]
}

// rotateKey advances to the next API key in the key ring (round-robin).
// Returns the new active key.
// Thread-safe: uses atomic CompareAndSwap to keep index bounded.
func (p *Provider) rotateKey() string {
	if len(p.keyRing) <= 1 {
		// Only one key — can't rotate
		return p.currentKey()
	}
	// Atomically advance and apply modulo to keep index bounded.
	// CAS loop ensures thread safety even under concurrent rotation.
	for {
		old := p.keyIdx.Load()
		newIdx := (old + 1) % int64(len(p.keyRing))
		if p.keyIdx.CompareAndSwap(old, newIdx) {
			p.keyRotErr.Store(true)
			return p.keyRing[newIdx]
		}
	}
}

// NumKeys returns the total number of keys in the key ring.
func (p *Provider) NumKeys() int {
	return len(p.keyRing)
}

// HasRotated returns true if key rotation has been triggered at least once.
func (p *Provider) HasRotated() bool {
	return p.keyRotErr.Load()
}

// isAuthError returns true if the error indicates an authentication/authorization
// problem that would warrant rotating to a different API key.
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	errLower := strings.ToLower(errStr)

	// Authentication / authorization patterns
	authPatterns := []string{
		"401",
		"unauthorized",
		"unauthorised",
		"invalid api key",
		"invalid_api_key",
		"api key",
		"api_key",
		"authentication",
		"auth error",
		"forbidden",
		"403",
		"insufficient_quota", // quota exceeded — may need to switch keys
		"insufficient quota",
		"quota exceeded",
		"quota_exceeded",
		"no permission",
		"permission denied",
		"access denied",
		"credential",
		"token invalid",
		"invalid token",
		"token expired",
		"expired token",
		"bad credentials",
		"key not found",
		"account disabled",
		"inactive",
		"subscription",
		"not authorized",
		"not authorised",
	}

	for _, pat := range authPatterns {
		if strings.Contains(errLower, pat) {
			return true
		}
	}
	return false
}

// Chat sends a chat completion request and returns the response.
// Retries automatically on transient errors (rate limits, server errors,
// network timeouts) using exponential backoff with jitter.
// Cumulative retry statistics are tracked and can be inspected via GetRetryStats.
//
// Key rotation: if the request fails with an auth/permission error (401, 403,
// quota exceeded, etc.), the provider rotates to the next API key in the key
// ring and retries. This repeats until a key works or all keys are exhausted.
func (p *Provider) Chat(ctx context.Context, messages []Message, tools ...ToolDef) (*ChatResponse, error) {
	// Build the request body once (shared across retries).
	reqBody := map[string]interface{}{
		"model":    p.config.Model,
		"messages": messages,
		"stream":   false,
	}
	if len(tools) > 0 {
		reqBody["tools"] = tools
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	var result *ChatResponse
	var lastAuthErr error     // track auth errors for key rotation
	var lastUpstreamErr error // track upstream errors for key rotation

	// Create a retry config with stats tracking injected via OnRetry.
	retryCfg := p.retry
	userOnRetry := retryCfg.OnRetry // preserve any user-set callback
	var retried bool                // set true if any retry was attempted
	retryCfg.OnRetry = func(attempt int, err error, delay time.Duration) {
		retried = true
		p.stats.recordRetry(delay)
		if userOnRetry != nil {
			userOnRetry(attempt, err, delay)
		}
	}

	err = retryWithBackoff(ctx, retryCfg, func() error {
		// Get the current active key (may have been rotated by a previous attempt)
		activeKey := p.currentKey()

		req, reqErr := http.NewRequestWithContext(ctx, "POST",
			p.config.BaseURL+"/chat/completions", bytes.NewReader(body))
		if reqErr != nil {
			return fmt.Errorf("create request: %w", reqErr)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+activeKey)

		resp, doErr := p.client.Do(req)
		if doErr != nil {
			// Network-level error (timeout, connection refused, DNS failure, etc.)
			return fmt.Errorf("http request: %w", doErr)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			statusCode := resp.StatusCode
			errMsg := formatAPIError(statusCode, respBody)

			// Check if this is an auth error that warrants key rotation
			if isAuthError(fmt.Errorf("%d", statusCode)) || isAuthError(fmt.Errorf("%s", errMsg)) {
				lastAuthErr = fmt.Errorf("%s", errMsg)
				if p.NumKeys() > 1 {
					// Rotate to next key and return a retryable error so the
					// retry loop tries again with the new key.
					// Wrap in RetryDelayError so isRetryableError returns true.
					p.rotateKey()
					rotIdx := p.keyIdx.Load() % int64(p.NumKeys())
					retriedMsg := fmt.Sprintf("rotated to key %d: %s", rotIdx, errMsg)
					return NewRetryDelayError(
						fmt.Errorf("%s — %s", retriedMsg, errMsg), 100*time.Millisecond)
				}
				// Only one key — can't rotate, return the error
				return &NonRetryableError{msg: errMsg}
			}

			// Check if this is a retryable HTTP status code.
			if isRetryableHTTPError(statusCode) {
				if retryAfter := parseRetryAfter(resp); retryAfter > 0 {
					return NewRetryDelayError(
						fmt.Errorf("%s (retryable)", errMsg), retryAfter)
				}
				return fmt.Errorf("%s (retryable)", errMsg)
			}

			// Special case: 400 with "upstream" in the body is transient
			// (e.g. "Upstream request failed", "upstream service unavailable").
			// These are NOT client errors — the upstream provider is having
			// a transient issue and retrying will likely succeed.
			// Do NOT rotate the API key here: all keys route to the same
			// upstream, so rotating would waste backup keys and produce
			// a misleading "all keys exhausted" error.
			if statusCode == 400 && isUpstreamError(respBody) {
				if retryAfter := parseRetryAfter(resp); retryAfter > 0 {
					return NewRetryDelayError(
						fmt.Errorf("%s (transient upstream)", errMsg), retryAfter)
				}
				return fmt.Errorf("%s (transient upstream — will retry)", errMsg)
			}

			// Non-retryable 4xx — bail immediately.
			return &NonRetryableError{msg: errMsg}
		}

		var apiResult struct {
			Choices []struct {
				Message struct {
					Content          string     `json:"content"`
					ReasoningContent string     `json:"reasoning_content"` // DeepSeek reasoning field
					ToolCalls        []ToolCall `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
			Usage struct {
				TotalTokens int `json:"total_tokens"`
			} `json:"usage"`
		}

		if decodeErr := json.NewDecoder(resp.Body).Decode(&apiResult); decodeErr != nil {
			return fmt.Errorf("decode response: %w", decodeErr)
		}

		if len(apiResult.Choices) == 0 {
			return fmt.Errorf("no choices in response")
		}

		msg := apiResult.Choices[0].Message
		result = &ChatResponse{
			Content:   msg.Content,
			Reasoning: msg.ReasoningContent,
			ToolCalls: msg.ToolCalls,
			Tokens:    apiResult.Usage.TotalTokens,
		}
		return nil
	})

	if err != nil {
		// If we exhausted keys, wrap with a helpful message
		if p.HasRotated() {
			if lastAuthErr != nil {
				err = fmt.Errorf("all %d API keys exhausted — last error: %w", p.NumKeys(), lastAuthErr)
			} else if lastUpstreamErr != nil {
				err = fmt.Errorf("all %d API keys exhausted — upstream errors on all keys: %w", p.NumKeys(), lastUpstreamErr)
			}
		}
		p.stats.recordCall(false, err)
		return nil, err
	}
	p.stats.recordCall(true, nil)
	if retried {
		p.stats.recordRetrySuccess()
	}
	return result, nil
}

// ChatStream sends a chat completion request and streams the response.
// Returns the accumulated text content and any tool calls the model requested.
//
// Retry behaviour: if the stream fails BEFORE any content was received (e.g.
// initial HTTP error or first chunk never arrived), the method will retry
// using the configured backoff.  If content has already started streaming,
// the partial result is returned along with the error rather than retrying
// (which would produce garbled/duplicate output).
func (p *Provider) ChatStream(ctx context.Context, messages []Message, onChunk func(string), tools ...ToolDef) (string, []ToolCall, error) {
	// Build request body once.
	reqBody := map[string]interface{}{
		"model":    p.config.Model,
		"messages": messages,
		"stream":   true,
	}
	if len(tools) > 0 {
		reqBody["tools"] = tools
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", nil, fmt.Errorf("marshal request: %w", err)
	}

	var (
		finalContent    string
		finalReasoning  string
		finalCalls      []ToolCall
		gotData         bool  // set true once any content/chunk has been received
		lastAuthErr     error // track auth errors for key rotation
		lastUpstreamErr error // track upstream errors for key rotation
	)

	// Create a retry config with stats tracking injected via OnRetry.
	retryCfg := p.retry
	userOnRetry := retryCfg.OnRetry
	var retried bool // set true if any retry was attempted
	retryCfg.OnRetry = func(attempt int, err error, delay time.Duration) {
		retried = true
		p.stats.recordRetry(delay)
		if userOnRetry != nil {
			userOnRetry(attempt, err, delay)
		}
	}

	err = retryWithBackoff(ctx, retryCfg, func() error {
		// If we already received data in a previous attempt, do NOT retry
		// — return the partial result with the error.
		if gotData {
			return &NonRetryableError{
				msg: "stream failed after partial content received; partial result returned",
			}
		}

		// Get the current active key (may have been rotated by a previous attempt)
		activeKey := p.currentKey()

		req, reqErr := http.NewRequestWithContext(ctx, "POST",
			p.config.BaseURL+"/chat/completions", bytes.NewReader(body))
		if reqErr != nil {
			return fmt.Errorf("create request: %w", reqErr)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+activeKey)

		resp, doErr := p.client.Do(req)
		if doErr != nil {
			return fmt.Errorf("http request: %w", doErr)
		}
		// NOTE: do not defer close here; we need the body open for scanning.
		// We'll close explicitly at the end.

		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			statusCode := resp.StatusCode
			errMsg := formatAPIError(statusCode, respBody)

			// Check if this is an auth error that warrants key rotation
			if isAuthError(fmt.Errorf("%d", statusCode)) || isAuthError(fmt.Errorf("%s", errMsg)) {
				lastAuthErr = fmt.Errorf("%s", errMsg)
				if p.NumKeys() > 1 {
					// Rotate to next key and return a retryable error so the
					// retry loop tries again with the new key.
					// Wrap in RetryDelayError so isRetryableError returns true.
					p.rotateKey()
					rotIdx := p.keyIdx.Load() % int64(p.NumKeys())
					return NewRetryDelayError(
						fmt.Errorf("rotated to key %d: %s", rotIdx, errMsg), 100*time.Millisecond)
				}
				return &NonRetryableError{msg: errMsg}
			}

			if isRetryableHTTPError(statusCode) {
				if retryAfter := parseRetryAfter(resp); retryAfter > 0 {
					return NewRetryDelayError(
						fmt.Errorf("%s (retryable)", errMsg), retryAfter)
				}
				return fmt.Errorf("%s (retryable)", errMsg)
			}

			// Special case: 400 with "upstream" in the body is transient
			// Do NOT rotate the API key here — all keys route to the same upstream.
			if statusCode == 400 && isUpstreamError(respBody) {
				if retryAfter := parseRetryAfter(resp); retryAfter > 0 {
					return NewRetryDelayError(
						fmt.Errorf("%s (transient upstream)", errMsg), retryAfter)
				}
				return fmt.Errorf("%s (transient upstream — will retry)", errMsg)
			}

			return &NonRetryableError{msg: errMsg}
		}

		var full strings.Builder
		var fullReasoning strings.Builder
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		pending := map[int]*pendingCall{}
		var order []int

		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}

			if !strings.HasPrefix(line, "data: ") {
				continue
			}

			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}

			var chunk struct {
				Choices []struct {
					Delta struct {
						Content          string `json:"content"`
						ReasoningContent string `json:"reasoning_content"` // DeepSeek reasoning field
						ToolCalls        []struct {
							Index    int    `json:"index"`
							ID       string `json:"id"`
							Function struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							} `json:"function"`
						} `json:"tool_calls"`
					} `json:"delta"`
				} `json:"choices"`
			}

			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}

			if len(chunk.Choices) > 0 {
				delta := chunk.Choices[0].Delta
				if delta.Content != "" {
					full.WriteString(delta.Content)
					gotData = true
					if onChunk != nil {
						onChunk(delta.Content)
					}
				}
				if delta.ReasoningContent != "" {
					fullReasoning.WriteString(delta.ReasoningContent)
				}
				for _, tc := range delta.ToolCalls {
					pc, ok := pending[tc.Index]
					if !ok {
						pc = &pendingCall{}
						pending[tc.Index] = pc
						order = append(order, tc.Index)
					}
					if tc.ID != "" {
						pc.id = tc.ID
					}
					if tc.Function.Name != "" {
						pc.name = tc.Function.Name
					}
					pc.args += tc.Function.Arguments
				}
			}
		}

		// Close body after scanning.
		_ = resp.Body.Close()

		scanErr := scanner.Err()
		if scanErr != nil {
			// If we got some content, save it and return the error so the
			// retry loop will see gotData=true and stop.
			if full.Len() > 0 {
				finalContent = full.String()
				finalReasoning = fullReasoning.String()
				finalCalls = assembleToolCalls(pending, order)
			}
			return fmt.Errorf("stream read error: %w", scanErr)
		}

		finalContent = full.String()
		finalReasoning = fullReasoning.String()
		finalCalls = assembleToolCalls(pending, order)
		return nil
	})

	if err != nil {
		// If we got partial content from a failed stream, return it.
		if finalContent != "" || len(finalCalls) > 0 {
			p.stats.recordCall(false, err)
			return finalContent, finalCalls, err
		}
		// If we exhausted keys, wrap with a helpful message
		if p.HasRotated() {
			if lastAuthErr != nil {
				err = fmt.Errorf("all %d API keys exhausted — last error: %w", p.NumKeys(), lastAuthErr)
			} else if lastUpstreamErr != nil {
				err = fmt.Errorf("all %d API keys exhausted — upstream errors on all keys: %w", p.NumKeys(), lastUpstreamErr)
			}
		}
		p.stats.recordCall(false, err)
		return "", nil, err
	}
	p.stats.recordCall(true, nil)
	if retried {
		p.stats.recordRetrySuccess()
	}
	// Prepend reasoning to the final content so streaming callers see it
	if finalReasoning != "" {
		finalContent = "🧠 [Reasoning: " + finalReasoning + "]\n\n" + finalContent
	}
	return finalContent, finalCalls, nil
}

// friendlyDuration formats a duration in a human-friendly way.
// Examples: "5s", "2m30s", "1h15m", "6m0s".
func friendlyDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	minutes := int(d.Minutes())
	seconds := int(d.Seconds()) - minutes*60
	if minutes < 60 {
		return fmt.Sprintf("%dm%ds", minutes, seconds)
	}
	hours := minutes / 60
	minutes = minutes % 60
	return fmt.Sprintf("%dh%dm", hours, minutes)
}

// formatAPIError produces a human-friendly, screen-friendly error string from
// an HTTP error response.  The raw body is parsed and translated into an
// actionable message the user can understand without reading raw JSON.
func formatAPIError(statusCode int, body []byte) string {
	// Truncate extremely large bodies before processing.
	if len(body) > 4000 {
		body = body[:4000]
	}

	// Try to parse as JSON and extract known error structures.
	humanMsg := humanizeAPIError(statusCode, body)
	if humanMsg != "" {
		return humanMsg
	}

	// Fall back to raw body, truncated.
	raw := string(body)
	if len(raw) > 500 {
		raw = raw[:500] + "... (truncated)"
	}
	// Strip trailing whitespace so the message is compact.
	raw = strings.TrimRight(raw, "\n\r\t ")

	// Map HTTP status codes to friendly descriptions
	statusHint := httpStatusHint(statusCode)

	// If the raw body already contains newlines, return with newline separator.
	if strings.Contains(raw, "\n") {
		lines := strings.Split(raw, "\n")
		if len(lines) > 15 {
			lines = append(lines[:15], "... (truncated)")
		}
		raw = strings.Join(lines, "\n")
		return fmt.Sprintf("API error %d (%s):\n%s", statusCode, statusHint, raw)
	}
	return fmt.Sprintf("API error %d (%s): %s", statusCode, statusHint, raw)
}

// httpStatusHint returns a human-friendly description for common HTTP status codes.
func httpStatusHint(code int) string {
	switch code {
	case 400:
		return "bad request"
	case 401:
		return "unauthorized — check your API key"
	case 403:
		return "forbidden — you don't have access"
	case 404:
		return "not found — check the model name or endpoint"
	case 408:
		return "request timeout"
	case 409:
		return "conflict"
	case 422:
		return "unprocessable — check your request parameters"
	case 429:
		return "rate limited — too many requests"
	case 500:
		return "internal server error"
	case 502:
		return "bad gateway — upstream service error"
	case 503:
		return "service unavailable"
	case 504:
		return "gateway timeout"
	default:
		return fmt.Sprintf("HTTP %d", code)
	}
}

// humanizeAPIError attempts to parse a JSON API error body and produce a
// human-readable message. It handles common error formats from OpenAI,
// OpenCode, DeepSeek, and other OpenAI-compatible providers.
// Returns "" if the body cannot be parsed into a known format.
func humanizeAPIError(statusCode int, body []byte) string {
	if !json.Valid(body) {
		return ""
	}

	// Try OpenAI-style error: { "error": { "message": "...", "type": "...", "code": "..." } }
	var openAIErr struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
			Type    string `json:"type"`
			Param   string `json:"param"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &openAIErr); err == nil && openAIErr.Error.Message != "" {
		return humanizeOpenAIError(statusCode, openAIErr.Error)
	}

	// Try simple error: { "message": "...", "code": "..." }
	var simpleErr struct {
		Message string `json:"message"`
		Code    string `json:"code"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(body, &simpleErr); err == nil {
		msg := simpleErr.Message
		if msg == "" {
			msg = simpleErr.Error
		}
		if msg != "" {
			return humanizeSimpleError(statusCode, msg, simpleErr.Code)
		}
	}

	// Try error array: { "errors": [...] }
	var errList struct {
		Errors []struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &errList); err == nil && len(errList.Errors) > 0 {
		var parts []string
		for _, e := range errList.Errors {
			if e.Message != "" {
				parts = append(parts, e.Message)
			} else {
				parts = append(parts, fmt.Sprintf("error code: %s", e.Code))
			}
		}
		statusHint := httpStatusHint(statusCode)
		return fmt.Sprintf("API error %d (%s):\n  • %s", statusCode, statusHint, strings.Join(parts, "\n  • "))
	}

	return ""
}

// humanizeOpenAIError translates an OpenAI-style error structure into a
// user-friendly message with an explanation and actionable suggestion.
func humanizeOpenAIError(statusCode int, err struct {
	Message string `json:"message"`
	Code    string `json:"code"`
	Type    string `json:"type"`
	Param   string `json:"param"`
}) string {
	msg := err.Message
	code := err.Code
	errType := err.Type

	// Build a friendly message based on the error code/type
	var friendlyMsg string
	var suggestion string

	switch {
	// Authentication errors
	case strings.Contains(msg, "API key") || strings.Contains(msg, "api_key") || strings.Contains(msg, "authentication") || code == "invalid_api_key" || strings.Contains(errType, "authentication"):
		friendlyMsg = "Invalid or missing API key"
		suggestion = "Check your API key in the config (eling-setup) and make sure it's correct."

	// Rate limiting
	case statusCode == 429 || code == "rate_limit_exceeded" || strings.Contains(errType, "rate_limit") || strings.Contains(msg, "rate limit"):
		friendlyMsg = "Rate limit exceeded"
		suggestion = "You're sending too many requests. Wait a moment and try again, or use a provider with higher rate limits."

	// Insufficient quota
	case code == "insufficient_quota" || strings.Contains(msg, "quota"):
		friendlyMsg = "Insufficient quota"
		suggestion = "Your API plan has run out of credits or tokens. Top up your account or switch to a different provider."

	// Model not found / invalid model
	case statusCode == 404 || code == "model_not_found" || strings.Contains(msg, "model") || strings.Contains(msg, "not found"):
		friendlyMsg = fmt.Sprintf("Model not found or unavailable")
		suggestion = "Check the model name in your config. It may have been deprecated or renamed."

	// Context length exceeded
	case code == "context_length_exceeded" || strings.Contains(msg, "context length") || strings.Contains(msg, "max_tokens") || strings.Contains(msg, "token limit") || strings.Contains(msg, "maximum context"):
		friendlyMsg = "Context length exceeded — the conversation is too long"
		suggestion = "Start a new conversation with /clear or reduce the prompt size. You can also increase MaxContext in settings."

	// Invalid request
	case statusCode == 400 && (strings.Contains(errType, "invalid_request") || code == "invalid_request_error"):
		// This is the one the user encountered: "Upstream request failed"
		if strings.Contains(msg, "Upstream request failed") || strings.Contains(msg, "upstream") {
			friendlyMsg = "Upstream provider temporarily unavailable"
			suggestion = "The provider's backend service is having a transient issue. Try again in a moment, or switch to a different model/provider."
		} else {
			friendlyMsg = "Invalid request"
			suggestion = "Check your request parameters. The model may not support the requested operation."
		}

	// Server errors
	case statusCode >= 500:
		friendlyMsg = "Provider server error"
		suggestion = "The provider's servers are experiencing issues. Try again later or switch providers."

	// Context overflow (400 with specific messages)
	case strings.Contains(msg, "maximum context") || strings.Contains(errType, "context_length"):
		friendlyMsg = "Context length exceeded"
		suggestion = "The conversation is too long. Start a fresh session with /clear or reduce prompt size."

	default:
		// Generic: extract the actionable part of the message
		friendlyMsg = msg
		// If the message is very technical, add a suggestion
		if strings.Contains(errType, "invalid_request_error") {
			suggestion = "This is usually a temporary issue on the provider side. Try again or use a different model."
		}
	}

	// Build the human-friendly output
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("❌ %s", friendlyMsg))
	if suggestion != "" {
		sb.WriteString(fmt.Sprintf("\n\n💡 %s", suggestion))
	}
	// Include the raw error code/type as a compact reference (not the full JSON)
	sb.WriteString(fmt.Sprintf("\n\n  Details: %s", msg))
	return sb.String()
}

// humanizeSimpleError translates a simple { message, code } error into a
// user-friendly message.
func humanizeSimpleError(statusCode int, msg, code string) string {
	statusHint := httpStatusHint(statusCode)
	return fmt.Sprintf("❌ API error %d (%s): %s", statusCode, statusHint, msg)
}

// assembleToolCalls converts pending tool-call fragments into a slice of
// ToolCall, maintaining insertion order.
func assembleToolCalls(pending map[int]*pendingCall, order []int) []ToolCall {
	toolCalls := make([]ToolCall, 0, len(order))
	for _, idx := range order {
		pc := pending[idx]
		tc := ToolCall{ID: pc.id, Type: "function"}
		tc.Function.Name = pc.name
		tc.Function.Arguments = pc.args
		toolCalls = append(toolCalls, tc)
	}
	return toolCalls
}

// pendingCall stores partial tool-call data streamed across multiple SSE
// chunks. Providers send tool call metadata (id, name, arguments) in
// separate chunks that must be assembled.
type pendingCall struct {
	id, name, args string
}

// Manager manages multiple LLM providers (like jcode's multi-provider system).
type Manager struct {
	mu        sync.RWMutex
	providers map[string]*Provider
	defaultPr string
}

// NewManager creates a new provider manager.
func NewManager() *Manager {
	return &Manager{
		providers: make(map[string]*Provider),
	}
}

// AddProvider registers a provider.
func (m *Manager) AddProvider(name string, p *Provider) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.providers[name] = p
	if m.defaultPr == "" {
		m.defaultPr = name
	}
}

// SetDefault sets the default provider.
func (m *Manager) SetDefault(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.providers[name]; !ok {
		return fmt.Errorf("provider %q not found", name)
	}
	m.defaultPr = name
	return nil
}

// Get returns a provider by name.
func (m *Manager) Get(name string) (*Provider, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.providers[name]
	return p, ok
}

// GetDefault returns the default provider.
func (m *Manager) GetDefault() *Provider {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.providers[m.defaultPr]
}

// List returns all provider names.
func (m *Manager) List() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.providers))
	for n := range m.providers {
		names = append(names, n)
	}
	return names
}

// GetAllStats returns retry statistics for all registered providers.
// The returned map is keyed by provider name.
func (m *Manager) GetAllStats() map[string]RetryStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	stats := make(map[string]RetryStats, len(m.providers))
	for name, p := range m.providers {
		stats[name] = p.GetRetryStats()
	}
	return stats
}
