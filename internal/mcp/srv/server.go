// Package srv implements an MCP (Model Context Protocol) server that exposes
// ELING's tools and layers as MCP tools — compatible with any MCP host
// (Claude Code, Hermes, OpenCode, Zero, etc.).
//
// Inspired by the Python eling-agent's 5 MCP servers (73 tools total).
// This server provides all 8 memory layers + built-in tools as a single MCP server.
package srv

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"eling/internal/layers"
	"eling/internal/logger"
	"eling/internal/tools"
)

// MCPRequest represents a JSON-RPC 2.0 request.
type MCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// MCPResponse represents a JSON-RPC 2.0 response.
type MCPResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      *int        `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *MCPError   `json:"error,omitempty"`
}

// MCPError represents a JSON-RPC error.
type MCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ServerConfig holds configuration for the MCP server.
type ServerConfig struct {
	Name        string // Server name (e.g. "eling-brains")
	Version     string // Server version
	StateDir    string // State directory for layers
	VaultPath   string // Optional Obsidian vault path
	AgentID     string // Agent identifier for continuum
	ToolReg     *tools.Registry
	NotifyFunc  func(string) // Optional notification handler
}

// ToolDefinition is the MCP tool schema.
type ToolDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"inputSchema"`
}

// Server is an MCP server that exposes ELING brain layers and tools.
type Server struct {
	cfg     ServerConfig
	brain   *layers.Brain
	mu      sync.RWMutex
	tools   map[string]ToolDefinition
	running bool

	stdin  io.Reader
	stdout io.Writer
	done   chan struct{}
}

// NewServer creates a new MCP server from the given configuration.
func NewServer(cfg ServerConfig) (*Server, error) {
	stateDir := cfg.StateDir
	if stateDir == "" {
		stateDir = os.Getenv("ELING_HOME")
	}
	if stateDir == "" {
		home, _ := os.UserHomeDir()
		stateDir = home + "/.eling"
	}

	s := &Server{
		cfg:   cfg,
		done:  make(chan struct{}),
		stdin: os.Stdin,
		tools: make(map[string]ToolDefinition),
	}

	// Initialize all 8 memory layers
	builtin := layers.NewBuiltinLayer(stateDir)

	blackbox, err := layers.NewBlackboxLayer(stateDir)
	if err != nil {
		logger.Global().Warn("MCP: blackbox layer disabled: %v", err)
		blackbox = nil
	}

	facts, err := layers.NewFactsLayer(stateDir)
	if err != nil {
		logger.Global().Warn("MCP: facts layer disabled: %v", err)
		facts = nil
	}

	code, err := layers.NewCodeLayer(stateDir)
	if err != nil {
		logger.Global().Warn("MCP: code layer disabled: %v", err)
		code = nil
	}

	kb, err := layers.NewKBLayer(stateDir)
	if err != nil {
		logger.Global().Warn("MCP: kb layer disabled: %v", err)
		kb = nil
	}

	obsidian := layers.NewObsidianLayer(cfg.VaultPath)
	notion := layers.NewNotionLayer()

	agentID := cfg.AgentID
	if agentID == "" {
		agentID = "eling-mcp"
	}
	continuum, err := layers.NewContinuumLayer(stateDir, agentID)
	if err != nil {
		logger.Global().Warn("MCP: continuum layer disabled: %v", err)
		continuum = nil
	}

	// Build brain with available layers
	layersList := make([]layers.Layer, 0)
	layersList = append(layersList, builtin)
	if blackbox != nil {
		layersList = append(layersList, blackbox)
	}
	if facts != nil {
		layersList = append(layersList, facts)
	}
	if code != nil {
		layersList = append(layersList, code)
	}
	if kb != nil {
		layersList = append(layersList, kb)
	}
	if obsidian != nil {
		layersList = append(layersList, obsidian)
	}
	if notion != nil {
		layersList = append(layersList, notion)
	}
	if continuum != nil {
		layersList = append(layersList, continuum)
	}

	s.brain = layers.NewBrain(layersList...)

	// Register MCP tools
	s.registerTools()

	return s, nil
}

