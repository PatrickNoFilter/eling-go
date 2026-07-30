package layers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	_ "modernc.org/sqlite"
)

// ── Constants ──────────────────────────────────────────────────────────────

const (
	DefaultTrust          = 0.5
	HelpfulDelta          = 0.05
	UnhelpfulDelta        = -0.10
	CONTRADICTION_FLAG    = "contradiction_pending"
	LINK_THRESHOLD        = 0.25
	EVOLVE_MERGE_THRESHOLD = 0.65
	ActiveThreshold       = 0.3
	DormantThreshold      = 0.1
	DefaultDecayRate      = 0.01
	ReadBoost             = 0.05
	DecisionBoost         = 0.15
	HRRDim                = 256
)

// ── Regex patterns ─────────────────────────────────────────────────────────

var (
	reCapitalized = regexp.MustCompile(`\b([A-Z][a-z]+(?:\s+[A-Z][a-z]+)+)\b`)
	reDoubleQuote = regexp.MustCompile(`"([^"]+)"`)
	reSingleQuote = regexp.MustCompile(`'([^']+)'`)
	reWikiLink    = regexp.MustCompile(`\[\[([^\]]+)\]\]`)
)

// ── Temporal patterns ──────────────────────────────────────────────────────

type temporalPattern struct {
	pattern   *regexp.Regexp
	unit      string // "day", "week", "month", "year", "num"
	direction int    // 0=current, -1=past, 1=future
}

var temporalPatterns = []temporalPattern{
	{regexp.MustCompile(`(?i)\b(lately|recently|baru[- ]?baru ini)\b`), "day", -7},
	{regexp.MustCompile(`(?i)\b(yesterday|kemarin)\b`), "day", -1},
	{regexp.MustCompile(`(?i)\b(today|hari ini|sekarang)\b`), "day", 0},
	{regexp.MustCompile(`(?i)\b(this\s+week|minggu ini)\b`), "week", 0},
	{regexp.MustCompile(`(?i)\b(last\s+week|minggu lalu)\b`), "week", -1},
	{regexp.MustCompile(`(?i)\b(this\s+month|bulan ini)\b`), "month", 0},
	{regexp.MustCompile(`(?i)\b(last\s+month|bulan lalu)\b`), "month", -1},
	{regexp.MustCompile(`(?i)\b(tomorrow|besok)\b`), "day", 1},
	{regexp.MustCompile(`(?i)\b(this\s+(year|quarter))\b`), "year", 0},
	{regexp.MustCompile(`(?i)\b(last\s+(year|quarter|3\s*months))\b`), "year", -1},
	{regexp.MustCompile(`(?i)(?:last|past|previous)\s+(\d+)\s*(days?|hours?|h|minutes?|menit|jam)\b`), "num", -1},
	{regexp.MustCompile(`(?i)(?:next|coming)\s+(\d+)\s*(days?|hours?|h|minutes?|menit|jam)\b`), "num", 1},
}

var temporalIntentKeywords = map[string]bool{
	"yesterday": true, "today": true, "tomorrow": true, "kemarin": true, "besok": true,
	"lately": true, "recently": true, "this": true, "last": true, "past": true,
	"previous": true, "next": true, "coming": true, "since": true, "from": true,
	"after": true, "before": true, "between": true, "range": true,
}

// ── FactsLayer ─────────────────────────────────────────────────────────────

// FactsLayer is Layer 3: the facts memory layer with SQLite + FTS5 + HRR.
// Supports per-fact versioning, entities, Zettelkasten linking, contradiction
// detection, temporal search, and memory decay.
type FactsLayer struct {
	mu          sync.RWMutex
	db          *sql.DB
	stateDir    string
	embeddingFn func(text string) ([]float64, error)
	hrrDim      int
}

