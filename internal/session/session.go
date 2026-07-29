// Package session provides session save/resume functionality.
// Inspired by jcode's session system with named sessions and resumability.
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// Get retrieves a session by name.
func (m *Manager) Get(name string) (*Session, bool) {
	m.mu.RLock()
	s, ok := m.sessions[name]
	m.mu.RUnlock()
	return s, ok
}

// Append adds an entry to a session with optional tool call data.
func (m *Manager) Append(name, role, content string, toolCalls ...ToolCallData) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[name]
	if !ok {
		return fmt.Errorf("session %q not found", name)
	}
	entry := Entry{
		Role:      role,
		Content:   content,
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
func (m *Manager) Save(name string) error {
	m.mu.RLock()
	s, ok := m.sessions[name]
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
		return last, nil
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
