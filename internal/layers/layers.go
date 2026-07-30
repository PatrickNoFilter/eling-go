// Package layers implements an 8-layer memory architecture for the ELING agent.
// Adapted from the Python eling-agent by PatrickNoFilter.
// Each layer is a distinct memory store with its own persistence, query method,
// and scoring system. Layers are composed together into a unified Brain.
//
// Architecture:
//
//	📡 Layer 8: CONTINUUM   — multi-agent orchestration hub (shared continuum.db)
//	🧠 Layer 7: NOTION     — online brain, persistent, human-readable (optional)
//	📝 Layer 6: OBSIDIAN   — local Markdown vault, project notes, daily logs
//	📚 Layer 5: KB         — FTS5 knowledge corpus for long-form knowledge
//	🕸️ Layer 4: CODE       — codegraph symbol intelligence
//	💎 Layer 3: FACTS      — SQLite + HRR + BM25 + Jaccard hybrid + versioning + entities + Zettelkasten + decay
//	🔎 Layer 2: BLACKBOX   — flight recorder + telemetry + 11-metric efficiency scoring
//	⚡ Layer 1: BUILTIN    — MEMORY.md / USER.md (always-on, zero setup)
package layers

import (
	"context"
	"fmt"
	"time"
)

// Layer is the common interface all memory layers implement.
type Layer interface {
	// Name returns the layer name (e.g. "builtin", "facts", "blackbox").
	Name() string

	// Priority returns the layer priority (1=highest, 8=lowest).
	Priority() int

	// Query retrieves relevant results from this layer.
	Query(ctx context.Context, q string, limit int) ([]Result, error)

	// Store persists a new item into the layer.
	Store(ctx context.Context, item Item) error

	// Close releases any resources held by the layer.
	Close() error
}

// Result is a unified search result from any layer.
type Result struct {
	Layer    string            `json:"layer"`
	Score    float64           `json:"score"`
	Content  string            `json:"content"`
	Category string            `json:"category,omitempty"`
	Tags     []string          `json:"tags,omitempty"`
	Source   string            `json:"source,omitempty"` // file path, URL, fact ID
	Metadata map[string]string `json:"metadata,omitempty"`
	Time     time.Time         `json:"time,omitempty"`
}

