package agent

import (
	"context"
	"fmt"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"eling/internal/logger"
	"eling/internal/tools"
)

// MemoryItem represents a single unit of stored memory.
type MemoryItem struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	Category  string    `json:"category"` // fact, preference, skill, pattern
	Tags      []string  `json:"tags"`
	CreatedAt time.Time `json:"created_at"`
	Accessed  int       `json:"accessed"`
	Strength  float64   `json:"strength"` // 0.0-1.0, decays over time, reinforced by use
}

// Memory manages short-term and long-term memory for the agent.
type Memory struct {
	mu          sync.RWMutex
	Items       []MemoryItem       `json:"items"`      // long-term storage
	ShortTerm   []MemoryItem       `json:"short_term"` // recent context window
	MaxShort    int                `json:"max_short"`
	MaxLong     int                `json:"max_long"`
	decayStop   chan struct{}      // signals decay goroutine to stop
	decayCancel context.CancelFunc // cancels decay goroutine context
}

// NewMemory creates a new Memory store.
func NewMemory() *Memory {
	return &Memory{
		Items:     make([]MemoryItem, 0),
		ShortTerm: make([]MemoryItem, 0),
		MaxShort:  50,
		MaxLong:   1000,
		decayStop: make(chan struct{}),
	}
}

// Remember stores a new memory.
func (m *Memory) Remember(content, category string, tags []string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	item := MemoryItem{
		ID:        generateID(),
		Content:   content,
		Category:  category,
		Tags:      tags,
		CreatedAt: time.Now(),
		Accessed:  0,
		Strength:  1.0,
	}

	// Add to short-term
	m.ShortTerm = append(m.ShortTerm, item)
	if len(m.ShortTerm) > m.MaxShort {
		// Consolidate to long-term — deep-copy so ShortTerm and Items
		// never share a backing array (prevents data races via aliasing).
		overflow := len(m.ShortTerm) - m.MaxShort
		consolidated := make([]MemoryItem, overflow)
		copy(consolidated, m.ShortTerm[:overflow])
		m.Items = append(m.Items, consolidated...)
		m.ShortTerm = append([]MemoryItem(nil), m.ShortTerm[overflow:]...)
		if len(m.Items) > m.MaxLong {
			// Forget oldest weakest memories
			m.forgetWeakest()
		}
	}
}

// Recall searches memory by content prefix or tags.
func (m *Memory) Recall(query string) []MemoryItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	var results []MemoryItem
	var hitItems, hitShort []int // indices of matched items

	for i := range m.Items {
		if contains(m.Items[i].Content, query) || hasMatchingTag(m.Items[i].Tags, query) {
			results = append(results, m.Items[i])
			hitItems = append(hitItems, i)
		}
	}
	for i := range m.ShortTerm {
		if contains(m.ShortTerm[i].Content, query) || hasMatchingTag(m.ShortTerm[i].Tags, query) {
			results = append(results, m.ShortTerm[i])
			hitShort = append(hitShort, i)
		}
	}

	// Apply strength boosts under the same lock
	for _, idx := range hitItems {
		if idx < len(m.Items) {
			m.Items[idx].Accessed++
			m.Items[idx].Strength = min(1.0, m.Items[idx].Strength+0.1)
		}
	}
	for _, idx := range hitShort {
		if idx < len(m.ShortTerm) {
			m.ShortTerm[idx].Accessed++
			m.ShortTerm[idx].Strength = min(1.0, m.ShortTerm[idx].Strength+0.1)
		}
	}

	return results
}

// Recent returns the most recent N memory items.
// Each item is deep-copied into a new slice so the caller holds no
// reference to the memory's internal backing arrays.
func (m *Memory) Recent(n int) []MemoryItem {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if n <= 0 {
		return nil
	}

	// Build a fresh concatenated slice so we never write into the
	// spare capacity of m.Items' backing array (which would race
	// with concurrent write-holders).
	total := len(m.Items) + len(m.ShortTerm)
	all := make([]MemoryItem, total)
	copy(all, m.Items)
	copy(all[len(m.Items):], m.ShortTerm)

	if total <= n {
		// all is already a fresh copy — return it directly
		return all
	}
	result := make([]MemoryItem, n)
	copy(result, all[total-n:])
	return result
}

// Len returns the total number of memory items.
func (m *Memory) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.Items) + len(m.ShortTerm)
}

// Stats returns memory statistics.
func (m *Memory) Stats() map[string]int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := map[string]int{
		"short_term": len(m.ShortTerm),
		"long_term":  len(m.Items),
		"total":      len(m.Items) + len(m.ShortTerm),
	}
	return stats
}

