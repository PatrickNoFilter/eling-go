package layers

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// CodeLayer is Layer 4: the codegraph symbol intelligence layer.
// It indexes function symbols, imports, and class hierarchies
// from the codebase and provides search over them.
//
// Adapted from Python eling's layers/code.py and layers/code_index.py
type CodeLayer struct {
	mu       sync.RWMutex
	db       *sql.DB
	stateDir string
}

// Symbol represents a code symbol indexed by the code layer.
type Symbol struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"` // function, class, variable, import
	FilePath  string `json:"file_path"`
	Line      int    `json:"line"`
	Signature string `json:"signature,omitempty"`
	Package   string `json:"package,omitempty"`
}

// NewCodeLayer creates a new CodeLayer.
func NewCodeLayer(stateDir string) (*CodeLayer, error) {
	dbPath := filepath.Join(stateDir, "code.db")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dbPath+"?_journal=WAL&_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open code db: %w", err)
	}

	l := &CodeLayer{
		db:       db,
		stateDir: stateDir,
	}

	if err := l.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("init code schema: %w", err)
	}

	return l, nil
}

// Name returns "code".
func (l *CodeLayer) Name() string { return "code" }

// Priority returns 4.
func (l *CodeLayer) Priority() int { return 4 }

// Query searches code symbols by name, package, or content.
func (l *CodeLayer) Query(ctx context.Context, q string, limit int) ([]Result, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	rows, err := l.db.QueryContext(ctx, `
		SELECT name, kind, file_path, line, signature, package
		FROM symbols
		WHERE name LIKE ? OR package LIKE ? OR signature LIKE ?
		ORDER BY kind, name
		LIMIT ?`, "%"+q+"%", "%"+q+"%", "%"+q+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Result
	for rows.Next() {
		var sym Symbol
		if err := rows.Scan(&sym.Name, &sym.Kind, &sym.FilePath, &sym.Line, &sym.Signature, &sym.Package); err != nil {
			continue
		}
		content := fmt.Sprintf("%s %s (%s:%d)", sym.Kind, sym.Name, sym.FilePath, sym.Line)
		if sym.Signature != "" {
			content += "\n" + sym.Signature
		}
		results = append(results, Result{
			Content:  content,
			Category: fmt.Sprintf("symbol:%s", sym.Kind),
			Source:   fmt.Sprintf("%s:%d", sym.FilePath, sym.Line),
			Score:    0.6,
			Tags:     []string{sym.Package, sym.Kind},
			Metadata: map[string]string{
				"package":   sym.Package,
				"file":      sym.FilePath,
				"line":      fmt.Sprintf("%d", sym.Line),
				"signature": sym.Signature,
			},
		})
	}
	return results, nil
}

// Store indexes a code symbol.
func (l *CodeLayer) Store(ctx context.Context, item Item) error {
	// Parse symbol from content
	parts := strings.SplitN(item.Content, " ", 3)
	if len(parts) < 2 {
		return nil
	}
	kind := parts[0]
	name := parts[1]

	sig := ""
	if len(parts) > 2 {
		sig = parts[2]
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	_, err := l.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO symbols (name, kind, file_path, line, signature, package)
		VALUES (?, ?, ?, ?, ?, ?)`,
		name, kind, item.Source, 0, sig, firstTag(item.Tags))
	return err
}

// IndexFile indexes symbols from a Go source file using simple parsing.
// Returns the number of symbols indexed.
func (l *CodeLayer) IndexFile(filePath, pkg string) (int, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return 0, err
	}

	content := string(data)
	lines := strings.Split(content, "\n")
	count := 0

	l.mu.Lock()
	defer l.mu.Unlock()

	tx, err := l.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO symbols (name, kind, file_path, line, signature, package)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "func ") {
			name := extractFuncName(trimmed)
			if name != "" {
				_, _ = stmt.Exec(name, "function", filePath, i+1, trimmed, pkg)
				count++
			}
		} else if strings.HasPrefix(trimmed, "type ") && strings.Contains(trimmed, "struct") {
			name := extractTypeName(trimmed)
			if name != "" {
				_, _ = stmt.Exec(name, "struct", filePath, i+1, trimmed, pkg)
				count++
			}
		} else if strings.HasPrefix(trimmed, "type ") && strings.Contains(trimmed, "interface") {
			name := extractTypeName(trimmed)
			if name != "" {
				_, _ = stmt.Exec(name, "interface", filePath, i+1, trimmed, pkg)
				count++
			}
		}
	}

	return count, tx.Commit()
}

// Close closes the database.
func (l *CodeLayer) Close() error {
	return l.db.Close()
}

func (l *CodeLayer) initSchema() error {
	_, err := l.db.Exec(`
		CREATE TABLE IF NOT EXISTS symbols (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			kind TEXT NOT NULL,
			file_path TEXT NOT NULL,
			line INTEGER DEFAULT 0,
			signature TEXT DEFAULT '',
			package TEXT DEFAULT ''
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_symbols_unique ON symbols(name, kind, file_path);
		CREATE INDEX IF NOT EXISTS idx_symbols_name ON symbols(name);
		CREATE INDEX IF NOT EXISTS idx_symbols_package ON symbols(package);
		CREATE INDEX IF NOT EXISTS idx_symbols_kind ON symbols(kind);
	`)
	return err
}

func extractFuncName(line string) string {
	// Parse "func Name(" or "func (r Receiver) Name("
	afterFunc := strings.TrimPrefix(line, "func ")
	if idx := strings.Index(afterFunc, "("); idx >= 0 {
		// Check for receiver: "func (r T) Name(..."
		if strings.HasPrefix(afterFunc, "(") {
			closeParen := strings.Index(afterFunc, ")")
			if closeParen >= 0 {
				rest := strings.TrimSpace(afterFunc[closeParen+1:])
				if spaceIdx := strings.IndexAny(rest, " (");
					spaceIdx >= 0 && spaceIdx < strings.Index(rest, "(") {
					return rest[:spaceIdx]
				}
				// Just function name after receiver
				if parenIdx := strings.Index(rest, "("); parenIdx >= 0 {
					return strings.TrimSpace(rest[:parenIdx])
				}
				return strings.TrimSpace(rest)
			}
		}
		// No receiver: just "func Name("
		return afterFunc[:idx]
	}
	return ""
}

func extractTypeName(line string) string {
	// Parse "type Name struct" or "type Name interface"
	afterType := strings.TrimPrefix(line, "type ")
	if idx := strings.IndexAny(afterType, " {="); idx >= 0 {
		return strings.TrimSpace(afterType[:idx])
	}
	return strings.TrimSpace(afterType)
}

func firstTag(tags []string) string {
	if len(tags) > 0 {
		return tags[0]
	}
	return ""
}
