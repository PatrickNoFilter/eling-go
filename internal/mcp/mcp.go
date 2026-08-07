// Package mcp provides MCP (Model Context Protocol) client support.
// Full implementation using JSON-RPC 2.0 over stdio, inspired by jcode's MCP system.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"eling/internal/config"
	"eling/internal/logger"
)

// DefaultConnectTimeout caps the initialize handshake so a server that starts
// but never answers cannot hang Connect (or startup) forever.
const DefaultConnectTimeout = 5 * time.Second

// Tool represents an MCP tool.
type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema,omitempty"`
}

// CallToolResult represents the result of an MCP tool call.
type CallToolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// Notification represents a server-to-client JSON-RPC notification.
type Notification struct {
	Method string          `json:"method"`
	Raw    json.RawMessage `json:"-"`
}

// Server represents a connection to an MCP server via stdio.
type Server struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string

	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Scanner
	stderr  *bufio.Scanner
	mu      sync.Mutex
	msgID   int
	pending map[int]chan<- json.RawMessage
	done    chan struct{}
	notifCh chan Notification // server-to-client notifications
}

// Manager manages multiple MCP server connections.
type Manager struct {
	mu             sync.RWMutex
	servers        map[string]*Server
	connErr        map[string]string // last connect error per server name (surfaced in /mcp, /stats, banner)
	connectTimeout time.Duration     // hard cap on the initialize handshake
}

// NewManager creates a new MCP manager.
func NewManager() *Manager {
	return &Manager{
		servers:        make(map[string]*Server),
		connErr:        make(map[string]string),
		connectTimeout: DefaultConnectTimeout,
	}
}

// SetConnectTimeout overrides the initialize-handshake timeout.
// Values <= 0 keep the current (default 5s) timeout.
func (m *Manager) SetConnectTimeout(d time.Duration) {
	if d <= 0 {
		return
	}
	m.mu.Lock()
	m.connectTimeout = d
	m.mu.Unlock()
}

// ManagerFromConfig builds a Manager configured from cfg.MCP. It applies the
// config's connect timeout when nonzero, but does NOT start any server — call
// Reset (or Connect per server) to actually connect. This is a load helper for
// the P2.2 "reload from config" path.
func ManagerFromConfig(cfg config.MCPConfig) *Manager {
	m := NewManager()
	if cfg.ConnectTimeout > 0 {
		m.SetConnectTimeout(cfg.ConnectTimeout)
	}
	return m
}

// Reset reconciles the manager's live servers against the servers in cfg.
// Servers still listed in config are left running (their in-flight state is
// preserved); servers no longer present are disconnected; new servers are
// connected. Any connect error for a newly added server is recorded in
// Failures() (surfaced in the badge) rather than making Reset fail atomically.
// The session itself is never dropped.
func (m *Manager) Reset(ctx context.Context, cfg config.MCPConfig) {
	m.mu.RLock()
	live := make(map[string]*Server, len(m.servers))
	for n, s := range m.servers {
		live[n] = s
	}
	m.mu.RUnlock()

	want := make(map[string]config.MCPServerConfig, len(cfg.Servers))
	for _, s := range cfg.Servers {
		want[s.Name] = s
	}

	// Disconnect servers no longer configured.
	for name := range live {
		if _, ok := want[name]; !ok {
			_ = m.Disconnect(name)
		}
	}

	// Connect new servers; keep existing ones untouched.
	for name, sc := range want {
		if _, ok := live[name]; ok {
			continue
		}
		if err := m.Connect(ctx, sc.Name, sc.Command, sc.Args, sc.Env); err != nil {
			// Already recorded via connErr; just surface once here.
			logger.Global().Debug("MCP reset failed to add %s: %v", name, err)
		}
	}
}