// Item represents a memory item to be stored.
type Item struct {
	Title     string            `json:"title,omitempty"`
	Content   string            `json:"content"`
	Category  string            `json:"category"`
	Tags      []string          `json:"tags"`
	Source    string            `json:"source,omitempty"`
	Trust     float64           `json:"trust,omitempty"` // 0.0-1.0 confidence
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// Brain orchestrates all 8 memory layers, routing queries and stores
// to the appropriate layers and fusing results with RRF (Reciprocal Rank Fusion).
// Also provides hooks, think/synthesis, verification, and rules generation.
type Brain struct {
	layers []Layer
	Hooks  *HookRegistry
}

// NewBrain creates a Brain with the given layers.
func NewBrain(layers ...Layer) *Brain {
	return &Brain{
		layers: layers,
		Hooks:  NewHookRegistry(),
	}
}

// AddLayer adds a layer to the brain.
func (b *Brain) AddLayer(l Layer) {
	b.layers = append(b.layers, l)
}

// Query searches all layers and returns fused results.
// Results are scored using RRF (Reciprocal Rank Fusion) across all layers.
func (b *Brain) Query(ctx context.Context, q string, limit int) ([]Result, error) {
	if limit <= 0 {
		limit = 10
	}

	type layerResult struct {
		layer   string
		results []Result
		err     error
	}

	// Query each layer concurrently
	ch := make(chan layerResult, len(b.layers))
	for _, l := range b.layers {
		go func(l Layer) {
			res, err := l.Query(ctx, q, limit)
			ch <- layerResult{layer: l.Name(), results: res, err: err}
		}(l)
	}

	// Collect all results
	var all []layerResult
	for range b.layers {
		r := <-ch
		if r.err == nil {
			all = append(all, r)
		}
	}

	// Fuse using RRF
	// RRF score = 1 / (60 + rank) for each result in each layer
	type scored struct {
		result Result
		score  float64
	}
	scoredMap := make(map[string]*scored)

	for _, lr := range all {
		for rank, r := range lr.results {
			key := lr.layer + ":" + r.Content
			rrfScore := 1.0 / (60.0 + float64(rank))
			if existing, ok := scoredMap[key]; ok {
				existing.score += rrfScore
			} else {
				r.Layer = lr.layer
				scoredMap[key] = &scored{result: r, score: rrfScore}
			}
		}
	}

	// Sort by score descending
	sorted := make([]scored, 0, len(scoredMap))
	for _, s := range scoredMap {
		sorted = append(sorted, *s)
	}
	// Simple bubble sort for small result sets
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].score > sorted[i].score {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	if len(sorted) > limit {
		sorted = sorted[:limit]
	}

	results := make([]Result, len(sorted))
	for i, s := range sorted {
		s.result.Score = s.score
		results[i] = s.result
	}
	return results, nil
}

// Store saves an item to the appropriate layer(s) based on category.
func (b *Brain) Store(ctx context.Context, item Item) error {
	for _, l := range b.layers {
		// Skip continuum and notion for direct stores
		if l.Name() == "continuum" || l.Name() == "notion" {
			continue
		}
		if err := l.Store(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

// Close closes all layers.
func (b *Brain) Close() {
	for _, l := range b.layers {
		_ = l.Close()
	}
}

// LayerCount returns the number of registered layers.
func (b *Brain) LayerCount() int {
	return len(b.layers)
}

// Layers returns all registered layers.
func (b *Brain) Layers() []Layer {
	return b.layers
}

// ── FactsLayer accessor ────────────────────────────────────────────────────

// FactsLayer returns the FactsLayer if registered, or nil.
func (b *Brain) FactsLayer() *FactsLayer {
	for _, l := range b.layers {
		if fl, ok := l.(*FactsLayer); ok {
			return fl
		}
	}
	return nil
}

// ── Per-Fact Versioning ────────────────────────────────────────────────────

// VersionedUpdate updates a fact with full version tracking.
func (b *Brain) VersionedUpdate(factID int64, newContent string, reason string) map[string]interface{} {
	fl := b.FactsLayer()
	if fl == nil {
		return nil
	}
	return fl.VersionedUpdate(factID, newContent, reason)
}

// GetVersionHistory returns all versions for a fact.
func (b *Brain) GetVersionHistory(factID int64, limit int) []FactVersion {
	fl := b.FactsLayer()
	if fl == nil {
		return nil
	}
	return fl.GetVersionHistory(factID, limit)
}

// UndoToVersion rolls back a fact to a previous version.
func (b *Brain) UndoToVersion(factID int64, versionID int64) map[string]interface{} {
	fl := b.FactsLayer()
	if fl == nil {
		return nil
	}
	return fl.UndoToVersion(factID, versionID)
}

// VersioningStats returns versioning statistics.
func (b *Brain) VersioningStats() map[string]interface{} {
	fl := b.FactsLayer()
	if fl == nil {
		return nil
	}
	return fl.VersioningStats()
}

// ── Temporal Search ────────────────────────────────────────────────────────

// SearchTemporal searches facts with optional time-window filter.
func (b *Brain) SearchTemporal(ctx context.Context, query string, timeStart, timeEnd string,
	category, source string, minTrust float64, limit int, includeCleared bool) ([]Result, error) {
	fl := b.FactsLayer()
	if fl == nil {
		return nil, nil
	}
	return fl.SearchTemporal(ctx, query, timeStart, timeEnd, category, source, minTrust, limit, includeCleared)
}

// HasTemporalIntent checks if a query has temporal filtering intent.
func (b *Brain) HasTemporalIntent(query string) bool {
	return HasTemporalIntent(query)
}

// ParseTimeQuery parses natural language time expressions.
func (b *Brain) ParseTimeQuery(query string) (string, string) {
	return ParseTimeQuery(query)
}

// ── Probe & Reason ─────────────────────────────────────────────────────────

// Probe finds facts mentioning a single entity.
func (b *Brain) Probe(ctx context.Context, entity string, limit int, includeCleared bool) ([]Result, error) {
	fl := b.FactsLayer()
	if fl == nil {
		return nil, nil
	}
	return fl.Probe(ctx, entity, limit, includeCleared)
}

// Reason performs compositional query — facts mentioning ALL entities.
func (b *Brain) Reason(ctx context.Context, entities []string, category string, limit int) ([]Result, error) {
	fl := b.FactsLayer()
	if fl == nil {
		return nil, nil
	}
	return fl.Reason(ctx, entities, category, limit)
}

// ── Entity System ──────────────────────────────────────────────────────────

// EntitiesForFact returns all entity names linked to a fact.
func (b *Brain) EntitiesForFact(factID int64) []string {
	fl := b.FactsLayer()
	if fl == nil {
		return nil
	}
	return fl.EntitiesForFact(factID)
}

// ── Zettelkasten Linking ───────────────────────────────────────────────────

// LinkedFacts returns facts linked to factID.
func (b *Brain) LinkedFacts(factID int64, limit int) []map[string]interface{} {
	fl := b.FactsLayer()
	if fl == nil {
		return nil
	}
	return fl.LinkedFacts(factID, limit)
}

// LinkStats returns statistics about the fact link graph.
func (b *Brain) LinkStats() map[string]interface{} {
	fl := b.FactsLayer()
	if fl == nil {
		return nil
	}
	return fl.LinkStats()
}

// Evolve merges near-duplicate facts based on Jaccard similarity.
func (b *Brain) Evolve(threshold float64) map[string]interface{} {
	fl := b.FactsLayer()
	if fl == nil {
		return nil
	}
	return fl.Evolve(threshold)
}

// ── Contradiction Detection ────────────────────────────────────────────────

// Forget deletes a fact by ID from the facts layer.
func (b *Brain) Forget(factID int64) error {
	fl := b.FactsLayer()
	if fl == nil {
		return fmt.Errorf("facts layer not available")
	}
	return fl.DeleteFact(factID)
}

// DetectContradictions finds facts with contradiction flags sharing entities
// with the given fact ID.
func (b *Brain) DetectContradictions(factID int64) []map[string]interface{} {
	fl := b.FactsLayer()
	if fl == nil {
		return nil
	}
	return fl.DetectContradictions(factID)
}

// ResolveContradictions removes contradiction flags from a fact and its contradictors.
func (b *Brain) ResolveContradictions(factID int64) int {
	fl := b.FactsLayer()
	if fl == nil {
		return 0
	}
	return fl.ResolveContradictions(factID)
}

// ── Memory Decay ───────────────────────────────────────────────────────────

// ApplyDecay applies exponential strength decay to all facts.
func (b *Brain) ApplyDecay(decayRate float64) map[string]interface{} {
	fl := b.FactsLayer()
	if fl == nil {
		return nil
	}
	return fl.ApplyDecay(decayRate)
}

// ── Trust & Stats ──────────────────────────────────────────────────────────

// GetFact retrieves a single fact by ID.
func (b *Brain) GetFact(factID int64) *Fact {
	fl := b.FactsLayer()
	if fl == nil {
		return nil
	}
	return fl.GetFact(factID)
}

// SetTrust updates the trust score for a fact.
func (b *Brain) SetTrust(factID int64, score float64) {
	fl := b.FactsLayer()
	if fl == nil {
		return
	}
	fl.SetTrust(factID, score)
}

// UpdateTrust adjusts trust based on helpful/unhelpful feedback.
func (b *Brain) UpdateTrust(factID int64, helpful bool) map[string]interface{} {
	fl := b.FactsLayer()
	if fl == nil {
		return nil
	}
	return fl.UpdateTrust(factID, helpful)
}

// FactsStats returns comprehensive facts layer statistics.
func (b *Brain) FactsStats() map[string]interface{} {
	fl := b.FactsLayer()
	if fl == nil {
		return nil
	}
	return fl.Stats()
}

// ── Hook System ─────────────────────────────────────────────────────────────

// FireHook fires a hook event to all registered handlers.
// Returns the list of handler return values.
func (b *Brain) FireHook(hookName string, ctx map[string]interface{}) []interface{} {
	if b.Hooks == nil {
		return nil
	}
	return b.Hooks.Fire(hookName, ctx)
}

// HasHookHandlers checks if a hook has any registered handlers.
func (b *Brain) HasHookHandlers(hookName string) bool {
	if b.Hooks == nil {
		return false
	}
	return b.Hooks.HasHandlers(hookName)
}

// RegisterHook registers a handler for a hook.
func (b *Brain) RegisterHook(hookName string, handler HookHandler) {
	if b.Hooks == nil {
		b.Hooks = NewHookRegistry()
	}
	b.Hooks.Register(hookName, handler)
}

// RegisterDefaultHooks registers all 15 built-in hook handlers.
func (b *Brain) RegisterDefaultHooks() {
	if b.Hooks == nil {
		b.Hooks = NewHookRegistry()
	}
	RegisterDefaultHooks(b)
}
