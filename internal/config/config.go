// Package config provides configuration management for ELING.
// Inspired by jcode's config.toml system.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the full ELING configuration.
type Config struct {
	Agent       AgentConfig       `yaml:"agent"`
	UI          UIConfig          `yaml:"ui"`
	Memory      MemoryConfig      `yaml:"memory"`
	MCP         MCPConfig         `yaml:"mcp"`
	LSP         LSPConfig         `yaml:"lsp"`
	Session     SessionConfig     `yaml:"session"`
	Server      ServerConfig      `yaml:"server"`
	Sandbox     SandboxConfig     `yaml:"sandbox"`
	Hooks       HooksConfig       `yaml:"hooks"`
	Autorepair  AutorepairConfig  `yaml:"autorepair"`
	Verify      VerifyConfig      `yaml:"verify"`
	Automate    AutomateConfig    `yaml:"automate"`
	Permissions PermissionsConfig `yaml:"permissions"`
	Agents      AgentsConfig      `yaml:"agents"`
	Output      OutputConfig      `yaml:"output"`
}

// OutputConfig governs how the agent shapes its user-facing output.
// All fields are OPT-IN: the zero value preserves today's exact behavior, so a
// fresh install sees no surprise capping or formatting changes.
type OutputConfig struct {
	// EndMessageRunes caps the length (in runes, i.e. user-visible chars) of
	// the final assistant message of each completed turn. 0 disables the cap.
	EndMessageRunes int `yaml:"end_message_runes,omitempty"`
	// EndMessageParas caps the number of blank-line-separated paragraphs in the
	// final message. 0 disables the cap.
	EndMessageParas int `yaml:"end_message_paras,omitempty"`
	// EndMessageNoMD strips common markdown bullets/bolding from the final
	// message (markdown syntax, not content). Default false (off).
	EndMessageNoMD bool `yaml:"end_message_no_md,omitempty"`
}

// Active reports whether any end-message output policy is enabled. A fully
// zero block is inactive, so a fresh install preserves today's exact output.
func (o OutputConfig) Active() bool {
	return o.EndMessageRunes > 0 || o.EndMessageParas > 0 || o.EndMessageNoMD
}

// PermissionsConfig configures the D6 per-tool permission profiles. It lets the
// user gate sensitive/destructive tools per project without blanket-approving
// everything. Each tool can be `allow`, `ask` (prompt once per call in the
// TUI), or `deny`; projects can carry a trust level. When the whole block is
// empty (fresh config) the policy is inactive and current behavior is
// preserved (no gates). When active, unlisted tools resolve to Default.
type PermissionsConfig struct {
	Default  string            `yaml:"default,omitempty"`  // "allow" | "ask" | "deny" for unlisted tools
	Rules    []PermissionRule  `yaml:"rules,omitempty"`    // per-tool overrides
	Projects map[string]string `yaml:"projects,omitempty"` // abs project path -> "full" | "ask" | "deny"
}

// PermissionRule pins one tool to a mode. Mode is one of allow / ask / deny.
type PermissionRule struct {
	Tool string `yaml:"tool"`
	Mode string `yaml:"mode"`
}

// Active reports whether any permission policy has been enabled. A fully
// empty block is inactive (inherits the historical allow-everything behavior),
// so a fresh install sees no surprise gates.
func (p PermissionsConfig) Active() bool {
	return p.Default != "" || len(p.Rules) > 0 || len(p.Projects) > 0
}

// AgentsConfig configures the D3 multi-agent parallelism subsystem. It is
// GATED and DEFAULT OFF: parallel sub-agents only engage when Enabled is true.
// When enabled, a job may be split into up to Max (default 2) focused
// sub-agents that each work in their OWN isolated git worktree, so their edits
// never collide on the real tree. Changes are merged only through an explicit
// review report — same-file overlapping edits surface as a conflict diff and
// are never auto-resolved (no silent merge). TokenBudget caps the per-agent
// API budget to protect a free-tier quota when running in parallel.
type AgentsConfig struct {
	Enabled      bool   `yaml:"enabled"`       // parallel sub-agents gate (default off)
	Max          int    `yaml:"max"`           // max concurrent sub-agents (0 = default 2)
	Token        int    `yaml:"token_budget"`  // per-sub-agent token budget cap (0 = unlimited)
	WorktreeRoot string `yaml:"worktree_root"` // override for ~/.eling/worktrees (test/CI)
}

