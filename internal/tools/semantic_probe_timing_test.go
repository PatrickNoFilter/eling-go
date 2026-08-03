package tools

import (
	"sync/atomic"
	"testing"
	"time"
)

// TestRealProviderProbeTiming is a throwaway timing check against the actual
// configured gateway (opencode.ai/zen/v1) using a dummy key. It verifies the
// endpoint-dead detection triggers after ~1 probe (~1s) instead of the old
// ~8s chain, and that subsequent turns are instant.
func TestRealProviderProbeTiming(t *testing.T) {
	if testing.Short() {
		t.Skip("skip in short mode")
	}
	resetEmbeddingProbeState()
	t.Setenv("ELING_API_KEY", "dummy-key-for-timing")
	t.Setenv("ELING_BASE_URL", "https://opencode.ai/zen/v1")
	t.Setenv("ELING_EMBEDDING_MODEL", "deepseek-v4-flash-free")
	t.Setenv("ELING_EMBED_PROBE_BUDGET_MS", "5000")

	start := time.Now()
	vec, err := getEmbedding("hi there")
	first := time.Since(start)
	t.Logf("first embedding (probe): %v err=%v len=%d endpointDead=%d", first, err, len(vec), atomic.LoadInt32(&embedEndpointDead))

	start = time.Now()
	vec2, err2 := getEmbedding("a completely different second prompt")
	second := time.Since(start)
	t.Logf("second embedding (after dead): %v err=%v len=%d endpointDead=%d", second, err2, len(vec2), atomic.LoadInt32(&embedEndpointDead))

	if atomic.LoadInt32(&embedEndpointDead) != 1 {
		t.Fatalf("endpoint not marked dead, got %d", atomic.LoadInt32(&embedEndpointDead))
	}
	if second > 200*time.Millisecond {
		t.Fatalf("second embedding took %v, want < 200ms", second)
	}
}