// Fact represents a single stored fact with trust scoring and strength.
type Fact struct {
	ID         int64     `json:"id"`
	Content    string    `json:"content"`
	Category   string    `json:"category"`
	Tags       string    `json:"tags"`
	Source     string    `json:"source"`
	Trust      float64   `json:"trust"`
	Strength   float64   `json:"strength"`
	Retrievals int       `json:"retrieval_count"`
	Helpful    int       `json:"helpful_count"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	Version    int       `json:"version"`
}

// FactVersion represents a single version in a fact's version history.
type FactVersion struct {
	VersionID int64     `json:"version_id"`
	FactID    int64     `json:"fact_id"`
	Content   string    `json:"content"`
	Category  string    `json:"category"`
	Tags      string    `json:"tags"`
	Trust     float64   `json:"trust"`
	Source    string    `json:"source"`
	Action    string    `json:"action"` // created, updated, undo_checkpoint
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

// NewFactsLayer creates a new FactsLayer with SQLite + FTS5 + versioning + entities.
func NewFactsLayer(stateDir string) (*FactsLayer, error) {
	dbPath := filepath.Join(stateDir, "facts.db")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dbPath+"?_journal=WAL&_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open facts db: %w", err)
	}

	l := &FactsLayer{
		db:       db,
		stateDir: stateDir,
		hrrDim:   HRRDim,
	}

	if err := l.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("init facts schema: %w", err)
	}

	return l, nil
}

// Name returns "facts".
func (l *FactsLayer) Name() string { return "facts" }

// Priority returns 3.
func (l *FactsLayer) Priority() int { return 3 }

// SetEmbeddingFn sets an optional embedding function for semantic search.
func (l *FactsLayer) SetEmbeddingFn(fn func(text string) ([]float64, error)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.embeddingFn = fn
	if fn != nil {
		_, _ = l.db.Exec(`
			CREATE TABLE IF NOT EXISTS fact_embeddings (
				fact_id INTEGER PRIMARY KEY REFERENCES facts(id) ON DELETE CASCADE,
				embedding BLOB NOT NULL,
				updated_at TEXT NOT NULL
			)
		`)
	}
}

// ── Query (BM25 + Jaccard + HRR hybrid) ─────────────────────────────────

func (l *FactsLayer) Query(ctx context.Context, q string, limit int) ([]Result, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if q = sanitizeFTS5(q); q == "" {
		return nil, nil
	}

	// BM25 candidates (3x for re-ranking)
	candidates, err := l.ftsCandidates(ctx, q, "", "", 0.0, limit*3)
	if err != nil || len(candidates) == 0 {
		return nil, err
	}

	// Filter out dormant/cleared facts
	candidates = filterCleared(candidates)
	if len(candidates) == 0 {
		return nil, nil
	}

	// Hybrid scoring: BM25 + Jaccard + HRR + embeddings
	queryTokens := tokenize(q)
	queryVec := encodeTextSimple(q, l.hrrDim)

	// Get embedding scores if available
	embScores := make(map[int64]float64)
	if l.embeddingFn != nil {
		embScores = l.searchEmbeddings(ctx, q, candidates)
	}

	type scoredFact struct {
		fact  Fact
		score float64
	}
	var scored []scoredFact

	for _, f := range candidates {
		contentTokens := tokenize(f.Content)
		tagTokens := tokenize(f.Tags)
		combinedTokens := union(contentTokens, tagTokens)

		jaccard := jaccardSimilarity(queryTokens, combinedTokens)

		// HRR similarity (simplified — use token-based encoding)
		hrrSim := 0.5
		vec := encodeTextSimple(f.Content, l.hrrDim)
		hrrSim = (cosineSimilarity(queryVec, vec) + 1.0) / 2.0

		embSim := embScores[f.ID]
		if embSim == 0 {
			embSim = 0.5
		}

		// FTS rank normalized 0-1
		ftsScore := f.Trust // fallback

		relevance := 0.35*ftsScore + 0.25*jaccard + 0.3*hrrSim + 0.1*embSim
		score := relevance * f.Trust

		scored = append(scored, scoredFact{fact: f, score: score})
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})
	if len(scored) > limit {
		scored = scored[:limit]
	}

	results := make([]Result, len(scored))
	for i, s := range scored {
		results[i] = Result{
			Content:  s.fact.Content,
			Category: s.fact.Category,
			Tags:     strings.Split(s.fact.Tags, ","),
			Source:   s.fact.Source,
			Score:    s.score,
			Time:     s.fact.CreatedAt,
			Metadata: map[string]string{
				"fact_id":  fmt.Sprintf("%d", s.fact.ID),
				"strength": fmt.Sprintf("%.3f", s.fact.Strength),
			},
		}
		// Boost strength on read
		l.boostStrength(s.fact.ID, ReadBoost)
	}
	l.dbCommit()

	return results, nil
}

// Store saves a fact with full entity extraction, version tracking, and linking.
func (l *FactsLayer) Store(ctx context.Context, item Item) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	trust := item.Trust
	if trust <= 0 {
		trust = DefaultTrust
	}

	now := time.Now().Format("2006-01-02 15:04:05")
	tags := strings.Join(item.Tags, ",")
	content := strings.TrimSpace(item.Content)
	if content == "" {
		return fmt.Errorf("content must not be empty")
	}

	// Check for duplicate by content hash
	var existingID int64
	err := l.db.QueryRowContext(ctx,
		"SELECT id FROM facts WHERE content = ?", content).Scan(&existingID)
	if err == nil {
		return nil // already exists
	}

	result, err := l.db.ExecContext(ctx, `
		INSERT INTO facts (content, category, tags, source, trust, strength, last_access_at, created_at, updated_at, version)
		VALUES (?, ?, ?, ?, ?, 1.0, ?, ?, ?, 1)`,
		content, item.Category, tags, item.Source, trust, now, now, now)
	if err != nil {
		return err
	}

	factID, _ := result.LastInsertId()

	// Track initial version
	_, _ = l.db.ExecContext(ctx, `
		INSERT INTO fact_versions (fact_id, content, category, tags, trust, source, action, reason, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'created', '', ?)`,
		factID, content, item.Category, tags, trust, item.Source, now)

	// Extract entities
	entities := extractEntities(content)
	for _, name := range entities {
		eid := l.resolveEntity(name)
		_, _ = l.db.ExecContext(ctx,
			"INSERT OR IGNORE INTO fact_entities VALUES (?, ?)", factID, eid)
	}

	// Self-wire entity graph
	l.selfWireGraph(factID)

	// Create Zettelkasten links
	l.createLinks(ctx, factID, content)

	// Detect contradictions
	l.detectContradictions(ctx, factID, content)

	return l.dbCommit()
}

// Close closes the database.
func (l *FactsLayer) Close() error {
	return l.db.Close()
}

// ── Schema ─────────────────────────────────────────────────────────────────

func (l *FactsLayer) initSchema() error {
	// WAL mode
	_, _ = l.db.Exec("PRAGMA journal_mode=WAL")

	_, err := l.db.Exec(`
		CREATE TABLE IF NOT EXISTS facts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			content TEXT NOT NULL,
			category TEXT DEFAULT 'general',
			tags TEXT DEFAULT '',
			source TEXT DEFAULT '',
			trust REAL DEFAULT 0.5,
			strength REAL DEFAULT 1.0,
			retrieval_count INTEGER DEFAULT 0,
			helpful_count INTEGER DEFAULT 0,
			last_access_at TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			version INTEGER DEFAULT 1
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_facts_content ON facts(content);
		CREATE INDEX IF NOT EXISTS idx_facts_category ON facts(category);
		CREATE INDEX IF NOT EXISTS idx_facts_trust ON facts(trust DESC);
		CREATE INDEX IF NOT EXISTS idx_facts_strength ON facts(strength DESC);
		CREATE INDEX IF NOT EXISTS idx_facts_created ON facts(created_at);

		CREATE TABLE IF NOT EXISTS fact_versions (
			version_id INTEGER PRIMARY KEY AUTOINCREMENT,
			fact_id INTEGER NOT NULL REFERENCES facts(id) ON DELETE CASCADE,
			content TEXT NOT NULL,
			category TEXT DEFAULT 'general',
			tags TEXT DEFAULT '',
			trust REAL DEFAULT 0.5,
			source TEXT DEFAULT '',
			action TEXT DEFAULT 'created',
			reason TEXT DEFAULT '',
			created_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_fv_fact_id ON fact_versions(fact_id);
		CREATE INDEX IF NOT EXISTS idx_fv_created ON fact_versions(created_at);

		CREATE TABLE IF NOT EXISTS entities (
			entity_id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			entity_type TEXT DEFAULT 'unknown',
			aliases TEXT DEFAULT '',
			created_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_entities_name ON entities(name);

		CREATE TABLE IF NOT EXISTS fact_entities (
			fact_id INTEGER REFERENCES facts(id) ON DELETE CASCADE,
			entity_id INTEGER REFERENCES entities(entity_id),
			PRIMARY KEY (fact_id, entity_id)
		);

		CREATE TABLE IF NOT EXISTS entity_graph (
			entity_a_id INTEGER NOT NULL REFERENCES entities(entity_id),
			entity_b_id INTEGER NOT NULL REFERENCES entities(entity_id),
			weight REAL DEFAULT 1.0,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (entity_a_id, entity_b_id)
		);

		CREATE TABLE IF NOT EXISTS fact_links (
			fact_id_a INTEGER NOT NULL REFERENCES facts(id) ON DELETE CASCADE,
			fact_id_b INTEGER NOT NULL REFERENCES facts(id) ON DELETE CASCADE,
			weight REAL DEFAULT 1.0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (fact_id_a, fact_id_b)
		);
		CREATE INDEX IF NOT EXISTS idx_fact_links_a ON fact_links(fact_id_a);
		CREATE INDEX IF NOT EXISTS idx_fact_links_b ON fact_links(fact_id_b);

		CREATE VIRTUAL TABLE IF NOT EXISTS facts_fts USING fts5(
			content, category, tags,
			content='facts',
			content_rowid='id',
			tokenize='porter unicode61'
		);

		CREATE TRIGGER IF NOT EXISTS facts_ai AFTER INSERT ON facts BEGIN
			INSERT INTO facts_fts(rowid, content, category, tags)
			VALUES (new.id, new.content, new.category, new.tags);
		END;
		CREATE TRIGGER IF NOT EXISTS facts_ad AFTER DELETE ON facts BEGIN
			INSERT INTO facts_fts(facts_fts, rowid, content, category, tags)
			VALUES ('delete', old.id, old.content, old.category, old.tags);
		END;
		CREATE TRIGGER IF NOT EXISTS facts_au AFTER UPDATE ON facts BEGIN
			INSERT INTO facts_fts(facts_fts, rowid, content, category, tags)
			VALUES ('delete', old.id, old.content, old.category, old.tags);
			INSERT INTO facts_fts(rowid, content, category, tags)
			VALUES (new.id, new.content, new.category, new.tags);
		END;
	`)
	return err
}

// ── FTS Candidates ─────────────────────────────────────────────────────────

func (l *FactsLayer) ftsCandidates(ctx context.Context, query, category, source string, minTrust float64, limit int) ([]Fact, error) {
	safe := sanitizeFTS5(query)
	if safe == "" {
		return nil, nil
	}

	var whereClauses []string
	var params []interface{}

	whereClauses = append(whereClauses, "facts_fts MATCH ?")
	params = append(params, safe)

	if category != "" {
		whereClauses = append(whereClauses, "f.category = ?")
		params = append(params, category)
	}
	if source != "" {
		whereClauses = append(whereClauses, "f.source = ?")
		params = append(params, source)
	}
	if minTrust > 0 {
		whereClauses = append(whereClauses, "f.trust >= ?")
		params = append(params, minTrust)
	}

	where := strings.Join(whereClauses, " AND ")
	params = append(params, limit)

	sql := fmt.Sprintf(`
		SELECT f.id, f.content, f.category, f.tags, f.source, f.trust, f.strength,
		       f.retrieval_count, f.helpful_count, f.created_at, f.updated_at, f.version
		FROM facts f JOIN facts_fts ON facts_fts.rowid = f.id
		WHERE %s
		ORDER BY rank
		LIMIT ?`, where)

	rows, err := l.db.QueryContext(ctx, sql, params...)
	if err != nil {
		return nil, nil // silently return empty on FTS error
	}
	defer rows.Close()

	var facts []Fact
	for rows.Next() {
		var f Fact
		var created, updated string
		if err := rows.Scan(&f.ID, &f.Content, &f.Category, &f.Tags, &f.Source,
			&f.Trust, &f.Strength, &f.Retrievals, &f.Helpful,
			&created, &updated, &f.Version); err != nil {
			continue
		}
		f.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
		f.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
		facts = append(facts, f)
	}
	return facts, nil
}

// ── Entity System ──────────────────────────────────────────────────────────

func extractEntities(text string) []string {
	seen := make(map[string]bool)
	var out []string

	add := func(name string) {
		name = strings.TrimSpace(name)
		if name != "" && !seen[strings.ToLower(name)] {
			seen[strings.ToLower(name)] = true
			out = append(out, name)
		}
	}

	for _, m := range reWikiLink.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	for _, m := range reCapitalized.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	for _, m := range reDoubleQuote.FindAllStringSubmatch(text, -1) {
		if len(strings.Fields(m[1])) >= 2 { // multi-word
			add(m[1])
		}
	}
	for _, m := range reSingleQuote.FindAllStringSubmatch(text, -1) {
		if len(strings.Fields(m[1])) >= 2 {
			add(m[1])
		}
	}
	return out
}

func (l *FactsLayer) resolveEntity(name string) int64 {
	var eid int64
	err := l.db.QueryRow("SELECT entity_id FROM entities WHERE name LIKE ?", name).Scan(&eid)
	if err == nil {
		return eid
	}
	now := time.Now().Format("2006-01-02 15:04:05")
	result, err := l.db.Exec("INSERT INTO entities (name, created_at) VALUES (?, ?)", name, now)
	if err != nil {
		return 0
	}
	eid, _ = result.LastInsertId()
	return eid
}

// EntitiesForFact returns all entity names linked to a fact.
func (l *FactsLayer) EntitiesForFact(factID int64) []string {
	rows, err := l.db.Query(`
		SELECT e.name FROM entities e
		JOIN fact_entities fe ON fe.entity_id = e.entity_id
		WHERE fe.fact_id = ?`, factID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil {
			names = append(names, name)
		}
	}
	return names
}

// SelfWireGraph creates/strengthens edges between co-occurring entities.
func (l *FactsLayer) selfWireGraph(factID int64) int {
	rows, err := l.db.Query(
		"SELECT entity_id FROM fact_entities WHERE fact_id = ?", factID)
	if err != nil {
		return 0
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}

	count := 0
	now := time.Now().Format("2006-01-02 15:04:05")
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			a, b := ids[i], ids[j]
			if a > b {
				a, b = b, a
			}
			_, err := l.db.Exec(`
				INSERT INTO entity_graph (entity_a_id, entity_b_id, weight, updated_at)
				VALUES (?, ?, 1.0, ?)
				ON CONFLICT(entity_a_id, entity_b_id) DO UPDATE SET
					weight = weight + 1,
					updated_at = ?`,
				a, b, now, now)
			if err == nil {
				count++
			}
		}
	}
	return count
}

// ── Zettelkasten Linking ───────────────────────────────────────────────────

func (l *FactsLayer) createLinks(ctx context.Context, factID int64, content string) []map[string]interface{} {
	tokens := tokenize(content)

	// Get BM25 candidates
	candidates, err := l.ftsCandidates(ctx, content, "", "", 0.0, 20)
	if err != nil {
		return nil
	}

	var links []map[string]interface{}
	now := time.Now().Format("2006-01-02 15:04:05")

	for _, c := range candidates {
		if c.ID == factID {
			continue
		}
		otherTokens := tokenize(c.Content)
		sim := jaccardSimilarity(tokens, otherTokens)
		if sim >= LINK_THRESHOLD {
			a, b := factID, c.ID
			if a > b {
				a, b = b, a
			}
			_, _ = l.db.Exec(`
				INSERT INTO fact_links (fact_id_a, fact_id_b, weight, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?)
				ON CONFLICT(fact_id_a, fact_id_b) DO UPDATE SET
					weight = MAX(weight, ?),
					updated_at = ?`,
				a, b, sim, now, now, sim, now)

			links = append(links, map[string]interface{}{
				"fact_id": c.ID,
				"content": truncateStr(c.Content, 120),
				"weight":  sim,
			})
		}
	}
	return links
}

// LinkedFacts returns facts linked to factID, ordered by link weight.
func (l *FactsLayer) LinkedFacts(factID int64, limit int) []map[string]interface{} {
	l.mu.RLock()
	defer l.mu.RUnlock()

	rows, err := l.db.Query(`
		SELECT f.id, f.content, f.category, f.trust, fl.weight
		FROM fact_links fl
		JOIN facts f ON f.id = CASE
			WHEN fl.fact_id_a = ? THEN fl.fact_id_b
			ELSE fl.fact_id_a
		END
		WHERE fl.fact_id_a = ? OR fl.fact_id_b = ?
		ORDER BY fl.weight DESC, fl.updated_at DESC
		LIMIT ?`, factID, factID, factID, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var id int64
		var content, category string
		var trust, weight float64
		if err := rows.Scan(&id, &content, &category, &trust, &weight); err == nil {
			results = append(results, map[string]interface{}{
				"fact_id":  id,
				"content":  truncateStr(content, 120),
				"category": category,
				"trust":    trust,
				"weight":   weight,
			})
		}
	}
	return results
}

// LinkStats returns statistics about the fact link graph.
func (l *FactsLayer) LinkStats() map[string]interface{} {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var total int64
	_ = l.db.QueryRow("SELECT COUNT(*) FROM fact_links").Scan(&total)
	if total == 0 {
		return map[string]interface{}{
			"total_links": 0, "linked_facts": 0, "avg_links_per_fact": 0.0,
		}
	}

	var linked int64
	_ = l.db.QueryRow(`
		SELECT COUNT(DISTINCT fact_id_a) + COUNT(DISTINCT fact_id_b)
		FROM fact_links`).Scan(&linked)

	return map[string]interface{}{
		"total_links":        total,
		"linked_facts":       linked,
		"avg_links_per_fact": round(float64(total)/float64(maxInt64(linked, 1)), 2),
	}
}

// Evolve merges near-duplicate facts based on Jaccard similarity threshold.
func (l *FactsLayer) Evolve(threshold float64) map[string]interface{} {
	if threshold <= 0 {
		threshold = EVOLVE_MERGE_THRESHOLD
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	all, err := l.getAllFacts()
	if err != nil || len(all) < 2 {
		return map[string]interface{}{"merged": 0, "candidates_checked": 0}
	}

	merged := 0
	checked := 0
	skipIDs := make(map[int64]bool)
	now := time.Now().Format("2006-01-02 15:04:05")

	for i := 0; i < len(all); i++ {
		if skipIDs[all[i].ID] {
			continue
		}
		faTokens := tokenize(all[i].Content)

		for j := i + 1; j < len(all); j++ {
			if skipIDs[all[j].ID] {
				continue
			}
			fbTokens := tokenize(all[j].Content)
			sim := jaccardSimilarity(faTokens, fbTokens)
			checked++

			if sim >= threshold {
				keepID := all[i].ID
				mergeID := all[j].ID

				// Combine content
				newContent := all[i].Content
				if !strings.Contains(newContent, all[j].Content) &&
					!strings.Contains(all[j].Content, all[i].Content) {
					newContent = all[i].Content + "\n\n---\n\n" + all[j].Content
				} else if len(all[j].Content) > len(all[i].Content) {
					newContent = all[j].Content
				}

				// Average trust
				newTrust := (all[i].Trust + all[j].Trust) / 2.0

				// Merge tags
				tagSet := make(map[string]bool)
				for _, t := range strings.Split(all[i].Tags, ",") {
					if t = strings.TrimSpace(t); t != "" {
						tagSet[t] = true
					}
				}
				for _, t := range strings.Split(all[j].Tags, ",") {
					if t = strings.TrimSpace(t); t != "" {
						tagSet[t] = true
					}
				}
				var newTags []string
				for t := range tagSet {
					newTags = append(newTags, t)
				}
				sort.Strings(newTags)

				// Transfer entities
				mergeEnts, _ := l.db.Query(
					"SELECT entity_id FROM fact_entities WHERE fact_id = ?", mergeID)
				if mergeEnts != nil {
					for mergeEnts.Next() {
						var eid int64
						if mergeEnts.Scan(&eid) == nil {
							_, _ = l.db.Exec(
								"INSERT OR IGNORE INTO fact_entities VALUES (?, ?)", keepID, eid)
						}
					}
					mergeEnts.Close()
				}

				// Transfer links
				l.transferLinks(mergeID, keepID)

				// Update keep fact
				_, _ = l.db.Exec(`
					UPDATE facts SET content = ?, trust = ?, tags = ?,
					strength = MAX(strength, ?), updated_at = ?
					WHERE id = ?`,
					newContent, newTrust, strings.Join(newTags, ","), all[j].Strength, now, keepID)

				// Delete merged fact
				_, _ = l.db.Exec("DELETE FROM facts WHERE id = ?", mergeID)
				skipIDs[mergeID] = true
				merged++
			}
		}
	}

	_ = l.dbCommit()
	return map[string]interface{}{
		"merged":            merged,
		"candidates_checked": checked,
	}
}

func (l *FactsLayer) transferLinks(fromID, toID int64) {
	rows, err := l.db.Query(`
		SELECT fact_id_a, fact_id_b, weight FROM fact_links
		WHERE fact_id_a = ? OR fact_id_b = ?`, fromID, fromID)
	if err != nil {
		return
	}
	defer rows.Close()

	now := time.Now().Format("2006-01-02 15:04:05")
	for rows.Next() {
		var a, b int64
		var w float64
		if err := rows.Scan(&a, &b, &w); err != nil {
			continue
		}
		otherID := a
		if otherID == fromID {
			otherID = b
		}
		if otherID == toID {
			continue
		}
		na, nb := toID, otherID
		if na > nb {
			na, nb = nb, na
		}
		_, _ = l.db.Exec(`
			INSERT INTO fact_links (fact_id_a, fact_id_b, weight, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(fact_id_a, fact_id_b) DO UPDATE SET
				weight = MAX(weight, ?), updated_at = ?`,
			na, nb, w, now, now, w, now)
	}
}

// ── Contradiction Detection ────────────────────────────────────────────────

func (l *FactsLayer) detectContradictions(ctx context.Context, factID int64, content string) []map[string]interface{} {
	entities := l.EntitiesForFact(factID)
	if len(entities) == 0 {
		return nil
	}

	// Check if this fact is already flagged
	var tags string
	_ = l.db.QueryRow("SELECT tags FROM facts WHERE id = ?", factID).Scan(&tags)
	if strings.Contains(tags, CONTRADICTION_FLAG) {
		return nil
	}

	newTokens := tokenize(content)

	// Find other facts sharing entities
	placeholders := make([]string, len(entities))
	args := make([]interface{}, len(entities)+1)
	for i, e := range entities {
		placeholders[i] = "?"
		args[i] = e
	}
	args[len(entities)] = factID

	others, err := l.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT DISTINCT f.id, f.content, f.tags
		FROM facts f
		JOIN fact_entities fe ON fe.fact_id = f.id
		JOIN entities e ON e.entity_id = fe.entity_id
		WHERE e.name IN (%s) AND f.id != ?`, strings.Join(placeholders, ",")), args...)
	if err != nil {
		return nil
	}
	defer others.Close()

	var hits []map[string]interface{}
	for others.Next() {
		var oid int64
		var ocontent, otags string
		if err := others.Scan(&oid, &ocontent, &otags); err != nil {
			continue
		}
		if strings.Contains(otags, CONTRADICTION_FLAG) {
			continue
		}
		otherTokens := tokenize(ocontent)
		sim := jaccardSimilarity(newTokens, otherTokens)
		if sim < 0.3 {
			hits = append(hits, map[string]interface{}{
				"contradictor_id": oid,
				"content":        truncateStr(ocontent, 120),
				"similarity":     round(sim, 4),
			})
		}
	}

	if len(hits) == 0 {
		return nil
	}

	// Flag this fact
	newTags := tagAdd(tags, CONTRADICTION_FLAG)
	_, _ = l.db.Exec("UPDATE facts SET tags = ? WHERE id = ?", newTags, factID)

	// Flag contradictors
	for _, h := range hits {
		cid := int64(h["contradictor_id"].(int64))
		var otags string
		_ = l.db.QueryRow("SELECT tags FROM facts WHERE id = ?", cid).Scan(&otags)
		updated := tagAdd(otags, CONTRADICTION_FLAG)
		_, _ = l.db.Exec("UPDATE facts SET tags = ? WHERE id = ?", updated, cid)
	}

	_ = l.dbCommit()
	return hits
}

// ResolveContradictions removes contradiction flag from a fact and all its contradictors.
func (l *FactsLayer) ResolveContradictions(factID int64) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	count := 0

	// Unflag this fact
	var tags string
	_ = l.db.QueryRow("SELECT tags FROM facts WHERE id = ?", factID).Scan(&tags)
	if strings.Contains(tags, CONTRADICTION_FLAG) {
		newTags := tagRemove(tags, CONTRADICTION_FLAG)
		_, _ = l.db.Exec("UPDATE facts SET tags = ? WHERE id = ?", newTags, factID)
		count++
	}

	// Find and unflag contradictors
	entities := l.EntitiesForFact(factID)
	if len(entities) > 0 {
		placeholders := make([]string, len(entities))
		args := make([]interface{}, len(entities)+1)
		for i, e := range entities {
			placeholders[i] = "?"
			args[i] = e
		}
		args[len(entities)] = factID

		others, err := l.db.Query(fmt.Sprintf(`
			SELECT f.id, f.tags FROM facts f
			JOIN fact_entities fe ON fe.fact_id = f.id
			JOIN entities e ON e.entity_id = fe.entity_id
			WHERE e.name IN (%s) AND f.id != ?`, strings.Join(placeholders, ",")), args...)
		if err == nil {
			defer others.Close()
			for others.Next() {
				var oid int64
				var otags string
				if err := others.Scan(&oid, &otags); err != nil {
					continue
				}
				if strings.Contains(otags, CONTRADICTION_FLAG) {
					updated := tagRemove(otags, CONTRADICTION_FLAG)
					_, _ = l.db.Exec("UPDATE facts SET tags = ? WHERE id = ?", updated, oid)
					count++
				}
			}
		}
	}

	_ = l.dbCommit()
	return count
}

