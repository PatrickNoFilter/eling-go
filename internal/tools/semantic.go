// Package tools provides the semantic_search tool for meaning-based retrieval.
// It generates vector embeddings for queries and content, then finds the most
// semantically similar items using cosine similarity.
package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Embedding types
// ---------------------------------------------------------------------------

// BrainSearchFn is an adapter that wraps Brain.Query() for use by semantic_search.
// Set by agent.go at startup. Returns results sorted by relevance.
type BrainSearchFn func(query string, limit int) ([]SearchResult, error)

// SearchResult is a unified result from any backend (Brain or local trigram).
type SearchResult struct {
	Content  string   `json:"content"`
	Score    float64  `json:"score"`
	Category string   `json:"category,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Source   string   `json:"source,omitempty"`
}

// BrainQuery is the package-level hook injected by agent.go.
// When nil, semantic_search falls back to local trigram search.
var BrainQuery BrainSearchFn

// EmbeddingVector is a list of floats representing a semantic embedding.
type EmbeddingVector []float64

const maxEmbedCacheSize = 1000

// embeddingCache caches computed embeddings locally to reduce API calls.
var (
	embedCache     = make(map[string]EmbeddingVector)     // content -> vector
	embedCacheKeys = make([]string, 0, maxEmbedCacheSize) // insertion order for eviction
	embedCacheMu   sync.RWMutex
)

// EmbeddingRequest is sent to the /embeddings API.
type EmbeddingRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

// EmbeddingResponse is the expected API response.
type EmbeddingResponse struct {
	Object string `json:"object"`
	Data   []struct {
		Object    string          `json:"object"`
		Index     int             `json:"index"`
		Embedding EmbeddingVector `json:"embedding"`
	} `json:"data"`
	Model string `json:"model"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// localEmbedding returns a simple local embedding vector using character
// trigram hash buckets. This works offline without any API call and provides
// a reasonable similarity measure for short-to-medium text.
// The vector dimension is 512 (8-bit hash of trigrams).
func localEmbedding(text string) EmbeddingVector {
	const dim = 512
	vec := make([]float64, dim)
	lower := strings.ToLower(text)
	runes := []rune(lower)
	for i := 0; i < len(runes)-2; i++ {
		// Simple hash of the trigram
		h := (int(runes[i])*31 + int(runes[i+1])*7 + int(runes[i+2])) % dim
		if h < 0 {
			h += dim
		}
		vec[h]++
	}
	// Also add word unigrams for better topical matching
	words := strings.Fields(lower)
	for _, w := range words {
		if len(w) > 2 {
			h := 0
			for _, r := range w {
				h = (h*31 + int(r)) % dim
			}
			if h < 0 {
				h += dim
			}
			vec[h] += 2.0 // word hash weighted double
		}
	}
	// L2 normalize
	var norm float64
	for _, v := range vec {
		norm += v * v
	}
	if norm > 0 {
		inv := 1.0 / math.Sqrt(norm)
		for i := range vec {
			vec[i] *= inv
		}
	}
	return vec
}

// getEmbedding returns a vector embedding for the given text.
// It first tries the configured API provider; if that fails, it falls back
// to a local embedding (character trigram hash) that works offline.
// Results are cached locally to avoid redundant computation.
func getEmbedding(text string) (EmbeddingVector, error) {
	if text == "" {
		return nil, fmt.Errorf("empty text")
	}

	// Normalize whitespace for better cache hits
	normalized := strings.Join(strings.Fields(text), " ")

	// Check cache
	embedCacheMu.RLock()
	if vec, ok := embedCache[normalized]; ok {
		embedCacheMu.RUnlock()
		return vec, nil
	}
	embedCacheMu.RUnlock()

	// Try API-based embedding first
	apiKey := os.Getenv("ELING_API_KEY")
	baseURL := os.Getenv("ELING_BASE_URL")
	model := os.Getenv("ELING_EMBEDDING_MODEL")

	if apiKey == "" {
		apiKey = os.Getenv("DEEPSEEK_API_KEY")
	}
	if baseURL == "" {
		baseURL = os.Getenv("DEEPSEEK_BASE_URL")
	}
	if baseURL == "" {
		baseURL = "https://api.deepseek.com"
	}

	// Build list of models to try: configured model first, then fallbacks
	modelsToTry := make([]string, 0)
	if model != "" {
		modelsToTry = append(modelsToTry, model)
	}
	// Fallback models in order of likelihood
	fallbackModels := []string{
		"deepseek-embedding",
		"text-embedding-ada-002",
		"text-embedding-3-small",
		"text-embedding-3-large",
		"nomic-embed-text",
		"llama-3.2-3b-embedding",
		"bge-m3",
	}
	for _, m := range fallbackModels {
		if m != model { // don't duplicate the configured model
			modelsToTry = append(modelsToTry, m)
		}
	}

	for _, tryModel := range modelsToTry {
		vec, err := callEmbeddingAPI(apiKey, baseURL, tryModel, normalized)
		if err == nil {
			// Cache the result (with LRU eviction when over cap)
			embedCacheMu.Lock()
			embedCache[normalized] = vec
			embedCacheKeys = append(embedCacheKeys, normalized)
			if len(embedCacheKeys) > maxEmbedCacheSize {
				evict := embedCacheKeys[0]
				embedCacheKeys = embedCacheKeys[1:]
				delete(embedCache, evict)
			}
			embedCacheMu.Unlock()
			return vec, nil
		}
	}

	// API embedding failed for all models — fall back to local embedding
	// This works offline and doesn't require any external API.
	vec := localEmbedding(normalized)

	// Cache and return the local embedding
	embedCacheMu.Lock()
	embedCache[normalized] = vec
	embedCacheKeys = append(embedCacheKeys, normalized)
	if len(embedCacheKeys) > maxEmbedCacheSize {
		evict := embedCacheKeys[0]
		embedCacheKeys = embedCacheKeys[1:]
		delete(embedCache, evict)
	}
	embedCacheMu.Unlock()

	return vec, nil
}

// callEmbeddingAPI calls the embedding API for a single model and returns the vector.
func callEmbeddingAPI(apiKey, baseURL, model, text string) (EmbeddingVector, error) {
	// Build the request
	reqBody := EmbeddingRequest{
		Model: model,
		Input: text,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal embedding request: %w", err)
	}

	// POST to /embeddings
	url := strings.TrimRight(baseURL, "/") + "/embeddings"
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create embedding request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embedding API error %d for model %q: %s", resp.StatusCode, model, string(respBody))
	}

	var embResp EmbeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
		return nil, fmt.Errorf("decode embedding response: %w", err)
	}

	if len(embResp.Data) == 0 {
		return nil, fmt.Errorf("no embedding data in response for model %q", model)
	}

	return embResp.Data[0].Embedding, nil
}

