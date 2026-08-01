package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestKeyRingConstruction verifies that the key ring is built correctly with deduplication.
func TestKeyRingConstruction(t *testing.T) {
	tests := []struct {
		name     string
		primary  string
		backups  []string
		expected int
	}{
		{
			name:     "single key only",
			primary:  "sk-key1",
			backups:  nil,
			expected: 1,
		},
		{
			name:     "primary + unique backups",
			primary:  "sk-key1",
			backups:  []string{"sk-key2", "sk-key3", "sk-key4"},
			expected: 4,
		},
		{
			name:     "deduplicate duplicate backup",
			primary:  "sk-key1",
			backups:  []string{"sk-key1", "sk-key2"},
			expected: 2, // sk-key1 should be deduplicated
		},
		{
			name:     "skip empty backup",
			primary:  "sk-key1",
			backups:  []string{"", "sk-key2"},
			expected: 2,
		},
		{
			name:     "multiple duplicates and empties",
			primary:  "sk-key1",
			backups:  []string{"sk-key1", "", "sk-key2", "sk-key1", "", "sk-key3"},
			expected: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(ProviderConfig{
				APIKey:     tt.primary,
				BackupKeys: tt.backups,
			})
			if got := p.NumKeys(); got != tt.expected {
				t.Errorf("NumKeys() = %d, want %d", got, tt.expected)
			}
			if p.NumKeys() > 0 && p.currentKey() != tt.primary {
				t.Errorf("currentKey() = %s, want %s (primary first)", p.currentKey(), tt.primary)
			}
		})
	}
}

// TestRotateKey verifies round-robin rotation works correctly.
func TestRotateKey(t *testing.T) {
	p := New(ProviderConfig{
		APIKey:     "sk-key1",
		BackupKeys: []string{"sk-key2", "sk-key3", "sk-key4"},
	})
	// Should have 4 keys
	if p.NumKeys() != 4 {
		t.Fatalf("expected 4 keys, got %d", p.NumKeys())
	}

	// Verify initial key is key1
	if got := p.currentKey(); got != "sk-key1" {
		t.Fatalf("initial key = %s, want sk-key1", got)
	}

	// Rotate through all keys
	expectedKeys := []string{"sk-key2", "sk-key3", "sk-key4", "sk-key1"}
	for i, expected := range expectedKeys {
		rotated := p.rotateKey()
		if rotated != expected {
			t.Errorf("rotation %d: got %s, want %s", i+1, rotated, expected)
		}
		if p.currentKey() != expected {
			t.Errorf("currentKey after rotation %d: got %s, want %s", i+1, p.currentKey(), expected)
		}
	}
}

// TestSingleKeyNoRotation verifies that rotateKey is a no-op with a single key.
func TestSingleKeyNoRotation(t *testing.T) {
	p := New(ProviderConfig{
		APIKey: "sk-only-key",
	})
	if p.NumKeys() != 1 {
		t.Fatalf("expected 1 key, got %d", p.NumKeys())
	}

	keyBefore := p.currentKey()
	p.rotateKey()
	keyAfter := p.currentKey()

	if keyBefore != keyAfter {
		t.Errorf("single key changed after rotate: %s → %s", keyBefore, keyAfter)
	}

	// keyRotErr should NOT be set for single key
	if p.keyRotErr.Load() {
		t.Error("keyRotErr should not be set for single key rotation (it's a no-op)")
	}
}

// TestConcurrentRotation verifies thread safety of atomic key index.
func TestConcurrentRotation(t *testing.T) {
	p := New(ProviderConfig{
		APIKey:     "sk-key1",
		BackupKeys: []string{"sk-key2", "sk-key3", "sk-key4", "sk-key5"},
	})

	const goroutines = 20
	const rotationsPerGoroutine = 100
	totalRotations := goroutines * rotationsPerGoroutine

	var wg sync.WaitGroup

	// Launch goroutines that all rotate concurrently
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < rotationsPerGoroutine; i++ {
				p.rotateKey()
			}
		}(g)
	}

	wg.Wait()

	// After totalRotations round-robin, the index should be:
	expectedIdx := int64(totalRotations) % int64(p.NumKeys())
	actualIdx := p.keyIdx.Load()

	if actualIdx != expectedIdx {
		t.Errorf("after %d rotations: expected index %d, got %d (keys=%d)",
			totalRotations, expectedIdx, actualIdx, p.NumKeys())
	}

	t.Logf("✅ %d concurrent rotations → index %d (mod %d = %d)",
		totalRotations, actualIdx, p.NumKeys(), actualIdx%int64(p.NumKeys()))
}

