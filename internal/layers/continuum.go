package layers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// ContinuumLayer is Layer 8: the multi-agent orchestration hub.
// It provides a shared registry where multiple agents can discover
// each other, share knowledge, and coordinate via reservations.
//
// Adapted from Python eling's continuum package.
type ContinuumLayer struct {
	mu       sync.RWMutex
	db       *sql.DB
	stateDir string
	agentID  string // identity of this agent in the continuum
}

// AgentRecord represents an agent registered in the continuum.
type AgentRecord struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	LastSeen  time.Time `json:"last_seen"`
	Status    string    `json:"status"` // active, idle, offline
	Capabilities []string `json:"capabilities"`
}

// SharedKnowledge represents knowledge shared across agents.
type SharedKnowledge struct {
	ID        int64     `json:"id"`
	AgentID   string    `json:"agent_id"`
	Content   string    `json:"content"`
	Tags      string    `json:"tags"`
	CreatedAt time.Time `json:"created_at"`
}

// NewContinuumLayer creates a new ContinuumLayer.
func NewContinuumLayer(stateDir string, agentID string) (*ContinuumLayer, error) {
	dbPath := filepath.Join(stateDir, "continuum.db")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dbPath+"?_journal=WAL&_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open continuum db: %w", err)
	}

	l := &ContinuumLayer{
		db:       db,
		stateDir: stateDir,
		agentID:  agentID,
	}

	if err := l.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("init continuum schema: %w", err)
	}

	// Register this agent
	_ = l.registerAgent(agentID)

	return l, nil
}

// Name returns "continuum".
func (l *ContinuumLayer) Name() string { return "continuum" }

// Priority returns 8.
func (l *ContinuumLayer) Priority() int { return 8 }

// Query searches shared knowledge.
func (l *ContinuumLayer) Query(ctx context.Context, q string, limit int) ([]Result, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	rows, err := l.db.QueryContext(ctx, `
		SELECT id, agent_id, content, tags, created_at
		FROM shared_knowledge
		WHERE content LIKE ? OR tags LIKE ?
		ORDER BY created_at DESC
		LIMIT ?`, "%"+q+"%", "%"+q+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Result
	for rows.Next() {
		var sk SharedKnowledge
		var ts string
		if err := rows.Scan(&sk.ID, &sk.AgentID, &sk.Content, &sk.Tags, &ts); err != nil {
			continue
		}
		sk.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", ts)
		results = append(results, Result{
			Content:  fmt.Sprintf("[%s] %s", sk.AgentID, sk.Content),
			Category: "shared_knowledge",
			Tags:     strings.Split(sk.Tags, ","),
			Score:    0.4,
			Time:     sk.CreatedAt,
			Metadata: map[string]string{
				"agent_id": sk.AgentID,
				"entry_id": fmt.Sprintf("%d", sk.ID),
			},
		})
	}
	return results, nil
}

// Store shares knowledge in the continuum.
func (l *ContinuumLayer) Store(ctx context.Context, item Item) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now().Format("2006-01-02 15:04:05")
	tags := strings.Join(item.Tags, ",")
	_, err := l.db.ExecContext(ctx, `
		INSERT INTO shared_knowledge (agent_id, content, tags, created_at)
		VALUES (?, ?, ?, ?)`, l.agentID, item.Content, tags, now)
	return err
}

// RegisterAgentHeartbeat updates the agent's last seen timestamp.
func (l *ContinuumLayer) RegisterAgentHeartbeat(agentID string, status string, capabilities []string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now().Format("2006-01-02 15:04:05")
	capJSON, _ := json.Marshal(capabilities)
	_, err := l.db.Exec(`
		INSERT OR REPLACE INTO agents (id, name, last_seen, status, capabilities)
		VALUES (?, ?, ?, ?, ?)`, agentID, agentID, now, status, string(capJSON))
	return err
}

// ListAgents returns all registered agents.
func (l *ContinuumLayer) ListAgents(ctx context.Context) ([]AgentRecord, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	rows, err := l.db.QueryContext(ctx, `
		SELECT id, name, last_seen, status, capabilities FROM agents ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []AgentRecord
	for rows.Next() {
		var a AgentRecord
		var ts string
		var capJSON string
		if err := rows.Scan(&a.ID, &a.Name, &ts, &a.Status, &capJSON); err != nil {
			continue
		}
		a.LastSeen, _ = time.Parse("2006-01-02 15:04:05", ts)
		json.Unmarshal([]byte(capJSON), &a.Capabilities)
		agents = append(agents, a)
	}
	return agents, nil
}

// Close closes the database.
func (l *ContinuumLayer) Close() error {
	return l.db.Close()
}

func (l *ContinuumLayer) initSchema() error {
	_, err := l.db.Exec(`
		CREATE TABLE IF NOT EXISTS agents (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			last_seen TEXT NOT NULL,
			status TEXT DEFAULT 'active',
			capabilities TEXT DEFAULT '[]'
		);
		CREATE TABLE IF NOT EXISTS shared_knowledge (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			agent_id TEXT NOT NULL,
			content TEXT NOT NULL,
			tags TEXT DEFAULT '',
			created_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_shared_knowledge_agent ON shared_knowledge(agent_id);
		CREATE INDEX IF NOT EXISTS idx_shared_knowledge_time ON shared_knowledge(created_at DESC);
		CREATE TABLE IF NOT EXISTS reservations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			agent_id TEXT NOT NULL,
			resource TEXT NOT NULL,
			purpose TEXT DEFAULT '',
			created_at TEXT NOT NULL,
			released_at TEXT,
			UNIQUE(resource)
		);
	`)
	return err
}

func (l *ContinuumLayer) registerAgent(agentID string) error {
	now := time.Now().Format("2006-01-02 15:04:05")
	_, err := l.db.Exec(`
		INSERT OR REPLACE INTO agents (id, name, last_seen, status, capabilities)
		VALUES (?, ?, ?, 'active', '[]')`, agentID, agentID, now)
	return err
}