// AutomateConfig configures the D4 scheduled-automations subsystem. When
// Enabled and the daemon (`eling serve`) is running, jobs defined in Jobs[]
// fire on their cron schedule (5-field crontab). Each job runs headlessly and
// its stdout/stderr is appended to ~/.eling/automations.log. Overlap is
// guarded: a job whose previous run is still in-flight is skipped (and
// logged), never run twice concurrently.
type AutomateConfig struct {
	Enabled bool            `yaml:"enabled"` // daemon starts the scheduler when true
	Jobs    []AutomationJob `yaml:"jobs"`
}

// AutomationJob is one scheduled automation. Exactly one of Command or Goal
// should be set. name must be unique across jobs.
type AutomationJob struct {
	Name       string `yaml:"name"`
	Command    string `yaml:"command,omitempty"` // shell command to run (headless exec)
	Goal       string `yaml:"goal,omitempty"`    // natural-language goal run through the agent
	Schedule   string `yaml:"schedule"`          // 5-field crontab: "min hour dom mon dow"
	Enabled    bool   `yaml:"enabled"`
	LastRun    string `yaml:"last_run,omitempty"`    // RFC3339 of most recent fire
	LastStatus string `yaml:"last_status,omitempty"` // ok | error:<summary>
}

// VerifyConfig configures the D2 verify→repair loop (DeepCode heist). When
// enabled (default), after the agent edits code files the loop runs appropriate
// verification evidence (Go → `go test ./...`, other languages → LSP
// diagnostics) and a FAILED verification is never reported as success — the
// failure feeds the next repair round, bounded by MaxRounds and TimeoutSec.
type VerifyConfig struct {
	Enabled    bool   `yaml:"enabled"`     // default true
	MaxRounds  int    `yaml:"max_rounds"`  // repair iterations (0 = default 2)
	TimeoutSec int    `yaml:"timeout_sec"` // per-run evidence timeout (0 = default 60)
	Evidence   string `yaml:"evidence"`    // "auto" (per-task evidence taxonomy, D5 folded in)
}

