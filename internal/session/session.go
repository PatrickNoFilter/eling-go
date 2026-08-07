// Package session provides session save/resume functionality.
// Inspired by jcode's session system with named sessions and resumability.
package session

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ToolCallData captures a tool invocation for session persistence.
type ToolCallData struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
	Result    string `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Entry represents a single entry in a session.
type Entry struct {
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	Reasoning string         `json:"reasoning,omitempty"` // DeepSeek reasoning_content, replayed on resume
	Timestamp time.Time      `json:"timestamp"`
	Tokens    int            `json:"tokens,omitempty"`
	ToolCalls []ToolCallData `json:"tool_calls,omitempty"`
}

// Session represents a saved conversation session.
type Session struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	Model     string            `json:"model"`
	Entries   []Entry           `json:"entries"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	// Plan holds the most recently approved execution plan (plan mode).
	// Persisted so saved/resumed sessions show the plan and re-inject it
	// into the model context on resume.
	Plan string `json:"plan,omitempty"`
}

// cloneSession returns a deep copy of s. Slices and maps are copied so
// mutations to the original (e.g. concurrent Append) never affect the copy.
func cloneSession(s *Session) *Session {
	if s == nil {
		return nil
	}
	c := *s
	if s.Entries != nil {
		c.Entries = make([]Entry, len(s.Entries))
		copy(c.Entries, s.Entries)
	}
	if s.Metadata != nil {
		c.Metadata = make(map[string]string, len(s.Metadata))
		for k, v := range s.Metadata {
			c.Metadata[k] = v
		}
	}
	return &c
}

// Manager handles session persistence.
type Manager struct {
	mu       sync.RWMutex
	saveDir  string
	sessions map[string]*Session // name -> session
}

// NewManager creates a session manager.
func NewManager(saveDir string) *Manager {
	return &Manager{
		saveDir:  saveDir,
		sessions: make(map[string]*Session),
	}
}

// Create creates a new session.
func (m *Manager) Create(name, model string) *Session {
	s := &Session{
		ID:        fmt.Sprintf("ses_%d", time.Now().UnixNano()),
		Name:      name,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Model:     model,
		Entries:   make([]Entry, 0),
		Metadata:  make(map[string]string),
	}
	m.mu.Lock()
	m.sessions[name] = s
	m.mu.Unlock()
	return s
}

// Get retrieves a session by name, returning the live pointer. Callers that
// hold the manager lock across their reads, or that need to mutate entries
// via the locked helpers (Append / SetLastEntryTokens / SetMetadata), may use
// this directly. External readers that only inspect the session (TUI, stats)
// MUST use GetCopy instead to avoid racing concurrent Append calls.
func (m *Manager) Get(name string) (*Session, bool) {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	return s, ok
}

// GetCopy returns a deep copy of the session made under the manager lock.
// Safe for any caller: the returned session is fully detached, so reading
// Entries/Metadata afterwards can never race a concurrent Append/AddEntry
// from another goroutine (Ask turns, interrupted-save defer, HTTP daemon).
func (m *Manager) GetCopy(name string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[name]
	if !ok || s == nil {
		return nil, false
	}
	return cloneSession(s), true
}

// SetLastEntryTokens updates the Tokens field of the most recent entry under
// the manager lock. Safe to call while other goroutines read via GetCopy.
func (m *Manager) SetLastEntryTokens(name string, tokens int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[name]
	if !ok || s == nil || len(s.Entries) == 0 {
		return
	}
	s.Entries[len(s.Entries)-1].Tokens = tokens
}

// LastEntry returns a copy of the most recent entry, or ok=false if the
// session is missing or empty. Safe for concurrent use.
func (m *Manager) LastEntry(name string) (Entry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[name]
	if !ok || s == nil || len(s.Entries) == 0 {
		return Entry{}, false
	}
	return s.Entries[len(s.Entries)-1], true
}

// GetEntriesCopy returns a copy of the session's entries while holding the
// session manager lock. This is safe for concurrent access because the copy
// is made before releasing the lock. Use this instead of calling Get() and
// then reading s.Entries outside the lock to avoid data races.
func (m *Manager) GetEntriesCopy(name string) ([]Entry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[name]
	if !ok || s == nil {
		return nil, false
	}
	entries := make([]Entry, len(s.Entries))
	copy(entries, s.Entries)
	return entries, true
}

// Append adds an entry to a session with optional tool call data.
func (m *Manager) Append(name, role, content string, toolCalls ...ToolCallData) error {
	return m.AppendWithReasoning(name, role, content, "", toolCalls...)
}