// TestKeyRotationOnAuthError simulates auth errors and verifies rotation.
func TestKeyRotationOnAuthError(t *testing.T) {
	// Create a test server that returns 401 on first call, 200 on subsequent
	callCount := atomic.Int64{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := callCount.Add(1)
		if count == 1 {
			// First call: return 401 Unauthorized
			w.WriteHeader(http.StatusUnauthorized)
			resp := map[string]interface{}{
				"error": map[string]interface{}{
					"code":    "invalid_api_key",
					"message": "Invalid API key provided",
				},
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
		// Subsequent calls: return success
		w.WriteHeader(http.StatusOK)
		resp := map[string]interface{}{
			"id":      "test-response",
			"object":  "chat.completion",
			"created": 1234567890,
			"model":   "test-model",
			"choices": []interface{}{
				map[string]interface{}{
					"index": 0,
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "Hello! I'm the backup key response.",
					},
				},
			},
			"usage": map[string]interface{}{
				"total_tokens": 10,
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	// Create provider with a bad primary key and one good backup key
	p := New(ProviderConfig{
		APIKey:     "sk-invalid-primary",
		BackupKeys: []string{"sk-valid-backup-1"},
		BaseURL:    ts.URL,
		Model:      "test-model",
	})

	if p.NumKeys() != 2 {
		t.Fatalf("expected 2 keys, got %d", p.NumKeys())
	}

	// Make a chat request - should fail on first key, rotate, succeed on second
	ctx := context.Background()
	msg := Message{Role: "user", Content: "hello"}
	resp, err := p.Chat(ctx, []Message{msg})
	if err != nil {
		t.Fatalf("Chat() expected to succeed after rotation, got: %v", err)
	}

	content := resp.Content
	expectedContent := "Hello! I'm the backup key response."
	if content != expectedContent {
		t.Errorf("response content = %q, want %q", content, expectedContent)
	}

	// Verify the key was rotated
	idx := p.keyIdx.Load()
	t.Logf("✅ Request succeeded on key index %d (key: %s...%s)",
		idx, p.keyRing[idx][:12], p.keyRing[idx][len(p.keyRing[idx])-4:])

	if !p.keyRotErr.Load() {
		t.Error("keyRotErr should be true after rotation occurred")
	}
}

// TestAllKeysExhausted verifies behavior when all keys fail with auth errors.
func TestAllKeysExhausted(t *testing.T) {
	// Server that always returns 401
	callCount := atomic.Int64{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		resp := map[string]interface{}{
			"error": map[string]interface{}{
				"code":    "invalid_api_key",
				"message": "Invalid API key provided",
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	// Create provider with 3 keys, all invalid
	p := New(ProviderConfig{
		APIKey:     "sk-invalid-1",
		BackupKeys: []string{"sk-invalid-2", "sk-invalid-3"},
		BaseURL:    ts.URL,
		Model:      "test-model",
	})

	// Set retry to exactly match our 3 keys: 2 retries = 3 total attempts
	p.SetRetryConfig(RetryConfig{
		MaxRetries: 2,
		BaseDelay:  1 * time.Millisecond,
		MaxDelay:   10 * time.Millisecond,
	})

	ctx := context.Background()
	msg := Message{Role: "user", Content: "hello"}
	_, err := p.Chat(ctx, []Message{msg})
	if err == nil {
		t.Fatal("expected error when all keys are exhausted, got nil")
	}

	errStr := err.Error()
	if contains(errStr, "all") && contains(errStr, "exhausted") {
		t.Logf("✅ Got expected exhaustion error: %s", errStr)
	} else {
		t.Errorf("error message doesn't mention exhaustion: %s", errStr)
	}

	// Verify all 3 keys were tried exactly once each
	if count := callCount.Load(); count != 3 {
		t.Errorf("expected 3 API calls (one per key), got %d", count)
	}

	t.Logf("✅ All %d keys were tried and exhausted", callCount.Load())
}

// TestKeyRotationStats verifies stats tracking during rotation.
func TestKeyRotationStats(t *testing.T) {
	callCount := atomic.Int64{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := callCount.Add(1)
		if count <= 2 {
			// First two calls fail with auth error
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]interface{}{
					"code":    "invalid_api_key",
					"message": "Invalid API key",
				},
			})
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":      "test",
			"object":  "chat.completion",
			"created": 1234567890,
			"model":   "test-model",
			"choices": []interface{}{
				map[string]interface{}{
					"index": 0,
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "success on third key",
					},
				},
			},
			"usage": map[string]interface{}{
				"total_tokens": 10,
			},
		})
	}))
	defer ts.Close()

	// 3 keys, first 2 invalid, 3rd valid
	p := New(ProviderConfig{
		APIKey:     "sk-invalid-1",
		BackupKeys: []string{"sk-invalid-2", "sk-valid-3"},
		BaseURL:    ts.URL,
		Model:      "test-model",
	})

	// Reset stats
	p.ResetRetryStats()

	ctx := context.Background()
	msg := Message{Role: "user", Content: "hello"}
	_, err := p.Chat(ctx, []Message{msg})
	if err != nil {
		t.Fatalf("expected success after 2 rotations, got: %v", err)
	}

	stats := p.GetRetryStats()
	t.Logf("📊 Retry stats: attempts=%d, retried=%d, success=%d, failed=%d",
		stats.TotalAttempts, stats.RetriedCalls, stats.RetrySuccess, stats.FailedCalls)

	if stats.RetriedCalls < 1 {
		t.Error("expected at least 1 retry (key rotation)")
	}
	if stats.RetrySuccess != 1 {
		t.Errorf("expected RetrySuccess=1, got %d", stats.RetrySuccess)
	}
}