// AutorepairConfig configures the tool auto-repair subsystem
// (internal/autorepair). Detection + classification always run; autofix is
// the opt-in gate that actually mutates the environment (probe-first,
// idempotent repairs only). Default: autofix OFF — nothing is ever mutated
// without explicit opt-in.
type AutorepairConfig struct {
	Autofix    bool `yaml:"autofix"`     // opt-in: allow probe-first idempotent repairs
	MaxRetries int  `yaml:"max_retries"` // per-repair attempt budget (0 = default 3)
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
	AutoTestTimeoutSec     int              `yaml:"auto_test_timeout_sec"`     // per-run go test timeout (0 = default 45s)
	AutoTestCooldownSec    int              `yaml:"auto_test_cooldown_sec"`    // min seconds between runs (0 = default 10s)
	LearnFromExchange      bool             `yaml:"learn_from_exchange"`       // from Python: LLM-based skill learning
	SaveConversation       bool             `yaml:"save_conversation"`         // save every conversation turn to semantic index
	PlanMode               bool             `yaml:"plan_mode"`                 // plan mode: draft a plan + get approval before executing tools
	ProjectRules           bool             `yaml:"project_rules"`             // ingest project rules (AGENTS.md etc.) into context (D1)
	ProjectRulesMaxChars   int              `yaml:"project_rules_max_chars"`   // cap for ingested project rules (default 4096)
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
	Enabled        bool              `yaml:"enabled"`
	Servers        []MCPServerConfig `yaml:"servers"`
	ConnectTimeout time.Duration     `yaml:"connect_timeout"` // initialize-handshake cap; 0 = default (5s)
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

	// MaxDurationSec is a total wall-clock cap for the whole process/session,
	// enforced as a root context.WithTimeout. 0 = off. Mainly for --run,
	// automate, serve, benchmark (unattended surfaces).
	MaxDurationSec int `yaml:"max_duration_sec"`

	// MaxTurns caps the number of user Ask turns in a session. 0 = off.
	MaxTurns int `yaml:"max_turns"`

	// IdleTimeoutSec auto-saves and exits after N seconds with no user
	// activity. 0 = off. Works for REPL and TUI (interactive surfaces).
	IdleTimeoutSec int `yaml:"idle_timeout_sec"`
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
	Enabled    bool   `yaml:"enabled"`     // default true in Termux root env
	Root       string `yaml:"root"`        // sandbox root dir, e.g. ~/.eling/sandbox
	MaxOutput  int    `yaml:"max_output"`  // max captured bytes (0 = default 512 KiB)
	TimeoutSec int    `yaml:"timeout_sec"` // default bash timeout when unset (0 = default 30s)
	GuardMode  string `yaml:"guard_mode"`  // "block" (default) or "warn" for destructive commands
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
			SystemPrompt:           "You are ELING, an auto-learning evolving AI agent.\n\nSEARCH RULE (enforced): All text searches MUST use ugrep 7.5.0. Call the `ugrep` tool (preferred — it executes ugrep directly); the legacy `grep` tool is a DEPRECATED alias for the same engine. NEVER invoke plain GNU grep via bash and never assume GNU-grep-only behavior. Use ugrep-native flags when useful: -Z (fuzzy), -z (compressed archives), -t <type> (file-type filter), -w (word boundary), -F (fixed strings), -S (smart case), -U (multiline), --json/--csv (structured output), --bool (boolean operators). ugrep is a superset, so standard grep flags (-rn, -I, -m, --exclude-dir, --include) pass through unchanged.\n\nATOMIC COMMIT DISCIPLINE (enforced): Work in small, reviewable steps. Before coding, plan the work as a numbered list of atomic steps. Implement ONE logical change per step, then verify the tree is green — go build + go vet + go test. Only then commit immediately with a conventional message (feat:, fix:, docs:, chore:, refactor:, test:, style:). Repeat: plan → change → verify → commit. Never batch unrelated changes into a single commit; never leave the tree red at commit time.",
			MaxContext:             32768,
			MaxTurnRounds:          0, // 0 = unlimited (uses MaxInt32 fallback)
			MaxTurnDuration:        0, // 0 = no wall-clock timeout
			MaxTurnDurationRetries: 2, // retry up to 2 times on timeout
			AutoTest:               true,
			AutoTestTimeoutSec:     180, // per-run go test timeout; slow ARM cold builds measured at ~95s
			AutoTestCooldownSec:    10,  // min seconds between runs (0 = default 10s)
			LearnFromExchange:      true,
			SaveConversation:       true,
			ProjectRules:           true,
			ProjectRulesMaxChars:   4096,
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
			Enabled:        false,
			Servers:        []MCPServerConfig{},
			ConnectTimeout: 5 * time.Second,
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
			AutoSave:       true,
			SaveDir:        filepath.Join(homeDir, ".eling", "sessions"),
			MaxDurationSec: 0, // 0 = off (no aggregate wall-clock bound)
			MaxTurns:       0, // 0 = off (no turn-count cap)
			IdleTimeoutSec: 0, // 0 = off (no auto exit on idle)
		},
		Server: ServerConfig{
			Enabled: false,
			Addr:    "127.0.0.1:8765", // loopback only by default (Termux-safe)
		},
		Sandbox: SandboxConfig{
			Enabled:    true,
			Root:       filepath.Join(homeDir, ".eling", "sandbox"),
			MaxOutput:  0, // 0 = default 512 KiB
			TimeoutSec: 0, // 0 = default 30s
			GuardMode:  "block",
		},
		Hooks: HooksConfig{
			Scripts: map[string][]string{}, // no user hooks by default
		},
		Autorepair: AutorepairConfig{
			Autofix:    false, // opt-in: nothing is ever mutated by default
			MaxRetries: 3,     // per-repair attempt budget
		},
		Verify: VerifyConfig{
			Enabled:    true, // verify→repair loop ON by default (D2)
			MaxRounds:  2,    // repair iterations before honest-failure reporting
			TimeoutSec: 60,   // per-run evidence timeout
			Evidence:   "auto",
		},
		Automate: AutomateConfig{
			Enabled: false,             // scheduler OFF by default (D4)
			Jobs:    []AutomationJob{}, // no jobs until added
		},
		Permissions: PermissionsConfig{}, // empty => inactive: historical allow behavior preserved (D6)
		Agents: AgentsConfig{
			Enabled: false, // parallel sub-agents OFF by default until battle-tested (D3)
			Max:     2,     // bounded 2-agent split when enabled
			Token:   0,     // 0 = no per-agent token cap
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
