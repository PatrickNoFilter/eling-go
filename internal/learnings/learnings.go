// Package learnings provides the A10 "learnings file" — a persistent,
// append-only markdown journal at ~/.eling/learnings.md where the agent (or
// the user via `eling learnings add "..."`) records durable lessons learned
// from sessions. Loaded at boot so lessons survive across sessions and are
// available to future conversations.
package learnings

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileName is the learnings journal file inside the ~/.eling state dir.
const FileName = "learnings.md"

// Path returns the learnings file path under ~/.eling.
func Path() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".eling", FileName)
}

// Load returns all recorded learnings (one per "- " bullet), oldest first.
// A missing file yields an empty slice and no error.
func Load() ([]string, error) {
	data, err := os.ReadFile(Path())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- ") {
			out = append(out, strings.TrimPrefix(line, "- "))
		}
	}
	return out, nil
}

// Append adds a timestamped learning to the journal, creating the file (and
// its parent dir) if needed. Multi-line entries are flattened to one line.
func Append(entry string) error {
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	clean := strings.TrimSpace(strings.Join(strings.Fields(entry), " "))
	if clean == "" {
		return fmt.Errorf("empty learning entry")
	}
	ts := time.Now().Format("2006-01-02 15:04")
	_, err = fmt.Fprintf(f, "- [%s] %s\n", ts, clean)
	return err
}

// Count returns the number of recorded learnings (0 if the file is missing).
func Count() int {
	ls, err := Load()
	if err != nil {
		return 0
	}
	return len(ls)
}
