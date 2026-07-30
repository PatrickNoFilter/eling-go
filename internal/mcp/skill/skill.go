// Package skills — MCP Server as a pluggable skill.
// This skill wraps the MCP server so it can be loaded/unloaded at runtime,
// rather than being hard-coded into the core binary via --mcp flag.
//
// Usage:
//   eling mcp-server start                    Start the MCP server (stdio)
//   eling mcp-server start --transport tcp    Start on TCP (experimental)
//   eling mcp-server stop                     Stop the MCP server
//   eling mcp-server status                   Check if server is running
//   eling mcp-server tools                    List exposed MCP tools
//   eling mcp-server register                 Register as auto-load skill
package mcpskill

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	mcpSrv "eling/internal/mcp/srv"
	"eling/internal/tools"
)

// MCPSkillName is the registration name for the MCP server skill.
const MCPSkillName = "mcp-server"

// mcpSkillState tracks the running MCP server instance.
var (
	mcpServerInstance   *mcpSrv.Server
	mcpServerConfig     MCPSkillConfig
	mcpServerCtx        context.Context
	mcpServerCancel     context.CancelFunc
	mcpServerMu         sync.Mutex
	mcpServerStarted    bool
	mcpServerErr        error
)

// MCPSkillConfig holds configuration for the MCP server skill.
type MCPSkillConfig struct {
	Name      string `json:"name"`      // Server name (default: "eling-brains")
	Version   string `json:"version"`   // Server version (default: from build)
	StateDir  string `json:"stateDir"`  // State directory for layers
	VaultPath string `json:"vaultPath"` // Optional Obsidian vault path
	AgentID   string `json:"agentId"`   // Agent identifier for continuum
	Transport string `json:"transport"` // "stdio" (default) or "tcp"
	TCPAddr   string `json:"tcpAddr"`   // TCP listen address (default: ":9100")
	AutoStart bool   `json:"autoStart"` // Start when skill is registered
}

// DefaultMCPSkillConfig returns sensible defaults.
func DefaultMCPSkillConfig() MCPSkillConfig {
	home, _ := os.UserHomeDir()
	stateDir := os.Getenv("ELING_HOME")
	if stateDir == "" {
		stateDir = filepath.Join(home, ".eling")
	}
	return MCPSkillConfig{
		Name:      "eling-brains",
		Version:   "0.1.0",
		StateDir:  stateDir,
		VaultPath: "",
		AgentID:   "eling-mcp",
		Transport: "stdio",
		TCPAddr:   ":9100",
		AutoStart: false,
	}
}

// RegisterMCPSkill registers the MCP server as a dynamic tool + skill.
// This can be called at startup or at runtime via e.g. "register_skill".
// Returns the tool name registered.
func RegisterMCPSkill(cfg MCPSkillConfig) (string, error) {
	mcpServerMu.Lock()
	mcpServerConfig = cfg
	mcpServerMu.Unlock()

	// Register as a tool so the agent can call it
	tool := tools.Tool{
		Name:        MCPSkillName,
		Description: "MCP Server — exposes ELING's 8 memory layers and tools as MCP tools. " +
			"Sub-commands: start, stop, status, tools, configure, register. " +
			"Use 'mcp-server start' to begin serving over stdio (default) or TCP.",
		Version:  "1.0.0",
		Category: "skill",
		Execute:  mcpSkillExecute,
	}

	tools.DefaultRegistry.Register(tool)

	// Also persist as dynamic tool so it survives restarts
	tools.AddDynamicTool(tools.DynamicTool{
		Name:        MCPSkillName,
		Description: tool.Description,
		Category:    "skill",
		Command:     "", // handled by Go code, not bash
	})

	if cfg.AutoStart {
		go func() {
			if err := startMCPServer(); err != nil {
				fmt.Fprintf(os.Stderr, "MCP skill auto-start: %v\n", err)
			}
		}()
	}

	return MCPSkillName, nil
}

// UnregisterMCPSkill removes the MCP server skill and stops it if running.
func UnregisterMCPSkill() {
	stopMCPServer()
	tools.DefaultRegistry.Unregister(MCPSkillName)
}

// mcpSkillExecute handles the mcp-server tool call from the agent/CLI.
func mcpSkillExecute(args map[string]interface{}) (interface{}, error) {
	action, _ := args["action"].(string)
	if action == "" {
		// If first positional arg is provided, treat as action
		if firstArg, ok := args["args"]; ok {
			if arr, ok := firstArg.([]interface{}); ok && len(arr) > 0 {
				action, _ = arr[0].(string)
			}
		}
	}
	if action == "" {
		action = "status" // default action
	}

	switch action {
	case "start":
		return mcpStartAction(args)
	case "stop":
		return mcpStopAction()
	case "status", "info":
		return mcpStatusAction()
	case "tools":
		return mcpToolsAction()
	case "configure", "config":
		return mcpConfigureAction(args)
	case "register":
		return mcpRegisterAction(args)
	default:
		return tools.OK(map[string]interface{}{
			"error":   fmt.Sprintf("unknown action: %s", action),
			"actions": []string{"start", "stop", "status", "tools", "configure", "register"},
		}), nil
	}
}