// ---------------------------------------------------------------------------
// Cosine similarity
// ---------------------------------------------------------------------------

// cosineSimilarity computes the cosine similarity between two vectors.
// Returns a value in [-1, 1] where 1 = identical direction.
func cosineSimilarity(a, b EmbeddingVector) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// ---------------------------------------------------------------------------
// ScoredItem is a retrieved item with its similarity score.
// ---------------------------------------------------------------------------

// SemanticResult is a single search result with score and metadata.
type SemanticResult struct {
	Content   string            `json:"content"`
	Score     float64           `json:"score"`
	Category  string            `json:"category,omitempty"`
	Tags      []string          `json:"tags,omitempty"`
	Timestamp string            `json:"timestamp,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// ---------------------------------------------------------------------------
// In-memory semantic index
// ---------------------------------------------------------------------------

// SemanticIndexItem is a text chunk with its embedding vector, stored for
// later retrieval. The agent can build this index from memory items,
// session history, or any other text content.
type SemanticIndexItem struct {
	Content   string            `json:"content"`
	Embedding EmbeddingVector   `json:"embedding"`
	Category  string            `json:"category,omitempty"`
	Tags      []string          `json:"tags,omitempty"`
	Timestamp time.Time         `json:"timestamp,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// maxSemanticIndexItems caps the total number of items in the semantic index
// to prevent unbounded memory growth. When the limit is reached, the oldest
// items are evicted (FIFO).
const maxSemanticIndexItems = 10000

var (
	semanticIndex      []SemanticIndexItem
	semanticIndexMu    sync.RWMutex
)

// AddToSemanticIndex adds one or more items to the global semantic index.
// Embeddings are computed lazily (on first search if not pre-computed).
// When the index exceeds maxSemanticIndexItems, the oldest items are evicted.
func AddToSemanticIndex(items ...SemanticIndexItem) {
	semanticIndexMu.Lock()
	defer semanticIndexMu.Unlock()
	semanticIndex = append(semanticIndex, items...)
	// Evict oldest items when over capacity
	if len(semanticIndex) > maxSemanticIndexItems {
		over := len(semanticIndex) - maxSemanticIndexItems
		semanticIndex = semanticIndex[over:]
	}
}

// ClearSemanticIndex clears the index (e.g., on state reset).
func ClearSemanticIndex() {
	semanticIndexMu.Lock()
	defer semanticIndexMu.Unlock()
	semanticIndex = nil
}

// SemanticIndexSize returns the number of items in the index.
func SemanticIndexSize() int {
	semanticIndexMu.RLock()
	defer semanticIndexMu.RUnlock()
	return len(semanticIndex)
}

// SemanticSearch performs a meaning-based search over the in-memory index.
// Returns up to topK results sorted by descending similarity score.
// If the embedding API is unavailable, returns nil results without error
// so callers can degrade gracefully to substring search.
func SemanticSearch(queryText string, topK int) []SemanticResult {
	results, err := semanticSearch(queryText, nil, topK)
	if err != nil {
		return nil
	}
	return results
}

// SetEmbeddingEnv sets the environment variables the embedding client uses.
// Called by the agent at startup so the embedding API uses the same
// provider credentials as the chat API.
// model is the embedding model to use (e.g. "deepseek-embedding", "text-embedding-ada-002").
// If empty, getEmbedding will try common models in order.
func SetEmbeddingEnv(apiKey, baseURL, model string) {
	if apiKey != "" {
		os.Setenv("ELING_API_KEY", apiKey)
	}
	if baseURL != "" {
		os.Setenv("ELING_BASE_URL", baseURL)
	}
	if model != "" {
		os.Setenv("ELING_EMBEDDING_MODEL", model)
	}
}

// semanticSearch performs a vector similarity search over the in-memory
// semantic index.  If queryEmbed is nil it will be computed from queryText.
// Returns up to topK results sorted by descending similarity score.
func semanticSearch(queryText string, queryEmbed EmbeddingVector, topK int) ([]SemanticResult, error) {
	// Compute embedding for the query if not provided
	var err error
	if queryEmbed == nil {
		queryEmbed, err = getEmbedding(queryText)
		if err != nil {
			return nil, fmt.Errorf("embed query: %w", err)
		}
	}

	semanticIndexMu.RLock()
	items := make([]SemanticIndexItem, len(semanticIndex))
	copy(items, semanticIndex)
	semanticIndexMu.RUnlock()

	type scored struct {
		item  SemanticIndexItem
		score float64
	}
	var scoredItems []scored

	for _, item := range items {
		// Use pre-computed embedding or compute on the fly
		vec := item.Embedding
		if vec == nil {
			vec, err = getEmbedding(item.Content)
			if err != nil {
				continue // skip items that can't be embedded
			}
			// Cache back (optional)
		}
		score := cosineSimilarity(queryEmbed, vec)
		if score > 0.1 { // filter near-zero scores
			scoredItems = append(scoredItems, scored{item, score})
		}
	}

	// Sort by score descending (simple insertion sort for small lists)
	for i := 1; i < len(scoredItems); i++ {
		for j := i; j > 0 && scoredItems[j].score > scoredItems[j-1].score; j-- {
			scoredItems[j], scoredItems[j-1] = scoredItems[j-1], scoredItems[j]
		}
	}

	// Take topK
	if topK <= 0 || topK > len(scoredItems) {
		topK = len(scoredItems)
	}
	if topK > 10 {
		topK = 10
	}

	results := make([]SemanticResult, 0, topK)
	for i := 0; i < topK; i++ {
		s := scoredItems[i]
		results = append(results, SemanticResult{
			Content:   truncateStr(s.item.Content, 500),
			Score:     s.score,
			Category:  s.item.Category,
			Tags:      s.item.Tags,
			Timestamp: s.item.Timestamp.Format(time.RFC3339),
			Metadata:  s.item.Metadata,
		})
	}

	return results, nil
}

// truncateStr truncates a string to at most n runes.
func truncateStr(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}

// ---------------------------------------------------------------------------
// Tool: semantic_search
// ---------------------------------------------------------------------------

func init() {
	DefaultRegistry.Register(Tool{
		Name: "semantic_search",
		Description: "Perform a meaning-based (semantic/vector) search over the agent's memory, " +
			"conversation history, and indexed content. Uses embeddings and cosine similarity to " +
			"find conceptually related items even when exact keywords don't match. " +
			"Results are ranked by relevance score (0.0–1.0). " +
			"Use this when you need to find information by meaning rather than exact text.",
		Version:  "1.0.0",
		Category: "system",
		Execute:  semanticSearchExecute,
	})

	DefaultRegistry.Register(Tool{
		Name: "semantic_index",
		Description: "Add content to the semantic search index. Provide text content, " +
			"optional category, tags, and metadata. The content will be vector-embedded and " +
			"made available for future semantic_search queries. Use this to build up a " +
			"searchable knowledge base of concepts, summaries, or important information.",
		Version:  "1.0.0",
		Category: "system",
		Execute:  semanticIndexExecute,
	})
}

// semanticSearchExecute handles the semantic_search tool call.
// Expected args:
//
//	query (string, required) – the semantic query
//	top_k (number, optional) – number of results (default 5)
//	source (string, optional) – "memory", "index", "all" (default "all")
func semanticSearchExecute(args map[string]interface{}) (interface{}, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return Err("query is required"), nil
	}

	topK := 5
	if n, ok := args["top_k"].(float64); ok && n > 0 {
		topK = int(n)
	}

	// Try Brain query first (more accurate, FTS5+HRR hybrid scoring)
	// BrainQuery is injected by agent.go at startup.
	if BrainQuery != nil {
		results, err := BrainQuery(query, topK)
		if err == nil && len(results) > 0 {
			return OK(map[string]interface{}{
				"query":   query,
				"results": results,
				"total":   len(results),
			}), nil
		}
		// Fall through to local search if Brain returns nothing or errors
	}

	// Get embedding for the query
	queryEmbed, err := getEmbedding(query)
	if err != nil {
		return nil, fmt.Errorf("failed to generate query embedding: %w", err)
	}

	var allResults []SemanticResult

	// Search the in-memory semantic index
	indexResults, err := semanticSearch(query, queryEmbed, topK)
	if err != nil {
		// Non-fatal: log but continue
		_ = err
	}
	allResults = append(allResults, indexResults...)

	// Deduplicate by content
	seen := make(map[string]bool)
	deduped := make([]SemanticResult, 0, len(allResults))
	for _, r := range allResults {
		if !seen[r.Content] {
			seen[r.Content] = true
			deduped = append(deduped, r)
		}
	}

	// Re-sort combined results
	for i := 1; i < len(deduped); i++ {
		for j := i; j > 0 && deduped[j].Score > deduped[j-1].Score; j-- {
			deduped[j], deduped[j-1] = deduped[j-1], deduped[j]
		}
	}

	// Take topK
	if len(deduped) > topK {
		deduped = deduped[:topK]
	}

	if len(deduped) == 0 {
		return OK(map[string]interface{}{
			"query":   query,
			"results": []SemanticResult{},
			"message": "No semantically similar content found. Try indexing content first with semantic_index, or use grep for exact text matching.",
		}), nil
	}

	return OK(map[string]interface{}{
		"query":   query,
		"results": deduped,
		"total":   len(deduped),
	}), nil
}

