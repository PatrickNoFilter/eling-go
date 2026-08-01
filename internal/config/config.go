// Package config provides configuration management for ELING.
// Inspired by jcode's config.toml system.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config represents the full ELING configuration.
type Config struct {
	Agent   AgentConfig   `yaml:"agent"`
	UI      UIConfig      `yaml:"ui"`
	Memory  MemoryConfig  `yaml:"memory"`
	MCP     MCPConfig     `yaml:"mcp"`
	LSP     LSPConfig     `yaml:"lsp"`
	Session SessionConfig `yaml:"session"`
	Server  ServerConfig  `yaml:"server"`
	Sandbox SandboxConfig `yaml:"sandbox"`
	Hooks   HooksConfig   `yaml:"hooks"`
}

// AgentConfig configures the AI agent.
type AgentConfig struct {
	DefaultModel           string           `yaml:"default_model"`
	DefaultBaseURL         string           `yaml:"default_base_url"`
	SystemPrompt           string           `yaml:"system_prompt"`
	MaxContext             int              `yaml:"max_context"`
	MaxTurnRounds          int              `yaml:"max_turn_rounds"`           // from Python: max tool rounds per turn
	MaxTurnDuration        int              `yaml:"max_turn_duration"`         // wall-clock timeout per turn (seconds)
	MaxTurnDurationRetries int              `yaml:"max_turn_duration_retries"` // max retries on timeout (self-adaptive)
	AutoTest               bool             `yaml:"auto_test"`                 // from Python: auto-run go test on touched test files
	AutoTestTimeoutSec     int              `yaml:"auto_test_timeout_sec"`      // per-run go test timeout (0 = default 45s)
	AutoTestCooldownSec    int              `yaml:"auto_test_cooldown_sec"`     // min seconds between runs (0 = default 10s)
	LearnFromExchange      bool             `yaml:"learn_from_exchange"`       // from Python: LLM-based skill learning
	SaveConversation       bool             `yaml:"save_conversation"`         // save every conversation turn to semantic index
	PlanMode               bool             `yaml:"plan_mode"`                 // plan mode: draft a plan + get approval before executing tools
	Providers              []ProviderConfig `yaml:"providers"`
}

// ProviderConfig configures an LLM provider.
type ProviderConfig struct {
	Name    string `yaml:"name"`
	Model   string `yaml:"model"`
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
	// BackupKeys are additional API keys for key rotation.
	// When the primary key (api_key) fails with an auth error, the provider
	// automatically rotates to the next key in round-robin order.
	BackupKeys []string `yaml:"backup_keys,omitempty"`
	// Retry settings (optional). If zero values, default retry config is used.
	MaxRetries   int `yaml:"max_retries"`
	BaseDelaySec int `yaml:"base_delay_sec"`
	MaxDelaySec  int `yaml:"max_delay_sec"`
	MaxBudgetSec int `yaml:"max_budget_sec"`
}

// UIConfig configures the TUI appearance.
type UIConfig struct {
	Theme             string `yaml:"theme"`
	ShowMemory        bool   `yaml:"show_memory"`
	ShowThinking      bool   `yaml:"show_thinking"`
	VerboseToolOutput bool   `yaml:"verbose_tool_output"` // from Python: show full tool args/results
	MaxMessages       int    `yaml:"max_messages"`
	Timezone          string `yaml:"timezone"` // e.g. "Local", "America/New_York", "Asia/Shanghai"
}

// MemoryConfig configures the memory system.
type MemoryConfig struct {
	MaxShortTerm int     `yaml:"max_short_term"`
	MaxLongTerm  int     `yaml:"max_long_term"`
	DecayRate    float64 `yaml:"decay_rate"`
}

// MCPConfig configures MCP servers.
type MCPConfig struct {
	Enabled bool              `yaml:"enabled"`
	Servers []MCPServerConfig `yaml:"servers"`
}

// MCPServerConfig configures a single MCP server.
type MCPServerConfig struct {
	Name    string            `yaml:"name"`
	Command string            `yaml:"command"`
	Args    []string          `yaml:"args"`
	Env     map[string]string `yaml:"env"`
}

// LSPConfig configures instant diagnostics via language servers (Phase 3).
type LSPConfig struct {
	Enabled bool              `yaml:"enabled"`
	Servers map[string]string `yaml:"servers"` // language key -> server binary
}

// SessionConfig configures session management.
type SessionConfig struct {
	AutoSave bool   `yaml:"auto_save"`
	SaveDir  string `yaml:"save_dir"`
}

// ServerConfig configures the HTTP daemon (`eling serve`, Phase 4).
type ServerConfig struct {
	Enabled bool   `yaml:"enabled"`
	Addr    string `yaml:"addr"`  // default 127.0.0.1:8765 (loopback only)
	Token   string `yaml:"token"` // Bearer token; empty = loopback-only, no auth
}

