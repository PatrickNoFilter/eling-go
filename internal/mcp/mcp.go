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

	"eling/internal/logger"
)

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
	mu      sync.RWMutex
	servers map[string]*Server
}

// NewManager creates a new MCP manager.
func NewManager() *Manager {
	return &Manager{
		servers: make(map[string]*Server),
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

	if err := server.start(ctx); err != nil {
		return fmt.Errorf("start MCP server %s: %w", name, err)
	}

	m.mu.Lock()
	m.servers[name] = server
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

// start launches the MCP server process and initializes it.
func (s *Server) start(ctx context.Context) error {
	s.cmd = exec.CommandContext(ctx, s.Command, s.Args...)

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
	initResp, err := s.sendRequest(ctx, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]interface{}{
			"name":    "eling",
			"version": "0.1.0",
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