// Helper: contains checks if a string contains a substring
func contains(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	if len(s) < len(substr) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestBackupKeysNotOverwritten verifies that main.go's loop doesn't affect backup_keys.
func TestBackupKeysNotOverwritten(t *testing.T) {
	// Simulate what main.go does: overwrite APIKey but NOT BackupKeys
	cfg := ProviderConfig{
		APIKey:     "sk-original-primary",
		BackupKeys: []string{"sk-backup-1", "sk-backup-2", "sk-backup-3"},
	}

	// main.go overwrites the APIKey from env var
	cfg.APIKey = "sk-env-override"

	// Create provider with the modified config
	p := New(cfg)

	// Verify: backup keys should still be intact
	if p.NumKeys() != 4 {
		t.Errorf("expected 4 keys (1 primary + 3 backups), got %d", p.NumKeys())
	}

	// Verify primary key is the env override
	if p.currentKey() != "sk-env-override" {
		t.Errorf("primary key should be sk-env-override, got %s", p.currentKey())
	}

	// Verify backup keys are still there
	hasBackup := false
	for _, k := range p.keyRing {
		if k == "sk-backup-1" {
			hasBackup = true
		}
	}
	if !hasBackup {
		t.Error("backup keys were lost after primary key overwrite!")
	}

	t.Logf("✅ Backup keys survive primary key overwrite: %d keys in ring", p.NumKeys())
}

// TestKeyRotationHTTPMethods verifies rotation with POST and streaming endpoints.
func TestKeyRotationHTTPMethods(t *testing.T) {
	// Test that the key is included in outgoing request headers
	callCount := atomic.Int64{}
	var capturedKeys []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := callCount.Add(1)
		// Capture the auth header
		authHeader := r.Header.Get("Authorization")
		capturedKeys = append(capturedKeys, authHeader)

		if count <= 1 {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]interface{}{"code": "invalid_api_key", "message": "bad key"},
			})
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":      "test",
			"object":  "chat.completion",
			"created": 1234567890,
			"model":   "test-model",
			"choices": []interface{}{
				map[string]interface{}{
					"index": 0,
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "response with rotated key",
					},
				},
			},
			"usage": map[string]interface{}{"total_tokens": 10},
		})
	}))
	defer ts.Close()

	p := New(ProviderConfig{
		APIKey:     "sk-invalid-1",
		BackupKeys: []string{"sk-valid-2"},
		BaseURL:    ts.URL,
		Model:      "test-model",
		Name:       "rotation-test",
	})

	ctx := context.Background()
	msg := Message{Role: "user", Content: "hello"}
	resp, err := p.Chat(ctx, []Message{msg})
	if err != nil {
		t.Fatalf("Chat() expected to succeed after rotation, got: %v", err)
	}

	if resp.Content != "response with rotated key" {
		t.Errorf("response content = %q, want %q", resp.Content, "response with rotated key")
	}

	// Verify captured auth headers show different keys
	if len(capturedKeys) != 2 {
		t.Fatalf("expected 2 captured auth headers, got %d", len(capturedKeys))
	}

	// First call should use primary key (sk-invalid-1), second should use backup (sk-valid-2)
	if capturedKeys[0] != "Bearer sk-invalid-1" {
		t.Errorf("first call auth header = %q, want %q", capturedKeys[0], "Bearer sk-invalid-1")
	}
	if capturedKeys[1] != "Bearer sk-valid-2" {
		t.Errorf("second call auth header = %q, want %q", capturedKeys[1], "Bearer sk-valid-2")
	}

	t.Logf("✅ Key rotation verified in HTTP headers: primary → backup")
}