// Connect starts and initializes an MCP server via stdio.
func (m *Manager) Connect(ctx context.Context, name, command string, args []string, env map[string]string) error {
	server := &Server{
		Name:    name,
		Command: command,
		Args:    args,
		Env:     env,
		pending: make(map[int]chan<- json.RawMessage),
		done:    make(chan struct{}),
		notifCh: make(chan Notification, 64),
	}

	// Bound the initialize handshake with a deadline, but apply it ONLY to the
	// initialize request — never to the child process lifetime. start() spawns
	// the process on a detached context.Background() and uses ctx only for the
	// handshake. A process bound to a cancelling ctx would be killed the moment
	// Connect() returns, so every later ListTools / CallTool would deadlock or
	// fail. The server stays alive until Disconnect() → stop().
	var handshakeCtx context.Context = ctx
	if _, ok := ctx.Deadline(); !ok {
		m.mu.RLock()
		timeout := m.connectTimeout
		m.mu.RUnlock()
		var cancel context.CancelFunc
		handshakeCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel() // only bounds sendRequest(initialize) inside start()
	}

	if err := server.start(ctx, handshakeCtx); err != nil {
		// Record the failure so the TUI can surface it (banner, /mcp, /stats)
		// instead of only logging to the external log stream.
		m.mu.Lock()
		m.connErr[name] = err.Error()
		m.mu.Unlock()
		return fmt.Errorf("start MCP server %s: %w", name, err)
	}

	m.mu.Lock()
	m.servers[name] = server
	delete(m.connErr, name)
	m.mu.Unlock()
	return nil
}

// Disconnect stops an MCP server.
func (m *Manager) Disconnect(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	server, ok := m.servers[name]
	if !ok {
		return fmt.Errorf("MCP server %q not found", name)
	}

	server.stop()
	delete(m.servers, name)
	return nil
}

// ListTools returns all tools from all connected MCP servers.
func (m *Manager) ListTools(ctx context.Context) (map[string][]Tool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string][]Tool)
	for name, server := range m.servers {
		tools, err := server.listTools(ctx)
		if err != nil {
			continue // skip failed servers
		}
		result[name] = tools
	}
	return result, nil
}

// CallTool calls a tool on the specified MCP server.
func (m *Manager) CallTool(ctx context.Context, serverName, toolName string, args map[string]interface{}) (*CallToolResult, error) {
	m.mu.RLock()
	server, ok := m.servers[serverName]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("MCP server %q not found", serverName)
	}

	return server.callTool(ctx, toolName, args)
}

// List returns all connected server names.
func (m *Manager) List() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.servers))
	for n := range m.servers {
		names = append(names, n)
	}
	return names
}

// Failures returns the last connect error per server name for servers that
// failed to start or initialize. Safe to call from the TUI banner hot path.
func (m *Manager) Failures() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make(map[string]string, len(m.connErr))
	for n, e := range m.connErr {
		out[n] = e
	}
	return out
}

// ConnectError returns the last connect error for a named server, or "".
func (m *Manager) ConnectError(name string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.connErr[name]
}

// start launches the MCP server process and initializes it.
// processCtx owns the child's lifetime (caller's context; for startup it is
// context.Background(), so the child survives Connect). handshakeCtx bounds
// only the initialize request — a silent server still fails loudly via the
// connect timeout instead of hanging forever.
func (s *Server) start(processCtx, handshakeCtx context.Context) error {
	s.cmd = exec.CommandContext(processCtx, s.Command, s.Args...)

	// Set environment
	if s.Env != nil {
		env := s.cmd.Environ()
		for k, v := range s.Env {
			env = append(env, k+"="+v)
		}
		s.cmd.Env = env
	}

	stdin, err := s.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	s.stdin = stdin

	stdout, err := s.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	s.stdout = bufio.NewScanner(stdout)
	s.stdout.Buffer(make([]byte, 0, 64*1024), 256*1024)

	stderr, err := s.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	s.stderr = bufio.NewScanner(stderr)

	// Start the process
	if err := s.cmd.Start(); err != nil {
		return fmt.Errorf("start process: %w", err)
	}

	// Read stderr in background
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.SafePanicRecover(r, fmt.Sprintf("MCP stderr reader (%s)", s.Name))
			}
		}()
		for s.stderr.Scan() {
			// Log stderr output for debugging
			line := s.stderr.Text()
			if line != "" {
				logger.Global().Debug("MCP %s stderr: %s", s.Name, line)
			}
		}
	}()

	// Read stdout in background (JSON-RPC response dispatcher)
	go s.readResponses()

	// Initialize with MCP protocol
	initResp, err := s.sendRequest(handshakeCtx, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]interface{}{
			"name":    "eling",
			"version": "0.2.2",
		},
	})
	if err != nil {
		s.stop()
		return fmt.Errorf("initialize: %w", err)
	}

	var initResult map[string]interface{}
	if err := json.Unmarshal(initResp, &initResult); err != nil {
		s.stop()
		return fmt.Errorf("parse init response: %w", err)
	}

	// Send initialized notification
	_ = s.sendNotification("initialized", map[string]interface{}{})

	return nil
}