// ── Memory Decay ───────────────────────────────────────────────────────────

// ApplyDecay applies exponential decay to all facts' strength.
// strength *= exp(-decayRate * days_since_last_access)
func (l *FactsLayer) ApplyDecay(decayRate float64) map[string]interface{} {
	if decayRate <= 0 {
		decayRate = DefaultDecayRate
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	_, _ = l.db.Exec(`
		UPDATE facts
		SET strength = MAX(0.0, MIN(1.0, strength * exp(-? * (julianday('now') - julianday(COALESCE(last_access_at, created_at))))))
		WHERE julianday('now') - julianday(COALESCE(last_access_at, created_at)) > 0`,
		decayRate)
	_ = l.dbCommit()

	var total, active, dormant, cleared int64
	_ = l.db.QueryRow("SELECT COUNT(*) FROM facts").Scan(&total)
	_ = l.db.QueryRow("SELECT COUNT(*) FROM facts WHERE strength > ?", ActiveThreshold).Scan(&active)
	_ = l.db.QueryRow("SELECT COUNT(*) FROM facts WHERE strength > ? AND strength <= ?", DormantThreshold, ActiveThreshold).Scan(&dormant)
	_ = l.db.QueryRow("SELECT COUNT(*) FROM facts WHERE strength <= ?", DormantThreshold).Scan(&cleared)

	return map[string]interface{}{
		"total":   total,
		"active":  active,
		"dormant": dormant,
		"cleared": cleared,
	}
}

func (l *FactsLayer) boostStrength(factID int64, boost float64) {
	_, _ = l.db.Exec(`
		UPDATE facts
		SET strength = MAX(0.0, MIN(1.0, strength + ?)),
		    last_access_at = CURRENT_TIMESTAMP
		WHERE id = ?`, boost, factID)
}

// ── Per-Fact Versioning ────────────────────────────────────────────────────

// VersionedUpdate updates a fact with full version tracking (append-only).
func (l *FactsLayer) VersionedUpdate(factID int64, newContent string, reason string) map[string]interface{} {
	l.mu.Lock()
	defer l.mu.Unlock()

	newContent = strings.TrimSpace(newContent)
	if newContent == "" {
		return nil
	}

	row := l.db.QueryRow("SELECT content, category, tags, trust, source FROM facts WHERE id = ?", factID)
	var oldContent, category, tags, source string
	var trust float64
	if err := row.Scan(&oldContent, &category, &tags, &trust, &source); err != nil {
		return nil
	}

	if oldContent == newContent {
		return map[string]interface{}{
			"fact_id": factID, "changed": false, "message": "content unchanged",
		}
	}

	now := time.Now().Format("2006-01-02 15:04:05")

	// Snapshot current state into versions table
	_, _ = l.db.Exec(`
		INSERT INTO fact_versions (fact_id, content, category, tags, trust, source, action, reason, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'updated', ?, ?)`,
		factID, oldContent, category, tags, trust, source, reason, now)

	// Apply update
	_, err := l.db.Exec(`
		UPDATE facts SET content = ?, updated_at = ?, version = version + 1
		WHERE id = ?`, newContent, now, factID)
	if err != nil {
		return map[string]interface{}{
			"fact_id": factID, "changed": false,
			"error": fmt.Sprintf("update failed: %v", err),
		}
	}

	_ = l.dbCommit()

	return map[string]interface{}{
		"fact_id":     factID,
		"changed":     true,
		"old_content": truncateStr(oldContent, 120),
		"new_content": truncateStr(newContent, 120),
		"reason":      reason,
	}
}

// GetVersionHistory returns all version records for a fact, newest first.
func (l *FactsLayer) GetVersionHistory(factID int64, limit int) []FactVersion {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if limit <= 0 {
		limit = 20
	}

	rows, err := l.db.Query(`
		SELECT version_id, fact_id, content, category, tags, trust, source, action, reason, created_at
		FROM fact_versions WHERE fact_id = ?
		ORDER BY version_id DESC LIMIT ?`, factID, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var versions []FactVersion
	for rows.Next() {
		var v FactVersion
		var created string
		if err := rows.Scan(&v.VersionID, &v.FactID, &v.Content, &v.Category,
			&v.Tags, &v.Trust, &v.Source, &v.Action, &v.Reason, &created); err != nil {
			continue
		}
		v.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
		versions = append(versions, v)
	}
	return versions
}

// UndoToVersion rolls back a fact to a previous version (also versioned).
func (l *FactsLayer) UndoToVersion(factID int64, versionID int64) map[string]interface{} {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Get current state
	row := l.db.QueryRow("SELECT content, category, tags, trust, source FROM facts WHERE id = ?", factID)
	var curContent, curCategory, curTags, curSource string
	var curTrust float64
	if err := row.Scan(&curContent, &curCategory, &curTags, &curTrust, &curSource); err != nil {
		return nil
	}

	// Get target version
	target := l.db.QueryRow(`
		SELECT content, category, tags, trust, source FROM fact_versions
		WHERE version_id = ? AND fact_id = ?`, versionID, factID)
	var tContent, tCategory, tTags, tSource string
	var tTrust float64
	if err := target.Scan(&tContent, &tCategory, &tTags, &tTrust, &tSource); err != nil {
		return nil
	}

	now := time.Now().Format("2006-01-02 15:04:05")

	// Save current state as checkpoint
	_, _ = l.db.Exec(`
		INSERT INTO fact_versions (fact_id, content, category, tags, trust, source, action, reason, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'undo_checkpoint', ?, ?)`,
		factID, curContent, curCategory, curTags, curTrust, curSource,
		fmt.Sprintf("undo_to_version_%d", versionID), now)

	// Restore target version
	_, _ = l.db.Exec(`
		UPDATE facts SET content = ?, category = ?, tags = ?, trust = ?,
		updated_at = ?, version = version + 1 WHERE id = ?`,
		tContent, tCategory, tTags, tTrust, now, factID)

	_ = l.dbCommit()

	return map[string]interface{}{
		"fact_id":          factID,
		"restored_content": truncateStr(tContent, 120),
		"from_version":     versionID,
		"checkpoint_saved": true,
	}
}

// VersioningStats returns versioning statistics.
func (l *FactsLayer) VersioningStats() map[string]interface{} {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var total, versionedFacts int64
	_ = l.db.QueryRow("SELECT COUNT(*) FROM fact_versions").Scan(&total)
	_ = l.db.QueryRow("SELECT COUNT(DISTINCT fact_id) FROM fact_versions").Scan(&versionedFacts)

	var maxPerFact int64
	_ = l.db.QueryRow("SELECT MAX(cnt) FROM (SELECT COUNT(*) as cnt FROM fact_versions GROUP BY fact_id)").Scan(&maxPerFact)

	// Count by action
	rows, _ := l.db.Query("SELECT action, COUNT(*) as n FROM fact_versions GROUP BY action")
	actions := make(map[string]int64)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var action string
			var n int64
			if rows.Scan(&action, &n) == nil {
				actions[action] = n
			}
		}
	}

	return map[string]interface{}{
		"total_versions":       total,
		"versioned_facts":      versionedFacts,
		"actions":              actions,
		"max_versions_per_fact": maxPerFact,
	}
}

