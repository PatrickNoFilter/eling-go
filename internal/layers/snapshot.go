package layers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ── Snapshot / Rollback ─────────────────────────────────────────────────────
//
// Provides Git-like snapshot and rollback for the facts database.
// Snapshots are file-level copies stored in a snapshots/ subdirectory
// next to the facts database, with a JSON index for listing.

const snapshotDir = "snapshots"
const snapshotIndex = "snapshot_index.json"

// SnapshotInfo describes a single snapshot.
type SnapshotInfo struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Reason    string `json:"reason"`
	SizeBytes int64  `json:"size_bytes"`
	FactCount int    `json:"fact_count,omitempty"`
}

// CreateSnapshot creates a snapshot of the facts database.
// Returns the snapshot ID and metadata.
func (l *FactsLayer) CreateSnapshot(reason string) (*SnapshotInfo, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.db == nil {
		return nil, fmt.Errorf("facts layer not initialized")
	}

	// Ensure WAL checkpoint so the DB file is consistent
	_, _ = l.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")

	// Determine source DB path
	dbPath := filepath.Join(l.stateDir, "facts.db")

	// Create snapshots directory
	snapDir := filepath.Join(l.stateDir, snapshotDir)
	if err := os.MkdirAll(snapDir, 0755); err != nil {
		return nil, fmt.Errorf("create snapshot dir: %w", err)
	}

	// Generate snapshot ID from timestamp
	now := time.Now()
	id := now.Format("20060702-150405") + fmt.Sprintf("-%d", now.UnixNano()%1000)

	snapshot := &SnapshotInfo{
		ID:        id,
		Timestamp: now.Format("2006-01-02 15:04:05"),
		Reason:    reason,
	}

	// Copy the database file
	snapPath := filepath.Join(snapDir, fmt.Sprintf("facts-%s.db", id))
	input, err := os.ReadFile(dbPath)
	if err != nil {
		return nil, fmt.Errorf("read source db: %w", err)
	}
	if err := os.WriteFile(snapPath, input, 0644); err != nil {
		return nil, fmt.Errorf("write snapshot: %w", err)
	}

	snapshot.SizeBytes = int64(len(input))

	// Count facts in the snapshot for display
	// (Open a temporary read-only connection)
	// We'll store 0 if we can't count
	count, _ := l.snapshotFactCount(snapPath)
	snapshot.FactCount = count

	// Update the snapshot index
	if err := l.addSnapshotToIndex(snapshot); err != nil {
		// Non-fatal: snapshot still exists on disk
		return snapshot, nil
	}

	return snapshot, nil
}

// ListSnapshots returns all available snapshots, newest first.
func (l *FactsLayer) ListSnapshots() ([]SnapshotInfo, error) {
	idxPath := filepath.Join(l.stateDir, snapshotDir, snapshotIndex)

	data, err := os.ReadFile(idxPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var snapshots []SnapshotInfo
	if err := json.Unmarshal(data, &snapshots); err != nil {
		return nil, fmt.Errorf("parse snapshot index: %w", err)
	}

	// Sort newest first
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].Timestamp > snapshots[j].Timestamp
	})

	return snapshots, nil
}

// Rollback restores the facts database to a named snapshot.
// Returns the snapshot info that was restored.
func (l *FactsLayer) Rollback(snapshotID string) (*SnapshotInfo, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Find the snapshot
	snapshots, err := l.ListSnapshots()
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}

	var target *SnapshotInfo
	for _, s := range snapshots {
		if s.ID == snapshotID {
			target = &s
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("snapshot %q not found", snapshotID)
	}

	// Close current DB connection
	if l.db != nil {
		l.db.Close()
	}

	// Backup current DB before restoring
	dbPath := filepath.Join(l.stateDir, "facts.db")
	backupPath := filepath.Join(l.stateDir, "facts.db.pre_rollback")
	_ = os.Remove(backupPath) // remove old backup if exists

	input, err := os.ReadFile(dbPath)
	if err == nil {
		_ = os.WriteFile(backupPath, input, 0644)
	}

	// Restore snapshot
	snapPath := filepath.Join(l.stateDir, snapshotDir, fmt.Sprintf("facts-%s.db", snapshotID))
	snapData, err := os.ReadFile(snapPath)
	if err != nil {
		return nil, fmt.Errorf("read snapshot file: %w", err)
	}
	if err := os.WriteFile(dbPath, snapData, 0644); err != nil {
		return nil, fmt.Errorf("restore snapshot: %w", err)
	}

	// Create a rollback note in the snapshot index
	rollbackInfo := SnapshotInfo{
		ID:        "rollback_" + time.Now().Format("20060702-150405"),
		Timestamp: time.Now().Format("2006-01-02 15:04:05"),
		Reason:    fmt.Sprintf("rolled_back_to_%s", snapshotID),
	}
	_ = l.addSnapshotToIndex(&rollbackInfo)

	return target, nil
}

// ── Internal helpers ───────────────────────────────────────────────────────

func (l *FactsLayer) addSnapshotToIndex(snapshot *SnapshotInfo) error {
	idxPath := filepath.Join(l.stateDir, snapshotDir, snapshotIndex)

	var snapshots []SnapshotInfo
	data, err := os.ReadFile(idxPath)
	if err == nil {
		_ = json.Unmarshal(data, &snapshots)
	}

	snapshots = append(snapshots, *snapshot)

	// Keep only the last 100 snapshots
	if len(snapshots) > 100 {
		snapshots = snapshots[len(snapshots)-100:]
	}

	output, err := json.MarshalIndent(snapshots, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(idxPath, output, 0644)
}

func (l *FactsLayer) snapshotFactCount(path string) (int, error) {
	// Read the snapshot DB to count facts
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	// Simple text match for counting — works for small DBs
	content := string(data)
	count := strings.Count(content, "INSERT INTO facts")
	if count == 0 {
		// Try to count from the SQLite header
		// Just return approximate
		return int(int64(len(data)) / 1024), nil
	}
	return count, nil
}

// Brain methods for snapshot/rollback

// Snapshot creates a snapshot of the facts database.
func (b *Brain) Snapshot(reason string) (*SnapshotInfo, error) {
	fl := b.FactsLayer()
	if fl == nil {
		return nil, fmt.Errorf("facts layer not available")
	}
	return fl.CreateSnapshot(reason)
}

// ListSnapshots returns all available snapshots.
func (b *Brain) ListSnapshots() ([]SnapshotInfo, error) {
	fl := b.FactsLayer()
	if fl == nil {
		return nil, fmt.Errorf("facts layer not available")
	}
	return fl.ListSnapshots()
}

// Rollback restores the facts database to a named snapshot.
func (b *Brain) Rollback(snapshotID string) (*SnapshotInfo, error) {
	fl := b.FactsLayer()
	if fl == nil {
		return nil, fmt.Errorf("facts layer not available")
	}
	return fl.Rollback(snapshotID)
}