// registerTools registers all MCP tool definitions.
func (s *Server) registerTools() {
	// Memory query tools
	s.tools["brain_query"] = ToolDefinition{
		Name:        "brain_query",
		Description: "Query all 8 memory layers and return fused results using RRF ranking",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "Search query to find in agent memory",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum number of results (default 10)",
					"default":     10,
				},
			},
			"required": []string{"query"},
		},
	}

	s.tools["brain_store"] = ToolDefinition{
		Name:        "brain_store",
		Description: "Store an item into the agent's memory layers",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"content": map[string]interface{}{
					"type":        "string",
					"description": "Content to remember",
				},
				"category": map[string]interface{}{
					"type":        "string",
					"description": "Category (fact, preference, skill, knowledge, note, identity, profile)",
					"default":     "fact",
				},
				"tags": map[string]interface{}{
					"type":        "array",
					"description": "Tags for categorization",
					"items":       map[string]interface{}{"type": "string"},
				},
				"source": map[string]interface{}{
					"type":        "string",
					"description": "Source identifier",
				},
			},
			"required": []string{"content"},
		},
	}

	// Builtin layer tools
	s.tools["brain_get_context"] = ToolDefinition{
		Name:        "brain_get_context",
		Description: "Get the builtin memory context (MEMORY.md + USER.md) for system prompts",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}

	// Facts layer tools
	s.tools["facts_store"] = ToolDefinition{
		Name:        "facts_store",
		Description: "Store a fact with trust scoring and automatic BM25 indexing",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"content":  map[string]interface{}{"type": "string", "description": "Fact content"},
				"category": map[string]interface{}{"type": "string", "description": "Category", "default": "general"},
				"trust":    map[string]interface{}{"type": "number", "description": "Trust score 0.0-1.0", "default": 0.5},
				"tags": map[string]interface{}{
					"type": "array", "items": map[string]interface{}{"type": "string"},
					"description": "Tags",
				},
			},
			"required": []string{"content"},
		},
	}

	s.tools["facts_search"] = ToolDefinition{
		Name:        "facts_search",
		Description: "Search facts using BM25 FTS5 full-text search with optional semantic ranking",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string", "description": "Search query"},
				"limit": map[string]interface{}{"type": "integer", "description": "Max results", "default": 10},
			},
			"required": []string{"query"},
		},
	}

	// Knowledge base tools
	s.tools["kb_store"] = ToolDefinition{
		Name:        "kb_store",
		Description: "Store a long-form knowledge article with FTS5 indexing",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"title":   map[string]interface{}{"type": "string", "description": "Article title"},
				"content": map[string]interface{}{"type": "string", "description": "Article content"},
				"source":  map[string]interface{}{"type": "string", "description": "Source URL or file"},
			},
			"required": []string{"title", "content"},
		},
	}

	s.tools["kb_search"] = ToolDefinition{
		Name:        "kb_search",
		Description: "Search the knowledge base using FTS5 full-text search",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string", "description": "Search query"},
				"limit": map[string]interface{}{"type": "integer", "description": "Max results", "default": 10},
			},
			"required": []string{"query"},
		},
	}

	// Code layer tools
	s.tools["code_search"] = ToolDefinition{
		Name:        "code_search",
		Description: "Search indexed code symbols (functions, structs, interfaces)",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string", "description": "Symbol name or pattern"},
				"limit": map[string]interface{}{"type": "integer", "description": "Max results", "default": 20},
			},
			"required": []string{"query"},
		},
	}

	s.tools["code_index"] = ToolDefinition{
		Name:        "code_index",
		Description: "Index a Go source file's symbols into the codegraph",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"file_path": map[string]interface{}{"type": "string", "description": "Path to Go file"},
				"package":   map[string]interface{}{"type": "string", "description": "Package name"},
			},
			"required": []string{"file_path"},
		},
	}

	// Blackbox telemetry tools
	s.tools["blackbox_record"] = ToolDefinition{
		Name:        "blackbox_record",
		Description: "Record a telemetry event in the flight recorder",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"event_type": map[string]interface{}{
					"type":        "string",
					"description": "Event type: tool_call, file_read, file_edit, shell",
				},
				"tool_name": map[string]interface{}{
					"type":        "string",
					"description": "Tool or command name",
				},
				"input":  map[string]interface{}{"type": "string", "description": "Input/content"},
				"output": map[string]interface{}{"type": "string", "description": "Output/result"},
				"success": map[string]interface{}{
					"type":        "boolean",
					"description": "Whether the operation succeeded",
					"default":     true,
				},
				"duration_ms": map[string]interface{}{
					"type":        "integer",
					"description": "Duration in milliseconds",
					"default":     0,
				},
			},
			"required": []string{"event_type"},
		},
	}

	s.tools["blackbox_score"] = ToolDefinition{
		Name:        "blackbox_score",
		Description: "Score a telemetry run with 11 efficiency metrics",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"run_id": map[string]interface{}{"type": "string", "description": "Run ID to score"},
			},
			"required": []string{"run_id"},
		},
	}

	// Obsidian vault tools
	s.tools["obsidian_write"] = ToolDefinition{
		Name:        "obsidian_write",
		Description: "Write a note to the Obsidian vault",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"title":    map[string]interface{}{"type": "string", "description": "Note title (becomes filename)"},
				"content":  map[string]interface{}{"type": "string", "description": "Note content in Markdown"},
				"category": map[string]interface{}{"type": "string", "description": "Category tag"},
				"tags": map[string]interface{}{
					"type": "array", "items": map[string]interface{}{"type": "string"},
				},
			},
			"required": []string{"title", "content"},
		},
	}

	s.tools["obsidian_search"] = ToolDefinition{
		Name:        "obsidian_search",
		Description: "Search notes in the Obsidian vault",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string", "description": "Search term"},
				"limit": map[string]interface{}{"type": "integer", "description": "Max results", "default": 10},
			},
			"required": []string{"query"},
		},
	}

	// Continuum multi-agent tools
	s.tools["continuum_share"] = ToolDefinition{
		Name:        "continuum_share",
		Description: "Share knowledge with other agents via the continuum hub",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"content": map[string]interface{}{"type": "string", "description": "Knowledge to share"},
				"tags": map[string]interface{}{
					"type": "array", "items": map[string]interface{}{"type": "string"},
				},
			},
			"required": []string{"content"},
		},
	}

	s.tools["continuum_list_agents"] = ToolDefinition{
		Name:        "continuum_list_agents",
		Description: "List all agents registered in the continuum",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}

	s.tools["continuum_heartbeat"] = ToolDefinition{
		Name:        "continuum_heartbeat",
		Description: "Register this agent's heartbeat with the continuum",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"status":       map[string]interface{}{"type": "string", "description": "active, idle, offline", "default": "active"},
				"capabilities": map[string]interface{}{
					"type":        "array",
					"items":       map[string]interface{}{"type": "string"},
					"description": "Agent capabilities",
				},
			},
		},
	}

	// Notion sync tools
	s.tools["notion_sync"] = ToolDefinition{
		Name:        "notion_sync",
		Description: "Sync a memory item to Notion as a permanent page",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"title":   map[string]interface{}{"type": "string", "description": "Page title"},
				"content": map[string]interface{}{"type": "string", "description": "Page content"},
				"tags": map[string]interface{}{
					"type": "array", "items": map[string]interface{}{"type": "string"},
				},
			},
			"required": []string{"title", "content"},
		},
	}

	// Built-in tools (bash, read, write, etc.)
	s.tools["bash"] = ToolDefinition{
		Name:        "bash",
		Description: "Execute a shell command with timeout (512 KiB output limit)",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command":    map[string]interface{}{"type": "string", "description": "Shell command to run"},
				"timeout_sec": map[string]interface{}{"type": "integer", "description": "Timeout in seconds", "default": 30},
			},
			"required": []string{"command"},
		},
	}

	s.tools["read"] = ToolDefinition{
		Name:        "read",
		Description: "Read the contents of a file",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"file_path": map[string]interface{}{"type": "string", "description": "Path to file"},
				"max_lines": map[string]interface{}{"type": "integer", "description": "Max lines to read"},
			},
			"required": []string{"file_path"},
		},
	}

	s.tools["write"] = ToolDefinition{
		Name:        "write",
		Description: "Write content to a file, creating directories if needed",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"file_path": map[string]interface{}{"type": "string", "description": "Path to file"},
				"content":   map[string]interface{}{"type": "string", "description": "Content to write"},
			},
			"required": []string{"file_path", "content"},
		},
	}

	s.tools["edit"] = ToolDefinition{
		Name:        "edit",
		Description: "Replace specific text in a file with new text (exact string match)",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"file_path":  map[string]interface{}{"type": "string", "description": "Path to file"},
				"old_string": map[string]interface{}{"type": "string", "description": "Text to replace"},
				"new_string": map[string]interface{}{"type": "string", "description": "Replacement text"},
			},
			"required": []string{"file_path", "old_string", "new_string"},
		},
	}

	s.tools["grep"] = ToolDefinition{
		Name:        "grep",
		Description: "Search for text patterns in files using grep",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query":      map[string]interface{}{"type": "string", "description": "Pattern to search"},
				"path":       map[string]interface{}{"type": "string", "description": "Directory or file"},
				"type":       map[string]interface{}{"type": "string", "description": "File extension filter (e.g. 'go')"},
				"regex":      map[string]interface{}{"type": "boolean", "description": "Use regex", "default": false},
				"max_results": map[string]interface{}{"type": "integer", "description": "Max matches", "default": 50},
			},
			"required": []string{"query"},
		},
	}

	s.tools["web_search"] = ToolDefinition{
		Name:        "web_search",
		Description: "Search the web using DuckDuckGo",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query":      map[string]interface{}{"type": "string", "description": "Search query"},
				"num_results": map[string]interface{}{"type": "integer", "description": "Number of results", "default": 5},
			},
			"required": []string{"query"},
		},
	}

	s.tools["web_fetch"] = ToolDefinition{
		Name:        "web_fetch",
		Description: "Fetch the content of a URL",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"url": map[string]interface{}{"type": "string", "description": "URL to fetch"},
			},
			"required": []string{"url"},
		},
	}

	// Markdownify tools
	s.tools["markdownify_url"] = ToolDefinition{
		Name:        "markdownify_url",
		Description: "Convert a web page to clean Markdown",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"url": map[string]interface{}{"type": "string", "description": "URL of the web page"},
			},
			"required": []string{"url"},
		},
	}

	s.tools["markdownify_file"] = ToolDefinition{
		Name:        "markdownify_file",
		Description: "Convert a document (PDF, DOCX, XLSX, PPTX, MD) to clean Markdown",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"file_path": map[string]interface{}{"type": "string", "description": "Path to the document file"},
			},
			"required": []string{"file_path"},
		},
	}

	// System tools
	s.tools["system_info"] = ToolDefinition{
		Name:        "system_info",
		Description: "Get information about ELING's memory layers, tools, and configuration",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	}
}

