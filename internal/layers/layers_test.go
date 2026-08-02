package layers

import (
	"context"
	"testing"
)

// panicLayer is a Layer whose Query panics — used to prove Brain.Query
// contains layer panics instead of crashing the whole process, and that
// the collect loop doesn't deadlock waiting for the dead goroutine.
type panicLayer struct{}

func (panicLayer) Name() string                      { return "panic" }
func (panicLayer) Priority() int                     { return 1 }
func (panicLayer) Store(context.Context, Item) error { return nil }
func (panicLayer) Close() error                      { return nil }
func (panicLayer) Query(ctx context.Context, q string, limit int) ([]Result, error) {
	panic("boom: layer exploded")
}

// okLayer is a healthy layer returning one result.
type okLayer struct{}

func (okLayer) Name() string                      { return "ok" }
func (okLayer) Priority() int                     { return 2 }
func (okLayer) Store(context.Context, Item) error { return nil }
func (okLayer) Close() error                      { return nil }
func (okLayer) Query(ctx context.Context, q string, limit int) ([]Result, error) {
	return []Result{{Layer: "ok", Content: "hello", Score: 0.9}}, nil
}

// TestBrainQueryPanicRecovery verifies a panicking layer can't crash the
// process or deadlock the collect loop: healthy layers' results still come
// back fused.
func TestBrainQueryPanicRecovery(t *testing.T) {
	b := NewBrain(panicLayer{}, okLayer{})
	results, err := b.Query(context.Background(), "test", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result from healthy layer, got %d", len(results))
	}
	if results[0].Layer != "ok" || results[0].Content != "hello" {
		t.Fatalf("unexpected result: %+v", results[0])
	}
}

// TestBrainQueryAllLayersPanic verifies the collect loop completes (no
// deadlock) even when every layer panics.
func TestBrainQueryAllLayersPanic(t *testing.T) {
	b := NewBrain(panicLayer{}, panicLayer{})
	results, err := b.Query(context.Background(), "test", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results when all layers panic, got %d", len(results))
	}
}