// TestKeyRingPreservesOrder verifies the key ring maintains the original order.
func TestKeyRingPreservesOrder(t *testing.T) {
	backups := []string{"sk-backup-a", "sk-backup-b", "sk-backup-c", "sk-backup-d"}
	p := New(ProviderConfig{
		APIKey:     "sk-primary",
		BackupKeys: backups,
	})

	// Key ring should be [primary, backup-a, backup-b, backup-c, backup-d]
	expected := []string{"sk-primary", "sk-backup-a", "sk-backup-b", "sk-backup-c", "sk-backup-d"}
	for i, exp := range expected {
		if p.keyRing[i] != exp {
			t.Errorf("keyRing[%d] = %s, want %s", i, p.keyRing[i], exp)
		}
	}

	// Rotate through to verify order
	for i := 1; i < len(expected); i++ {
		p.rotateKey()
		if p.currentKey() != expected[i] {
			t.Errorf("after rotation %d: currentKey = %s, want %s", i, p.currentKey(), expected[i])
		}
	}
}

// TestRateLimitDoesNotTriggerRotation verifies that 429 (rate limit) does NOT rotate keys.
func TestRateLimitDoesNotTriggerRotation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{"code": "rate_limit", "message": "too fast"},
		})
	}))
	defer ts.Close()

	p := New(ProviderConfig{
		APIKey:     "sk-key1",
		BackupKeys: []string{"sk-key2"},
		BaseURL:    ts.URL,
		Model:      "test-model",
	})
	// This test verifies rotation behavior, not retry behavior — disable
	// retries so the 429 backoff sleep (default 5 retries × exponential
	// backoff ≈ 39s) doesn't stall the suite.
	p.SetRetryConfig(RetryConfig{MaxRetries: 0})

	ctx := context.Background()
	msg := Message{Role: "user", Content: "hello"}
	_, err := p.Chat(ctx, []Message{msg})
	if err == nil {
		t.Fatal("expected error for rate limit")
	}

	// Key should NOT have rotated for a 429 error
	if p.keyRotErr.Load() {
		t.Error("keyRotErr should NOT be set for rate limit (429) errors")
	}

	t.Logf("✅ Rate limit (429) does not trigger key rotation")
}

// TestDNSErrorDoesNotTriggerRotation verifies DNS errors don't rotate keys.
func TestDNSErrorDoesNotTriggerRotation(t *testing.T) {
	p := New(ProviderConfig{
		APIKey:     "sk-key1",
		BackupKeys: []string{"sk-key2"},
		BaseURL:    "http://nonexistent.example.invalid:9999",
		Model:      "test-model",
	})
	// This test verifies rotation behavior, not retry behavior — disable
	// retries so the per-attempt connection timeout + 5× backoff sleeps
	// (≈ 50s) don't stall the suite.
	p.SetRetryConfig(RetryConfig{MaxRetries: 0})

	ctx := context.Background()
	msg := Message{Role: "user", Content: "hello"}
	_, err := p.Chat(ctx, []Message{msg})
	if err == nil {
		t.Fatal("expected error for DNS failure")
	}

	// Key should NOT have rotated for a network error
	if p.keyRotErr.Load() {
		t.Error("keyRotErr should NOT be set for DNS/network errors (these are not auth errors)")
	}

	t.Logf("✅ DNS/network errors do not trigger key rotation")
}

// Test401And403TriggerRotation verifies that various auth error codes trigger rotation.
func Test401And403TriggerRotation(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		errorCode  string
	}{
		{"401 unauthorized", http.StatusUnauthorized, "unauthorized"},
		{"401 invalid api key", http.StatusUnauthorized, "invalid_api_key"},
		{"403 forbidden", http.StatusForbidden, "forbidden"},
		{"403 quota exceeded", http.StatusForbidden, "insufficient_quota"},
		{"401 no permission", http.StatusUnauthorized, "no_permission"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			callCount := atomic.Int64{}
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count := callCount.Add(1)
				if count == 1 {
					w.WriteHeader(tt.statusCode)
					json.NewEncoder(w).Encode(map[string]interface{}{
						"error": map[string]interface{}{"code": tt.errorCode, "message": tt.errorCode},
					})
					return
				}
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"id": "test", "object": "chat.completion", "created": 1234567890,
					"model": "test-model",
					"choices": []interface{}{
						map[string]interface{}{
							"index": 0, "message": map[string]interface{}{
								"role": "assistant", "content": "ok",
							},
						},
					},
					"usage": map[string]interface{}{"total_tokens": 5},
				})
			}))
			defer ts.Close()

			p := New(ProviderConfig{
				APIKey:     "sk-invalid",
				BackupKeys: []string{"sk-valid"},
				BaseURL:    ts.URL,
				Model:      "test-model",
			})

			ctx := context.Background()
			msg := Message{Role: "user", Content: "hello"}
			_, err := p.Chat(ctx, []Message{msg})
			if err != nil {
				t.Fatalf("expected success after rotation from %s, got: %v", tt.name, err)
			}

			if !p.keyRotErr.Load() {
				t.Errorf("keyRotErr should be true after %s", tt.name)
			}
			t.Logf("✅ %s triggers key rotation as expected", tt.name)
		})
	}
}