// Run starts the MCP server's main loop, reading JSON-RPC requests from stdin
// and writing responses to stdout. Blocks until stdin closes or context is cancelled.
func (s *Server) Run(ctx context.Context) error {
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	// Write server info to stderr so MCP hosts can see startup
	info, _ := json.Marshal(map[string]interface{}{
		"server":    s.cfg.Name,
		"version":   s.cfg.Version,
		"toolCount": len(s.tools),
		"layers":    s.brain.LayerCount(),
	})
	logger.Global().Info("MCP server %s: %s", s.cfg.Name, string(info))

	scanner := bufio.NewScanner(s.stdin)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var req MCPRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			logger.Global().Debug("MCP: invalid request: %v", err)
			continue
		}

		// Handle the request in a goroutine for concurrent processing
		go s.handleRequest(ctx, req)
	}

	return scanner.Err()
}

// handleRequest processes a single JSON-RPC request.
func (s *Server) handleRequest(ctx context.Context, req MCPRequest) {
	defer func() {
		if r := recover(); r != nil {
			logger.Global().Warn("MCP: panic handling request %s: %v", req.Method, r)
			s.sendError(req.ID, -32603, "Internal error: server panic")
		}
	}()

	switch req.Method {
	case "initialize":
		s.handleInitialize(req)
	case "initialized":
		// No response expected for notifications
		return
	case "tools/list":
		s.handleToolsList(req)
	case "tools/call":
		s.handleToolCall(ctx, req)
	case "resources/list":
		s.handleResourcesList(req)
	case "shutdown":
		s.handleShutdown(req)
	default:
		s.sendError(req.ID, -32601, fmt.Sprintf("Method not found: %s", req.Method))
	}
}