// semanticIndexExecute handles the semantic_index tool call.
// Expected args:
//
//	content (string, required) – text content to index
//	category (string, optional) – e.g. "fact", "preference", "concept"
//	tags (string, optional) – comma-separated tags
//	metadata (object, optional) – key-value metadata pairs
func semanticIndexExecute(args map[string]interface{}) (interface{}, error) {
	content, _ := args["content"].(string)
	if content == "" {
		return Err("content is required"), nil
	}

	category, _ := args["category"].(string)
	tagsStr, _ := args["tags"].(string)
	var tags []string
	if tagsStr != "" {
		tags = strings.Split(tagsStr, ",")
		for i := range tags {
			tags[i] = strings.TrimSpace(tags[i])
		}
	}

	var metadata map[string]string
	if m, ok := args["metadata"].(map[string]interface{}); ok {
		metadata = make(map[string]string, len(m))
		for k, v := range m {
			metadata[k] = fmt.Sprintf("%v", v)
		}
	}

	// Compute embedding now (or lazily – we do it eagerly so the user knows
	// it worked)
	vec, err := getEmbedding(content)
	if err != nil {
		return nil, fmt.Errorf("failed to generate embedding for content: %w", err)
	}

	item := SemanticIndexItem{
		Content:   content,
		Embedding: vec,
		Category:  category,
		Tags:      tags,
		Timestamp: time.Now(),
		Metadata:  metadata,
	}

	AddToSemanticIndex(item)

	return OK(map[string]interface{}{
		"indexed":         true,
		"content_preview": truncateStr(content, 100),
		"category":        category,
		"tags":            tags,
		"index_size":      SemanticIndexSize(),
		"message":         "Content added to semantic search index. Use semantic_search to find it by meaning.",
	}), nil
}