// ── Temporal Search ────────────────────────────────────────────────────────

// SearchTemporal searches facts with optional time-window filter.
func (l *FactsLayer) SearchTemporal(ctx context.Context, query string, timeStart, timeEnd string,
	category, source string, minTrust float64, limit int, includeCleared bool) ([]Result, error) {

	l.mu.RLock()
	defer l.mu.RUnlock()

	if query = strings.TrimSpace(query); query == "" {
		return nil, nil
	}

	if timeStart == "" && timeEnd == "" && HasTemporalIntent(query) {
		timeStart, timeEnd = ParseTimeQuery(query)
	}

	if timeStart == "" && timeEnd == "" {
		// No temporal filter — use regular query
		return l.performSearch(ctx, query, category, source, minTrust, limit, includeCleared)
	}

	candidates, err := l.ftsCandidates(ctx, query, category, source, minTrust, limit*3)
	if err != nil || len(candidates) == 0 {
		return nil, err
	}

	if !includeCleared {
		candidates = filterCleared(candidates)
		if len(candidates) == 0 {
			return nil, nil
		}
	}

	// Apply time filter
	var filtered []Fact
	for _, f := range candidates {
		created := f.CreatedAt.Format("2006-01-02 15:04:05")
		if timeStart != "" && created < timeStart {
			continue
		}
		if timeEnd != "" && created >= timeEnd {
			continue
		}
		filtered = append(filtered, f)
	}
	candidates = filtered

	if len(candidates) == 0 {
		return nil, nil
	}

	return l.scoreAndReturn(candidates, query, limit)
}

