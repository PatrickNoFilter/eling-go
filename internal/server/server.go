// Package server implements the ELING HTTP daemon (`eling serve`, Phase 4 of
// the qwen-code feature steal). It exposes a long-running agent over HTTP+SSE
// so any client (curl, the TUI via --daemon-url, another device on the LAN)
// can talk to the same brain.
//
// Design notes:
//   - One *agent.Agent per session_id — sequential chats to the same id
//     continue the same in-memory conversation (agent holds message history).
//   - Auth: `Authorization: Bearer <token>`. Empty token = loopback-only, no
//     auth (Termux-friendly default).
//   - SSE events: message (delta), tool_call, done (final text), error.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"eling/internal/agent"
	"eling/internal/config"
	"eling/internal/session"
	"eling/internal/tools"
)

// AgentFactory creates a fully-wired Agent for a session. Injectable so tests
// can substitute a fake upstream provider (httptest) without real credentials.
type AgentFactory func(cfg *config.Config) (*agent.Agent, error)

// Server is the HTTP daemon.
type Server struct {
	cfg     *config.Config
	version string
	token   string
	newAgent AgentFactory

	mu     sync.Mutex
	agents map[string]*agent.Agent // sessionID -> live agent

	httpSrv *http.Server
}

// NewServer builds a daemon. version is the eling version string (e.g. "0.2.5").
func NewServer(cfg *config.Config, version string, factory AgentFactory) *Server {
	if factory == nil {
		factory = agent.New
	}
	return &Server{
		cfg:      cfg,
		version:  version,
		token:    cfg.Server.Token,
		newAgent: factory,
		agents:   make(map[string]*agent.Agent),
	}
}

// Handler returns the full HTTP routing (auth-wrapped). Exposed for tests.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /v1/sessions", s.handleListSessions)
	mux.HandleFunc("GET /v1/sessions/{id}", s.handleGetSession)
	mux.HandleFunc("POST /v1/chat", s.handleChat)
	return s.auth(mux)
}

// Serve runs the daemon on addr until the server is shut down. addr overrides
// cfg.Server.Addr when non-empty (CLI --addr flag).
func (s *Server) Serve(addr string) error {
	if addr == "" {
		addr = s.cfg.Server.Addr
	}
	if addr == "" {
		addr = "127.0.0.1:8765"
	}
	s.httpSrv = &http.Server{Addr: addr, Handler: s.Handler()}
	log.Printf("🧠 ELING daemon listening on http://%s (version %s)", addr, s.version)
	if s.token == "" {
		log.Printf("⚠️  no auth token set — bound to %s; use --token or server.token for LAN", addr)
	}
	err := s.httpSrv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Shutdown gracefully stops the daemon and kills all agent sessions.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	for id, a := range s.agents {
		if err := a.SaveState(); err != nil {
			log.Printf("server: save state for session %s: %v", id, err)
		}
	}
	s.mu.Unlock()
	if s.httpSrv != nil {
		return s.httpSrv.Shutdown(ctx)
	}
	return nil
}

// ── auth ────────────────────────────────────────────────────────────────────

// auth enforces Bearer-token auth when a token is configured.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.token == "" {
			next.ServeHTTP(w, r)
			return
		}
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") || strings.TrimSpace(strings.TrimPrefix(h, "Bearer ")) != s.token {
			writeJSON(w, http.StatusUnauthorized, map[string]interface{}{
				"error": "unauthorized: invalid or missing Bearer token",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ── handlers ────────────────────────────────────────────────────────────────

// handleHealth reports daemon + agent inventory (version, providers, tools, mcp).
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	toolNames := make([]string, 0)
	for _, t := range tools.DefaultRegistry.List() {
		toolNames = append(toolNames, t.Name)
	}
	sort.Strings(toolNames)

	providers := make([]map[string]string, 0, len(s.cfg.Agent.Providers))
	for _, p := range s.cfg.Agent.Providers {
		providers = append(providers, map[string]string{
			"name":     p.Name,
			"model":    p.Model,
			"base_url": p.BaseURL,
		})
	}

	mcpNames := make([]string, 0)
	for _, m := range s.cfg.MCP.Servers {
		mcpNames = append(mcpNames, m.Name)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"version":      s.version,
		"addr":         s.cfg.Server.Addr,
		"providers":    providers,
		"tools":        toolNames,
		"mcp_servers":  mcpNames,
		"active_sessions": s.activeSessionIDs(),
	})
}

// handleListSessions lists session ids across live agents.
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"sessions": s.activeSessionIDs(),
	})
}

// handleGetSession returns the persisted entries for a session id.
func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	a, ok := s.agents[id]
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"error": "session not found: " + id})
		return
	}
	entries, ok := a.Sessions.GetEntriesCopy(a.SessionName())
	if !ok {
		// fall back to scanning saved sessions
		entries = []session.Entry{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session_id": id,
		"entries":    entries,
	})
}

// chatRequest is the POST /v1/chat body.
type chatRequest struct {
	SessionID string `json:"session_id"`
	Prompt    string `json:"prompt"`
}

// handleChat streams an agent turn as SSE.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "bad request: " + err.Error()})
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "prompt is required"})
		return
	}

	ag, err := s.agentFor(req.SessionID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		return
	}

	// SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": "streaming unsupported"})
		return
	}

	// announce which session this turn belongs to
	sseEvent(w, flusher, "session", map[string]interface{}{"session_id": ag.SessionName()})

	// stream the turn
	sseErr := make(chan error, 1)
	go func() {
		_, err := ag.AskStream(r.Context(), req.Prompt,
			func(chunk string) {
				sseEvent(w, flusher, "message", map[string]interface{}{"delta": chunk})
			},
			func(ev agent.ToolCallEvent) {
				sseEvent(w, flusher, "tool_call", map[string]interface{}{
					"tool":  ev.Name,
					"input": ev.Args,
				})
			},
		)
		sseErr <- err
	}()

	select {
	case err := <-sseErr:
		if err != nil {
			sseEvent(w, flusher, "error", map[string]interface{}{"error": err.Error()})
			return
		}
		// pull final text from the session for the done payload
		entries, _ := ag.Sessions.GetEntriesCopy(ag.SessionName())
		final := ""
		if len(entries) > 0 {
			final = entries[len(entries)-1].Content
		}
		sseEvent(w, flusher, "done", map[string]interface{}{"text": final})
	case <-r.Context().Done():
		sseEvent(w, flusher, "error", map[string]interface{}{"error": "client disconnected"})
	}
}

// ── session/agent management ────────────────────────────────────────────────

// agentFor returns the live agent for sessionID, creating one if needed.
// Empty sessionID gets a fresh server-managed id.
func (s *Server) agentFor(sessionID string) (*agent.Agent, error) {
	if strings.TrimSpace(sessionID) == "" {
		sessionID = fmt.Sprintf("srv_%d", time.Now().UnixNano())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.agents[sessionID]; ok {
		return a, nil
	}
	a, err := s.newAgent(s.cfg)
	if err != nil {
		return nil, fmt.Errorf("create agent for session %s: %w", sessionID, err)
	}
	if err := a.LoadState(); err != nil {
		log.Printf("server: no prior state for %s: %v", sessionID, err)
	}
	s.agents[sessionID] = a
	return a, nil
}

// activeSessionIDs returns sorted live session ids.
func (s *Server) activeSessionIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.agents))
	for id := range s.agents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ── helpers ─────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// sseEvent writes one SSE event: `event: <name>\ndata: <json>\n\n`.
func sseEvent(w http.ResponseWriter, f http.Flusher, name string, payload interface{}) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
	f.Flush()
}