// AppendWithReasoning adds an entry with optional reasoning (DeepSeek
// reasoning_content) and tool call data. Reasoning is persisted so that
// resumed sessions can pass reasoning_content back to the API — DeepSeek
// rejects assistant messages that omit it in thinking mode.
func (m *Manager) AppendWithReasoning(name, role, content, reasoning string, toolCalls ...ToolCallData) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[name]
	if !ok {
		return fmt.Errorf("session %q not found", name)
	}
	entry := Entry{
		Role:      role,
		Content:   content,
		Reasoning: reasoning,
		Timestamp: time.Now(),
	}
	if len(toolCalls) > 0 {
		entry.ToolCalls = make([]ToolCallData, len(toolCalls))
		copy(entry.ToolCalls, toolCalls)
	}
	s.Entries = append(s.Entries, entry)
	s.UpdatedAt = time.Now()
	return nil
}

// List returns all saved session names.
func (m *Manager) List() ([]string, error) {
	if err := os.MkdirAll(m.saveDir, 0755); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(m.saveDir)
	if err != nil {
		return nil, err
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			names = append(names, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	// Sort by modification time (newest first)
	sort.Slice(names, func(i, j int) bool {
		fi, _ := os.Stat(filepath.Join(m.saveDir, names[i]+".json"))
		fj, _ := os.Stat(filepath.Join(m.saveDir, names[j]+".json"))
		if fi != nil && fj != nil {
			return fi.ModTime().After(fj.ModTime())
		}
		return names[i] > names[j]
	})
	return names, nil
}

// Save persists a session to disk.
// The session is deep-copied under the manager lock so that concurrent
// Append/AddEntry mutations during JSON marshaling cannot cause a
// "reflect: slice index out of range" panic (the entry slice is snapshotted
// before metadata generation and marshaling happen outside the lock).
func (m *Manager) Save(name string) error {
	m.mu.RLock()
	s, ok := m.sessions[name]
	if ok {
		// Deep-copy the session so marshaling works on an immutable snapshot.
		s = cloneSession(s)
	}
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("session %q not found", name)
	}

	// Auto-populate metadata summary
	if s.Metadata == nil {
		s.Metadata = make(map[string]string)
	}
	s.Metadata["summary"] = SummarizeSession(s)
	s.Metadata["entry_count"] = fmt.Sprintf("%d", len(s.Entries))
	s.Metadata["updated_at"] = s.UpdatedAt.Format(time.RFC3339)

	// Recompute drift-sensitive metadata against the actual entry slice so the
	// persisted file is self-consistent (P2.1). Does not hard-fail a save, but
	// any correction is logged to the audit stream so drift is observable.
	verifyTotals(s, name)

	if err := os.MkdirAll(m.saveDir, 0755); err != nil {
		return err
	}

	var (
		data []byte
		err  error
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic during session marshal: %v", r)
			}
		}()
		data, err = json.MarshalIndent(s, "", "  ")
	}()
	if err != nil {
		return err
	}

	path := filepath.Join(m.saveDir, name+".json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return err
	}

	return nil
}

// verifyTotals reconciles drift-sensitive metadata against the session's actual
// entry slice before a save (P2.1). It recomputes the source-of-truth values
// and logs an audit line whenever a correction was needed, so a stale or
// hand-edited value is repaired (never silently trusted) without ever failing
// the save. Guarded: s.Metadata is guaranteed non-nil by the caller.
func verifyTotals(s *Session, name string) {
	entryCount := len(s.Entries)
	// Sum of per-entry token counts as recorded at append time.
	sumTokens := 0
	for _, e := range s.Entries {
		sumTokens += e.Tokens
	}
	if got := s.Metadata["entry_count"]; got != strconv.Itoa(entryCount) {
		s.Metadata["entry_count"] = strconv.Itoa(entryCount)
		log.Printf("session: %q entry_count drifted (%s -> %d), reconciled on save", name, got, entryCount)
	}
	// total_tokens is a per-turn cumulative accounting key set by the agent;
	// only a negative value is structurally impossible, so flag it when seen.
	if tt, ok := s.Metadata["total_tokens"]; ok && tt != "" {
		if n, err := strconv.Atoi(tt); err == nil && n < 0 {
			s.Metadata["total_tokens"] = "0"
			log.Printf("session: %q total_tokens was negative (%d); clamped to 0 on save", name, n)
		} else if n, err := strconv.Atoi(tt); err == nil && n < sumTokens {
			log.Printf("session: %q total_tokens %d < sum of entry tokens %d", name, n, sumTokens)
		}
	}
}