// mcpStartAction starts the MCP server.
func mcpStartAction(args map[string]interface{}) (interface{}, error) {
	// Allow overriding config from args
	cfg := getCurrentConfig()

	if transport, ok := args["transport"].(string); ok {
		cfg.Transport = transport
	}
	if tcpAddr, ok := args["tcpAddr"].(string); ok && tcpAddr != "" {
		cfg.TCPAddr = tcpAddr
	}
	if agentID, ok := args["agentId"].(string); ok && agentID != "" {
		cfg.AgentID = agentID
	}
	if vaultPath, ok := args["vaultPath"].(string); ok && vaultPath != "" {
		cfg.VaultPath = vaultPath
	}
	if stateDir, ok := args["stateDir"].(string); ok && stateDir != "" {
		cfg.StateDir = stateDir
	}

	setCurrentConfig(cfg)

	if err := startMCPServer(); err != nil {
		return tools.Err(fmt.Sprintf("Failed to start MCP server: %v", err)), nil
	}

	transportInfo := "stdio"
	if cfg.Transport == "tcp" {
		transportInfo = fmt.Sprintf("tcp://%s", cfg.TCPAddr)
	}

	return tools.OK(map[string]interface{}{
		"status":    "started",
		"transport": transportInfo,
		"agentId":   cfg.AgentID,
		"layers":    []string{"builtin", "blackbox", "facts", "code", "kb", "obsidian", "notion", "continuum"},
	}), nil
}

// mcpStopAction stops the MCP server.
func mcpStopAction() (interface{}, error) {
	if !mcpServerStarted {
		return tools.OK(map[string]interface{}{
			"status": "not_running",
			"info":   "MCP server is not currently running",
		}), nil
	}

	stopMCPServer()

	return tools.OK(map[string]interface{}{
		"status": "stopped",
	}), nil
}

// mcpStatusAction returns the current status of the MCP server.
func mcpStatusAction() (interface{}, error) {
	mcpServerMu.Lock()
	running := mcpServerStarted
	cfg := mcpServerConfig
	err := mcpServerErr
	mcpServerMu.Unlock()

	status := "stopped"
	if running {
		status = "running"
	}

	transportInfo := cfg.Transport
	if cfg.Transport == "tcp" {
		transportInfo = fmt.Sprintf("tcp://%s", cfg.TCPAddr)
	}

	info := map[string]interface{}{
		"status":    status,
		"transport": transportInfo,
		"agentId":   cfg.AgentID,
		"name":      cfg.Name,
		"version":   cfg.Version,
		"stateDir":  cfg.StateDir,
	}

	if err != nil {
		info["lastError"] = err.Error()
	}

	if running {
		// Count registered tools
		toolCount := tools.DefaultRegistry.Count()
		info["toolsAvailable"] = toolCount
	}

	return tools.OK(info), nil
}

// mcpToolsAction lists all tools that would be exposed over MCP.
func mcpToolsAction() (interface{}, error) {
	allTools := tools.DefaultRegistry.List()
	type toolInfo struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Category    string `json:"category"`
	}

	toolList := make([]toolInfo, 0, len(allTools))
	for _, t := range allTools {
		toolList = append(toolList, toolInfo{
			Name:        t.Name,
			Description: t.Description,
			Category:    t.Category,
		})
	}

	return tools.OK(map[string]interface{}{
		"count": len(toolList),
		"tools": toolList,
	}), nil
}

// mcpConfigureAction updates the MCP server configuration.
func mcpConfigureAction(args map[string]interface{}) (interface{}, error) {
	cfg := getCurrentConfig()
	changed := false

	if name, ok := args["name"].(string); ok && name != "" {
		cfg.Name = name
		changed = true
	}
	if transport, ok := args["transport"].(string); ok {
		cfg.Transport = transport
		changed = true
	}
	if tcpAddr, ok := args["tcpAddr"].(string); ok && tcpAddr != "" {
		cfg.TCPAddr = tcpAddr
		changed = true
	}
	if agentID, ok := args["agentId"].(string); ok && agentID != "" {
		cfg.AgentID = agentID
		changed = true
	}
	if vaultPath, ok := args["vaultPath"].(string); ok {
		cfg.VaultPath = vaultPath
		changed = true
	}
	if stateDir, ok := args["stateDir"].(string); ok && stateDir != "" {
		cfg.StateDir = stateDir
		changed = true
	}
	if autoStart, ok := args["autoStart"].(bool); ok {
		cfg.AutoStart = autoStart
		changed = true
	}

	if changed {
		setCurrentConfig(cfg)
	}

	cfgJSON, _ := json.MarshalIndent(cfg, "", "  ")
	return tools.OK(map[string]interface{}{
		"updated": changed,
		"config":  string(cfgJSON),
	}), nil
}