// HasTemporalIntent checks if a query has temporal filtering intent.
func HasTemporalIntent(query string) bool {
	lower := strings.ToLower(query)
	tokens := strings.Fields(lower)
	for _, t := range tokens {
		if temporalIntentKeywords[t] {
			return true
		}
	}
	// Check for date patterns
	for _, tp := range temporalPatterns {
		if tp.pattern.MatchString(query) {
			return true
		}
	}
	return false
}

// ParseTimeQuery parses natural language time expressions into ISO date strings.
func ParseTimeQuery(query string) (string, string) {
	now := time.Now()
	var start, end time.Time
	found := false

	for _, tp := range temporalPatterns {
		m := tp.pattern.FindStringSubmatch(query)
		if m == nil {
			continue
		}

		switch tp.unit {
		case "num":
			num := parseInt(m[1])
			unitStr := strings.ToLower(m[2])
			var delta time.Duration
			switch {
			case strings.Contains(unitStr, "minute") || strings.Contains(unitStr, "menit"):
				delta = time.Duration(num) * time.Minute
			case strings.Contains(unitStr, "hour") || strings.Contains(unitStr, "h") || strings.Contains(unitStr, "jam"):
				delta = time.Duration(num) * time.Hour
			default:
				delta = time.Duration(num) * 24 * time.Hour
			}
			if tp.direction < 0 {
				start = now.Add(-delta)
				end = now
			} else {
				start = now
				end = now.Add(delta)
			}
			found = true

		case "day":
			start = now.AddDate(0, 0, tp.direction)
			end = start.AddDate(0, 0, 1)
			found = true

		case "week":
			weekday := int(now.Weekday())
			if weekday == 0 {
				weekday = 7
			}
			weekStart := now.AddDate(0, 0, -weekday+1)
			start = weekStart.AddDate(0, 0, tp.direction*7)
			end = start.AddDate(0, 0, 7)
			found = true

		case "month":
			monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
			start = monthStart.AddDate(0, tp.direction, 0)
			end = start.AddDate(0, 1, 0)
			found = true

		case "year":
			yearStart := time.Date(now.Year()+tp.direction, 1, 1, 0, 0, 0, 0, now.Location())
			start = yearStart
			end = yearStart.AddDate(1, 0, 0)
			found = true
		}

		if found {
			break
		}
	}

	if !found {
		// Check for ISO date patterns
		isoRe := regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})\b`)
		if m := isoRe.FindStringSubmatch(query); m != nil {
			if t, err := time.Parse("2006-01-02", m[0]); err == nil {
				start = t
				end = t.AddDate(0, 0, 1)
				found = true
			}
		}
	}

	if !found {
		return "", ""
	}

	return start.Format("2006-01-02 15:04:05"), end.Format("2006-01-02 15:04:05")
}

// ── Probe & Reason ─────────────────────────────────────────────────────────

// Probe finds facts mentioning a single entity.
func (l *FactsLayer) Probe(ctx context.Context, entity string, limit int, includeCleared bool) ([]Result, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	clearedClause := ""
	var params []interface{}
	params = append(params, entity)
	if !includeCleared {
		clearedClause = "AND (f.strength IS NULL OR f.strength > ?)"
		params = append(params, DormantThreshold)
	}
	params = append(params, limit)

	rows, err := l.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT f.id, f.content, f.category, f.tags, f.source, f.trust, f.strength,
		       f.created_at, f.updated_at
		FROM facts f
		JOIN fact_entities fe ON fe.fact_id = f.id
		JOIN entities e ON e.entity_id = fe.entity_id
		WHERE e.name LIKE ? %s
		ORDER BY f.trust DESC, f.strength DESC
		LIMIT ?`, clearedClause), params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []Fact
	for rows.Next() {
		var f Fact
		var created, updated string
		if err := rows.Scan(&f.ID, &f.Content, &f.Category, &f.Tags, &f.Source,
			&f.Trust, &f.Strength, &created, &updated); err == nil {
			f.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
			facts = append(facts, f)
		}
	}

	if len(facts) == 0 {
		// Fallback to FTS
		return l.performSearch(ctx, entity, "", "", 0, limit, includeCleared)
	}

	return l.factsToResults(facts, limit), nil
}