// Load loads a session from disk.
func (m *Manager) Load(name string) (*Session, error) {
	path := filepath.Join(m.saveDir, name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.sessions[name] = &s
	m.mu.Unlock()
	return &s, nil
}

// Delete removes a session from disk and memory.
func (m *Manager) Delete(name string) error {
	m.mu.Lock()
	delete(m.sessions, name)
	m.mu.Unlock()
	path := filepath.Join(m.saveDir, name+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Resume loads a session and returns its context string.
func (m *Manager) Resume(name string) (string, error) {
	s, err := m.Load(name)
	if err != nil {
		return "", err
	}

	// Build context from session entries
	context := fmt.Sprintf("Resuming session %q from %s\n\n", s.Name, s.CreatedAt.Format(time.RFC822))
	for _, e := range s.Entries {
		prefix := "User: "
		if e.Role == "assistant" {
			prefix = "You: "
		}
		context += prefix + e.Content + "\n\n"
	}
	context += "---\nContinue from where we left off."

	return context, nil
}

// SaveAll persists all in-memory sessions to disk.
func (m *Manager) SaveAll() error {
	m.mu.RLock()
	names := make([]string, 0, len(m.sessions))
	for n := range m.sessions {
		names = append(names, n)
	}
	m.mu.RUnlock()

	for _, name := range names {
		if err := m.Save(name); err != nil {
			return err
		}
	}
	return nil
}

// GetLastSession returns the session with the most recent UpdatedAt timestamp.
// It scans both in-memory sessions and saved session files on disk.
func (m *Manager) GetLastSession() (*Session, error) {
	// First check in-memory sessions
	m.mu.RLock()
	var last *Session
	for _, s := range m.sessions {
		if last == nil || s.UpdatedAt.After(last.UpdatedAt) {
			last = s
		}
	}
	m.mu.RUnlock()
	if last != nil {
		return cloneSession(last), nil
	}

	// Check disk for sessions
	names, err := m.List()
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no sessions found")
	}

	// Sort by modification time (newest first)
	var sessions []*Session
	for _, name := range names {
		s, err := m.Load(name)
		if err != nil {
			continue
		}
		sessions = append(sessions, s)
	}
	if len(sessions) == 0 {
		return nil, fmt.Errorf("no valid sessions found")
	}

	// Find the latest
	last = sessions[0]
	for _, s := range sessions[1:] {
		if s.UpdatedAt.After(last.UpdatedAt) {
			last = s
		}
	}
	return last, nil
}

// UpdateSessionName renames a session in memory and on disk.
func (m *Manager) UpdateSessionName(oldName, newName string) error {
	m.mu.Lock()
	s, ok := m.sessions[oldName]
	if !ok {
		// Try loading from disk
		var err error
		s, err = m.Load(oldName)
		if err != nil {
			m.mu.Unlock()
			return fmt.Errorf("session %q not found", oldName)
		}
	}
	s.Name = newName
	m.sessions[newName] = s
	delete(m.sessions, oldName)
	m.mu.Unlock()

	// Save under new name
	if err := m.Save(newName); err != nil {
		return err
	}

	// Remove old file
	oldPath := filepath.Join(m.saveDir, oldName+".json")
	_ = os.Remove(oldPath)

	return nil
}

// SetMetadata sets a metadata key on a session.
func (m *Manager) SetMetadata(name, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[name]
	if !ok {
		return fmt.Errorf("session %q not found", name)
	}
	if s.Metadata == nil {
		s.Metadata = make(map[string]string)
	}
	s.Metadata[key] = value
	return nil
}

// SummarizeSession creates a brief summary of a session's content.
func SummarizeSession(s *Session) string {
	if len(s.Entries) == 0 {
		return "Empty session"
	}
	// Count messages by role
	userCount := 0
	assistantCount := 0
	totalChars := 0
	for _, e := range s.Entries {
		if e.Role == "user" {
			userCount++
		} else if e.Role == "assistant" {
			assistantCount++
		}
		totalChars += len(e.Content)
	}
	// Get first user message as hint
	firstUser := ""
	lastUser := ""
	for _, e := range s.Entries {
		if e.Role == "user" {
			if firstUser == "" {
				firstUser = truncateStr(e.Content, 80)
			}
			lastUser = truncateStr(e.Content, 80)
		}
	}
	summary := fmt.Sprintf("%d messages (%d user, %d assistant), %d chars",
		len(s.Entries), userCount, assistantCount, totalChars)
	if firstUser != "" {
		summary += fmt.Sprintf(". Started: %q", firstUser)
	}
	if lastUser != "" && lastUser != firstUser {
		summary += fmt.Sprintf(". Last: %q", lastUser)
	}
	return summary
}

func truncateStr(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}