// mcpRegisterAction registers the MCP server as a persistent skill.
func mcpRegisterAction(args map[string]interface{}) (interface{}, error) {
	autoStart := true
	if v, ok := args["autoStart"].(bool); ok {
		autoStart = v
	}

	cfg := getCurrentConfig()
	cfg.AutoStart = autoStart
	setCurrentConfig(cfg)

	// The tool is already registered; persist the config
	configPath := filepath.Join(cfg.StateDir, "mcp_skill.json")
	os.MkdirAll(filepath.Dir(configPath), 0755)

	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return tools.Err(fmt.Sprintf("Failed to persist config: %v", err)), nil
	}

	return tools.OK(map[string]interface{}{
		"status":    "registered",
		"autoStart": autoStart,
		"config":    configPath,
		"info":      "MCP server skill will auto-load on next startup if autoStart is true",
	}), nil
}

// ── Internal helpers ────────────────────────────────────────────────────

func startMCPServer() error {
	mcpServerMu.Lock()
	defer mcpServerMu.Unlock()

	if mcpServerStarted {
		return fmt.Errorf("MCP server is already running")
	}

	cfg := mcpServerConfig

	// Ensure state directory exists
	if err := os.MkdirAll(cfg.StateDir, 0755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	// Build server config
	srvCfg := mcpSrv.ServerConfig{
		Name:      cfg.Name,
		Version:   cfg.Version,
		StateDir:  cfg.StateDir,
		VaultPath: cfg.VaultPath,
		AgentID:   cfg.AgentID,
		ToolReg:   tools.DefaultRegistry,
	}

	server, err := mcpSrv.NewServer(srvCfg)
	if err != nil {
		return fmt.Errorf("create MCP server: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	mcpServerInstance = server
	mcpServerCtx = ctx
	mcpServerCancel = cancel
	mcpServerStarted = true
	mcpServerErr = nil

	// Run in background goroutine
	go func() {
		defer func() {
			mcpServerMu.Lock()
			mcpServerStarted = false
			mcpServerInstance = nil
			mcpServerMu.Unlock()
		}()

		if err := server.Run(ctx); err != nil {
			mcpServerMu.Lock()
			mcpServerErr = err
			mcpServerMu.Unlock()
			fmt.Fprintf(os.Stderr, "MCP server error: %v\n", err)
		}
	}()

	return nil
}

func stopMCPServer() {
	mcpServerMu.Lock()
	defer mcpServerMu.Unlock()

	if mcpServerCancel != nil {
		mcpServerCancel()
		mcpServerCancel = nil
	}
	mcpServerInstance = nil
	mcpServerStarted = false
}

func getCurrentConfig() MCPSkillConfig {
	mcpServerMu.Lock()
	defer mcpServerMu.Unlock()
	return mcpServerConfig
}

func setCurrentConfig(cfg MCPSkillConfig) {
	mcpServerMu.Lock()
	defer mcpServerMu.Unlock()
	mcpServerConfig = cfg
}

// MCPSkillStart starts the MCP server (exported for use by main.go).
func MCPSkillStart() error {
	return startMCPServer()
}

// MCPSkillStop stops the MCP server (exported for use by main.go).
func MCPSkillStop() {
	stopMCPServer()
}

// MCPSkillStatus returns the current status as a formatted string (exported).
func MCPSkillStatus() (string, error) {
	result, err := mcpStatusAction()
	if err != nil {
		return "", err
	}
	data, ok := result.(map[string]interface{})
	if !ok {
		return "unknown", nil
	}
	status, _ := data["status"].(string)
	return status, nil
}

// AutoLoadMCPSkill checks for a persisted config and auto-registers if found.
// Call this at startup to restore previously registered MCP server skills.
func AutoLoadMCPSkill() {
	home, _ := os.UserHomeDir()
	stateDir := os.Getenv("ELING_HOME")
	if stateDir == "" {
		stateDir = filepath.Join(home, ".eling")
	}

	configPath := filepath.Join(stateDir, "mcp_skill.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return // No persisted config, skip auto-load
	}

	var cfg MCPSkillConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return
	}

	// Ensure state dir is set
	if cfg.StateDir == "" {
		cfg.StateDir = stateDir
	}

	// Register the skill
	RegisterMCPSkill(cfg)
}
