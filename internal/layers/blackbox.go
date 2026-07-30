package layers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// BlackboxLayer is Layer 2: the flight recorder and telemetry layer.
// It captures all agent tool calls, file operations, shell commands,
// and scores them with 11 context-efficiency metrics.
//
// Adapted from Python eling's blackbox package.
type BlackboxLayer struct {
	mu       sync.RWMutex
	db       *sql.DB
	stateDir string
}

// TraceEvent represents a single telemetry event captured by the blackbox.
type TraceEvent struct {
	ID        int64     `json:"id"`
	RunID     string    `json:"run_id"`
	Timestamp time.Time `json:"timestamp"`
	EventType string    `json:"event_type"` // tool_call, file_read, file_edit, shell, subagent
	ToolName  string    `json:"tool_name,omitempty"`
	Input     string    `json:"input,omitempty"`
	Output    string    `json:"output,omitempty"`
	Duration  int64     `json:"duration_ms,omitempty"`
	Success   bool      `json:"success"`
	FilePath  string    `json:"file_path,omitempty"`
	LineCount int       `json:"line_count,omitempty"`
}

// EfficiencyScore holds the 11 efficiency metrics.
type EfficiencyScore struct {
	RedundantReads          int     `json:"redundant_reads"`
	CacheHitRatio           float64 `json:"cache_hit_ratio"`
	ReadAmplification       float64 `json:"read_amplification"`
	RetryWaste              int     `json:"retry_waste"`
	YieldDensity            float64 `json:"yield_density"`
	TokenEfficiency         int     `json:"token_efficiency"`
	EditEfficiency          float64 `json:"edit_efficiency"`
	TestSuccess             float64 `json:"test_success"`
	CommitFrequency         float64 `json:"commit_frequency"`
	ContextWindowUtilization float64 `json:"context_window_utilization"`
	SubagentOverhead        int     `json:"subagent_overhead"`
}

// NewBlackboxLayer creates a new blackbox flight recorder.
func NewBlackboxLayer(stateDir string) (*BlackboxLayer, error) {
	dbPath := filepath.Join(stateDir, "blackbox.db")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dbPath+"?_journal=WAL&_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open blackbox db: %w", err)
	}

	l := &BlackboxLayer{
		db:       db,
		stateDir: stateDir,
	}

	if err := l.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("init blackbox schema: %w", err)
	}

	return l, nil
}

// Name returns "blackbox".
func (l *BlackboxLayer) Name() string { return "blackbox" }

// Priority returns 2.
func (l *BlackboxLayer) Priority() int { return 2 }