// forgetWeakest removes the weakest long-term memories while
// preserving the original chronological (insertion) order.
func (m *Memory) forgetWeakest() {
	if len(m.Items) <= 1 {
		return
	}
	remove := len(m.Items) / 10
	if remove < 1 {
		remove = 1
	}

	// Build an index slice, sort by strength so we know which
	// indices to drop, then rebuild m.Items in original order.
	indices := make([]int, len(m.Items))
	for i := range indices {
		indices[i] = i
	}
	sort.Slice(indices, func(i, j int) bool {
		return m.Items[indices[i]].Strength < m.Items[indices[j]].Strength
	})

	// Mark the weakest 'remove' items for deletion
	kill := make(map[int]bool, remove)
	for i := 0; i < remove; i++ {
		kill[indices[i]] = true
	}

	// Rebuild in original (chronological) order, skipping killed items
	kept := make([]MemoryItem, 0, len(m.Items)-remove)
	for i := range m.Items {
		if !kill[i] {
			kept = append(kept, m.Items[i])
		}
	}
	m.Items = kept
}

// ItemsData returns all memory items (long-term + short-term) as
// a flat slice for semantic search indexing.
func (m *Memory) ItemsData() []tools.MemoryItemData {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]tools.MemoryItemData, 0, len(m.Items)+len(m.ShortTerm))
	for _, item := range m.Items {
		out = append(out, tools.MemoryItemData{
			Content:  item.Content,
			Category: item.Category,
			Tags:     item.Tags,
		})
	}
	for _, item := range m.ShortTerm {
		out = append(out, tools.MemoryItemData{
			Content:  item.Content,
			Category: item.Category,
			Tags:     item.Tags,
		})
	}
	return out
}

// StartDecay begins a background goroutine that decays memory strength
// at the given interval. Each tick, all items have their Strength reduced
// by decayRate (0.0-1.0). Items that fall below 0.1 are removed.
// Call StopDecay to halt.  This is safe to call multiple times; any
// previously-running decay goroutine is stopped before starting a new one.
func (m *Memory) StartDecay(interval time.Duration, decayRate float64) {
	m.mu.Lock()
	// Cancel any previous decay context
	if m.decayCancel != nil {
		m.decayCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.decayCancel = cancel
	stopCh := m.decayStop
	m.mu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				if gl := logger.Global(); gl != nil {
					gl.Panic(r)
				}
				logger.WriteCrashReport(r, string(debug.Stack()))
			}
		}()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.mu.Lock()
				// Check for cancellation before doing work so StopDecay
				// doesn't have to wait for a running decayOnce to finish.
				select {
				case <-ctx.Done():
					m.mu.Unlock()
					return
				default:
				}
				m.decayOnce(decayRate)
				m.mu.Unlock()
			case <-stopCh:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
}

// StopDecay stops the background decay goroutine.
// Safe to call multiple times.  Does not block waiting for an in-flight
// decay tick — the goroutine checks for cancellation on the next tick.
func (m *Memory) StopDecay() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.decayCancel != nil {
		m.decayCancel()
		m.decayCancel = nil
	}
	if m.decayStop != nil {
		// Set to nil BEFORE closing to prevent a second caller from
		// closing the same channel again (panic: close of closed channel).
		ch := m.decayStop
		m.decayStop = nil
		select {
		case <-ch:
			// Already closed
		default:
			close(ch)
		}
	}
}

// decayOnce applies one round of strength decay to all items.
// Items with Strength < 0.1 are removed.
func (m *Memory) decayOnce(rate float64) {
	// Decay long-term items
	keep := make([]MemoryItem, 0, len(m.Items))
	for i := range m.Items {
		m.Items[i].Strength -= rate
		if m.Items[i].Strength < 0 {
			m.Items[i].Strength = 0
		}
		if m.Items[i].Strength >= 0.1 {
			keep = append(keep, m.Items[i])
		}
	}
	// Zero out trailing elements for GC
	for i := len(keep); i < len(m.Items); i++ {
		m.Items[i] = MemoryItem{}
	}
	m.Items = keep

	// Decay short-term items
	keepST := make([]MemoryItem, 0, len(m.ShortTerm))
	for i := range m.ShortTerm {
		m.ShortTerm[i].Strength -= rate
		if m.ShortTerm[i].Strength < 0 {
			m.ShortTerm[i].Strength = 0
		}
		if m.ShortTerm[i].Strength >= 0.1 {
			keepST = append(keepST, m.ShortTerm[i])
		}
	}
	for i := len(keepST); i < len(m.ShortTerm); i++ {
		m.ShortTerm[i] = MemoryItem{}
	}
	m.ShortTerm = keepST
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

func hasMatchingTag(tags []string, query string) bool {
	for _, t := range tags {
		if t == query || strings.Contains(t, query) {
			return true
		}
	}
	return false
}

var idCounter atomic.Int64

func generateID() string {
	return fmt.Sprintf("mem_%d_%d", time.Now().UnixNano(), idCounter.Add(1))
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