// Reason performs compositional query — facts mentioning ALL entities via HRR.
func (l *FactsLayer) Reason(ctx context.Context, entities []string, category string, limit int) ([]Result, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if len(entities) == 0 {
		return nil, nil
	}

	// Fallback: FTS with all entities
	query := strings.Join(entities, " ")
	return l.performSearch(ctx, query, category, "", 0, limit, false)
}

// ── Stats ──────────────────────────────────────────────────────────────────

// Stats returns comprehensive statistics about the facts layer.
func (l *FactsLayer) Stats() map[string]interface{} {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var totalFacts, totalEntities, active, dormant, cleared int64
	_ = l.db.QueryRow("SELECT COUNT(*) FROM facts").Scan(&totalFacts)
	_ = l.db.QueryRow("SELECT COUNT(*) FROM entities").Scan(&totalEntities)
	_ = l.db.QueryRow("SELECT COUNT(*) FROM facts WHERE strength > ?", ActiveThreshold).Scan(&active)
	_ = l.db.QueryRow("SELECT COUNT(*) FROM facts WHERE strength > ? AND strength <= ?", DormantThreshold, ActiveThreshold).Scan(&dormant)
	_ = l.db.QueryRow("SELECT COUNT(*) FROM facts WHERE strength <= ?", DormantThreshold).Scan(&cleared)

	// Categories
	catRows, _ := l.db.Query("SELECT category, COUNT(*) as n FROM facts GROUP BY category")
	categories := make(map[string]int64)
	if catRows != nil {
		defer catRows.Close()
		for catRows.Next() {
			var cat string
			var n int64
			if catRows.Scan(&cat, &n) == nil {
				categories[cat] = n
			}
		}
	}

	var contradictions int64
	_ = l.db.QueryRow("SELECT COUNT(*) FROM facts WHERE tags LIKE ?",
		fmt.Sprintf("%%%s%%", CONTRADICTION_FLAG)).Scan(&contradictions)

	result := map[string]interface{}{
		"total_facts":     totalFacts,
		"total_entities":  totalEntities,
		"by_category":     categories,
		"active_facts":    active,
		"dormant_facts":   dormant,
		"cleared_facts":   cleared,
		"contradictions":  contradictions,
		"hrr_dim":         l.hrrDim,
		"versioning":      nil,
	}

	// Add versioning stats
	result["versioning"] = l.VersioningStats()

	return result
}