// Query searches telemetry events for matching patterns.
func (l *BlackboxLayer) Query(ctx context.Context, q string, limit int) ([]Result, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	rows, err := l.db.QueryContext(ctx, `
		SELECT run_id, event_type, tool_name, input, output, success, duration_ms, timestamp
		FROM trace_events
		WHERE input LIKE ? OR output LIKE ? OR tool_name LIKE ?
		ORDER BY timestamp DESC
		LIMIT ?`, "%"+q+"%", "%"+q+"%", "%"+q+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Result
	for rows.Next() {
		var e TraceEvent
		var ts string
		if err := rows.Scan(&e.RunID, &e.EventType, &e.ToolName, &e.Input,
			&e.Output, &e.Success, &e.Duration, &ts); err != nil {
			continue
		}
		e.Timestamp, _ = time.Parse("2006-01-02 15:04:05", ts)
		content := fmt.Sprintf("[%s] %s(%s) → %s (dur:%dms, ok:%v)",
			e.EventType, e.ToolName, truncateStr(e.Input, 80),
			truncateStr(e.Output, 100), e.Duration, e.Success)
		results = append(results, Result{
			Content:  content,
			Category: "telemetry",
			Source:   fmt.Sprintf("run:%s", e.RunID),
			Score:    0.5,
			Time:     e.Timestamp,
		})
	}
	return results, nil
}

// Store records a telemetry event.
func (l *BlackboxLayer) Store(ctx context.Context, item Item) error {
	var event TraceEvent
	// Try to parse as JSON event
	if err := json.Unmarshal([]byte(item.Content), &event); err == nil {
		return l.RecordEvent(ctx, event)
	}

	// Store as a generic event entry
	event = TraceEvent{
		RunID:     fmt.Sprintf("run_%d", time.Now().Unix()),
		Timestamp: time.Now(),
		EventType: item.Category,
		Input:     item.Content,
		Success:   true,
	}
	return l.RecordEvent(ctx, event)
}

// RecordEvent records a single trace event.
func (l *BlackboxLayer) RecordEvent(ctx context.Context, event TraceEvent) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if event.RunID == "" {
		event.RunID = fmt.Sprintf("run_%d", time.Now().Unix())
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	_, err := l.db.ExecContext(ctx, `
		INSERT INTO trace_events (run_id, timestamp, event_type, tool_name, input, output, duration_ms, success, file_path, line_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.RunID, event.Timestamp.Format("2006-01-02 15:04:05"),
		event.EventType, event.ToolName, truncateStr(event.Input, 2000),
		truncateStr(event.Output, 2000), event.Duration, event.Success,
		event.FilePath, event.LineCount)
	return err
}

// ScoreRun scores a run with the 11 efficiency metrics.
func (l *BlackboxLayer) ScoreRun(ctx context.Context, runID string) (*EfficiencyScore, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	score := &EfficiencyScore{}

	// Count redundant reads (same file read twice with no writes between)
	rows, err := l.db.QueryContext(ctx, `
		SELECT file_path, event_type, timestamp FROM trace_events
		WHERE run_id = ? AND (event_type = 'file_read' OR event_type = 'file_edit')
		ORDER BY timestamp ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var lastReads = make(map[string]time.Time)
	var lastWrites = make(map[string]time.Time)
	for rows.Next() {
		var fp, etype, ts string
		if err := rows.Scan(&fp, &etype, &ts); err != nil {
			continue
		}
		t, _ := time.Parse("2006-01-02 15:04:05", ts)
		if etype == "file_read" {
			if lastWrite, ok := lastWrites[fp]; ok && t.After(lastWrite) {
				// Read after write — legitimate
			} else if lastRead, ok := lastReads[fp]; ok && t.Sub(lastRead) < 30*time.Second {
				score.RedundantReads++
			}
			lastReads[fp] = t
		} else if etype == "file_edit" {
			lastWrites[fp] = t
		}
	}

	// Cache hit ratio
	var totalShell, failedShell int
	_ = l.db.QueryRowContext(ctx, `
		SELECT COUNT(*), SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END)
		FROM trace_events WHERE run_id = ? AND event_type = 'shell'`, runID).Scan(&totalShell, &failedShell)
	if totalShell > 0 {
		score.CacheHitRatio = 1.0 - float64(failedShell)/float64(totalShell)
	}

	// Read amplification
	var totalLinesRead, totalLinesWritten int
	_ = l.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(line_count), 0) FROM trace_events
		WHERE run_id = ? AND event_type = 'file_read'`, runID).Scan(&totalLinesRead)
	_ = l.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(line_count), 0) FROM trace_events
		WHERE run_id = ? AND event_type = 'file_edit'`, runID).Scan(&totalLinesWritten)
	if totalLinesWritten > 0 {
		score.ReadAmplification = float64(totalLinesRead) / float64(totalLinesWritten)
	}

	// Retry waste
	_ = l.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM trace_events
		WHERE run_id = ? AND event_type = 'shell' AND success = 0`, runID).Scan(&score.RetryWaste)

	// Yield density: edits per tool call
	var totalEdits, totalTools int
	_ = l.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM trace_events
		WHERE run_id = ? AND event_type = 'file_edit'`, runID).Scan(&totalEdits)
	_ = l.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM trace_events
		WHERE run_id = ? AND event_type = 'tool_call'`, runID).Scan(&totalTools)
	if totalTools > 0 {
		score.YieldDensity = float64(totalEdits) / float64(totalTools)
	}

	// Test success rate
	var totalTests, passedTests int
	_ = l.db.QueryRowContext(ctx, `
		SELECT COUNT(*), SUM(CASE WHEN success = 1 THEN 1 ELSE 0 END)
		FROM trace_events WHERE run_id = ? AND event_type = 'test'`, runID).Scan(&totalTests, &passedTests)
	if totalTests > 0 {
		score.TestSuccess = float64(passedTests) / float64(totalTests)
	}

	// Token efficiency: estimate from input/output lengths
	var totalInputLen, totalOutputLen int
	_ = l.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(LENGTH(input)), 0), COALESCE(SUM(LENGTH(output)), 0)
		FROM trace_events WHERE run_id = ?`, runID).Scan(&totalInputLen, &totalOutputLen)
	score.TokenEfficiency = totalOutputLen / max(totalInputLen/4, 1)

	// Edit efficiency: edits per file open
	var fileOpens int
	_ = l.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT file_path) FROM trace_events
		WHERE run_id = ? AND event_type = 'file_read'`, runID).Scan(&fileOpens)
	if fileOpens > 0 {
		score.EditEfficiency = float64(totalEdits) / float64(fileOpens)
	}

	// Commit frequency (estimated from test events per hour)
	var firstEvent, lastEvent string
	_ = l.db.QueryRowContext(ctx, `
		SELECT MIN(timestamp), MAX(timestamp) FROM trace_events
		WHERE run_id = ?`, runID).Scan(&firstEvent, &lastEvent)
	if firstEvent != "" && lastEvent != "" {
		t1, _ := time.Parse("2006-01-02 15:04:05", firstEvent)
		t2, _ := time.Parse("2006-01-02 15:04:05", lastEvent)
		hours := t2.Sub(t1).Hours()
		if hours > 0 && totalEdits > 0 {
			score.CommitFrequency = float64(totalEdits) / hours
		}
	}

	return score, nil
}

// Close closes the database connection.
func (l *BlackboxLayer) Close() error {
	return l.db.Close()
}

// initSchema creates the trace_events table if it doesn't exist.
func (l *BlackboxLayer) initSchema() error {
	_, err := l.db.Exec(`
		CREATE TABLE IF NOT EXISTS trace_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id TEXT NOT NULL,
			timestamp TEXT NOT NULL,
			event_type TEXT NOT NULL,
			tool_name TEXT,
			input TEXT,
			output TEXT,
			duration_ms INTEGER DEFAULT 0,
			success INTEGER DEFAULT 1,
			file_path TEXT,
			line_count INTEGER DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_trace_events_run ON trace_events(run_id);
		CREATE INDEX IF NOT EXISTS idx_trace_events_type ON trace_events(event_type);
		CREATE INDEX IF NOT EXISTS idx_trace_events_time ON trace_events(timestamp);
	`)
	return err
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