// stop terminates the MCP server.
func (s *Server) stop() {
	s.mu.Lock()
	select {
	case <-s.done:
		// Already closed — safe to proceed
	default:
		close(s.done)
	}
	s.mu.Unlock()
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	}
}

// listTools retrieves the tool list from the MCP server.
func (s *Server) listTools(ctx context.Context) ([]Tool, error) {
	resp, err := s.sendRequest(ctx, "tools/list", map[string]interface{}{})
	if err != nil {
		return nil, err
	}

	var result struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("parse tools list: %w", err)
	}

	return result.Tools, nil
}

// callTool invokes a tool on the MCP server.
func (s *Server) callTool(ctx context.Context, name string, args map[string]interface{}) (*CallToolResult, error) {
	resp, err := s.sendRequest(ctx, "tools/call", map[string]interface{}{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return nil, err
	}

	var result CallToolResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("parse tool result: %w", err)
	}

	return &result, nil
}

// sendRequest sends a JSON-RPC request and waits for the response.
func (s *Server) sendRequest(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	s.mu.Lock()
	s.msgID++
	id := s.msgID
	ch := make(chan json.RawMessage, 1)
	s.pending[id] = ch
	s.mu.Unlock()

	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	_, err = s.stdin.Write(append(data, '\n'))
	s.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	select {
	case resp := <-ch:
		// Check for error
		var errResp struct {
			Error *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(resp, &errResp); err != nil {
			return nil, fmt.Errorf("parse response: %w", err)
		}
		if errResp.Error != nil {
			return nil, fmt.Errorf("MCP error %d: %s", errResp.Error.Code, errResp.Error.Message)
		}
		return errResp.Result, nil

	case <-ctx.Done():
		return nil, ctx.Err()

	case <-s.done:
		return nil, fmt.Errorf("server stopped")
	}
}

// sendNotification sends a JSON-RPC notification (no response expected).
func (s *Server) sendNotification(method string, params interface{}) error {
	notif := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}

	data, err := json.Marshal(notif)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.stdin.Write(append(data, '\n'))
	return err
}

// readResponses reads JSON-RPC responses from stdout and dispatches them.
// Handles both responses (with id) and notifications (no id, method present).
func (s *Server) readResponses() {
	defer func() {
		if r := recover(); r != nil {
			logger.SafePanicRecover(r, fmt.Sprintf("MCP readResponses (%s)", s.Name))
		}
	}()
	for s.stdout.Scan() {
		line := strings.TrimSpace(s.stdout.Text())
		if line == "" {
			continue
		}

		var base struct {
			ID     *int   `json:"id"`
			Method string `json:"method,omitempty"`
		}
		if err := json.Unmarshal([]byte(line), &base); err != nil {
			continue
		}

		if base.ID != nil {
			// This is a response to a request — dispatch to the pending channel.
			s.mu.Lock()
			ch, ok := s.pending[*base.ID]
			delete(s.pending, *base.ID)
			s.mu.Unlock()
			if ok {
				// Safe send + recover from panic if channel was already closed
				// (e.g. due to request timeout + late response).
				func() {
					defer func() { recover() }()
					select {
					case ch <- json.RawMessage(line):
						close(ch)
					default:
						// Channel full or closed; drop the response silently.
					}
				}()
			}
		} else if base.Method != "" {
			// Server-to-client notification (no id). Notify the manager
			// so that higher-level code can react (e.g., tool list changes).
			select {
			case s.notifCh <- Notification{Method: base.Method, Raw: json.RawMessage(line)}:
			default:
				// Drop notification if nobody is listening
			}
		}
	}
}