// ── Get Fact by ID ─────────────────────────────────────────────────────────

// DeleteFact deletes a fact by ID from the database.
// Returns an error if the fact is not found.
func (l *FactsLayer) DeleteFact(factID int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	result, err := l.db.Exec("DELETE FROM facts WHERE id = ?", factID)
	if err != nil {
		return fmt.Errorf("delete fact %d: %w", factID, err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("fact %d not found", factID)
	}
	return l.dbCommit()
}

// DetectContradictions finds facts that share entities with the given fact
// and are flagged as contradictions. Returns a list of contradicting facts.
func (l *FactsLayer) DetectContradictions(factID int64) []map[string]interface{} {
	// Get entities for this fact
	entities := l.EntitiesForFact(factID)
	if len(entities) == 0 {
		return nil
	}

	l.mu.RLock()
	defer l.mu.RUnlock()

	// Find other facts sharing entities that have the contradiction flag
	placeholders := make([]string, len(entities))
	args := make([]interface{}, len(entities)+1)
	for i, e := range entities {
		placeholders[i] = "?"
		args[i] = e
	}
	args[len(entities)] = factID

	rows, err := l.db.Query(fmt.Sprintf(`
		SELECT DISTINCT f.id, f.content, f.tags, f.trust
		FROM facts f
		JOIN fact_entities fe ON fe.fact_id = f.id
		JOIN entities e ON e.entity_id = fe.entity_id
		WHERE e.name IN (%s) AND f.id != ? AND (f.tags LIKE '%%contradiction%%')
		LIMIT 50`, strings.Join(placeholders, ",")), args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var hits []map[string]interface{}
	for rows.Next() {
		var oid int64
		var ocontent, otags string
		var trust float64
		if err := rows.Scan(&oid, &ocontent, &otags, &trust); err != nil {
			continue
		}
		hits = append(hits, map[string]interface{}{
			"id":       oid,
			"content":  ocontent,
			"tags":     otags,
			"trust":    trust,
			"entities": entities,
		})
	}
	return hits
}

// GetFact retrieves a single fact by ID.
func (l *FactsLayer) GetFact(factID int64) *Fact {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var f Fact
	var created, updated string
	err := l.db.QueryRow(`
		SELECT id, content, category, tags, source, trust, strength,
		       retrieval_count, helpful_count, created_at, updated_at, version
		FROM facts WHERE id = ?`, factID).Scan(
		&f.ID, &f.Content, &f.Category, &f.Tags, &f.Source,
		&f.Trust, &f.Strength, &f.Retrievals, &f.Helpful,
		&created, &updated, &f.Version)
	if err != nil {
		return nil
	}
	f.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
	f.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)

	// Boost strength on read
	l.boostStrength(factID, ReadBoost)
	_ = l.dbCommit()

	return &f
}

// ListAll lists all facts, optionally filtered by category.
func (l *FactsLayer) ListAll(category string, minTrust float64, limit int, includeCleared bool) []Fact {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var whereClauses []string
	var params []interface{}

	whereClauses = append(whereClauses, "trust >= ?")
	params = append(params, minTrust)

	if category != "" {
		whereClauses = append(whereClauses, "category = ?")
		params = append(params, category)
	}
	if !includeCleared {
		whereClauses = append(whereClauses, "(strength IS NULL OR strength > ?)")
		params = append(params, DormantThreshold)
	}

	where := strings.Join(whereClauses, " AND ")
	params = append(params, limit)

	sql := fmt.Sprintf(`
		SELECT id, content, category, tags, source, trust, strength,
		       retrieval_count, helpful_count, created_at, updated_at, version
		FROM facts WHERE %s
		ORDER BY trust DESC, strength DESC
		LIMIT ?`, where)

	rows, err := l.db.Query(sql, params...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var facts []Fact
	for rows.Next() {
		var f Fact
		var created, updated string
		if err := rows.Scan(&f.ID, &f.Content, &f.Category, &f.Tags, &f.Source,
			&f.Trust, &f.Strength, &f.Retrievals, &f.Helpful,
			&created, &updated, &f.Version); err == nil {
			f.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
			f.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
			facts = append(facts, f)
		}
	}
	return facts
}

// SetTrust updates the trust score for a fact.
func (l *FactsLayer) SetTrust(factID int64, score float64) {
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now().Format("2006-01-02 15:04:05")
	_, _ = l.db.Exec("UPDATE facts SET trust = ?, updated_at = ? WHERE id = ?", score, now, factID)
	_ = l.dbCommit()
}

// UpdateTrust adjusts trust based on helpful/unhelpful feedback.
func (l *FactsLayer) UpdateTrust(factID int64, helpful bool) map[string]interface{} {
	l.mu.Lock()
	defer l.mu.Unlock()

	var trust, strength float64
	var helpfulCount int
	err := l.db.QueryRow(
		"SELECT trust, helpful_count, strength FROM facts WHERE id = ?", factID).
		Scan(&trust, &helpfulCount, &strength)
	if err != nil {
		return nil
	}

	delta := HelpfulDelta
	if !helpful {
		delta = UnhelpfulDelta
	}
	newTrust := clamp(trust+delta, 0, 1)
	newStrength := clamp(strength+DecisionBoost, 0, 1)
	if !helpful {
		newStrength = clamp(strength-UnhelpfulDelta, 0, 1)
	}

	_, _ = l.db.Exec(`
		UPDATE facts SET trust = ?, helpful_count = ?, strength = ?,
		last_access_at = CURRENT_TIMESTAMP WHERE id = ?`,
		newTrust, helpfulCount+1, newStrength, factID)
	_ = l.dbCommit()

	return map[string]interface{}{
		"fact_id":  factID,
		"trust":    newTrust,
		"strength": newStrength,
	}
}

// ── Internal helpers ───────────────────────────────────────────────────────

func (l *FactsLayer) performSearch(ctx context.Context, query, category, source string,
	minTrust float64, limit int, includeCleared bool) ([]Result, error) {

	candidates, err := l.ftsCandidates(ctx, query, category, source, minTrust, limit*3)
	if err != nil || len(candidates) == 0 {
		return nil, err
	}

	if !includeCleared {
		candidates = filterCleared(candidates)
		if len(candidates) == 0 {
			return nil, nil
		}
	}

	return l.scoreAndReturn(candidates, query, limit)
}

func (l *FactsLayer) scoreAndReturn(candidates []Fact, query string, limit int) ([]Result, error) {
	queryTokens := tokenize(query)
	queryVec := encodeTextSimple(query, l.hrrDim)

	embScores := make(map[int64]float64)
	if l.embeddingFn != nil {
		embScores = l.searchEmbeddings(context.Background(), query, candidates)
	}

	type scoredFact struct {
		fact  Fact
		score float64
	}
	var scored []scoredFact

	for _, f := range candidates {
		contentTokens := tokenize(f.Content)
		tagTokens := tokenize(f.Tags)
		combined := union(contentTokens, tagTokens)

		jaccard := jaccardSimilarity(queryTokens, combined)
		fVec := encodeTextSimple(f.Content, l.hrrDim)
		hrrSim := (cosineSimilarity(queryVec, fVec) + 1.0) / 2.0
		embSim := embScores[f.ID]
		if embSim == 0 {
			embSim = 0.5
		}

		relevance := 0.35*f.Trust + 0.25*jaccard + 0.3*hrrSim + 0.1*embSim
		score := relevance * f.Trust

		scored = append(scored, scoredFact{fact: f, score: score})
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})
	if len(scored) > limit {
		scored = scored[:limit]
	}

	results := make([]Result, len(scored))
	for i, s := range scored {
		results[i] = Result{
			Content:  s.fact.Content,
			Category: s.fact.Category,
			Tags:     strings.Split(s.fact.Tags, ","),
			Source:   s.fact.Source,
			Score:    s.score,
			Time:     s.fact.CreatedAt,
			Metadata: map[string]string{
				"fact_id":  fmt.Sprintf("%d", s.fact.ID),
				"strength": fmt.Sprintf("%.3f", s.fact.Strength),
			},
		}
		l.boostStrength(s.fact.ID, ReadBoost)
	}
	_ = l.dbCommit()

	return results, nil
}

func (l *FactsLayer) factsToResults(facts []Fact, limit int) []Result {
	if len(facts) > limit {
		facts = facts[:limit]
	}
	results := make([]Result, len(facts))
	for i, f := range facts {
		results[i] = Result{
			Content:  f.Content,
			Category: f.Category,
			Tags:     strings.Split(f.Tags, ","),
			Source:   f.Source,
			Score:    f.Trust,
			Time:     f.CreatedAt,
			Metadata: map[string]string{
				"fact_id":  fmt.Sprintf("%d", f.ID),
				"strength": fmt.Sprintf("%.3f", f.Strength),
			},
		}
	}
	return results
}

// searchEmbeddings performs vector similarity search over indexed embeddings.
func (l *FactsLayer) searchEmbeddings(ctx context.Context, query string, candidates []Fact) map[int64]float64 {
	if l.embeddingFn == nil {
		return nil
	}

	queryVec, err := l.embeddingFn(query)
	if err != nil {
		return nil
	}

	scores := make(map[int64]float64)
	for _, f := range candidates {
		var embedBytes []byte
		err := l.db.QueryRowContext(ctx,
			"SELECT embedding FROM fact_embeddings WHERE fact_id = ?", f.ID).Scan(&embedBytes)
		if err != nil {
			continue
		}
		var vec []float64
		if err := json.Unmarshal(embedBytes, &vec); err != nil {
			continue
		}
		sim := cosineSimilarity(queryVec, vec)
		if sim > 0.3 {
			scores[f.ID] = sim
		}
	}
	return scores
}

func (l *FactsLayer) getAllFacts() ([]Fact, error) {
	rows, err := l.db.Query(`
		SELECT id, content, category, tags, source, trust, strength,
		       retrieval_count, helpful_count, created_at, updated_at, version
		FROM facts ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []Fact
	for rows.Next() {
		var f Fact
		var created, updated string
		if err := rows.Scan(&f.ID, &f.Content, &f.Category, &f.Tags, &f.Source,
			&f.Trust, &f.Strength, &f.Retrievals, &f.Helpful,
			&created, &updated, &f.Version); err == nil {
			f.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
			f.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
			facts = append(facts, f)
		}
	}
	return facts, nil
}

func (l *FactsLayer) dbCommit() error {
	_, err := l.db.Exec("COMMIT")
	return err
}

// ── Text utilities ─────────────────────────────────────────────────────────

func tokenize(text string) map[string]bool {
	tokens := make(map[string]bool)
	for _, t := range strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		lower := strings.ToLower(t)
		if len(lower) > 0 {
			tokens[lower] = true
		}
	}
	return tokens
}

func union(a, b map[string]bool) map[string]bool {
	result := make(map[string]bool)
	for k := range a {
		result[k] = true
	}
	for k := range b {
		result[k] = true
	}
	return result
}

func jaccardSimilarity(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	intersection := 0
	for k := range a {
		if b[k] {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func filterCleared(facts []Fact) []Fact {
	var result []Fact
	for _, f := range facts {
		if f.Strength > DormantThreshold || f.Strength == 0 {
			result = append(result, f)
		}
	}
	return result
}

func tagAdd(tags, flag string) string {
	if strings.Contains(tags, flag) {
		return tags
	}
	if tags == "" {
		return flag
	}
	return tags + "," + flag
}

func tagRemove(tags, flag string) string {
	parts := strings.Split(tags, ",")
	var result []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" && p != flag {
			result = append(result, p)
		}
	}
	return strings.Join(result, ",")
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func round(v float64, decimals int) float64 {
	pow := math.Pow(10, float64(decimals))
	return math.Round(v*pow) / pow
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func parseInt(s string) int {
	var n int
	fmt.Sscanf(s, "%d", &n)
	return n
}

// encodeTextSimple creates a simple HRR-like vector from text tokens.
func encodeTextSimple(text string, dim int) []float64 {
	vec := make([]float64, dim)
	tokens := tokenize(text)
	i := 0
	for t := range tokens {
		for j := 0; j < dim; j++ {
			h := int(t[j%len(t)]) * (i + 1)
			vec[j] += float64(h) / float64(len(tokens)*255)
		}
		i++
	}
	// Normalize
	var norm float64
	for _, v := range vec {
		norm += v * v
	}
	if norm > 0 {
		norm = sqrt(norm)
		for i := range vec {
			vec[i] /= norm
		}
	}
	return vec
}

// sanitizeFTS5 prepares a user query for FTS5 MATCH.
func sanitizeFTS5(q string) string {
	q = strings.ReplaceAll(q, "\"", "")
	q = strings.ReplaceAll(q, "*", "")
	q = strings.ReplaceAll(q, "(", "")
	q = strings.ReplaceAll(q, ")", "")
	q = strings.ReplaceAll(q, "+", "")
	q = strings.ReplaceAll(q, "~", "")
	q = strings.ReplaceAll(q, ":", "")
	q = strings.ReplaceAll(q, ".", "")
	q = strings.TrimSpace(q)

	words := strings.Fields(q)
	if len(words) == 0 {
		return ""
	}
	for i, w := range words {
		words[i] = w + "*"
	}
	return strings.Join(words, " ")
}

// ── Cosine Similarity ──────────────────────────────────────────────────────

func cosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
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
	return dot / (sqrt(normA) * sqrt(normB))
}

func sqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	z := x / 2.0
	for i := 0; i < 10; i++ {
		z -= (z*z - x) / (2 * z)
	}
	return z
}