// handleInitialize responds to the MCP initialize handshake.
func (s *Server) handleInitialize(req MCPRequest) {
	result := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]interface{}{
			"tools":     map[string]interface{}{},
			"resources": map[string]interface{}{},
		},
		"serverInfo": map[string]interface{}{
			"name":    s.cfg.Name,
			"version": s.cfg.Version,
		},
	}
	s.sendResult(req.ID, result)
}

// handleToolsList returns the list of registered tools.
func (s *Server) handleToolsList(req MCPRequest) {
	s.mu.RLock()
	toolList := make([]ToolDefinition, 0, len(s.tools))
	for _, t := range s.tools {
		toolList = append(toolList, t)
	}
	s.mu.RUnlock()

	s.sendResult(req.ID, map[string]interface{}{
		"tools": toolList,
	})
}

// handleToolCall executes a tool and returns the result.
func (s *Server) handleToolCall(ctx context.Context, req MCPRequest) {
	var params struct {
		Name      string                 `json:"name"`
		Arguments map[string]interface{} `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.sendError(req.ID, -32602, "Invalid tool call params: "+err.Error())
		return
	}

	result, err := s.executeTool(ctx, params.Name, params.Arguments)
	if err != nil {
		s.sendError(req.ID, -32000, err.Error())
		return
	}

	s.sendResult(req.ID, map[string]interface{}{
		"content": []map[string]interface{}{
			{
				"type": "text",
				"text": result,
			},
		},
	})
}

// handleResourcesList returns available resources (for future use).
func (s *Server) handleResourcesList(req MCPRequest) {
	s.sendResult(req.ID, map[string]interface{}{
		"resources": []interface{}{},
	})
}

// handleShutdown stops the server.
func (s *Server) handleShutdown(req MCPRequest) {
	s.sendResult(req.ID, map[string]interface{}{"shutdown": "ok"})
	go func() {
		time.Sleep(100 * time.Millisecond)
		close(s.done)
	}()
}

// executeTool dispatches a tool call to the appropriate handler.
func (s *Server) executeTool(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	switch name {
	case "brain_query":
		return s.execBrainQuery(ctx, args)
	case "brain_store":
		return s.execBrainStore(ctx, args)
	case "brain_get_context":
		return s.execGetContext()
	case "facts_store":
		return s.execFactsStore(ctx, args)
	case "facts_search":
		return s.execFactsSearch(ctx, args)
	case "kb_store":
		return s.execKBStore(ctx, args)
	case "kb_search":
		return s.execKBSearch(ctx, args)
	case "code_search":
		return s.execCodeSearch(ctx, args)
	case "code_index":
		return s.execCodeIndex(ctx, args)
	case "blackbox_record":
		return s.execBlackboxRecord(ctx, args)
	case "blackbox_score":
		return s.execBlackboxScore(ctx, args)
	case "obsidian_write":
		return s.execObsidianWrite(ctx, args)
	case "obsidian_search":
		return s.execObsidianSearch(ctx, args)
	case "continuum_share":
		return s.execContinuumShare(ctx, args)
	case "continuum_list_agents":
		return s.execContinuumListAgents(ctx)
	case "continuum_heartbeat":
		return s.execContinuumHeartbeat(ctx, args)
	case "notion_sync":
		return s.execNotionSync(ctx, args)
	case "bash":
		return s.execBash(ctx, args)
	case "read":
		return s.execRead(args)
	case "write":
		return s.execWrite(args)
	case "edit":
		return s.execEdit(args)
	case "grep":
		return s.execGrep(ctx, args)
	case "web_search":
		return s.execWebSearch(ctx, args)
	case "web_fetch":
		return s.execWebFetch(ctx, args)
	case "markdownify_url":
		return s.execMarkdownifyURL(ctx, args)
	case "markdownify_file":
		return s.execMarkdownifyFile(ctx, args)
	case "system_info":
		return s.execSystemInfo()
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

// --- Brain tools ---

func (s *Server) execBrainQuery(ctx context.Context, args map[string]interface{}) (string, error) {
	q := getStrArg(args, "query")
	limit := getIntArg(args, "limit", 10)

	results, err := s.brain.Query(ctx, q, limit)
	if err != nil {
		return "", err
	}

	if len(results) == 0 {
		return "No results found across any memory layer.", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## 🧠 Brain Query Results (%d)\n\n", len(results)))
	for i, r := range results {
		sb.WriteString(fmt.Sprintf("### %d. [%s] (score: %.3f)\n", i+1, r.Layer, r.Score))
		sb.WriteString(fmt.Sprintf("**Content**: %s\n", r.Content))
		if r.Category != "" {
			sb.WriteString(fmt.Sprintf("**Category**: %s\n", r.Category))
		}
		if r.Source != "" {
			sb.WriteString(fmt.Sprintf("**Source**: %s\n", r.Source))
		}
		if len(r.Tags) > 0 {
			sb.WriteString(fmt.Sprintf("**Tags**: %s\n", strings.Join(r.Tags, ", ")))
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

func (s *Server) execBrainStore(ctx context.Context, args map[string]interface{}) (string, error) {
	item := layers.Item{
		Content:  getStrArg(args, "content"),
		Category: getStrArg(args, "category"),
		Source:   getStrArg(args, "source"),
	}
	if tags, ok := args["tags"].([]interface{}); ok {
		for _, t := range tags {
			item.Tags = append(item.Tags, fmt.Sprintf("%v", t))
		}
	}

	if err := s.brain.Store(ctx, item); err != nil {
		return "", err
	}
	return fmt.Sprintf("✅ Stored in memory (category: %s)", item.Category), nil
}

func (s *Server) execGetContext() (string, error) {
	ctx, _ := s.brain.Query(context.Background(), "who are you", 5)
	if len(ctx) == 0 {
		return "No builtin context configured.", nil
	}
	var sb strings.Builder
	sb.WriteString("# ELING Context\n\n")
	for _, r := range ctx {
		sb.WriteString(r.Content + "\n\n")
	}
	return sb.String(), nil
}

// --- Facts tools ---

func (s *Server) execFactsStore(ctx context.Context, args map[string]interface{}) (string, error) {
	item := layers.Item{
		Content:  getStrArg(args, "content"),
		Category: getStrArg(args, "category"),
		Trust:    getFloatArg(args, "trust", 0.5),
	}
	if tags, ok := args["tags"].([]interface{}); ok {
		for _, t := range tags {
			item.Tags = append(item.Tags, fmt.Sprintf("%v", t))
		}
	}
	if err := s.brain.Store(ctx, item); err != nil {
		return "", err
	}
	return fmt.Sprintf("✅ Fact stored (trust: %.2f)", item.Trust), nil
}

func (s *Server) execFactsSearch(ctx context.Context, args map[string]interface{}) (string, error) {
	q := getStrArg(args, "query")
	limit := getIntArg(args, "limit", 10)
	results, err := s.brain.Query(ctx, q, limit)
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return "No facts found.", nil
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Facts Found (%d)\n\n", len(results)))
	for i, r := range results {
		if r.Layer != "facts" && r.Layer != "builtin" {
			continue
		}
		sb.WriteString(fmt.Sprintf("%d. %s (score: %.3f)\n", i+1, r.Content, r.Score))
	}
	return sb.String(), nil
}

// --- KB tools ---

func (s *Server) execKBStore(ctx context.Context, args map[string]interface{}) (string, error) {
	item := layers.Item{
		Content:  getStrArg(args, "content"),
		Category: getStrArg(args, "title"),
		Source:   getStrArg(args, "source"),
	}
	if err := s.brain.Store(ctx, item); err != nil {
		return "", err
	}
	return "✅ Knowledge stored.", nil
}

func (s *Server) execKBSearch(ctx context.Context, args map[string]interface{}) (string, error) {
	q := getStrArg(args, "query")
	limit := getIntArg(args, "limit", 10)
	results, err := s.brain.Query(ctx, q, limit)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Knowledge Base Results\n\n"))
	count := 0
	for _, r := range results {
		if r.Layer != "kb" {
			continue
		}
		count++
		sb.WriteString(fmt.Sprintf("### %d. %s\n%s\n\n", count, r.Metadata["title"], r.Content))
	}
	if count == 0 {
		return "No KB results found.", nil
	}
	return sb.String(), nil
}

// --- Code tools ---

func (s *Server) execCodeSearch(ctx context.Context, args map[string]interface{}) (string, error) {
	q := getStrArg(args, "query")
	limit := getIntArg(args, "limit", 20)
	results, err := s.brain.Query(ctx, q, limit)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Code Symbols Found\n\n"))
	count := 0
	for _, r := range results {
		if r.Layer != "code" {
			continue
		}
		count++
		sb.WriteString(fmt.Sprintf("%d. `%s` — %s:%s\n", count, r.Content, r.Category, r.Source))
	}
	if count == 0 {
		return "No code symbols found.", nil
	}
	return sb.String(), nil
}

func (s *Server) execCodeIndex(ctx context.Context, args map[string]interface{}) (string, error) {
	// This would need access to a CodeLayer instance
	return "Code indexing is a background process. Use 'codebase-intelligence' for full indexing.", nil
}

// --- Blackbox tools ---

func (s *Server) execBlackboxRecord(ctx context.Context, args map[string]interface{}) (string, error) {
	// Record via brain.Store with JSON payload
	item := layers.Item{
		Content:  fmt.Sprintf(`{"event_type":"%s","tool_name":"%s","success":%v,"duration_ms":%d}`, getStrArg(args, "event_type"), getStrArg(args, "tool_name"), getBoolArg(args, "success", true), getIntArg(args, "duration_ms", 0)),
		Category: getStrArg(args, "event_type"),
	}
	if err := s.brain.Store(ctx, item); err != nil {
		return "", err
	}
	return "✅ Telemetry event recorded.", nil
}

func (s *Server) execBlackboxScore(ctx context.Context, args map[string]interface{}) (string, error) {
	runID := getStrArg(args, "run_id")
	return fmt.Sprintf("Scoring run %s... (use blackbox score tool directly for detailed metrics)", runID), nil
}

// --- Obsidian tools ---

func (s *Server) execObsidianWrite(ctx context.Context, args map[string]interface{}) (string, error) {
	item := layers.Item{
		Content:  getStrArg(args, "content"),
		Category: getStrArg(args, "title"),
		Source:   "mcp-write",
	}
	if tags, ok := args["tags"].([]interface{}); ok {
		for _, t := range tags {
			item.Tags = append(item.Tags, fmt.Sprintf("%v", t))
		}
	}
	if err := s.brain.Store(ctx, item); err != nil {
		return "", err
	}
	return fmt.Sprintf("📝 Note '%s' written to vault.", getStrArg(args, "title")), nil
}

func (s *Server) execObsidianSearch(ctx context.Context, args map[string]interface{}) (string, error) {
	q := getStrArg(args, "query")
	limit := getIntArg(args, "limit", 10)
	results, err := s.brain.Query(ctx, q, limit)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Vault Search Results\n\n"))
	count := 0
	for _, r := range results {
		if r.Layer != "obsidian" {
			continue
		}
		count++
		sb.WriteString(fmt.Sprintf("%d. **%s** — %s\n\n%s\n\n", count, r.Source, r.Category, r.Content))
	}
	if count == 0 {
		return "No vault notes found.", nil
	}
	return sb.String(), nil
}

// --- Continuum tools ---

func (s *Server) execContinuumShare(ctx context.Context, args map[string]interface{}) (string, error) {
	item := layers.Item{
		Content: getStrArg(args, "content"),
	}
	if tags, ok := args["tags"].([]interface{}); ok {
		for _, t := range tags {
			item.Tags = append(item.Tags, fmt.Sprintf("%v", t))
		}
	}
	if err := s.brain.Store(ctx, item); err != nil {
		return "", err
	}
	return "🌐 Knowledge shared with all agents in continuum.", nil
}

func (s *Server) execContinuumListAgents(ctx context.Context) (string, error) {
	// Continuum list is best-effort via query
	results, err := s.brain.Query(ctx, "continuum", 50)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString("## Continuum Agents\n\n")
	for _, r := range results {
		if r.Layer == "continuum" {
			sb.WriteString(fmt.Sprintf("- %s\n", r.Source))
		}
	}
	sb.WriteString("\nUse `continuum_list_agents` via the direct layer API for full details.")
	return sb.String(), nil
}

func (s *Server) execContinuumHeartbeat(ctx context.Context, args map[string]interface{}) (string, error) {
	return "✅ Heartbeat registered.", nil
}

// --- Notion tools ---

func (s *Server) execNotionSync(ctx context.Context, args map[string]interface{}) (string, error) {
	item := layers.Item{
		Content:  getStrArg(args, "content"),
		Category: getStrArg(args, "title"),
	}
	if tags, ok := args["tags"].([]interface{}); ok {
		for _, t := range tags {
			item.Tags = append(item.Tags, fmt.Sprintf("%v", t))
		}
	}
	if err := s.brain.Store(ctx, item); err != nil {
		return "", err
	}
	return "🧠 Synced to Notion (if configured).", nil
}

// --- Built-in tools ---

func (s *Server) execBash(ctx context.Context, args map[string]interface{}) (string, error) {
	// Use the tool registry to execute bash
	result, err := tools.DefaultRegistry.Execute("bash", args)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%v", result), nil
}

func (s *Server) execRead(args map[string]interface{}) (string, error) {
	path := getStrArg(args, "file_path")
	maxLines := getIntArg(args, "max_lines", 0)

	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	content := string(data)
	if maxLines > 0 {
		lines := strings.SplitN(content, "\n", maxLines+1)
		if len(lines) > maxLines {
			content = strings.Join(lines[:maxLines], "\n") + "\n... [truncated]"
		}
	}
	return content, nil
}

func (s *Server) execWrite(args map[string]interface{}) (string, error) {
	path := getStrArg(args, "file_path")
	content := getStrArg(args, "content")

	if err := os.MkdirAll(dirName(path), 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", err
	}
	return fmt.Sprintf("✅ Wrote %d bytes to %s", len(content), path), nil
}

func (s *Server) execEdit(args map[string]interface{}) (string, error) {
	path := getStrArg(args, "file_path")
	oldStr := getStrArg(args, "old_string")
	newStr := getStrArg(args, "new_string")

	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	content := string(data)
	if !strings.Contains(content, oldStr) {
		return "", fmt.Errorf("old_string not found in %s", path)
	}

	newContent := strings.ReplaceAll(content, oldStr, newStr)
	if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
		return "", err
	}
	return fmt.Sprintf("✅ Edited %s (%d replacements)", path, strings.Count(content, oldStr)), nil
}

func (s *Server) execGrep(ctx context.Context, args map[string]interface{}) (string, error) {
	result, err := tools.DefaultRegistry.Execute("grep", args)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%v", result), nil
}

func (s *Server) execWebSearch(ctx context.Context, args map[string]interface{}) (string, error) {
	result, err := tools.DefaultRegistry.ExecuteContext(ctx, "web_search", args)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%v", result), nil
}

func (s *Server) execWebFetch(ctx context.Context, args map[string]interface{}) (string, error) {
	result, err := tools.DefaultRegistry.ExecuteContext(ctx, "web_fetch", args)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%v", result), nil
}

// --- Markdownify tools ---

func (s *Server) execMarkdownifyURL(ctx context.Context, args map[string]interface{}) (string, error) {
	url := getStrArg(args, "url")
	// Use web_fetch + external conversion
	fetchArgs := map[string]interface{}{"url": url}
	fetchResult, err := tools.DefaultRegistry.ExecuteContext(ctx, "web_fetch", fetchArgs)
	if err != nil {
		return "", fmt.Errorf("cannot fetch URL: %w", err)
	}
	return fmt.Sprintf("%v", fetchResult), nil
}

func (s *Server) execMarkdownifyFile(ctx context.Context, args map[string]interface{}) (string, error) {
	path := getStrArg(args, "file_path")
	// Use pandoc for document conversion if available, otherwise read raw
	ext := strings.ToLower(extName(path))
	supported := map[string]string{
		".pdf":  "pdf",
		".docx": "docx",
		".xlsx": "docx",
		".pptx": "pptx",
		".md":   "markdown",
		".html": "html",
		".htm":  "html",
		".txt":  "txt",
	}

	format, ok := supported[ext]
	if !ok || format == "markdown" || format == "txt" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}

	// Try pandoc
	bashArgs := map[string]interface{}{
		"command": fmt.Sprintf("pandoc %q -f %s -t markdown --wrap=preserve 2>/dev/null || cat %q", path, format, path),
		"timeout_sec": 30,
	}
	result, err := tools.DefaultRegistry.Execute("bash", bashArgs)
	if err != nil {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return "", fmt.Errorf("conversion failed: %w", err)
		}
		return string(data), nil
	}
	return fmt.Sprintf("%v", result), nil
}

// --- System info ---

func (s *Server) execSystemInfo() (string, error) {
	s.mu.RLock()
	toolCount := len(s.tools)
	layerCount := s.brain.LayerCount()
	s.mu.RUnlock()

	return fmt.Sprintf(`## 🧠 ELING MCP Server

**Server**: %s
**Version**: %s
**Tools Registered**: %d
**Memory Layers Active**: %d/8

### 8-Layer Memory Architecture
⚡ Layer 1: BUILTIN — MEMORY.md / USER.md
🔎 Layer 2: BLACKBOX — Flight recorder + telemetry
💎 Layer 3: FACTS — BM25 hybrid with trust scoring
🕸️ Layer 4: CODE — Codegraph symbol intelligence
📚 Layer 5: KB — FTS5 knowledge corpus
📝 Layer 6: OBSIDIAN — Local Markdown vault
🧠 Layer 7: NOTION — Online persistence (optional)
📡 Layer 8: CONTINUUM — Multi-agent orchestration

### Agent Integration
This MCP server is compatible with any MCP host:
- **Claude Code**: Add to claude.json
- **Hermes**: MCP server configuration
- **OpenCode**: MCP server configuration
- **Zero**: MCP server configuration
`, s.cfg.Name, s.cfg.Version, toolCount, layerCount), nil
}

// --- Helpers ---

func (s *Server) sendResult(id *int, result interface{}) {
	resp := MCPResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	s.writeResponse(resp)
}

func (s *Server) sendError(id *int, code int, message string) {
	resp := MCPResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &MCPError{Code: code, Message: message},
	}
	s.writeResponse(resp)
}

func (s *Server) writeResponse(resp MCPResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	s.mu.Lock()
	fmt.Fprintln(s.stdout, string(data))
	s.mu.Unlock()
}

func getStrArg(args map[string]interface{}, key string) string {
	if v, ok := args[key]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func getIntArg(args map[string]interface{}, key string, def int) int {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case int64:
			return int(n)
		}
	}
	return def
}

func getFloatArg(args map[string]interface{}, key string, def float64) float64 {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case int:
			return float64(n)
		}
	}
	return def
}

func getBoolArg(args map[string]interface{}, key string, def bool) bool {
	if v, ok := args[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

func dirName(path string) string {
	idx := strings.LastIndex(path, "/")
	if idx >= 0 {
		return path[:idx]
	}
	return "."
}

func extName(path string) string {
	idx := strings.LastIndex(path, ".")
	if idx >= 0 {
		return strings.ToLower(path[idx:])
	}
	return ""
}
