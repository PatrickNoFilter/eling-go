package layers

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

// KBLayer is Layer 5: the knowledge corpus layer.
// It stores long-form articles, documentation, and knowledge chunks
// with FTS5 full-text search.
//
// Adapted from Python eling's layers/kb.py
type KBLayer struct {
	mu       sync.RWMutex
	db       *sql.DB
	stateDir string
}

// KnowledgeEntry represents a knowledge base entry.
type KnowledgeEntry struct {
	ID      int64  `json:"id"`
	Title   string `json:"title"`
	Content string `json:"content"`
	Source  string `json:"source"`
	URL     string `json:"url,omitempty"`
}

// NewKBLayer creates a new KBLayer.
func NewKBLayer(stateDir string) (*KBLayer, error) {
	dbPath := filepath.Join(stateDir, "kb.db")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dbPath+"?_journal=WAL&_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open kb db: %w", err)
	}

	l := &KBLayer{
		db:       db,
		stateDir: stateDir,
	}

	if err := l.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("init kb schema: %w", err)
	}

	return l, nil
}

// Name returns "kb".
func (l *KBLayer) Name() string { return "kb" }

// Priority returns 5.
func (l *KBLayer) Priority() int { return 5 }

// Query searches knowledge base using FTS5.
func (l *KBLayer) Query(ctx context.Context, q string, limit int) ([]Result, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	q = sanitizeFTS5(q)
	if q == "" {
		return nil, nil
	}

	rows, err := l.db.QueryContext(ctx, `
		SELECT k.id, k.title, k.content, k.source, k.url
		FROM knowledge k
		JOIN knowledge_fts ON knowledge_fts.rowid = k.id
		WHERE knowledge_fts MATCH ?
		ORDER BY rank
		LIMIT ?`, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Result
	for rows.Next() {
		var entry KnowledgeEntry
		if err := rows.Scan(&entry.ID, &entry.Title, &entry.Content, &entry.Source, &entry.URL); err != nil {
			continue
		}
		results = append(results, Result{
			Content:  fmt.Sprintf("# %s\n\n%s", entry.Title, truncateStr(entry.Content, 500)),
			Category: "knowledge",
			Source:   entry.Source,
			Score:    0.5,
			Metadata: map[string]string{
				"title":  entry.Title,
				"url":    entry.URL,
				"source": entry.Source,
			},
		})
	}
	return results, nil
}

// Store saves a knowledge entry.
func (l *KBLayer) Store(ctx context.Context, item Item) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	title := item.Category
	if title == "" {
		title = "Untitled"
	}
	source := item.Source
	if source == "" {
		source = "manual"
	}

	_, err := l.db.ExecContext(ctx, `
		INSERT INTO knowledge (title, content, source, url)
		VALUES (?, ?, ?, ?)`,
		title, item.Content, source, firstString(item.Metadata, "url"))
	return err
}

// Close closes the database.
func (l *KBLayer) Close() error {
	return l.db.Close()
}

func (l *KBLayer) initSchema() error {
	_, err := l.db.Exec(`
		CREATE TABLE IF NOT EXISTS knowledge (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT NOT NULL,
			content TEXT NOT NULL,
			source TEXT DEFAULT '',
			url TEXT DEFAULT ''
		);
		CREATE VIRTUAL TABLE IF NOT EXISTS knowledge_fts USING fts5(
			title, content,
			content='knowledge',
			content_rowid='id',
			tokenize='porter unicode61'
		);
		CREATE TRIGGER IF NOT EXISTS knowledge_ai AFTER INSERT ON knowledge BEGIN
			INSERT INTO knowledge_fts(rowid, title, content)
			VALUES (new.id, new.title, new.content);
		END;
		CREATE TRIGGER IF NOT EXISTS knowledge_ad AFTER DELETE ON knowledge BEGIN
			INSERT INTO knowledge_fts(knowledge_fts, rowid, title, content)
			VALUES ('delete', old.id, old.title, old.content);
		END;
	`)
	return err
}

func firstString(m map[string]string, key string) string {
	if m == nil {
		return ""
	}
	return m[key]
}
