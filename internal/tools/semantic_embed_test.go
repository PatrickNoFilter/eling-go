package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// resetEmbeddingProbeState clears the package-level probe state so tests run
// in isolation.
func resetEmbeddingProbeState() {
	atomic.StoreInt32(&embedEndpointDead, 0)
	embedDeadModels.Range(func(k, _ interface{}) bool {
		embedDeadModels.Delete(k)
		return true
	})
	embedCacheMu.Lock()
	for k := range embedCache {
		delete(embedCache, k)
	}
	embedCacheKeys = embedCacheKeys[:0]
	embedCacheMu.Unlock()
	ClearSemanticIndex()
}

// TestGetEmbeddingSkipsBrokenEndpointAfterRoute404 verifies that a non-JSON
// 404 from /embeddings (a chat-only gateway) marks the endpoint dead after a
// single probe, and that later turns never touch the network again.
func TestGetEmbeddingSkipsBrokenEndpointAfterRoute404(t *testing.T) {
	resetEmbeddingProbeState()
	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<html>404 Not Found</html>"))
	}))
	defer ts.Close()

	t.Setenv("ELING_API_KEY", "test-key")
	t.Setenv("ELING_BASE_URL", ts.URL)
	t.Setenv("ELING_EMBEDDING_MODEL", "chat-model")
	t.Setenv("ELING_EMBED_PROBE_BUDGET_MS", "10000")

	// First call probes the API, gets an HTML 404, and must mark the endpoint
	// dead while still returning a usable local embedding.
	vec, err := getEmbedding("hello world")
	if err != nil {
		t.Fatalf("getEmbedding error: %v", err)
	}
	if len(vec) == 0 {
		t.Fatal("expected non-empty embedding vector")
	}
	if atomic.LoadInt32(&embedEndpointDead) != 1 {
		t.Fatal("expected endpoint to be marked dead after HTML 404")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("API calls after first probe = %d, want 1", got)
	}

	// A second (different) prompt must not hit the network at all.
	if _, err := getEmbedding("a completely different prompt"); err != nil {
		t.Fatalf("second getEmbedding error: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("API calls after dead-endpoint turn = %d, want 1", got)
	}
}

// TestGetEmbeddingSkipsDeadModelButTriesFallback verifies model-level
// failures (JSON 404) only kill that model: the fallback chain still works,
// the endpoint stays live, and the dead model is never tried again.
func TestGetEmbeddingSkipsDeadModelButTriesFallback(t *testing.T) {
	resetEmbeddingProbeState()
	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if atomic.LoadInt32(&calls) == 1 {
			// First model rejected at the model level (JSON API error).
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"model not found"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{
				{"embedding": []float64{0.1, 0.2, 0.3}},
			},
		})
	}))
	defer ts.Close()

	t.Setenv("ELING_API_KEY", "test-key")
	t.Setenv("ELING_BASE_URL", ts.URL)
	t.Setenv("ELING_EMBEDDING_MODEL", "bad-model")
	t.Setenv("ELING_EMBED_PROBE_BUDGET_MS", "10000")

	vec, err := getEmbedding("semantic query")
	if err != nil {
		t.Fatalf("getEmbedding error: %v", err)
	}
	if len(vec) == 0 {
		t.Fatal("expected non-empty embedding vector")
	}
	if atomic.LoadInt32(&embedEndpointDead) != 0 {
		t.Fatal("endpoint must stay live when a fallback model works")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("API calls = %d, want 2 (dead model + working fallback)", got)
	}

	// The dead model is cached — a second call must go straight to the
	// working fallback model.
	if _, err := getEmbedding("another semantic query"); err != nil {
		t.Fatalf("second getEmbedding error: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("API calls after cached dead model = %d, want 3", got)
	}
}

// TestSemanticSearchEmptyIndexSkipsEmbedding verifies that searching an empty
// in-memory index never computes an embedding (no API calls at all).
func TestSemanticSearchEmptyIndexSkipsEmbedding(t *testing.T) {
	resetEmbeddingProbeState()
	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.NotFound(w, r)
	}))
	defer ts.Close()

	t.Setenv("ELING_API_KEY", "test-key")
	t.Setenv("ELING_BASE_URL", ts.URL)
	t.Setenv("ELING_EMBEDDING_MODEL", "chat-model")

	results, err := semanticSearch("anything", nil, 3)
	if err != nil {
		t.Fatalf("semanticSearch error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results for empty index, got %d", len(results))
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("API calls for empty index = %d, want 0", got)
	}
}