// SandboxConfig configures bash sandboxing (Phase 1). When enabled, every
// bash command runs in an isolated per-invocation directory with a scrubbed
// environment and a destructive-command guard. Real-tree operations require
// the explicit `allow_host: true` opt-in arg on the bash tool.
type SandboxConfig struct {
	Enabled   bool   `yaml:"enabled"`             // default true in Termux root env
	Root      string `yaml:"root"`                // sandbox root dir, e.g. ~/.eling/sandbox
	MaxOutput int    `yaml:"max_output"`          // max captured bytes (0 = default 512 KiB)
	TimeoutSec int   `yaml:"timeout_sec"`         // default bash timeout when unset (0 = default 30s)
	GuardMode string `yaml:"guard_mode"`          // "block" (default) or "warn" for destructive commands
}

// HooksConfig configures user-defined shell-script hooks (Phase 5). Each key
// is a lifecycle hook name (e.g. "pre_tool_use", "post_tool_use",
// "error_occurred"); the value is a list of script paths executed in order.
// Scripts receive the hook context as JSON on stdin and may emit
// {"block":true,"reason":"..."} on stdout for pre_tool_use to veto a call.
type HooksConfig struct {
	Scripts map[string][]string `yaml:"scripts"`
}

// DefaultConfig returns the default configuration.
func DefaultConfig() *Config {
	homeDir, _ := os.UserHomeDir()
	return &Config{
		Agent: AgentConfig{
			DefaultModel:           "deepseek-v4-flash-free",
			DefaultBaseURL:         "https://opencode.ai/zen/v1",
			SystemPrompt:           "You are ELING, an auto-learning evolving AI agent.",
			MaxContext:             32768,
			MaxTurnRounds:          0, // 0 = unlimited (uses MaxInt32 fallback)
			MaxTurnDuration:        0, // 0 = no wall-clock timeout
			MaxTurnDurationRetries: 2, // retry up to 2 times on timeout
			AutoTest:               true,
			AutoTestTimeoutSec:     180, // per-run go test timeout; slow ARM cold builds measured at ~95s
			AutoTestCooldownSec:    10, // min seconds between runs (0 = default 10s)
			LearnFromExchange:      true,
			SaveConversation:       true,
			Providers: []ProviderConfig{
				{
					Name:    "opencode-zen",
					Model:   "deepseek-v4-flash",
					BaseURL: "https://opencode.ai/zen/v1",
				},
				{
					Name:    "opencode-zen-free",
					Model:   "deepseek-v4-flash-free",
					BaseURL: "https://opencode.ai/zen/v1",
				},
				{
					Name:    "deepseek-direct",
					Model:   "deepseek-v4-flash",
					BaseURL: "https://api.deepseek.com",
				},
			},
		},
		UI: UIConfig{
			Theme:             "default",
			ShowMemory:        true,
			ShowThinking:      true,
			VerboseToolOutput: true,
			MaxMessages:       500,
			Timezone:          "Local",
		},
		Memory: MemoryConfig{
			MaxShortTerm: 50,
			MaxLongTerm:  1000,
			DecayRate:    0.01,
		},
		MCP: MCPConfig{
			Enabled: false,
			Servers: []MCPServerConfig{},
		},
		LSP: LSPConfig{
			Enabled: true,
			Servers: map[string]string{
				"go":         "gopls",
				"python":     "pyright-langserver",
				"typescript": "typescript-language-server",
			},
		},
		Session: SessionConfig{
			AutoSave: true,
			SaveDir:  filepath.Join(homeDir, ".eling", "sessions"),
		},
		Server: ServerConfig{
			Enabled: false,
			Addr:    "127.0.0.1:8765", // loopback only by default (Termux-safe)
		},
		Sandbox: SandboxConfig{
			Enabled:    true,
			Root:       filepath.Join(homeDir, ".eling", "sandbox"),
			MaxOutput:  0,   // 0 = default 512 KiB
			TimeoutSec: 0,   // 0 = default 30s
			GuardMode:  "block",
		},
		Hooks: HooksConfig{
			Scripts: map[string][]string{}, // no user hooks by default
		},
	}
}

// Load loads configuration from a YAML file.
// If the file doesn't exist, returns the default config.
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	return cfg, nil
}

// Save writes the configuration to a YAML file.
func (c *Config) Save(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	return nil
}

// FindConfigPath returns the config file path, trying several locations.
func FindConfigPath() string {
	// Try ELING_CONFIG env var
	if p := os.Getenv("ELING_CONFIG"); p != "" {
		return p
	}

	homeDir, _ := os.UserHomeDir()

	// Try standard locations
	candidates := []string{
		filepath.Join(homeDir, ".eling", "config.yaml"),
		filepath.Join(homeDir, ".eling", "config.yml"),
		filepath.Join(homeDir, ".config", "eling", "config.yaml"),
		".eling.yaml",
		"eling.yaml",
	}

	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	// Return default location
	return filepath.Join(homeDir, ".eling", "config.yaml")
}
