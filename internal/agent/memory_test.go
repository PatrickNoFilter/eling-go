package agent

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestMemoryRememberAndRecall(t *testing.T) {
	mem := NewMemory()

	// Test remembering
	mem.Remember("The sky is blue", "fact", []string{"nature", "color"})
	mem.Remember("Go is a programming language", "fact", []string{"programming", "go"})
	mem.Remember("User likes dark mode", "preference", []string{"user", "theme"})

	// Test stats
	stats := mem.Stats()
	if stats["total"] != 3 {
		t.Errorf("expected 3 memories, got %d", stats["total"])
	}

	// Test recall
	results := mem.Recall("sky")
	if len(results) != 1 {
		t.Errorf("expected 1 result for 'sky', got %d", len(results))
	}
	if results[0].Category != "fact" {
		t.Errorf("expected category 'fact', got '%s'", results[0].Category)
	}

	// Test recall by tag
	results = mem.Recall("dark")
	if len(results) != 1 {
		t.Errorf("expected 1 result for 'dark', got %d", len(results))
	}
}

func TestMemoryRecent(t *testing.T) {
	mem := NewMemory()
	mem.Remember("first", "test", nil)
	mem.Remember("second", "test", nil)
	mem.Remember("third", "test", nil)

	recent := mem.Recent(2)
	if len(recent) != 2 {
		t.Errorf("expected 2 recent items, got %d", len(recent))
	}

	if recent[0].Content != "second" || recent[1].Content != "third" {
		t.Errorf("unexpected order of recent items: %v", recent)
	}
}

func TestMemoryForgettingPreservesOrder(t *testing.T) {
	mem := NewMemory()
	// Set very small limits to force consolidation and forgetting
	mem.MaxShort = 10
	mem.MaxLong = 20

	// Add items in order with known content
	for i := 0; i < 30; i++ {
		mem.Remember(fmt.Sprintf("order_%03d", i), "test", nil)
	}

	// Weaken the middle items so they get dropped by forgetWeakest
	mem.mu.Lock()
	// Manually weaken items 10-15 (the weakest batch)
	for i := 10; i < 16 && i < len(mem.Items); i++ {
		mem.Items[i].Strength = 0.01
	}
	mem.mu.Unlock()

	// Trigger forgetting by adding more items to overflow MaxLong
	for i := 0; i < 20; i++ {
		mem.Remember("overflow", "test", nil)
	}

	// Recent should still return items in chronological order
	recent := mem.Recent(10)
	for i := 1; i < len(recent); i++ {
		if recent[i].CreatedAt.Before(recent[i-1].CreatedAt) {
			t.Errorf("Recent items not in chronological order at index %d: %v before %v",
				i, recent[i].CreatedAt, recent[i-1].CreatedAt)
		}
	}

	// Total should not be empty
	stats := mem.Stats()
	t.Logf("Memory stats after forced forgetting: short=%d, long=%d, total=%d",
		stats["short_term"], stats["long_term"], stats["total"])
	if stats["total"] == 0 {
		t.Error("memory should not be empty after forgetting")
	}
}

func TestMemoryForgetting(t *testing.T) {
	mem := NewMemory()
	// Fill with items
	for i := 0; i < 100; i++ {
		mem.Remember("item", "test", nil)
	}
	// Now add more to trigger consolidation and forgetting
	for i := 0; i < 50; i++ {
		mem.Remember("new", "test", nil)
	}
	// Should not crash or get stuck
	stats := mem.Stats()
	t.Logf("Memory stats: short=%d, long=%d, total=%d", stats["short_term"], stats["long_term"], stats["total"])
	if stats["total"] == 0 {
		t.Error("memory should not be empty")
	}
}

func TestMemoryEmptyRecall(t *testing.T) {
	mem := NewMemory()
	results := mem.Recall("nothing")
	if len(results) != 0 {
		t.Errorf("expected 0 results for empty memory, got %d", len(results))
	}
}

func TestMemoryJSONSerialization(t *testing.T) {
	// Create memory with items
	mem := NewMemory()
	mem.Remember("test item 1", "fact", []string{"test"})
	mem.Remember("test item 2", "preference", []string{"test", "example"})
	mem.Remember("test item 3", "pattern", nil)

	// Serialize
	data, err := json.Marshal(mem)
	if err != nil {
		t.Fatalf("failed to marshal memory: %v", err)
	}

	// Verify JSON contains items
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("failed to unmarshal as map: %v", err)
	}

	if _, ok := raw["items"]; !ok {
		t.Errorf("JSON missing 'items' field. Got keys: %v", raw)
	}
	if _, ok := raw["short_term"]; !ok {
		t.Errorf("JSON missing 'short_term' field")
	}

	// Deserialize into new Memory
	var restored Memory
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("failed to unmarshal memory: %v", err)
	}

	// Verify items survived round-trip
	if restored.Len() != 3 {
		t.Errorf("expected 3 items after restore, got %d", restored.Len())
	}

	// Verify content
	results := restored.Recall("test item 1")
	if len(results) != 1 {
		t.Errorf("expected 1 result for 'test item 1', got %d", len(results))
	}

	// Verify strength was preserved
	if results[0].Strength != 1.0 {
		t.Errorf("expected strength 1.0, got %f", results[0].Strength)
	}
}

func TestMemoryForgetWeakest(t *testing.T) {
	mem := NewMemory()
	mem.MaxShort = 5
	mem.MaxLong = 10

	// Fill items so some go to long-term and trigger forgetting
	for i := 0; i < 20; i++ {
		mem.Remember("item", "test", nil)
	}

	// We should have some items in short-term and long-term
	stats := mem.Stats()
	t.Logf("Memory stats: short=%d, long=%d, total=%d", stats["short_term"], stats["long_term"], stats["total"])

	// Long-term should be at most MaxLong (forgetWeakest should have kept it under)
	if stats["long_term"] > 10 {
		t.Errorf("expected long_term <= 10 after forgetWeakest, got %d", stats["long_term"])
	}

	// Total should be non-zero
	if stats["total"] == 0 {
		t.Error("memory should not be empty after forgetting")
	}
}

func TestMemoryStrengthBoostOnRecall(t *testing.T) {
	mem := NewMemory()
	mem.Remember("test item", "test", nil)

	// Item should start with strength 1.0
	mem.mu.RLock()
	if mem.ShortTerm[0].Strength != 1.0 {
		t.Errorf("expected initial strength 1.0, got %f", mem.ShortTerm[0].Strength)
	}
	mem.mu.RUnlock()

	// Recall should boost strength
	mem.Recall("test item")

	mem.mu.RLock()
	if mem.ShortTerm[0].Strength < 1.0 {
		t.Errorf("expected strength >= 1.0 after recall, got %f", mem.ShortTerm[0].Strength)
	}
	mem.mu.RUnlock()

	// Access count should increment
	mem.mu.RLock()
	if mem.ShortTerm[0].Accessed != 1 {
		t.Errorf("expected Accessed=1 after recall, got %d", mem.ShortTerm[0].Accessed)
	}
	mem.mu.RUnlock()
}
