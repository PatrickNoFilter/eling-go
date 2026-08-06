// Package agent implements the core auto-learning AI agent.
// Integrates tool registry, MCP, config, session management, and multi-provider support.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"eling/internal/config"
	"eling/internal/hooks"
	"eling/internal/layers"
	"eling/internal/lsp"
	"eling/internal/learnings"
	"eling/internal/mcp"
	"eling/internal/provider"
	"eling/internal/session"
	"eling/internal/tools"
)

// LearnedSkill is a skill the agent has learned from experience.
type LearnedSkill struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Confidence  float64   `json:"confidence"`
	LearnedAt   time.Time `json:"learned_at"`
	UsedCount   int       `json:"used_count"`
}

// Evolution records a self-improvement step.
type Evolution struct {
	ID          string    `json:"id"`
	Before      string    `json:"before"`
	After       string    `json:"after"`
	Reason      string    `json:"reason"`
	Timestamp   time.Time `json:"timestamp"`
	EffectScore float64   `json:"effect_score"`
}

// ToolCallEvent describes one tool invocation within an Ask turn.
// Sent to the onToolCall callback so the UI can display progress.
// When IsThinking is true, the model is reasoning between tool rounds.
type ToolCallEvent struct {
	SeqID      int                    `json:"seq_id"`
	Name       string                 `json:"name"`
	Args       map[string]interface{} `json:"args,omitempty"`
	ResultText string                 `json:"result,omitempty"`
	Error      string                 `json:"error,omitempty"`
	Reasoning  string                 `json:"reasoning,omitempty"` // model's chain-of-thought reasoning text
	IsStart    bool                   `json:"is_start"`            // true = beginning, false = completed
	IsThinking bool                   `json:"is_thinking"`         // true = model reasoning between rounds
}

// TurnTimeoutRecord stores information about a completed or timed-out turn
// so the agent can predict how long future similar turns might take.
type TurnTimeoutRecord struct {
	PromptLength   int           `json:"prompt_length"`
	ToolCount      int           `json:"tool_count"`
	RoundCount     int           `json:"round_count"`
	ActualDuration time.Duration `json:"actual_duration"`
	TimedOut       bool          `json:"timed_out"`
	Timestamp      time.Time     `json:"timestamp"`
}

// PlanVerdict is the user's decision on a drafted plan (plan mode).
type PlanVerdict int

const (
	// PlanApprove accepts the plan and proceeds with tools enabled.
	PlanApprove PlanVerdict = iota
	// PlanReject aborts the turn without executing any tools.
	PlanReject
	// PlanSkip bypasses plan gating for this turn (Esc in the TUI).
	PlanSkip
)

// Agent is the core auto-learning AI agent, inspired by jcode's architecture.
type Agent struct {
	mu  sync.RWMutex
	cfg *config.Config

	// Providers
	providers *provider.Manager

	// Memory
	memory *Memory

	// Brain with 8-layer memory architecture + lifecycle hooks
	Brain *layers.Brain

	// Tools (like jcode's tool registry)
	ToolRegistry *tools.Registry

	// MCP (like jcode's MCP system)
	MCP *mcp.Manager

	// Sessions (like jcode's session resume)
	Sessions *session.Manager

	// Skills
	skills     []LearnedSkill
	evolutions []Evolution

	// Conversation summary for long-term context compression
	conversationSummary string

	// Learnings (A10): durable lessons loaded from ~/.eling/learnings.md at
	// boot and injected into the system context of every turn so the model
	// applies lessons learned in past sessions. Refreshed by Learn().
	learnings []string

	// projectRules (D1, DeepCode heist): the project's own rules file
	// (AGENTS.md / DEEPCODE.md / CLAUDE.md / .cursor/rules) loaded at boot and
	// injected into every turn's system context so repo-specific conventions
	// steer the agent. Immutable after New() — read under the caller-held
	// a.mu.RLock() in buildMessages (same discipline as a.learnings).
	projectRules     string
	projectRulesFile string

	// State
	stateDir    string
	sessionName string

	// Plan mode (Qwen-code steal, Phase 2): when enabled, Ask() drafts a plan
	// with tools stripped and waits for user approval before executing tools.
	// PlanApprover is the approval callback (set by the TUI); nil = auto-approve.
	// PlanEnabled is atomic so the TUI /plan toggle (event-loop goroutine) can
	// flip it while an Ask goroutine is mid-turn without a data race.
	PlanEnabled  atomic.Bool
	PlanApprover func(plan string) PlanVerdict

	// planApproverMu guards PlanApprover so the TUI can replace it while a
	// previous Ask goroutine is still unwinding (e.g. Ctrl+C → immediate
	// resubmit) without a data race on the callback field.
	planApproverMu sync.RWMutex

	// Turn timeout history for self-adaptive timeout prediction
	turnTimeoutMu   sync.RWMutex
	turnTimeoutHist []TurnTimeoutRecord

	// lastToolMetrics tracks the most recent tool loop execution for accurate
	// turn duration recording. Populated by runToolLoop / runStreamToolLoop
	// on success, read by Ask() after the tool loop completes.
	lastToolRoundCount atomic.Int64
	lastToolCallCount  atomic.Int64

	// lastReasoning stores the most recent model reasoning_content (e.g.
	// DeepSeek thinking mode). Persisted with the assistant session entry so
	// resumed conversations can pass reasoning_content back to the API —
	// DeepSeek rejects assistant messages that omit it in thinking mode.
	lastReasoning atomic.Value // string

	// autoTestCache memoizes autoTest results per package-dir signature so
	// consecutive tool rounds touching the same files don't re-run `go test`
	// (the old behavior made every round slow — the "it takes very long time"
	// complaint). Key: sorted package-arg list joined by "|"; value: outcome.
	autoTestMu    sync.Mutex
	autoTestCache map[string]autoTestOutcome
	autoTestLast  time.Time

	// Provider call metrics (A5 stats dashboard, oh-my-pi steal A5): per
	// provider calls/failures/latency recorded around every ChatStream
	// attempt in chatStreamWithRetry. Read by GetStats.
	providerStatsMu   sync.RWMutex
	providerStats     map[string]*ProviderStat
}

// ProviderStat tracks per-provider call metrics for the stats dashboard (A5).
type ProviderStat struct {
	Calls        int64
	Failures     int64
	TotalLatency time.Duration
	LastCallAt   time.Time
}

// autoTestOutcome records the result of a memoized autoTest run.
type autoTestOutcome struct {
	Passed    bool      // true = all tests passed
	FailText  string    // failure summary when !Passed
	Timestamp time.Time // when the run happened
}

// New creates a new Agent with all subsystems initialized.
func New(cfg *config.Config) (*Agent, error) {
	homeDir, _ := os.UserHomeDir()
	stateDir := filepath.Join(homeDir, ".eling")

	// Initialize providers
	pm := provider.NewManager()
	for _, p := range cfg.Agent.Providers {
		key := p.APIKey
		if key == "" {
			key = os.Getenv("DEEPSEEK_API_KEY")
		}
		prov := provider.New(provider.ProviderConfig{
			Name:       p.Name,
			Model:      p.Model,
			BaseURL:    p.BaseURL,
			APIKey:     key,
			BackupKeys: p.BackupKeys,
		})

		// Apply per-provider retry configuration if any values are set
		rc := prov.GetRetryConfig() // get current defaults
		if p.MaxRetries > 0 {
			rc.MaxRetries = p.MaxRetries
		}
		if p.BaseDelaySec > 0 {
			rc.BaseDelay = time.Duration(p.BaseDelaySec) * time.Second
		}
		if p.MaxDelaySec > 0 {
			rc.MaxDelay = time.Duration(p.MaxDelaySec) * time.Second
		}
		if p.MaxBudgetSec > 0 {
			rc.MaxBudget = time.Duration(p.MaxBudgetSec) * time.Second
		}
		prov.SetRetryConfig(rc)

		pm.AddProvider(p.Name, prov)
	}

	// Set default provider
	if cfg.Agent.DefaultModel != "" {
		for _, p := range cfg.Agent.Providers {
			if p.Model == cfg.Agent.DefaultModel {
				_ = pm.SetDefault(p.Name)
				break
			}
		}
	}

	// Set embedding API env vars from the default provider so the
	// semantic search tools can use the same credentials.
	if len(cfg.Agent.Providers) > 0 {
		p := cfg.Agent.Providers[0]
		// Use the provider's model as the embedding model; many providers have
		// a dedicated embedding model or the chat model can double as one.
		embedModel := p.Model
		tools.SetEmbeddingEnv(p.APIKey, p.BaseURL, embedModel)
	}

	// Initialize subsystems
	mem := NewMemory()
	mem.MaxShort = cfg.Memory.MaxShortTerm
	mem.MaxLong = cfg.Memory.MaxLongTerm

	sesMgr := session.NewManager(cfg.Session.SaveDir)
	mcpMgr := mcp.NewManager()

	a := &Agent{
		cfg:             cfg,
		providers:       pm,
		memory:          mem,
		ToolRegistry:    tools.DefaultRegistry,
		MCP:             mcpMgr,
		Sessions:        sesMgr,
		skills:          make([]LearnedSkill, 0),
		evolutions:      make([]Evolution, 0),
		stateDir:        stateDir,
		sessionName:     fmt.Sprintf("session_%d", time.Now().Unix()),
		turnTimeoutHist: make([]TurnTimeoutRecord, 0),
		autoTestCache:   make(map[string]autoTestOutcome),
		providerStats:   make(map[string]*ProviderStat),
	}

	// Create a default session
	sesMgr.Create(a.sessionName, cfg.Agent.DefaultModel)

	// Connect MCP servers from config
	if cfg.MCP.Enabled {
		for _, s := range cfg.MCP.Servers {
			ctx := context.Background()
			if err := mcpMgr.Connect(ctx, s.Name, s.Command, s.Args, s.Env); err != nil {
				log.Printf("Warning: failed to connect MCP server %s: %v", s.Name, err)
			}
		}
	}

	// Phase 3 (Qwen-code steal): configure the instant-diagnostics LSP client.
	// Best-effort — missing server binaries are silently skipped.
	lsp.Configure(lsp.Config{Enabled: cfg.LSP.Enabled, Servers: cfg.LSP.Servers})

	// A10: load durable learnings from ~/.eling/learnings.md at boot so lessons
	// recorded in past sessions are injected into every turn's system context.
	if ls, err := learnings.Load(); err == nil {
		a.learnings = ls
	} else {
		log.Printf("Warning: could not load learnings: %v", err)
	}

	// D1 (DeepCode heist): ingest the project's own rules file (AGENTS.md /
	// DEEPCODE.md / CLAUDE.md / .cursor/rules) at boot so repo-specific
	// conventions steer every turn. Best-effort: a missing rules file or a
	// probe error is a silent skip (no crash, no rules injected).
	if cfg.Agent.ProjectRules {
		cwd, err := os.Getwd()
		if err != nil {
			log.Printf("Warning: could not resolve cwd for project rules: %v", err)
		} else if file, content := layers.LoadProjectRules(cwd); content != "" {
			a.projectRules = content
			a.projectRulesFile = file
			log.Printf("Project rules loaded from %s (%d chars)", file, len(content))
		}
	}

	return a, nil
}

// maxToolRounds caps how many tool-call round-trips a single Ask/AskStream
// turn may take before we force a final answer.
// Set to MaxInt32 to effectively remove the limit — the real constraint
// is the wall-clock timeout (MaxTurnDuration) and the config MaxTurnRounds.
const maxToolRounds = math.MaxInt32 // effectively unlimited

// defaultMaxTurnDuration is the default wall-clock timeout per turn in seconds.
// 0 means no timeout — the conversation runs until it completes naturally.
const defaultMaxTurnDuration = 0 // no timeout by default

// maxToolResultBytes caps the size of a single tool result string to prevent
// OOM from unbounded tool output (e.g. huge files or command output).
const maxToolResultBytes = 256 * 1024 // 256 KiB

// maxMessagesInToolLoop limits how many messages we keep in the tool loop
// at any point. Older messages are dropped to keep memory bounded.
const maxMessagesInToolLoop = 100

// SetBrain attaches the 8-layer memory Brain to the Agent and registers
// the default lifecycle hooks. Must be called before the first Ask/AskStream
// for hooks to fire. Safe to call once; panics on re-assignment.
func (a *Agent) SetBrain(brain *layers.Brain) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Brain != nil {
		panic("agent: SetBrain called twice")
	}
	a.Brain = brain

	// Wire up semantic_search tool to use Brain.Query for more accurate results
	tools.BrainQuery = func(query string, limit int) ([]tools.SearchResult, error) {
		ctx := context.Background()
		results, err := brain.Query(ctx, query, limit)
		if err != nil {
			return nil, err
		}
		converted := make([]tools.SearchResult, 0, len(results))
		for _, r := range results {
			converted = append(converted, tools.SearchResult{
				Content:  r.Content,
				Score:    r.Score,
				Category: r.Category,
				Tags:     r.Tags,
				Source:   r.Source,
			})
		}
		return converted, nil
	}

	// Register all 15 default lifecycle hooks on this Brain
	brain.RegisterDefaultHooks()
	// Phase 5: register user-defined shell-script hooks from config.yaml.
	// Each script gets a layers.HookHandler; pre_tool_use scripts can veto
	// tool calls via {"block":true,"reason":"..."} on stdout.
	if a.cfg != nil {
		hooks.RegisterUserHooks(brain, a.cfg.Hooks.Scripts)
	}
	// Fire session-start hook with agent metadata
	brain.FireHook(layers.HookSessionStart, map[string]interface{}{
		"agent":       "eling-go",
		"layers":      len(brain.Layers()),
		"total_hooks": brain.Hooks.TotalHandlers(),
	})
}

// fireHook fires a lifecycle hook on the Brain, if available.
// Context values are passed as a map; nil is safe.
// Returns the hook results (nil if Brain is nil or no handlers fired) so
// callers can inspect vetoes (see hooks.CheckVeto).
func (a *Agent) fireHook(hookName string, ctx map[string]interface{}) []interface{} {
	if a.Brain == nil {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[agent] hook %q panicked: %v (caught)", hookName, r)
		}
	}()
	return a.Brain.FireHook(hookName, ctx)
}

// SetPlanApprover atomically replaces the plan-approval callback. Safe to
// call while a previous Ask goroutine may still be running — it waits for
// any in-flight PlanApprover read (inside draftPlan) to finish first.
func (a *Agent) SetPlanApprover(fn func(plan string) PlanVerdict) {
	a.planApproverMu.Lock()
	a.PlanApprover = fn
	a.planApproverMu.Unlock()
}

// draftPlan asks the LLM — with tools stripped and a plan-only system suffix —
// to produce a numbered execution plan for the current prompt, then asks the
// user for a verdict via PlanApprover (nil = auto-approve, used by the CLI).
func (a *Agent) draftPlan(ctx context.Context, prov *provider.Provider, messages []provider.Message, maxDuration int, callbacks ...func(ToolCallEvent)) (PlanVerdict, string, error) {
	planMessages := make([]provider.Message, len(messages)+1)
	copy(planMessages, messages)
	planMessages[len(messages)] = provider.Message{
		Role: "system",
		Content: "Respond ONLY with a numbered execution plan for the user's request. " +
			"No code. No tool calls. No preamble. Format exactly:\n" +
			"1. Step one\n2. Step two\n...",
	}

	planText, _, err := a.runToolLoop(ctx, prov, planMessages, nil, maxDuration, callbacks...)
	if err != nil {
		return PlanReject, "", err
	}
	planText = strings.TrimSpace(planText)

	// Read the callback under lock: the TUI may replace it (or clear it) at
	// any time — e.g. after the user interrupts a turn and submits a new one.
	a.planApproverMu.RLock()
	approver := a.PlanApprover
	a.planApproverMu.RUnlock()
	verdict := PlanApprove
	if approver != nil {
		verdict = approver(planText)
	}
	return verdict, planText, nil
}

// Ask sends a prompt to the LLM, executing any tool calls the model makes
// along the way, and returns the final text response.
// If onToolCall is not nil, it is invoked synchronously for each tool invocation.
// Features self-adaptive timeout: if the turn times out, it retries with a longer
// estimated timeout based on past turn duration history.
func (a *Agent) Ask(ctx context.Context, prompt string, onToolCall ...func(ToolCallEvent)) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// Plan mode: clear any stale plan from a previous turn so buildMessages
	// doesn't inject an outdated plan into the new context.
	if a.PlanEnabled.Load() {
		if s, ok := a.Sessions.Get(a.sessionName); ok && s.Plan != "" {
			s.Plan = ""
		}
	}

	// Build context and messages under read lock (fast)
	a.mu.RLock()
	contextPrompt := a.buildContext(prompt)
	messages := a.buildMessages(contextPrompt)
	prov := a.providers.GetDefault()
	toolDefs := a.ToolRegistry.ToProviderDefs()
	a.mu.RUnlock()

	if prov == nil {
		return "", fmt.Errorf("no provider configured")
	}

	var callbacks []func(ToolCallEvent)
	if len(onToolCall) > 0 && onToolCall[0] != nil {
		callbacks = append(callbacks, onToolCall[0])
	}

	// Self-adaptive timeout: start with the configured value, then increase
	// on timeout based on how far we got.
	maxRetries := a.cfg.Agent.MaxTurnDurationRetries
	if maxRetries <= 0 {
		maxRetries = 2 // default
	}

	startTime := time.Now()
	var lastErr error
	configuredDuration := a.cfg.Agent.MaxTurnDuration
	if configuredDuration <= 0 {
		configuredDuration = defaultMaxTurnDuration
	}
	currentDuration := configuredDuration

	// Predict a better initial timeout based on past similar turns
	predictedDuration := a.estimateTurnDuration(prompt)
	if predictedDuration > 0 {
		// Use the larger of configured and predicted
		if configuredDuration <= 0 || predictedDuration > configuredDuration {
			currentDuration = predictedDuration
		}
	}

	// PLAN MODE: draft a numbered plan with tools stripped, then ask the user
	// to approve / reject / skip it before any tool executes.
	if a.PlanEnabled.Load() {
		verdict, planText, planErr := a.draftPlan(ctx, prov, messages, currentDuration, callbacks...)
		if planErr != nil {
			return "", fmt.Errorf("plan drafting failed: %w", planErr)
		}
		switch verdict {
		case PlanReject:
			return "❌ Plan rejected — no tools were executed.", nil
		case PlanSkip:
			// Skip plan gating for this turn; continue straight to execution.
			log.Printf("[plan] user skipped plan mode for this turn")
		case PlanApprove:
			// Persist the approved plan on the session (shows in saved JSON).
			if s, ok := a.Sessions.Get(a.sessionName); ok {
				s.Plan = planText
			}
			// Rebuild messages so buildMessages injects the approved plan
			// into the execution context (tools stay enabled).
			a.mu.RLock()
			messages = a.buildMessages(contextPrompt)
			a.mu.RUnlock()
		}
	}

	// Fire pre-user-message hook before processing
	a.fireHook(layers.HookPreUserMessage, map[string]interface{}{
		"content": prompt,
		"source":  "user_prompt",
	})

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// On retry, estimate a longer timeout: use the time already spent
			// (which is close to the last timeout) plus a generous buffer.
			elapsed := time.Since(startTime)
			// Use 2x the elapsed time as the new estimate, minimum 60s
			newEstimate := int(elapsed.Seconds()) * 2
			if newEstimate < 60 {
				newEstimate = 60
			}
			currentDuration = newEstimate

			// Notify about retry via thinking event
			retryMsg := fmt.Sprintf("⏱ Turn timed out after %ds (attempt %d/%d). Restarting with %ds timeout...",
				int(time.Since(startTime).Seconds()), attempt, maxRetries, currentDuration)
			for _, cb := range callbacks {
				cb(ToolCallEvent{
					IsThinking: true,
					Reasoning:  retryMsg,
				})
			}
		}

		finalContent, totalTokens, err := a.runToolLoop(ctx, prov, messages, toolDefs, currentDuration, callbacks...)
		if err == nil {
			// Success — record the turn duration for future predictions
			elapsed := time.Since(startTime)
			// Use the metrics tracked by runToolLoop itself (accurate)
			toolCount := int(a.lastToolCallCount.Load())
			roundCount := int(a.lastToolRoundCount.Load())
			a.recordTurnDuration(TurnTimeoutRecord{
				PromptLength:   len(prompt),
				ToolCount:      toolCount,
				RoundCount:     roundCount,
				ActualDuration: elapsed,
				TimedOut:       false,
				Timestamp:      time.Now(),
			})

			// State mutations under write lock (fast)
			a.mu.Lock()
			_ = a.Sessions.Append(a.sessionName, "user", prompt)
			if last, ok := a.Sessions.LastEntry(a.sessionName); ok {
				a.Sessions.SetLastEntryTokens(a.sessionName, estimateSessionEntryTokens(last))
			}

			// Fire post-user-message hook after persisting the user's prompt
			a.fireHook(layers.HookPostUserMessage, map[string]interface{}{
				"content": prompt,
				"source":  "user_prompt",
			})

			_ = a.Sessions.AppendWithReasoning(a.sessionName, "assistant", finalContent, a.lastReasoningString())
			if _, ok := a.Sessions.LastEntry(a.sessionName); ok {
				a.Sessions.SetLastEntryTokens(a.sessionName, totalTokens/2)
			}
			if totalTokens > 0 {
				_ = a.Sessions.SetMetadata(a.sessionName, "total_tokens", fmt.Sprintf("%d", totalTokens))
			}
			a.mu.Unlock()

			// Fire post-assistant-message hook
			a.fireHook(layers.HookPostAssistantMessage, map[string]interface{}{
				"content": finalContent,
				"prompt":  prompt,
			})

			// Auto-learn background tasks
			go a.autoLearn(prompt, finalContent)
			go a.updateConversationSummary()

			return finalContent, nil
		}

		lastErr = err
		// Check if this is a timeout error
		if !isTurnTimeout(err) {
			// Non-timeout error — record but don't retry
			a.recordTurnDuration(TurnTimeoutRecord{
				PromptLength:   len(prompt),
				ActualDuration: time.Since(startTime),
				TimedOut:       false,
				Timestamp:      time.Now(),
			})

			// ★ Save user prompt to session so conversation context is not lost
			// on transient provider errors. The user can then retry or rephrase
			// without the agent forgetting what was being asked.
			a.mu.Lock()
			_ = a.Sessions.Append(a.sessionName, "user",
				prompt+"\n\n[⚠️ This query failed — the provider returned an error. See below.]")
			_ = a.Sessions.Append(a.sessionName, "assistant",
				"[⚠️ Error: "+err.Error()+"]")
			a.mu.Unlock()

			// Fire error-occurred hook
			a.fireHook(layers.HookErrorOccurred, map[string]interface{}{
				"error":     err.Error(),
				"context":   prompt,
				"tool_name": "ask",
			})

			return "", err
		}

		// Timeout occurred — record it and retry with longer estimate
		a.recordTurnDuration(TurnTimeoutRecord{
			PromptLength:   len(prompt),
			ActualDuration: time.Since(startTime),
			TimedOut:       true,
			Timestamp:      time.Now(),
		})

		// If we've exhausted retries, return the timeout error
		if attempt >= maxRetries {
			timeoutErr := fmt.Errorf("turn timed out after %d seconds (retried %d times): %w",
				int(time.Since(startTime).Seconds()), maxRetries, lastErr)
			a.fireHook(layers.HookErrorOccurred, map[string]interface{}{
				"error":     timeoutErr.Error(),
				"context":   prompt,
				"tool_name": "ask_timeout",
			})
			return "", timeoutErr
		}
	}

	return "", lastErr
}

// lastReasoningString returns the most recently stored reasoning_content, or
// "" if none was recorded. Used when persisting assistant session entries so
// resumed conversations can pass reasoning_content back to DeepSeek.
func (a *Agent) lastReasoningString() string {
	if v := a.lastReasoning.Load(); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// runToolLoop drives the provider through successive tool-call rounds until
// it returns a plain text answer (no more tool calls).
// onToolCall callbacks receive ToolCallEvent for each tool invocation.
// Returns the final content and the total tokens used across all rounds.
// maxDuration is the wall-clock timeout in seconds (0 = no timeout).
func (a *Agent) runToolLoop(ctx context.Context, prov *provider.Provider, messages []provider.Message, toolDefs []tools.ToolDef, maxDuration int, onToolCall ...func(ToolCallEvent)) (string, int, error) {
	pToolDefs := make([]provider.ToolDef, len(toolDefs))
	for i, td := range toolDefs {
		pToolDefs[i] = provider.ToolDef{Type: td.Type, Function: td.Function}
	}

	toolSeq := 0
	totalTokens := 0
	var round int
	var partialResponse string // accumulates any partial content for saving on interrupt

	// Extract the user's original prompt from the last user message so we can
	// save it to the session even if the turn is interrupted (Ctrl+C).
	var userPrompt string
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			userPrompt = messages[i].Content
			break
		}
	}

	// Wall-clock timeout: if configured (duration > 0), derive a deadline
	// context so that in-flight provider calls are also interrupted.
	// duration == 0 means no timeout — run indefinitely.
	toolCtx := ctx
	if maxDuration > 0 {
		var cancel context.CancelFunc
		toolCtx, cancel = context.WithTimeout(ctx, time.Duration(maxDuration)*time.Second)
		defer cancel()
	}

	maxRounds := a.cfg.Agent.MaxTurnRounds
	if maxRounds <= 0 {
		maxRounds = maxToolRounds
	}

	// Save interrupted prompt to session on early exit (Ctrl+C or timeout)
	// so the user's query is never lost. Only saves when the tool loop
	// is interrupted, not on successful completion (Ask() handles success saving).
	defer func() {
		if userPrompt == "" {
			return
		}
		// Only save if context was cancelled or timed out (i.e., interrupted)
		if toolCtx.Err() != nil {
			entryContent := userPrompt
			if partialResponse != "" {
				entryContent = userPrompt + "\n\n[INTERRUPTED - partial response received before interruption]"
			} else {
				entryContent = userPrompt + "\n\n[INTERRUPTED - query was interrupted before completion]"
			}
			_ = a.Sessions.Append(a.sessionName, "user", entryContent)
			_ = a.Sessions.Append(a.sessionName, "assistant", "[The agent was interrupted while processing this query. Use /retry to resubmit.]")
			log.Printf("Saved interrupted prompt to session (interrupted)")
		}
	}()

	for round = 0; round < maxRounds; round++ {
		select {
		case <-toolCtx.Done():
			return partialResponse, totalTokens, fmt.Errorf("turn timed out after %d seconds", maxDuration)
		default:
		}
		// Emit thinking event before non-first round Chat() calls
		if round > 0 {
			for _, cb := range onToolCall {
				cb(ToolCallEvent{IsThinking: true})
			}
		}

		// Call provider with outer retry for transient failures.
		// The provider already retries internally (default 5 attempts with
		// exponential backoff), but if those are exhausted, we add a few
		// more at the agent level to handle longer-lived blips.
		chatResp, err := a.chatWithRetry(toolCtx, prov, messages, pToolDefs, onToolCall...)
		if err != nil {
			return "", totalTokens, err
		}
		resp := chatResp

		// Emit reasoning content from the model's response (e.g. DeepSeek reasoning_content)
		// Always store (even when empty) so stale reasoning from an earlier
		// round never leaks into the final session save.
		a.lastReasoning.Store(resp.Reasoning)
		if resp.Reasoning != "" {
			for _, cb := range onToolCall {
				cb(ToolCallEvent{
					Reasoning:  resp.Reasoning,
					IsThinking: true,
				})
			}
		}

		totalTokens += resp.Tokens

		if len(resp.ToolCalls) == 0 {
			if resp.Content == "" {
				// Model returned empty. Retry without tool definitions once.
				if len(pToolDefs) > 0 && round == 0 {
					pToolDefs = nil
					continue
				}
				a.lastToolRoundCount.Store(int64(round))
				a.lastToolCallCount.Store(int64(toolSeq))
				return "(no response from model)", totalTokens, nil
			}
			a.lastToolRoundCount.Store(int64(round))
			a.lastToolCallCount.Store(int64(toolSeq))
			return resp.Content, totalTokens, nil
		}

		// Record the assistant's tool-call turn, then execute each tool
		// and feed the results back as "tool" messages. ReasoningContent is
		// included so DeepSeek thinking mode accepts the follow-up request.
		// Some providers stream tool calls WITHOUT an id — assign unique ids
		// so the assistant tool_calls and the tool result messages always
		// pair up (an empty id would be dropped by sanitize and the API
		// would reject the orphaned tool_calls message).
		resp.ToolCalls = normalizeToolCallIDs(resp.ToolCalls, toolSeq)
		messages = append(messages, provider.Message{
			Role:             "assistant",
			Content:          resp.Content,
			ReasoningContent: resp.Reasoning,
			ToolCalls:        resp.ToolCalls,
		})

		for _, tc := range resp.ToolCalls {
			var args map[string]interface{}
			if tc.Function.Arguments != "" {
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
			}

			toolSeq++
			seq := toolSeq // capture

			// Notify callbacks that the tool is starting
			for _, cb := range onToolCall {
				cb(ToolCallEvent{
					SeqID:   seq,
					Name:    tc.Function.Name,
					Args:    args,
					IsStart: true,
				})
			}

			// Fire pre-tool-use hook before execution
			hookResults := a.fireHook(layers.HookPreToolUse, map[string]interface{}{
				"tool_name": tc.Function.Name,
				"arguments": tc.Function.Arguments,
			})

			// Phase 5: user-defined pre_tool_use hooks can veto the call.
			var result interface{}
			var execErr error
			if blocked, reason := hooks.CheckVeto(hookResults); blocked {
				result = map[string]interface{}{
					"blocked": true,
					"reason":  reason,
				}
			} else {
				result, execErr = a.ToolRegistry.ExecuteContext(toolCtx, tc.Function.Name, args)
			}
			a.incrementSkillUsedCount(tc.Function.Name)
			var resultText string
			if execErr != nil {
				resultText = fmt.Sprintf("Error: %v", execErr)
			} else {
				resultText = safeMarshalCompactJSON(result)
			}

			// Cap tool result size to prevent OOM — use rune-aware truncation
			// to avoid splitting multi-byte UTF-8 runes (e.g. ✅, 🧠, ⚠️)
			if len(resultText) > maxToolResultBytes {
				b := []byte(resultText)
				cut := maxToolResultBytes
				for cut > 0 && !utf8.RuneStart(b[cut]) {
					cut--
				}
				resultText = string(b[:cut]) + "\n... [truncated: result too large]"
			}

			// Fire post-tool-use hook after execution
			hookCtx := map[string]interface{}{
				"tool_name": tc.Function.Name,
				"arguments": tc.Function.Arguments,
				"result":    resultText,
			}
			if execErr != nil {
				hookCtx["error"] = execErr.Error()
			}
			a.fireHook(layers.HookPostToolUse, hookCtx)

			// Phase 3: feed instant LSP diagnostics back to the model so it
			// can self-correct syntax/type errors before the next round.
			resultText = a.augmentToolResultWithLSP(tc.Function.Name, args, resultText)

			errStr := ""
			if execErr != nil {
				errStr = execErr.Error()
			}

			// Notify callbacks that the tool completed
			for _, cb := range onToolCall {
				cb(ToolCallEvent{
					SeqID:      seq,
					Name:       tc.Function.Name,
					Args:       args,
					ResultText: resultText,
					Error:      errStr,
					IsStart:    false,
				})
			}

			messages = append(messages, provider.Message{
				Role:       "tool",
				Content:    resultText,
				ToolCallID: tc.ID,
			})
		}

		// Trim messages to prevent unbounded growth: keep the system message
		// and the most recent messages up to maxMessagesInToolLoop.
		messages = trimToolLoopMessages(messages, maxMessagesInToolLoop)

		// Auto-test on touched files (ported from Python)
		if a.cfg.Agent.AutoTest {
			testFail := a.autoTest(messages)
			if testFail != "" {
				// Feed the test failures back as a plain user message instead
				// of a synthetic assistant tool_calls + tool pair. The old
				// approach used a hardcoded "_auto_test" tool_call_id that
				// DUPLICATED across rounds when tests failed repeatedly, which
				// DeepSeek/OpenAI reject with "insufficient tool messages
				// following tool_calls message". A user message is protocol-safe
				// in every position and cannot produce orphaned tool messages.
				messages = append(messages, provider.Message{
					Role:    "user",
					Content: "[Auto-test result — system-generated, not a user message]\n" + testFail,
				})
			}
		}
	}

	return "", totalTokens, fmt.Errorf("max tool rounds reached without a final answer")
}

// chatWithRetry wraps prov.Chat() with an additional outer retry loop and
// automatic provider fallback on transient errors.
// The provider already retries internally (exponential backoff with jitter),
// but if the transient error survives those attempts we:
//  1. Add a few more agent-level retries with jittered backoff
//  2. If all retries on the current provider fail, fall back to other
//     registered providers automatically
//  3. Only return an error if ALL providers fail
func (a *Agent) chatWithRetry(ctx context.Context, prov *provider.Provider, messages []provider.Message, pToolDefs []provider.ToolDef, onToolCall ...func(ToolCallEvent)) (*provider.ChatResponse, error) {
	const maxOuterRetries = provider.DefaultOuterRetries
	var lastErr error
	var triedProviders []string

	// Defensive: strip orphaned tool messages before sending. OpenAI-compatible
	// APIs reject "tool" messages that don't follow an assistant tool_calls
	// message, so we drop any that slipped through (trimming, fallback, etc.).
	messages = sanitizeToolMessages(messages)

	// Collect all available providers for fallback
	allProviders := a.getProvidersForFallback(prov)
	fallbackIdx := 0 // 0 = current provider, 1+ = fallbacks

	for fallbackIdx < len(allProviders) {
		currentProv := allProviders[fallbackIdx]
		provName := a.getProviderName(currentProv)
		triedProviders = append(triedProviders, provName)

		// Emit thinking event when switching providers
		if fallbackIdx > 0 {
			msg := fmt.Sprintf("🔄 Primary provider busy, switching to %q...", provName)
			for _, cb := range onToolCall {
				cb(ToolCallEvent{
					IsThinking: true,
					Reasoning:  msg,
				})
			}
			// Brief cooldown before using fallback provider
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("retry aborted during provider fallback: %w", ctx.Err())
			case <-time.After(500 * time.Millisecond):
			}
		}

		for attempt := 0; attempt <= maxOuterRetries; attempt++ {
			if attempt > 0 {
				// Jittered exponential backoff: 2s, 4s, 8s
				baseDelay := time.Duration(1<<uint(attempt)) * time.Second
				// Add up to 30% jitter
				jitter := time.Duration(rand.Int63n(int64(float64(baseDelay) * 0.3)))
				delay := baseDelay + jitter
				select {
				case <-ctx.Done():
					return nil, fmt.Errorf("agent-level retry aborted: %w", ctx.Err())
				case <-time.After(delay):
				}
				// Emit thinking event to notify the UI
				for _, cb := range onToolCall {
					cb(ToolCallEvent{IsThinking: true})
				}
			}

			resp, err := currentProv.Chat(ctx, messages, pToolDefs...)
			if err == nil {
				return resp, nil
			}
			lastErr = err

			// If error is non-retryable, bail immediately (don't try fallbacks either)
			if !provider.IsRetryable(err) {
				return nil, err // already human-friendly from formatAPIError
			}

			// If budget is exceeded, the provider already exhausted its own retries.
			// Don't continue outer retries — try the next provider instead.
			if provider.RetryBudgetExceeded(err) || attempt >= maxOuterRetries {
				break // out of retry loop, try next provider
			}
			// continue retrying on this provider
		}

		fallbackIdx++

		// Check if context was cancelled during the retries
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("retry aborted after provider fallback: %w", ctx.Err())
		default:
		}
	}

	// All providers exhausted — produce a clear, human-readable message
	if len(triedProviders) > 1 {
		return nil, fmt.Errorf("⚠️ All %d providers unavailable after retries (%s). Last error: %w",
			len(triedProviders), strings.Join(triedProviders, ", "), lastErr)
	}
	if provider.RetryBudgetExceeded(lastErr) {
		return nil, fmt.Errorf("⚠️ Provider %q temporarily unavailable — the service didn't respond after several retries. Check your connection or try again later.", triedProviders[0])
	}
	return nil, fmt.Errorf("⚠️ %w", lastErr)
}

// chatStreamWithRetry wraps prov.ChatStream() with an additional outer retry
// loop for transient failures that happen before any content streamed, PLUS
// automatic provider fallback if the current provider is unavailable.
// If content has already started streaming, the error is returned as-is
// (the provider already handles partial-stream retry correctly).
// Returns (content, reasoning, toolCalls, err) — reasoning is the DeepSeek
// reasoning_content accumulated during the stream.
func (a *Agent) chatStreamWithRetry(ctx context.Context, prov *provider.Provider, messages []provider.Message, onChunk func(string), pToolDefs []provider.ToolDef, onToolCall ...func(ToolCallEvent)) (string, string, []provider.ToolCall, error) {
	const maxOuterRetries = provider.DefaultOuterRetries
	var lastErr error
	var triedProviders []string

	// Defensive: strip orphaned tool messages before sending (same rationale
	// as chatWithRetry — a lone tool message breaks the whole request).
	messages = sanitizeToolMessages(messages)

	// Collect all available providers for fallback
	allProviders := a.getProvidersForFallback(prov)
	fallbackIdx := 0

	for fallbackIdx < len(allProviders) {
		currentProv := allProviders[fallbackIdx]
		provName := a.getProviderName(currentProv)
		triedProviders = append(triedProviders, provName)

		// Emit thinking event when switching providers
		if fallbackIdx > 0 {
			msg := fmt.Sprintf("🔄 Primary provider busy, switching to %q...", provName)
			for _, cb := range onToolCall {
				cb(ToolCallEvent{IsThinking: true, Reasoning: msg})
			}
			select {
			case <-ctx.Done():
				return "", "", nil, fmt.Errorf("stream retry aborted during fallback: %w", ctx.Err())
			case <-time.After(500 * time.Millisecond):
			}
		}

		for attempt := 0; attempt <= maxOuterRetries; attempt++ {
			if attempt > 0 {
				// Jittered exponential backoff: 2s, 4s
				baseDelay := time.Duration(1<<uint(attempt)) * time.Second
				jitter := time.Duration(rand.Int63n(int64(float64(baseDelay) * 0.3)))
				delay := baseDelay + jitter
				select {
				case <-ctx.Done():
					return "", "", nil, fmt.Errorf("agent-level stream retry aborted: %w", ctx.Err())
				case <-time.After(delay):
				}
				for _, cb := range onToolCall {
					cb(ToolCallEvent{IsThinking: true})
				}
			}

			content, reasoning, toolCalls, err := currentProv.ChatStream(ctx, messages, onChunk, pToolDefs...)
			// A5: per-provider call metrics for the stats dashboard.
			a.recordProviderCall(provName, err, time.Now())
			if err == nil {
				return content, reasoning, toolCalls, nil
			}
			lastErr = err

			// If we got partial content, don't retry on this provider — return what we have
			if content != "" || len(toolCalls) > 0 {
				return content, reasoning, toolCalls, err
			}

			if !provider.IsRetryable(err) {
				return "", "", nil, err // already human-friendly from formatAPIError
			}

			// Exhausted retries on this provider — try next one
			if provider.RetryBudgetExceeded(err) || attempt >= maxOuterRetries {
				break
			}
		}

		fallbackIdx++

		select {
		case <-ctx.Done():
			return "", "", nil, fmt.Errorf("stream retry aborted after fallback: %w", ctx.Err())
		default:
		}
	}

	if len(triedProviders) > 1 {
		return "", "", nil, fmt.Errorf("⚠️ All %d providers unavailable after retries (%s). Last error: %w",
			len(triedProviders), strings.Join(triedProviders, ", "), lastErr)
	}
	if provider.RetryBudgetExceeded(lastErr) {
		return "", "", nil, fmt.Errorf("⚠️ Provider %q temporarily unavailable — the service didn't respond after several retries. Check your connection or try again later.", triedProviders[0])
	}
	return "", "", nil, fmt.Errorf("⚠️ %w", lastErr)
}

// trimToolLoopMessages keeps only the system message (first) and the last n
// messages, to prevent the messages slice from growing without bound across
// many tool rounds.
// Unlike a simple tail-slice, this function ensures that tool calls and their
// corresponding results are never separated (tool call pairing), matching
// jcode's safe_compaction_cutoff behavior.
func trimToolLoopMessages(msgs []provider.Message, keepLast int) []provider.Message {
	if len(msgs) <= keepLast+1 {
		return msgs
	}
	// Always keep the system message at index 0, then the last keepLast
	// messages. But ensure we don't split tool-call/result pairs.
	system := msgs[0]
	tail := msgs[len(msgs)-keepLast:]

	// Scan backward to prevent splitting tool-call/result pairs.
	// A tool call is an assistant message with ToolCalls, followed by one or
	// more tool messages. If the tail starts with tool results, walk back
	// through consecutive tool results until we find the assistant message
	// that issued them, and include it to keep the pair intact.
	adjusted := tail
	if len(adjusted) > 0 && adjusted[0].Role == "tool" {
		// Walk backward from the cut point through ALL tool results
		// to find the assistant message that issued the tool calls.
		cutIdx := len(msgs) - keepLast
		for i := cutIdx - 1; i >= 0; i-- {
			prev := msgs[i]
			if prev.Role == "assistant" && len(prev.ToolCalls) > 0 {
				// Found the issuing assistant message — prepend it
				adjusted = append([]provider.Message{prev}, adjusted...)
				break
			} else if prev.Role != "tool" {
				// Non-tool, non-assistant-with-calls — stop looking
				break
			}
			// prev is another tool result — keep walking backward
		}
	}

	result := make([]provider.Message, 0, 1+len(adjusted))
	result = append(result, system)
	result = append(result, adjusted...)
	return result
}

// sanitizeToolMessages is a defensive safety net that repairs the message
// sequence in BOTH directions so OpenAI-compatible APIs (DeepSeek, OpenAI,
// etc.) never reject a request:
//
//  1. Drops any "tool" role message whose tool_call_id was NOT declared by a
//     preceding assistant message's tool_calls ("orphan" tool messages).
//
//  2. Strips tool_calls from any assistant message that is NOT immediately
//     followed by tool messages responding to every tool_call_id it declared
//     ("unsatisfied" tool calls — e.g. after an interrupted/aborted turn
//     where the connection dropped mid-execution, or after trimming removed
//     the results). DeepSeek rejects these with:
//
//     "An assistant message with 'tool_calls' must be followed by tool
//     messages responding to each 'tool_call_id'. (insufficient tool
//     messages following tool_calls message)"
//
// This guards against every path that could produce an invalid sequence
// (trimming edge cases, resumed sessions, interrupted turns, synthetic
// injections, provider fallbacks) so a single bad message can never take
// down the whole request.
func sanitizeToolMessages(msgs []provider.Message) []provider.Message {
	// Fast path: nothing to do if there are no tool messages AND no assistant
	// tool_calls messages (an assistant tool_calls with no results is itself
	// an invalid sequence that must be stripped).
	hasTool := false
	for _, m := range msgs {
		if m.Role == "tool" || (m.Role == "assistant" && len(m.ToolCalls) > 0) {
			hasTool = true
			break
		}
	}
	if !hasTool {
		return msgs
	}

	// Pass 1: drop orphaned tool messages (id never declared by a preceding
	// assistant tool_calls message, or empty id).
	declared := make(map[string]bool)
	clean := make([]provider.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				if tc.ID != "" {
					declared[tc.ID] = true
				}
			}
			clean = append(clean, m)
			continue
		}
		if m.Role == "tool" {
			// Keep only if its id was declared by a preceding assistant
			// tool_calls message. Drop orphans (including empty ids).
			if m.ToolCallID != "" && declared[m.ToolCallID] {
				clean = append(clean, m)
			}
			continue
		}
		clean = append(clean, m)
	}

	// Pass 2: for each assistant message with tool_calls, verify its calls are
	// satisfied by tool messages immediately following it. If not (interrupted
	// turn, dropped results, role boundary), strip the tool_calls so the API
	// never sees an unsatisfied tool_calls message, and drop the now-orphaned
	// tool messages that followed it.
	out := make([]provider.Message, 0, len(clean))
	for i := 0; i < len(clean); i++ {
		m := clean[i]
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			out = append(out, m)
			continue
		}

		// Collect the ids this assistant message declares. Calls with an
		// EMPTY id can never be satisfied: Pass 1 already dropped any tool
		// message with an empty tool_call_id, so the API would see a
		// tool_calls entry with no response ("insufficient tool messages
		// following tool_calls message"). Treat them as unsatisfied.
		want := make(map[string]bool)
		hasEmptyID := false
		for _, tc := range m.ToolCalls {
			if tc.ID == "" {
				hasEmptyID = true
			} else {
				want[tc.ID] = true
			}
		}

		// Look ahead through consecutive tool messages to see which ids
		// actually received a response.
		j := i + 1
		for j < len(clean) && clean[j].Role == "tool" {
			delete(want, clean[j].ToolCallID)
			j++
		}

		if len(want) == 0 && !hasEmptyID {
			// Every declared call got a response — keep the assistant
			// message and its tool results intact.
			out = append(out, m)
			for k := i + 1; k < j; k++ {
				out = append(out, clean[k])
			}
			i = j - 1
			continue
		}

		// Unsatisfied calls — strip tool_calls from the assistant message and
		// drop the (now orphaned) tool results that followed it. The content
		// (if any) is preserved so the model still sees the text.
		m.ToolCalls = nil
		out = append(out, m)
		i = j - 1 // skip the tool messages; they are dropped with the calls
	}
	return out
}

// AskStream sends a prompt and streams the response, executing any tool
// calls the model makes along the way (tool rounds are not themselves
// streamed token-by-token to onChunk, only the final text-generating round is).
// If onToolCall is not nil, it is invoked synchronously for each tool invocation.
// Features self-adaptive timeout: if the turn times out, it retries with a longer
// estimated timeout based on past turn duration history.
func (a *Agent) AskStream(ctx context.Context, prompt string, onChunk func(string), onToolCall ...func(ToolCallEvent)) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	a.mu.RLock()
	contextPrompt := a.buildContext(prompt)
	messages := a.buildMessages(contextPrompt)
	prov := a.providers.GetDefault()
	toolDefs := a.ToolRegistry.ToProviderDefs()
	a.mu.RUnlock()
	if prov == nil {
		return "", fmt.Errorf("no provider configured")
	}

	pToolDefs := make([]provider.ToolDef, len(toolDefs))
	for i, td := range toolDefs {
		pToolDefs[i] = provider.ToolDef{Type: td.Type, Function: td.Function}
	}

	var callbacks []func(ToolCallEvent)
	if len(onToolCall) > 0 && onToolCall[0] != nil {
		callbacks = append(callbacks, onToolCall[0])
	}

	// Self-adaptive timeout: start with the configured value, then increase
	// on timeout based on how far we got.
	maxRetries := a.cfg.Agent.MaxTurnDurationRetries
	if maxRetries <= 0 {
		maxRetries = 2
	}

	startTime := time.Now()
	var lastErr error
	configuredDuration := a.cfg.Agent.MaxTurnDuration
	if configuredDuration <= 0 {
		configuredDuration = defaultMaxTurnDuration
	}
	currentDuration := configuredDuration

	// Predict a better initial timeout based on past similar turns
	predictedDuration := a.estimateTurnDuration(prompt)
	if predictedDuration > 0 {
		if configuredDuration <= 0 || predictedDuration > configuredDuration {
			currentDuration = predictedDuration
		}
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			elapsed := time.Since(startTime)
			newEstimate := int(elapsed.Seconds()) * 2
			if newEstimate < 60 {
				newEstimate = 60
			}
			currentDuration = newEstimate

			retryMsg := fmt.Sprintf("⏱ Turn timed out after %ds (attempt %d/%d). Restarting with %ds timeout...",
				int(time.Since(startTime).Seconds()), attempt, maxRetries, currentDuration)
			for _, cb := range callbacks {
				cb(ToolCallEvent{
					IsThinking: true,
					Reasoning:  retryMsg,
				})
			}
		}

		fullResponse, totalTokens, err := a.runStreamToolLoop(ctx, prov, messages, onChunk, pToolDefs, currentDuration, callbacks...)
		if err == nil {
			// Success — record the turn duration
			a.recordTurnDuration(TurnTimeoutRecord{
				PromptLength:   len(prompt),
				ToolCount:      int(a.lastToolCallCount.Load()),
				RoundCount:     int(a.lastToolRoundCount.Load()),
				ActualDuration: time.Since(startTime),
				TimedOut:       false,
				Timestamp:      time.Now(),
			})

			a.mu.Lock()
			_ = a.Sessions.Append(a.sessionName, "user", prompt)
			if last, ok := a.Sessions.LastEntry(a.sessionName); ok {
				a.Sessions.SetLastEntryTokens(a.sessionName, estimateSessionEntryTokens(last))
			}
			_ = a.Sessions.AppendWithReasoning(a.sessionName, "assistant", fullResponse, a.lastReasoningString())
			if _, ok := a.Sessions.LastEntry(a.sessionName); ok {
				a.Sessions.SetLastEntryTokens(a.sessionName, totalTokens/2)
			}
			if totalTokens > 0 {
				_ = a.Sessions.SetMetadata(a.sessionName, "total_tokens", fmt.Sprintf("%d", totalTokens))
			}
			a.mu.Unlock()

			go a.autoLearn(prompt, fullResponse)
			go a.updateConversationSummary()

			return fullResponse, nil
		}

		lastErr = err
		if !isTurnTimeout(err) {
			a.recordTurnDuration(TurnTimeoutRecord{
				PromptLength:   len(prompt),
				ActualDuration: time.Since(startTime),
				TimedOut:       false,
				Timestamp:      time.Now(),
			})

			// ★ Save user prompt and any partial response to session so
			// conversation context is not lost on transient provider errors.
			savedContent := fullResponse
			if savedContent == "" {
				savedContent = "[⚠️ Error: " + err.Error() + "]"
			} else {
				savedContent = savedContent + "\n\n[⚠️ Stream interrupted: " + err.Error() + "]"
			}
			a.mu.Lock()
			_ = a.Sessions.Append(a.sessionName, "user",
				prompt+"\n\n[⚠️ This query failed — the provider returned an error. See below.]")
			_ = a.Sessions.Append(a.sessionName, "assistant", savedContent)
			a.mu.Unlock()

			// Return partial content if we have it, along with the error
			if fullResponse != "" {
				return fullResponse, fmt.Errorf("partial response with error: %w", err)
			}
			return "", err
		}

		// Timeout — record and retry
		a.recordTurnDuration(TurnTimeoutRecord{
			PromptLength:   len(prompt),
			ActualDuration: time.Since(startTime),
			TimedOut:       true,
			Timestamp:      time.Now(),
		})

		if attempt >= maxRetries {
			return "", fmt.Errorf("turn timed out after %d seconds (retried %d times): %w",
				int(time.Since(startTime).Seconds()), maxRetries, lastErr)
		}
	}

	return "", lastErr
}

// runStreamToolLoop drives the streaming tool-call loop with a wall-clock timeout.
// Returns the full response, total tokens, or an error on timeout.
func (a *Agent) runStreamToolLoop(ctx context.Context, prov *provider.Provider, messages []provider.Message, onChunk func(string), pToolDefs []provider.ToolDef, maxDuration int, onToolCall ...func(ToolCallEvent)) (string, int, error) {
	toolSeq := 0
	totalTokens := 0
	var round int

	// Extract the user's original prompt so we can save on interrupt
	var userPrompt string
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			userPrompt = messages[i].Content
			break
		}
	}
	var partialResponse string

	// Wall-clock timeout: if configured (duration > 0), derive a deadline
	// context so that in-flight provider calls are also interrupted.
	toolCtx := ctx
	if maxDuration > 0 {
		var cancel context.CancelFunc
		toolCtx, cancel = context.WithTimeout(ctx, time.Duration(maxDuration)*time.Second)
		defer cancel()
	}

	maxRounds := a.cfg.Agent.MaxTurnRounds
	if maxRounds <= 0 {
		maxRounds = maxToolRounds
	}

	// Save interrupted prompt to session on early exit (Ctrl+C or timeout)
	defer func() {
		if userPrompt == "" {
			return
		}
		if toolCtx.Err() != nil {
			entryContent := userPrompt
			if partialResponse != "" {
				entryContent = userPrompt + "\n\n[INTERRUPTED - partial response received before interruption]"
			} else {
				entryContent = userPrompt + "\n\n[INTERRUPTED - query was interrupted before completion]"
			}
			_ = a.Sessions.Append(a.sessionName, "user", entryContent)
			_ = a.Sessions.Append(a.sessionName, "assistant", "[The agent was interrupted while processing this query. Use /retry to resubmit.]")
			log.Printf("Saved interrupted prompt to session (stream interrupted)")
		}
	}()

	var fullResponse string
	gotFinalAnswer := false
	for round = 0; round < maxRounds; round++ {
		select {
		case <-toolCtx.Done():
			return partialResponse, totalTokens, fmt.Errorf("turn timed out after %d seconds", maxDuration)
		default:
		}
		// Emit thinking event before non-first round Chat() calls
		if round > 0 {
			for _, cb := range onToolCall {
				cb(ToolCallEvent{IsThinking: true})
			}
		}
		content, reasoning, toolCalls, err := a.chatStreamWithRetry(toolCtx, prov, messages, onChunk, pToolDefs, onToolCall...)
		if err != nil {
			// Preserve partial content/tool calls instead of discarding them.
			// chatStreamWithRetry returns (content, reasoning, toolCalls, err)
			// when the stream got partial results before the error — content
			// was already streamed to the UI via onChunk callbacks.
			if content != "" || len(toolCalls) > 0 {
				partialResponse = content
				return content, totalTokens, err
			}
			return "", totalTokens, err
		}

		// Emit thinking event for DeepSeek reasoning_content (streaming path).
		// Always store (even when empty) so stale reasoning from an earlier
		// round never leaks into the final session save.
		a.lastReasoning.Store(reasoning)
		if reasoning != "" {
			for _, cb := range onToolCall {
				cb(ToolCallEvent{Reasoning: reasoning, IsThinking: true})
			}
		}

		// ChatStream doesn't return token counts, so estimate from content
		totalTokens += EstimateTokens(content)
		for _, tc := range toolCalls {
			totalTokens += EstimateTokens(tc.Function.Name) + EstimateTokens(tc.Function.Arguments)
		}

		if len(toolCalls) == 0 {
			// If streaming returned empty content and we sent tools, retry without
			if content == "" && len(pToolDefs) > 0 && round == 0 {
				pToolDefs = nil
				continue
			}
			fullResponse = content
			gotFinalAnswer = true
			break
		}

		// ReasoningContent is included so DeepSeek thinking mode accepts the
		// follow-up request with this assistant turn in the history.
		// Some providers stream tool calls WITHOUT an id — assign unique ids
		// so the assistant tool_calls and the tool result messages always
		// pair up (see normalizeToolCallIDs).
		toolCalls = normalizeToolCallIDs(toolCalls, toolSeq)
		messages = append(messages, provider.Message{
			Role:             "assistant",
			Content:          content,
			ReasoningContent: reasoning,
			ToolCalls:        toolCalls,
		})

		for _, tc := range toolCalls {
			var args map[string]interface{}
			if tc.Function.Arguments != "" {
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
			}

			toolSeq++
			seq := toolSeq // capture

			// Notify callbacks that the tool is starting
			for _, cb := range onToolCall {
				cb(ToolCallEvent{
					SeqID:   seq,
					Name:    tc.Function.Name,
					Args:    args,
					IsStart: true,
				})
			}

			// Fire pre-tool-use hook before execution
			hookResults := a.fireHook(layers.HookPreToolUse, map[string]interface{}{
				"tool_name": tc.Function.Name,
				"arguments": tc.Function.Arguments,
			})

			// Phase 5: user-defined pre_tool_use hooks can veto the call.
			var result interface{}
			var execErr error
			if blocked, reason := hooks.CheckVeto(hookResults); blocked {
				result = map[string]interface{}{
					"blocked": true,
					"reason":  reason,
				}
			} else {
				result, execErr = a.ToolRegistry.ExecuteContext(toolCtx, tc.Function.Name, args)
			}
			a.incrementSkillUsedCount(tc.Function.Name)
			var resultText string
			if execErr != nil {
				resultText = fmt.Sprintf("Error: %v", execErr)
			} else {
				resultText = safeMarshalCompactJSON(result)
			}

			// Cap tool result size to prevent OOM — use rune-aware truncation
			// to avoid splitting multi-byte UTF-8 runes (e.g. ✅, 🧠, ⚠️)
			if len(resultText) > maxToolResultBytes {
				b := []byte(resultText)
				cut := maxToolResultBytes
				for cut > 0 && !utf8.RuneStart(b[cut]) {
					cut--
				}
				resultText = string(b[:cut]) + "\n... [truncated: result too large]"
			}

			// Fire post-tool-use hook after execution
			hookCtx := map[string]interface{}{
				"tool_name": tc.Function.Name,
				"arguments": tc.Function.Arguments,
				"result":    resultText,
			}
			if execErr != nil {
				hookCtx["error"] = execErr.Error()
			}
			a.fireHook(layers.HookPostToolUse, hookCtx)

			// Phase 3: feed instant LSP diagnostics back to the model so it
			// can self-correct syntax/type errors before the next round.
			resultText = a.augmentToolResultWithLSP(tc.Function.Name, args, resultText)

			errStr := ""
			if execErr != nil {
				errStr = execErr.Error()
			}

			// Notify callbacks that the tool completed
			for _, cb := range onToolCall {
				cb(ToolCallEvent{
					SeqID:      seq,
					Name:       tc.Function.Name,
					Args:       args,
					ResultText: resultText,
					Error:      errStr,
					IsStart:    false,
				})
			}

			messages = append(messages, provider.Message{
				Role:       "tool",
				Content:    resultText,
				ToolCallID: tc.ID,
			})
		}

		// Trim messages to prevent unbounded growth
		messages = trimToolLoopMessages(messages, maxMessagesInToolLoop)

		// Auto-test on touched files (ported from Python)
		if a.cfg.Agent.AutoTest {
			testFail := a.autoTest(messages)
			if testFail != "" {
				// Feed the test failures back as a plain user message instead
				// of a synthetic assistant tool_calls + tool pair. The old
				// approach used a hardcoded "_auto_test" tool_call_id that
				// DUPLICATED across rounds when tests failed repeatedly, which
				// DeepSeek/OpenAI reject with "insufficient tool messages
				// following tool_calls message". A user message is protocol-safe
				// in every position and cannot produce orphaned tool messages.
				messages = append(messages, provider.Message{
					Role:    "user",
					Content: "[Auto-test result — system-generated, not a user message]\n" + testFail,
				})
			}
		}
	}

	if !gotFinalAnswer {
		return "", totalTokens, fmt.Errorf("max tool rounds reached without a final answer")
	}

	a.lastToolRoundCount.Store(int64(round))
	a.lastToolCallCount.Store(int64(toolSeq))
	return fullResponse, totalTokens, nil
}

// getProvidersForFallback returns the list of all available providers to try,
// starting with the given primary provider, followed by any other registered
// providers. This enables automatic failover when the primary is rate-limited.
func (a *Agent) getProvidersForFallback(primary *provider.Provider) []*provider.Provider {
	a.mu.RLock()
	defer a.mu.RUnlock()

	allNames := a.providers.List()
	if len(allNames) <= 1 {
		return []*provider.Provider{primary}
	}

	// Build ordered list: primary first, then all others
	var result []*provider.Provider
	seen := make(map[*provider.Provider]bool)

	// Primary goes first
	result = append(result, primary)
	seen[primary] = true

	// Add all other providers
	for _, name := range allNames {
		if p, ok := a.providers.Get(name); ok && !seen[p] {
			result = append(result, p)
			seen[p] = true
		}
	}
	return result
}

// getProviderName returns a human-readable name for a provider.
func (a *Agent) getProviderName(p *provider.Provider) string {
	allNames := a.providers.List()
	for _, name := range allNames {
		if prov, ok := a.providers.Get(name); ok && prov == p {
			return name
		}
	}
	return "unknown"
}

// recordProviderCall records one ChatStream attempt against a provider (A5).
// Thread-safe; called from chatStreamWithRetry around every attempt so the
// stats dashboard can report per-provider calls, failures, success rate, and
// average latency. The named return err is captured at the call site.
func (a *Agent) recordProviderCall(name string, err error, start time.Time) {
	a.providerStatsMu.Lock()
	defer a.providerStatsMu.Unlock()
	st := a.providerStats[name]
	if st == nil {
		st = &ProviderStat{}
		a.providerStats[name] = st
	}
	st.Calls++
	st.TotalLatency += time.Since(start)
	st.LastCallAt = time.Now()
	if err != nil {
		st.Failures++
	}
}

// providerStatsSnapshot returns a copy of the per-provider call metrics for
// GetStats, computing success rate and average latency on the fly.
func (a *Agent) providerStatsSnapshot() map[string]interface{} {
	a.providerStatsMu.RLock()
	defer a.providerStatsMu.RUnlock()
	out := make(map[string]interface{}, len(a.providerStats))
	for name, st := range a.providerStats {
		rate := 1.0
		avgMs := 0.0
		if st.Calls > 0 {
			rate = float64(st.Calls-st.Failures) / float64(st.Calls)
			avgMs = float64(st.TotalLatency.Milliseconds()) / float64(st.Calls)
		}
		out[name] = map[string]interface{}{
			"calls":          st.Calls,
			"failures":       st.Failures,
			"success_rate":   rate,
			"avg_latency_ms": avgMs,
			"last_call":      st.LastCallAt.Format(time.RFC3339),
		}
	}
	return out
}

// recordTurnDuration stores a turn timeout record for future prediction.
// isTurnTimeout reports whether err indicates the turn's wall-clock deadline
// expired. This covers both the explicit "turn timed out after" error from
// runToolLoop and context deadline exceeded errors surfaced from the
// retry/backoff layer (e.g. "agent-level retry aborted: context deadline
// exceeded"). Callers use this to decide whether to retry the turn with a
// longer timeout (self-adaptive) rather than treating it as a hard failure.
func isTurnTimeout(err error) bool {
	if err == nil {
		return false
	}
	if strings.Contains(err.Error(), "turn timed out after") {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

func (a *Agent) recordTurnDuration(rec TurnTimeoutRecord) {
	a.turnTimeoutMu.Lock()
	defer a.turnTimeoutMu.Unlock()
	// Keep at most 100 records (FIFO)
	if len(a.turnTimeoutHist) >= 100 {
		a.turnTimeoutHist = a.turnTimeoutHist[1:]
	}
	a.turnTimeoutHist = append(a.turnTimeoutHist, rec)
}

// estimateTurnDuration predicts how many seconds a turn might take based on
// past similar turns. Returns 0 if no historical data is available.
// The estimate is based on prompt length similarity to past records.
func (a *Agent) estimateTurnDuration(prompt string) int {
	a.turnTimeoutMu.RLock()
	defer a.turnTimeoutMu.RUnlock()

	if len(a.turnTimeoutHist) == 0 {
		return 0
	}

	promptLen := len(prompt)

	// Find historical records with similar prompt length (±20%) and average
	// their durations. If none match closely, use the overall average.
	var similar []time.Duration
	var all []time.Duration

	for _, rec := range a.turnTimeoutHist {
		all = append(all, rec.ActualDuration)
		if rec.PromptLength > 0 {
			ratio := float64(promptLen) / float64(rec.PromptLength)
			if ratio >= 0.8 && ratio <= 1.25 {
				similar = append(similar, rec.ActualDuration)
			}
		}
	}

	var avgDuration time.Duration
	if len(similar) >= 3 {
		// Use similar records if we have enough
		var sum time.Duration
		for _, d := range similar {
			sum += d
		}
		avgDuration = sum / time.Duration(len(similar))
	} else if len(all) > 0 {
		// Fall back to overall average including timed-out turns
		var sum time.Duration
		for _, d := range all {
			sum += d
		}
		avgDuration = sum / time.Duration(len(all))
	}

	if avgDuration <= 0 {
		return 0
	}

	estimated := int(avgDuration.Seconds())
	// Add a 50% safety margin for prediction
	estimated = int(float64(estimated) * 1.5)
	if estimated < 30 {
		estimated = 30 // minimum 30 seconds
	}
	return estimated
}

// UseTool executes a tool by name.
func (a *Agent) UseTool(name string, args map[string]interface{}) (interface{}, error) {
	result, err := a.ToolRegistry.Execute(name, args)
	a.incrementSkillUsedCount(name)
	return result, err
}

// incrementSkillUsedCount increments the UsedCount for a skill tool when it gets called.
func (a *Agent) incrementSkillUsedCount(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.skills {
		if a.skills[i].Name == name {
			a.skills[i].UsedCount++
			break
		}
	}
}

// GetMemory returns the agent's memory.
func (a *Agent) GetMemory() *Memory {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.memory
}

// GetStats returns agent statistics.
func (a *Agent) GetStats() map[string]interface{} {
	a.mu.RLock()
	defer a.mu.RUnlock()

	mcpServers := a.MCP.List()
	s, _ := a.Sessions.GetCopy(a.sessionName)
	entries := 0
	totalTokens := 0
	if s != nil {
		entries = len(s.Entries) / 2
		if s.Metadata != nil {
			if _, err := fmt.Sscanf(s.Metadata["total_tokens"], "%d", &totalTokens); err != nil {
				totalTokens = 0
			}
		}
	}

	return mergeStats(map[string]interface{}{
		"conversations":     entries,
		"learned_skills":    len(a.skills),
		"evolutions":        len(a.evolutions),
		"memory_items":      a.memory.Len(),
		"learnings":         len(a.learnings),
		"tools_available":   a.ToolRegistry.Count(),
		"mcp_servers":       len(mcpServers),
		"mcp_tools":         0,
		"model":             a.cfg.Agent.DefaultModel,
		"session":           a.sessionName,
		"token_budget":      a.cfg.Agent.MaxContext,
		"total_tokens_used": totalTokens,
		// A5 stats dashboard (oh-my-pi steal): runtime tool + provider metrics.
		"provider_calls": a.providerStatsSnapshot(),
	}, a.ToolRegistry.Stats())
}

// mergeStats merges live registry metrics into the stats map without
// overwriting non-metric keys (keeps GetStats readable and testable).
func mergeStats(stats map[string]interface{}, reg map[string]interface{}) map[string]interface{} {
	for k, v := range reg {
		stats[k] = v
	}
	return stats
}

// Learn records a durable lesson in the learnings journal (~/.eling/learnings.md)
// and refreshes the in-memory slice so it is injected into subsequent turns
// (A10). The journal keeps the last 100 entries in memory; the file is
// append-only and unbounded.
func (a *Agent) Learn(entry string) error {
	if err := learnings.Append(entry); err != nil {
		return err
	}
	a.mu.Lock()
	a.learnings = append(a.learnings, entry)
	if len(a.learnings) > 100 {
		a.learnings = a.learnings[len(a.learnings)-100:]
	}
	a.mu.Unlock()
	return nil
}

// ListTools returns all available tools from the registry.
func (a *Agent) ListTools() []tools.Tool {
	return a.ToolRegistry.List()
}

// SetProvider switches to a different provider.
func (a *Agent) SetProvider(name string) error {
	return a.providers.SetDefault(name)
}

// ListProviders returns all configured provider names.
func (a *Agent) ListProviders() []string {
	return a.providers.List()
}

// ListSkills returns all registered skills (skills are tools with category "skill").
func (a *Agent) ListSkills() []tools.Tool {
	return a.ToolRegistry.ListByCategory("skill")
}

// ListDynamicTools returns all dynamic (persisted) tool registrations.
func (a *Agent) ListDynamicTools() []tools.DynamicTool {
	return tools.GetDynamicTools()
}

// UnregisterTool removes a tool from the registry by name.
func (a *Agent) UnregisterTool(name string) {
	a.ToolRegistry.Unregister(name)
	tools.RemoveDynamicTool(name)
}

// AddToolFromCommand registers a new tool from a command specification.
// Used by the TUI /add tool command.
func (a *Agent) AddToolFromCommand(name, description, command string) error {
	if _, exists := a.ToolRegistry.Get(name); exists {
		return fmt.Errorf("tool %q already exists", name)
	}
	cat := "dynamic"
	tool := tools.Tool{
		Name:        name,
		Description: description,
		Version:     "1.0.0",
		Category:    cat,
		Execute: func(args map[string]interface{}) (interface{}, error) {
			return tools.RunDynamicCommand(command, args)
		},
	}
	a.ToolRegistry.Register(tool)
	tools.AddDynamicTool(tools.DynamicTool{
		Name:        name,
		Description: description,
		Category:    cat,
		Command:     command,
	})
	return nil
}

// AddPluginFromCommand registers a new plugin (runnable command) that shows
// as a skill. Used by the TUI /add plugin command.
func (a *Agent) AddPluginFromCommand(name, description, command string) error {
	if _, exists := a.ToolRegistry.Get(name); exists {
		return fmt.Errorf("plugin %q already exists", name)
	}
	cat := "plugin"
	tool := tools.Tool{
		Name:        name,
		Description: description,
		Version:     "1.0.0",
		Category:    cat,
		Execute: func(args map[string]interface{}) (interface{}, error) {
			return tools.RunDynamicCommand(command, args)
		},
	}
	a.ToolRegistry.Register(tool)
	tools.AddDynamicTool(tools.DynamicTool{
		Name:        name,
		Description: description,
		Category:    cat,
		Command:     command,
	})
	return nil
}

// AddSkill registers a new skill. Used by the TUI /add skill command.
func (a *Agent) AddSkill(name, description string) error {
	// Register in tool registry so the LLM can call it
	cat := "skill"
	if _, exists := a.ToolRegistry.Get(name); !exists {
		a.ToolRegistry.Register(tools.Tool{
			Name:        name,
			Description: description,
			Version:     "1.0.0",
			Category:    cat,
			Execute: func(args map[string]interface{}) (interface{}, error) {
				return tools.OK(map[string]interface{}{
					"skill":   name,
					"message": fmt.Sprintf("Skill %q executed", name),
				}), nil
			},
		})
		tools.AddDynamicTool(tools.DynamicTool{
			Name:        name,
			Description: description,
			Category:    cat,
		})
	}
	return nil
}

// GetSession returns the current session.
func (a *Agent) GetSession() *session.Session {
	a.mu.RLock()
	defer a.mu.RUnlock()
	s, _ := a.Sessions.GetCopy(a.sessionName)
	return s
}

// ResumeSession loads and resumes a saved session.
func (a *Agent) ResumeSession(name string) (string, error) {
	context, err := a.Sessions.Resume(name)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.sessionName = name
	a.mu.Unlock()
	return context, nil
}

// SaveSession saves the current session.
func (a *Agent) SaveSession() error {
	return a.Sessions.Save(a.sessionName)
}

// ListSessions returns all saved session names.
func (a *Agent) ListSessions() ([]string, error) {
	return a.Sessions.List()
}

// GetLastSession loads the most recently updated session and resumes it.
// Returns the session context string and the session name.
func (a *Agent) GetLastSession() (*session.Session, error) {
	return a.Sessions.GetLastSession()
}

// SessionName returns the current session's name.
func (a *Agent) SessionName() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.sessionName
}

// SetSessionName renames the current session to a meaningful name.
func (a *Agent) SetSessionName(name string) error {
	a.mu.Lock()
	oldName := a.sessionName
	a.sessionName = name
	a.mu.Unlock()
	return a.Sessions.UpdateSessionName(oldName, name)
}

// SummarizeCurrentSession returns a summary of the current session.
func (a *Agent) SummarizeCurrentSession() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	s, ok := a.Sessions.GetCopy(a.sessionName)
	if !ok {
		return "No active session"
	}
	return session.SummarizeSession(s)
}

// LoadState loads agent state from disk.
func (a *Agent) LoadState() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if err := os.MkdirAll(a.stateDir, 0755); err != nil {
		return err
	}

	// Load memory — handle both old format (PascalCase Go field names) and
	// current format (lowercase json tags). Go's json decoder does NOT fall
	// back to struct field names when a json tag is present, so we check for
	// zero values and attempt a legacy decode if needed.
	memData, err := os.ReadFile(filepath.Join(a.stateDir, "memory.json"))
	if err == nil {
		var mem Memory
		if err := json.Unmarshal(memData, &mem); err == nil {
			// Check if fields were actually populated (json tag format)
			if mem.MaxShort == 0 && mem.MaxLong == 0 {
				// Try old format (PascalCase keys — pre-json-tag serialization)
				var oldMem struct {
					OldItems     []MemoryItem `json:"Items"`
					OldShortTerm []MemoryItem `json:"ShortTerm"`
					MaxShort     int          `json:"MaxShort"`
					MaxLong      int          `json:"MaxLong"`
				}
				if err2 := json.Unmarshal(memData, &oldMem); err2 == nil {
					mem.Items = oldMem.OldItems
					mem.ShortTerm = oldMem.OldShortTerm
					mem.MaxShort = oldMem.MaxShort
					mem.MaxLong = oldMem.MaxLong
				} else {
					// Old format also failed; keep original memory but log warning
					log.Printf("Warning: failed to decode memory in old format: %v", err2)
				}
			}
			// Only replace memory if we got actual items (either new or old format).
			// Zero capacities are valid when the user intentionally configures them,
			// but the presence of items proves the decode succeeded.
			if len(mem.Items) > 0 || len(mem.ShortTerm) > 0 || mem.MaxShort > 0 || mem.MaxLong > 0 {
				a.memory = &mem
			} else {
				log.Printf("Warning: loaded memory with zero items and zero capacities, keeping existing memory")
			}
		} else {
			log.Printf("Warning: failed to decode memory: %v", err)
		}
	}

	// Load skills
	skillData, err := os.ReadFile(filepath.Join(a.stateDir, "skills.json"))
	if err == nil {
		var ls []LearnedSkill
		if err := json.Unmarshal(skillData, &ls); err == nil {
			a.skills = ls
			// Re-register all loaded skills into the tool registry so the LLM can call them
			for _, s := range a.skills {
				if _, exists := a.ToolRegistry.Get(s.Name); !exists {
					skillCopy := s
					a.ToolRegistry.Register(tools.Tool{
						Name:        skillCopy.Name,
						Description: skillCopy.Description,
						Version:     "1.0.0",
						Category:    "skill",
						Noop:        true, // learned-skill stubs have no real command (P1.6)
						Execute: func(args map[string]interface{}) (interface{}, error) {
							return tools.OK(map[string]interface{}{
								"skill":   skillCopy.Name,
								"message": fmt.Sprintf("Skill %q executed — follow the description guidance", skillCopy.Name),
							}), nil
						},
					})
				}
			}
		}
	}

	// Load evolutions
	evData, err := os.ReadFile(filepath.Join(a.stateDir, "evolutions.json"))
	if err == nil {
		var evs []Evolution
		if err := json.Unmarshal(evData, &evs); err == nil {
			a.evolutions = evs
		}
	}

	// Load conversation summary
	summaryData, err := os.ReadFile(filepath.Join(a.stateDir, "summary.txt"))
	if err == nil {
		a.conversationSummary = strings.TrimSpace(string(summaryData))
	}

	// Load turn timeout history (for self-adaptive timeout prediction).
	// Without this, estimateTurnDuration() returns 0 after every restart
	// ("timeout prediction mechanism not initialized") and turns fall back
	// to the default fixed timeout until enough new turns have been recorded.
	a.turnTimeoutMu.Lock()
	histData, err := os.ReadFile(filepath.Join(a.stateDir, "turn_timeout_history.json"))
	if err == nil {
		var hist []TurnTimeoutRecord
		if err := json.Unmarshal(histData, &hist); err == nil && len(hist) > 0 {
			a.turnTimeoutHist = hist
			log.Printf("Loaded %d turn timeout history record(s) for adaptive timeout prediction", len(hist))
		}
	}
	a.turnTimeoutMu.Unlock()

	// Load dynamic tools and re-register them
	toolData, err := os.ReadFile(filepath.Join(a.stateDir, "tools.json"))
	if err == nil {
		var dts []tools.DynamicTool
		if err := json.Unmarshal(toolData, &dts); err == nil {
			tools.SetDynamicTools(dts)
			for _, dt := range dts {
				a.restoreDynamicTool(dt)
			}
		}
	}

	return nil
}

// restoreDynamicTool re-registers a persisted dynamic tool into the registry.
func (a *Agent) restoreDynamicTool(dt tools.DynamicTool) {
	if _, exists := a.ToolRegistry.Get(dt.Name); exists {
		return
	}
	cmd := dt.Command
	cat := dt.Category
	if cat == "" {
		cat = "dynamic"
	}
	tool := tools.Tool{
		Name:        dt.Name,
		Description: dt.Description,
		Version:     "1.0.0",
		Category:    cat,
		Noop:        cmd == "", // no command → placeholder stub, never advertise (P1.6)
		Execute: func(args map[string]interface{}) (interface{}, error) {
			if cmd != "" {
				return tools.RunDynamicCommand(cmd, args)
			}
			return tools.OK(map[string]interface{}{"note": "no command defined"}), nil
		},
	}
	a.ToolRegistry.Register(tool)
}

// safeMarshalCompactJSON marshals v to compact JSON (no indentation),
// recovering from any panic (e.g. corrupted slice metadata).
// Returns the JSON bytes as a string, or a fallback string representation if marshaling fails.
// augmentToolResultWithLSP appends instant LSP diagnostics to the result of
// file-editing tools (write/edit). Best-effort: disabled config, unsupported
// extensions, missing server binaries, or unreadable files are silently
// ignored — the model just doesn't get a [lsp] section.
func (a *Agent) augmentToolResultWithLSP(toolName string, args map[string]interface{}, resultText string) string {
	if a.cfg == nil || !a.cfg.LSP.Enabled {
		return resultText
	}
	switch toolName {
	case "write", "edit":
	default:
		return resultText
	}
	path, _ := args["file_path"].(string)
	if path == "" {
		path, _ = args["path"].(string)
	}
	if path == "" || !lsp.Supports(path) {
		return resultText
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return resultText
	}
	diags := lsp.Diagnostics(path, string(content))
	if len(diags) == 0 {
		return resultText
	}
	if len(diags) > 20 {
		diags = diags[:20]
	}
	var sb strings.Builder
	sb.WriteString("\n[lsp]")
	for _, d := range diags {
		msg := strings.TrimSpace(d.Message)
		if msg == "" {
			continue
		}
		fmt.Fprintf(&sb, "\n  %s:%d:%d %s: %s", path, d.Line+1, d.Col+1, d.SeverityText(), msg)
	}
	return resultText + sb.String()
}

func safeMarshalCompactJSON(v interface{}) string {
	var data []byte
	var marshalErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("WARNING: safeMarshalCompactJSON recovered from panic: %v", r)
				marshalErr = fmt.Errorf("panic during marshal: %v", r)
			}
		}()
		data, marshalErr = json.Marshal(v)
	}()
	if marshalErr != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(data)
}

// safeMarshalJSON marshals v to JSON, recovering from any panic (e.g. corrupted slice metadata).
// Returns the JSON bytes or nil if marshaling panicked.
func safeMarshalJSON(v interface{}) []byte {
	// Try to marshal inside an inner func so we can double-recover.
	// Go's json.Marshal catches panics from the encoder but re-panics
	// runtime.Error (like "reflect: slice index out of range").
	// We catch that re-panic here.
	var data []byte
	var marshalErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("WARNING: safeMarshalJSON recovered from panic: %v", r)
				marshalErr = fmt.Errorf("panic during marshal: %v", r)
			}
		}()
		data, marshalErr = json.MarshalIndent(v, "", "  ")
	}()
	if marshalErr != nil {
		log.Printf("WARNING: safeMarshalJSON marshal error: %v", marshalErr)
		return nil
	}
	return data
}

// SaveState saves agent state to disk.
// Uses a timeout-based lock to prevent deadlock: if the write lock is held
// by another goroutine (e.g., during a panic while holding a.mu.Lock()),
// this will time out after 3 seconds and return an error rather than
// blocking forever. This is critical for safeSaveState recovery.
func (a *Agent) SaveState() error {
	// Try to acquire the read lock with a 3-second deadline. TryRLock never
	// blocks, so no goroutine is stranded waiting for a lock that may never
	// be released (the old goroutine+channel approach leaked a blocked
	// goroutine forever on timeout).
	deadline := time.Now().Add(3 * time.Second)
	for {
		if a.mu.TryRLock() {
			defer a.mu.RUnlock()
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("save state: could not acquire read lock within 3 seconds (possible deadlock)")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if err := os.MkdirAll(a.stateDir, 0755); err != nil {
		return err
	}

	// Save memory (with panic recovery for corrupted state)
	// Lock Memory's own mutex to prevent data race with concurrent Remember/Recall
	var memData []byte
	if a.memory != nil {
		a.memory.mu.RLock()
		memData = safeMarshalJSON(a.memory)
		a.memory.mu.RUnlock()
	}
	if memData != nil {
		if err := os.WriteFile(filepath.Join(a.stateDir, "memory.json"), memData, 0644); err != nil {
			return err
		}
	}

	// Save skills — deep-copy under lock to prevent data race with autoLearn()
	// P1.6 durability: only persist skills with real usage or high confidence,
	// so a session holding pre-prune in-memory lists cannot re-pollute
	// skills.json on its 5-min auto-save or exit (regression guard §9.3).
	skillsCopy := make([]LearnedSkill, 0, len(a.skills))
	for _, s := range a.skills {
		if s.UsedCount > 0 || s.Confidence >= 0.6 {
			skillsCopy = append(skillsCopy, s)
		}
	}
	if skillData := safeMarshalJSON(skillsCopy); skillData != nil {
		if err := os.WriteFile(filepath.Join(a.stateDir, "skills.json"), skillData, 0644); err != nil {
			return err
		}
	}

	// Save evolutions — deep-copy under lock
	evolutionsCopy := make([]Evolution, len(a.evolutions))
	copy(evolutionsCopy, a.evolutions)
	if evData := safeMarshalJSON(evolutionsCopy); evData != nil {
		if err := os.WriteFile(filepath.Join(a.stateDir, "evolutions.json"), evData, 0644); err != nil {
			return err
		}
	}

	// Save dynamic tools — P1.6 durability: drop no-command placeholder
	// entries so tools.json cannot be re-polluted by a stale session.
	persistedTools := tools.GetDynamicTools()
	realTools := make([]tools.DynamicTool, 0, len(persistedTools))
	for _, dt := range persistedTools {
		if dt.Command != "" {
			realTools = append(realTools, dt)
		}
	}
	if toolData := safeMarshalJSON(realTools); toolData != nil {
		if err := os.WriteFile(filepath.Join(a.stateDir, "tools.json"), toolData, 0644); err != nil {
			return err
		}
	}

	// Save session
	_ = a.Sessions.Save(a.sessionName)

	// Save conversation summary
	if a.conversationSummary != "" {
		_ = os.WriteFile(filepath.Join(a.stateDir, "summary.txt"), []byte(a.conversationSummary), 0644)
	}

	// Save turn timeout history (for self-adaptive timeout prediction)
	a.turnTimeoutMu.Lock()
	histData := safeMarshalJSON(a.turnTimeoutHist)
	a.turnTimeoutMu.Unlock()
	if histData != nil {
		_ = os.WriteFile(filepath.Join(a.stateDir, "turn_timeout_history.json"), histData, 0644)
	}

	return nil
}

// buildContext enriches the prompt with memory and context.
func (a *Agent) buildContext(prompt string) string {
	ctx := prompt

	// 1. Substring memory recall (fast, always works)
	memories := a.memory.Recall(prompt)
	hasSubstringResults := len(memories) > 0
	if hasSubstringResults {
		ctx += "\n\n[Relevant memories:]"
		for i, m := range memories {
			if i >= 3 {
				break
			}
			ctx += fmt.Sprintf("\n- [%s] %s", m.Category, m.Content)
		}
	}

	// 2. Semantic search recall (meaning-based, fallback when substring
	// finds nothing, or supplement when it does). Silently skipped if
	// the embedding API is unavailable.
	semanticResults := tools.SemanticSearch(prompt, 3)
	if len(semanticResults) > 0 {
		// Check if these add anything beyond the substring results
		hasNew := false
		for _, sr := range semanticResults {
			found := false
			for _, m := range memories {
				if m.Content == sr.Content {
					found = true
					break
				}
			}
			if !found {
				hasNew = true
				break
			}
		}
		if hasNew {
			label := "Related memories (by meaning):"
			if !hasSubstringResults {
				label = "Relevant memories (by meaning):"
			}
			ctx += "\n\n[" + label + "]"
			for i, sr := range semanticResults {
				if i >= 3 {
					break
				}
				ctx += fmt.Sprintf("\n- [%s] %.2f %s", sr.Category, sr.Score, sr.Content)
			}
		}
	}

	// Tools are already sent as structured function definitions via
	// ToProviderDefs() in the API call. Don't repeat verbose descriptions
	// here — it wastes tokens and can cause 400 errors (context overflow).
	// Include only a compact reference so the model knows what's available.
	toolList := a.ToolRegistry.List()
	if len(toolList) > 0 {
		allow := tools.ToolAllowlist()
		names := make([]string, 0, len(toolList))
		for _, t := range toolList {
			if allow != nil && !allow[t.Name] {
				continue
			}
			names = append(names, t.Name)
		}
		ctx += fmt.Sprintf("\n\n[Tools available: %s]", strings.Join(names, ", "))
	}

	return ctx
}

// summaryMaxChars returns the max characters allowed for the injected
// conversation summary (from ELING_SUMMARY_MAX_CHARS), or 0 when unset
// (meaning no cap — full summary is used).
func summaryMaxChars() int {
	raw := os.Getenv("ELING_SUMMARY_MAX_CHARS")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// buildMessages creates the message array for the provider.
// It trims the conversation history to fit within MaxContext token budget,
// keeping the most recent entries and dropping older ones when necessary.
// When history is trimmed, a compressed conversation summary is injected
// so old information isn't completely lost. Even when not trimmed, a compact
// summary is always included to give the model a bird's-eye view of the
// full conversation (like jcode's always-present context injection).
func (a *Agent) buildMessages(prompt string) []provider.Message {
	systemPrompt := a.cfg.Agent.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = `You are ELING, an auto-learning evolving AI agent.

You use tools (defined below as function-calling tools) to accomplish tasks. Use them when needed.
You have persistent memory, session save/resume, multi-provider support, and auto-learning.
Always be helpful, precise, and proactive. Think step by step.

SEARCH RULE (enforced): All text searches MUST use ugrep 7.5.0. Call the 'ugrep' tool (preferred — it executes ugrep directly); the legacy 'grep' tool is a DEPRECATED alias for the same engine. NEVER invoke plain GNU grep via bash and never assume GNU-grep-only behavior. Use ugrep-native flags when useful: -Z (fuzzy), -z (compressed archives), -t <type> (file-type filter), -w (word boundary), -F (fixed strings), -S (smart case), -U (multiline), --json/--csv (structured output), --bool (boolean operators). ugrep is a superset, so standard grep flags (-rn, -I, -m, --exclude-dir, --include) pass through unchanged.`
	}

	// Start with system prompt. If we have a conversation summary, inject it
	// right after the main system prompt as a compact context hint.
	messages := []provider.Message{
		{Role: "system", Content: systemPrompt},
	}

	// Always include the conversation summary if available — it provides a
	// bird's-eye view of the entire conversation even when all messages fit
	// in the budget. This mirrors jcode's approach of always keeping context.
	// When ELING_SUMMARY_MAX_CHARS is set (e.g. for small-context local
	// models), the injected summary is capped to that many characters to keep
	// the prompt small; without it the full summary is used (cloud behavior).
	if a.conversationSummary != "" {
		summary := a.conversationSummary
		if maxChars := summaryMaxChars(); maxChars > 0 && len(summary) > maxChars {
			summary = summary[:maxChars] + "\n...[summary truncated]"
		}
		summaryMsg := provider.Message{
			Role:    "system",
			Content: "[Conversation context so far: " + summary + "]",
		}
		messages = append(messages, summaryMsg)
	}

	// A10: inject durable learnings from past sessions (loaded at boot, kept
	// fresh by Learn()) so lessons learned carry across sessions. Capped to
	// the most recent 10 to protect the prompt budget for small local models.
	if n := len(a.learnings); n > 0 {
		ls := a.learnings
		if len(ls) > 10 {
			ls = ls[len(ls)-10:]
		}
		var sb strings.Builder
		sb.WriteString("[Durable learnings from past sessions — apply when relevant]:\n")
		for _, l := range ls {
			sb.WriteString("- ")
			sb.WriteString(l)
			sb.WriteString("\n")
		}
		messages = append(messages, provider.Message{
			Role:    "system",
			Content: sb.String(),
		})
	}

	// D1 (DeepCode heist): inject the project's own rules file so repo-specific
	// conventions steer this turn. Loaded once at boot (immutable), capped at
	// ~4 KiB by LoadProjectRules to protect small local-model budgets. Read
	// under the caller-held a.mu.RLock() — same discipline as a.learnings.
	if a.projectRules != "" {
		// Honor a tighter per-instance cap if configured (0/negative = default).
		maxChars := a.cfg.Agent.ProjectRulesMaxChars
		if maxChars <= 0 {
			maxChars = 4096
		}
		content := a.projectRules
		if len(content) > maxChars {
			content = content[:maxChars] + "\n... [project rules truncated]"
		}
		messages = append(messages, provider.Message{
			Role:    "system",
			Content: "[Project rules — apply when relevant]:\n" + content,
		})
	}

	// Inject the most recently approved execution plan (plan mode) so the
	// model follows it step by step during execution. Cleared at the start
	// of each Ask() turn when plan mode is enabled.
	if s, ok := a.Sessions.Get(a.sessionName); ok && s.Plan != "" {
		messages = append(messages, provider.Message{
			Role:    "system",
			Content: "[Approved execution plan — follow it step by step]:\n" + s.Plan,
		})
	}

	// Calculate token budget.
	budget := a.cfg.Agent.MaxContext
	if budget <= 0 {
		budget = 32768
	}

	// Deduct system and prompt tokens from budget.
	budget -= estimateMessageTokens(messages[0])
	if len(messages) > 1 {
		budget -= estimateMessageTokens(messages[1]) // summary message
	}
	promptMsg := provider.Message{Role: "user", Content: prompt}
	budget -= estimateMessageTokens(promptMsg)

	// If budget went negative, we still include the system and prompt messages
	// but no history entries.
	s, ok := a.Sessions.Get(a.sessionName)
	if ok && len(s.Entries) > 0 && budget > 0 {
		// Reserve a portion of the budget for recent messages to ensure
		// conversational continuity. Always keep at least 2 complete turns.
		minRecentBudget := budget / 3
		minRecentEntries := 0
		recentTokens := 0
		for i := len(s.Entries) - 1; i >= 0; i-- {
			e := s.Entries[i]
			msg := provider.Message{Role: e.Role, Content: e.Content}
			entryTokens := estimateMessageTokens(msg)
			if recentTokens+entryTokens > minRecentBudget && minRecentEntries >= 4 {
				break
			}
			recentTokens += entryTokens
			minRecentEntries++
		}

		// Walk session entries from newest to oldest, accumulating until
		// we hit the token budget. This keeps the most recent context.
		var keepEntries []session.Entry
		keptTokens := 0
		entriesKept := 0
		for i := len(s.Entries) - 1; i >= 0; i-- {
			e := s.Entries[i]
			msg := provider.Message{Role: e.Role, Content: e.Content}
			entryTokens := estimateMessageTokens(msg)
			// Always keep at least minRecentEntries, then apply budget cap
			if entriesKept >= minRecentEntries && keptTokens+entryTokens > budget {
				break
			}
			keptTokens += entryTokens
			keepEntries = append(keepEntries, e)
			entriesKept++
		}

		// Append in chronological order (oldest kept first). Reasoning is
		// replayed as reasoning_content so DeepSeek thinking mode accepts
		// resumed conversations.
		for i := len(keepEntries) - 1; i >= 0; i-- {
			messages = append(messages, provider.Message{
				Role:             keepEntries[i].Role,
				Content:          keepEntries[i].Content,
				ReasoningContent: keepEntries[i].Reasoning,
			})
		}

		// When entries are trimmed, update the summary message with a note
		// about truncated older entries so the model knows something is missing.
		if entriesKept < len(s.Entries) && a.conversationSummary != "" {
			// Update the summary message (already included above) — no need
			// to add it again, it's already there.
		}
	}

	messages = append(messages, promptMsg)
	return messages
}

// updateConversationSummary generates or refreshes a compressed summary of
// older conversation history. Called asynchronously after responses so the
// summary stays reasonably fresh without blocking interaction.
// Now uses the LLM itself to generate a structured summary (like jcode's
// SUMMARY_PROMPT) when there's enough new content, falling back to the
// simple extraction method for intermediate updates.
func (a *Agent) updateConversationSummary() {
	a.mu.RLock()
	summary := a.conversationSummary
	// Use GetEntriesCopy which safely copies entries under the session
	// manager's own lock, avoiding data races with concurrent Append() calls.
	entries, _ := a.Sessions.GetEntriesCopy(a.sessionName)
	a.mu.RUnlock()

	if len(entries) < 6 {
		return // not enough conversation to summarize meaningfully
	}

	// Sample from the earlier part (skip last 4 entries = most recent turn)
	sampleEnd := len(entries) - 4
	if sampleEnd < 2 {
		return
	}

	// Use LLM-generated summary every N turns (like jcode's smart compaction)
	// Trigger LLM summarization when the conversation has grown significantly
	// since last summary (every 10+ turns) or when the summary is empty.
	shouldUseLLM := false
	if summary == "" && len(entries) >= 10 {
		shouldUseLLM = true
	} else if len(entries)%10 == 0 && len(entries) >= 20 {
		// Every 10 turns, refresh with LLM
		shouldUseLLM = true
	}

	if shouldUseLLM {
		a.generateLLMSummary(entries, sampleEnd)
		return
	}

	// ── Fast path: simple extraction (same as before but improved) ──

	// Determine which portion is new since last summary
	startIdx := 0
	if summary != "" {
		lastSummaryTokens := EstimateTokens(summary)
		lastSummaryEntries := lastSummaryTokens / 20 // ~20 tokens per entry heuristic
		if lastSummaryEntries > sampleEnd {
			lastSummaryEntries = sampleEnd
		}
		startIdx = sampleEnd - lastSummaryEntries
		if startIdx < 0 {
			startIdx = 0
		}
	}

	// Extract key content from entries not yet summarized
	var newFacts []string
	for i := startIdx; i < sampleEnd; i++ {
		e := entries[i]
		if e.Role == "user" {
			lines := strings.SplitN(e.Content, "\n", 2)
			gist := strings.TrimSpace(lines[0])
			if len(gist) > 80 {
				gist = TruncateStr(gist, 80)
			}
			if gist != "" {
				newFacts = append(newFacts, "user: "+gist)
			}
		} else if e.Role == "assistant" {
			if len(e.Content) > 20 {
				gist := TruncateStr(e.Content, 100)
				newFacts = append(newFacts, "assistant: "+gist)
			}
		}
		if len(newFacts) >= 30 {
			break
		}
	}

	if len(newFacts) == 0 {
		return
	}

	// Build the updated summary — store WITHOUT the prefix to prevent
	// prefix duplication on subsequent updates. The prefix is added only
	// in buildMessages() where the summary is injected into the conversation.
	var sb strings.Builder
	if summary != "" {
		// Strip any existing prefix to be safe (backward compat with old data)
		cleaned := strings.TrimPrefix(summary, "Conversation covered so far: ")
		sb.WriteString(cleaned)
		sb.WriteString("; ")
	}
	sb.WriteString(strings.Join(newFacts, "; "))

	a.mu.Lock()
	a.conversationSummary = sb.String()
	a.mu.Unlock()
}

// generateLLMSummary uses the LLM to create a structured summary of older
// conversation history. This produces much richer summaries than the simple
// extraction method, capturing context, decisions, and user preferences.
// Inspired by jcode's SUMMARY_PROMPT and smart compaction system.
func (a *Agent) generateLLMSummary(entries []session.Entry, sampleEnd int) {
	// Build a compact representation of the older entries
	var sb strings.Builder
	sb.WriteString("Summarize the following conversation between User and Assistant. ")
	sb.WriteString("Extract: 1) What topics were discussed 2) What decisions were made ")
	sb.WriteString("3) What code/files were created or modified 4) What the user's preferences/goals are ")
	sb.WriteString("5) What is the current state (what's done, what's pending)\n\n")

	count := 0
	for i := 0; i < sampleEnd; i++ {
		e := entries[i]
		if e.Role == "user" || e.Role == "assistant" {
			content := strings.TrimSpace(e.Content)
			// Truncate long messages to keep the summary prompt manageable
			if len(content) > 500 {
				content = content[:500] + "..."
			}
			if content != "" {
				label := "User"
				if e.Role == "assistant" {
					label = "Assistant"
				}
				sb.WriteString(fmt.Sprintf("%s: %s\n\n", label, content))
				count++
			}
		}
		if count >= 20 {
			sb.WriteString("...(more conversation follows)...\n\n")
			break
		}
	}

	sb.WriteString("---\nRespond with a concise summary (3-5 sentences) covering the key points above.")
	summaryPrompt := sb.String()

	// Get the provider — hold lock through the full provider access
	a.mu.RLock()
	prov := a.providers.GetDefault()
	a.mu.RUnlock()
	if prov == nil {
		return // No provider available; fall back silently
	}

	// Build the summarization messages
	messages := []provider.Message{
		{Role: "system", Content: "You are a conversation summarizer. Extract and preserve ALL important context, decisions, code references, and user preferences. Keep summaries structured and factual."},
		{Role: "user", Content: summaryPrompt},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	resp, err := prov.Chat(ctx, messages)
	if err != nil {
		log.Printf("generateLLMSummary: provider error (falling back): %v", err)
		return
	}

	newSummary := strings.TrimSpace(resp.Content)
	if newSummary == "" {
		return
	}

	a.mu.Lock()
	a.conversationSummary = newSummary
	a.mu.Unlock()

	log.Printf("generateLLMSummary: generated new summary (%d chars)", len(newSummary))
}

// autoLearn uses the LLM to decide if a reusable skill should be
// learned from this exchange. Ported from the Python eling-agent's
// learn_from_exchange() function.
func (a *Agent) autoLearn(prompt, response string) {
	// Only run if enabled and we have a provider
	if !a.cfg.Agent.LearnFromExchange {
		return
	}

	// Filter: skip trivial interactions
	if len(prompt) < 10 || len(response) < 50 {
		return
	}

	// Build the skill-learning prompt
	truncPrompt := TruncateStr(prompt, 2000)
	truncResponse := TruncateStr(response, 2000)

	learnPrompt := `You extract reusable skill patterns from conversations.

A skill is worth learning when the assistant solved a real problem — wrote code, debugged, explained a technique, or performed multi-step work.

Examples:
- {"learn": true, "name": "live-elapsed-timer", "trigger": "add time counter", "body": "1) Record start time 2) Spawn daemon thread updating display every 0.5s 3) Show elapsed seconds 4) Join on exit"}
- {"learn": true, "name": "system-health-check", "trigger": "check system health", "body": "Run top -bn1, free -h, df -h, uptime to get system metrics"}

Respond STRICT JSON only. No markdown, no extra text.
If learn is false: {"learn": false}`

	userMsg := fmt.Sprintf("User query: %s\n\nAssistant response: %s", truncPrompt, truncResponse)

	messages := []provider.Message{
		{Role: "system", Content: learnPrompt},
		{Role: "user", Content: userMsg},
	}

	prov := a.providers.GetDefault()
	if prov == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := prov.Chat(ctx, messages)
	if err != nil {
		log.Printf("autoLearn: provider error: %v (skipping)", err)
		return
	}

	content := strings.TrimSpace(resp.Content)
	if content == "" {
		return
	}

	// Strip markdown code fences if present
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var result struct {
		Learn   bool   `json:"learn"`
		Name    string `json:"name"`
		Trigger string `json:"trigger"`
		Body    string `json:"body"`
	}
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		log.Printf("autoLearn: failed to parse LLM response: %v", err)
		return
	}

	if !result.Learn || result.Name == "" {
		return
	}

	name := strings.TrimSpace(result.Name)
	body := strings.TrimSpace(result.Body)
	trigger := strings.TrimSpace(result.Trigger)

	// Quality heuristic: require meaningful body content
	if len(body) < 50 {
		log.Printf("autoLearn: skill %q body too short (%d chars), skipping", name, len(body))
		return
	}

	// Quality heuristic: reject overly generic names
	genericNames := map[string]bool{"fix": true, "debug": true, "help": true, "solve": true, "patch": true, "workaround": true}
	if genericNames[strings.ToLower(name)] {
		log.Printf("autoLearn: skill name %q too generic, skipping", name)
		return
	}

	// P1.5: reject canned replies (e.g. "The codebase is indexed! Let me share
	// a comprehensive architecture overview…") — these are chat boilerplate,
	// not reusable procedures, and previously polluted skills.json (pattern_88).
	cannedMarkers := []string{
		"the codebase is indexed",
		"let me share a comprehensive architecture overview",
		"here is a comprehensive",
		"i would be happy to help",
		"as an ai",
	}
	lowerBody := strings.ToLower(body)
	lowerName := strings.ToLower(name)
	for _, m := range cannedMarkers {
		if strings.Contains(lowerBody, m) || strings.Contains(lowerName, m) {
			log.Printf("autoLearn: skill %q looks like a canned reply (marker %q), skipping", name, m)
			return
		}
	}

	// Check for duplicates
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, s := range a.skills {
		if s.Name == name {
			log.Printf("autoLearn: skill %q already exists, skipping", name)
			return
		}
	}

	// Rotate out oldest/least-used skill if at capacity
	// P1.5: cap lowered from 100 → 25 so the registry can never balloon again.
	const maxSkills = 25
	if len(a.skills) >= maxSkills {
		// Find the skill to evict: lowest UsedCount, then oldest LearnedAt
		evictIdx := 0
		for i := 1; i < len(a.skills); i++ {
			if a.skills[i].UsedCount < a.skills[evictIdx].UsedCount ||
				(a.skills[i].UsedCount == a.skills[evictIdx].UsedCount && a.skills[i].LearnedAt.Before(a.skills[evictIdx].LearnedAt)) {
				evictIdx = i
			}
		}
		evicted := a.skills[evictIdx]
		a.skills = append(a.skills[:evictIdx], a.skills[evictIdx+1:]...)
		log.Printf("autoLearn: rotated out skill %q (used=%d, learned=%s) to make room for %q",
			evicted.Name, evicted.UsedCount, evicted.LearnedAt.Format("2006-01-02"), name)

		// Also remove from tool registry if present
		a.ToolRegistry.Unregister(evicted.Name)
	}

	// Learn the new skill
	// P1.5: confidence floor raised 0.5 → 0.6 so weak/tentative patterns
	// don't get persisted as skills (they were polluting skills.json).
	skill := LearnedSkill{
		Name:        name,
		Description: TruncateStr(body, 100),
		Confidence:  0.6,
		LearnedAt:   time.Now(),
		UsedCount:   0,
	}
	a.skills = append(a.skills, skill)

	// Also register in tool registry so the LLM can call it
	skillCopy := skill
	if _, exists := a.ToolRegistry.Get(skillCopy.Name); !exists {
		bodyCopy := body
		a.ToolRegistry.Register(tools.Tool{
			Name:        skillCopy.Name,
			Description: TruncateStr(bodyCopy, 80),
			Version:     "1.0.0",
			Category:    "skill",
			Noop:        true, // learned-skill stubs have no real command (P1.6)
			Execute: func(args map[string]interface{}) (interface{}, error) {
				return tools.OK(map[string]interface{}{
					"skill":   skillCopy.Name,
					"trigger": trigger,
					"body":    bodyCopy,
					"message": fmt.Sprintf("Skill %q executed — follow the body guidance", skillCopy.Name),
				}), nil
			},
		})
		// Persist as dynamic tool so it survives restart
		tools.AddDynamicTool(tools.DynamicTool{
			Name:        skillCopy.Name,
			Description: TruncateStr(bodyCopy, 80),
			Category:    "skill",
		})
	}

	// Record evolution
	a.evolutions = append(a.evolutions, Evolution{
		ID:          fmt.Sprintf("evo_%d", len(a.evolutions)+1),
		Before:      fmt.Sprintf("skills_count=%d", len(a.skills)-1),
		After:       fmt.Sprintf("skills_count=%d", len(a.skills)),
		Reason:      fmt.Sprintf("LLM-learned skill: %s (%s)", name, trigger),
		Timestamp:   time.Now(),
		EffectScore: 0.5,
	})

	log.Printf("autoLearn: learned new skill: %s", name)
}

// extractGoFiles scans tool results for .go file paths.
// Uses a constrained regex to avoid matching .go in error messages,
// comments, or documentation. Only matches paths that look like real
// files: absolute paths, relative paths with slashes, or known prefixes.
var goFileRe = regexp.MustCompile(`(?:^|\s)((?:/[^\s]*)?[a-zA-Z0-9_./-]+\.go)(?:\s|$|[\.,;:!?)])`)

func extractGoFiles(results []provider.Message) []string {
	seen := make(map[string]bool)
	var files []string
	for _, msg := range results {
		if msg.Role != "tool" {
			continue
		}
		text := msg.Content
		if text == "" {
			continue
		}
		matches := goFileRe.FindAllStringSubmatch(text, -1)
		for _, m := range matches {
			candidate := strings.TrimRight(m[1], ".,;:!?)'\"")
			if _, err := os.Stat(candidate); err == nil {
				if !seen[candidate] {
					seen[candidate] = true
					files = append(files, candidate)
				}
			}
		}
	}
	return files
}

// normalizeToolCallIDs assigns a unique, non-empty id to any tool call that
// arrived from the provider without one. Some OpenAI-compatible providers
// stream tool_call chunks with an id, but others omit it entirely — the
// agent then cannot pair the assistant tool_calls message with its tool
// result messages (empty tool_call_id tool messages are dropped by
// sanitizeToolMessages and the API rejects the orphaned tool_calls message
// with "insufficient tool messages following tool_calls message").
// Synthesizing an id here keeps the conversation protocol-valid end to end.
func normalizeToolCallIDs(calls []provider.ToolCall, base int) []provider.ToolCall {
	now := time.Now().UnixNano()
	for i := range calls {
		if calls[i].ID == "" {
			calls[i].ID = fmt.Sprintf("call_%d_%d", now, base+i)
		}
	}
	return calls
}

// autoTest runs go test on touched test files after tool rounds.
// Ported from the Python eling-agent's _auto_pytest() function.
//
// Performance guard (fixes "it takes very long time"): results are memoized
// per package-arg signature. If the same set of packages was already tested
// and passed within the cooldown window, the run is skipped entirely — the
// old behavior re-ran `go test -count=1` on EVERY tool round even when the
// files hadn't changed, which made multi-round edits crawl.
func (a *Agent) autoTest(results []provider.Message) string {
	if !a.cfg.Agent.AutoTest {
		return ""
	}

	allFiles := extractGoFiles(results)
	if len(allFiles) == 0 {
		return ""
	}

	// Find test files to run
	testTargets := make(map[string]bool)
	for _, f := range allFiles {
		rel := f
		if strings.HasSuffix(rel, "_test.go") {
			testTargets[rel] = true
		} else {
			// Source file → look for matching test
			base := strings.TrimSuffix(filepath.Base(f), ".go")
			dir := filepath.Dir(f)
			candidates := []string{
				filepath.Join(dir, "tests", base+"_test.go"),
				filepath.Join(dir, base+"_test.go"),
			}
			for _, c := range candidates {
				if _, err := os.Stat(c); err == nil {
					testTargets[c] = true
				}
			}
		}
	}

	if len(testTargets) == 0 {
		return ""
	}

	var targets []string
	for t := range testTargets {
		// Normalize to absolute paths so os.Stat (filesUnchangedSince) and
		// filepath.Abs work correctly regardless of the process CWD — the
		// agent may be started from /root while the module lives in
		// /root/eling.
		abs, err := filepath.Abs(t)
		if err != nil {
			abs = t
		}
		targets = append(targets, abs)
	}
	sort.Strings(targets)

	// Go rejects `go test <file1> <file2>` when the named files live in
	// different directories OR mix absolute/relative paths ("named files
	// must all be in one directory"). Tool results can contain both
	// (/root/eling/... and eling/...), so instead of passing files we
	// group the targets by package directory (normalized to absolute
	// paths so mixed forms dedupe to the same key) and run
	// `go test ./pkg...` once per touched package. This is protocol-safe
	// and compiles each package properly (file-mode also fails to resolve
	// module imports).
	pkgDirs := make(map[string]bool)
	for _, t := range targets {
		abs, err := filepath.Abs(t)
		if err != nil {
			abs = t
		}
		pkgDirs[filepath.Dir(abs)] = true
	}
	// Resolve against the absolute working directory so filepath.Rel can
	// relativize absolute package dirs (Rel("." , abs) errors on mixed
	// relative/absolute inputs, which would silently fall back to the
	// absolute path — valid for go test, but noisy).
	cwd, _ := os.Getwd()
	if cwd == "" {
		cwd = "."
	}
	// Walk up from the touched package dirs to find the Go module root so
	// `go test` works even when this process was started from another
	// directory (e.g. /root), which previously produced "go.mod file not
	// found". Fall back to the process CWD when no go.mod exists.
	moduleRoot := ""
	for d := range pkgDirs {
		if mr := findGoModuleRoot(d); mr != "" {
			moduleRoot = mr
			break
		}
	}
	if moduleRoot == "" {
		moduleRoot = cwd
	}
	pkgArgs := make([]string, 0, len(pkgDirs))
	for d := range pkgDirs {
		if rel, err := filepath.Rel(moduleRoot, d); err == nil && !strings.HasPrefix(rel, "..") {
			pkgArgs = append(pkgArgs, "./"+filepath.ToSlash(rel))
		} else {
			pkgArgs = append(pkgArgs, d)
		}
	}
	sort.Strings(pkgArgs)
	sig := strings.Join(pkgArgs, "|")

	// Memoization: if this exact package set already passed recently and the
	// source files haven't changed since, skip the run (huge speedup for
	// multi-round edits that touch the same package repeatedly).
	a.autoTestMu.Lock()
	cooldown := time.Duration(a.cfg.Agent.AutoTestCooldownSec) * time.Second
	if cooldown <= 0 {
		cooldown = 10 * time.Second
	}
	if out, ok := a.autoTestCache[sig]; ok {
		// A PASSED result is reusable while the files are unchanged and we're
		// inside the cooldown window. A FAILED result is always re-run so the
		// model gets fresh feedback once it starts fixing the tests.
		if out.Passed && time.Since(out.Timestamp) < cooldown && filesUnchangedSince(targets, out.Timestamp) {
			a.autoTestMu.Unlock()
			return ""
		}
	}
	// Global cooldown: never run go test more often than every N seconds,
	// regardless of which packages were touched (protects against a tight
	// write→test→write→test loop hammering the compiler).
	if time.Since(a.autoTestLast) < cooldown {
		a.autoTestMu.Unlock()
		return ""
	}
	a.autoTestLast = time.Now()
	a.autoTestMu.Unlock()

	log.Printf("autoTest: running go test on %d file(s) (pkg %s)", len(targets), sig)

	// Run go test with short mode and verbose only on failures
	timeout := time.Duration(a.cfg.Agent.AutoTestTimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 180 * time.Second // slow ARM cold builds measured at ~95s
	}
	args := append([]string{"test", "-short", "-count=1"}, pkgArgs...)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", args...)
	// Run from the module root — the agent may have been started from
	// another directory (e.g. /root), which broke `go test` with
	// "go.mod file not found".
	cmd.Dir = moduleRoot
	output, err := cmd.CombinedOutput()

	if err == nil {
		log.Printf("autoTest: all passed")
		a.autoTestMu.Lock()
		a.autoTestCache[sig] = autoTestOutcome{Passed: true, Timestamp: time.Now()}
		a.autoTestMu.Unlock()
		return ""
	}

	// Build failure summary — use rune-aware truncation to avoid splitting
	// multi-byte UTF-8 characters (e.g. ✅, 🧠, ⚠️)
	failSummary := string(output)
	if len([]rune(failSummary)) > 800 {
		failSummary = string([]rune(failSummary)[:800]) + "\n... [truncated]"
	}
	// A context-deadline kill produces empty output and a generic error like
	// "signal: killed" — report it clearly instead of an empty failure block
	// (which would confuse the model into thinking the build had no errors).
	if failSummary == "" {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			failSummary = fmt.Sprintf("go test timed out after %s (device too slow or build too large)", timeout)
		} else if err != nil {
			failSummary = fmt.Sprintf("go test failed to run: %v", err)
		} else {
			failSummary = "go test exited non-zero with no output"
		}
	}
	log.Printf("autoTest: failures:\n%s", failSummary)

	a.autoTestMu.Lock()
	a.autoTestCache[sig] = autoTestOutcome{Passed: false, FailText: failSummary, Timestamp: time.Now()}
	a.autoTestMu.Unlock()

	return fmt.Sprintf("*Auto-test found failures:*\n```\n%s\n```\n*Fix the test(s) above and re-run.*", failSummary)
}

// findGoModuleRoot walks up from dir until it finds a go.mod file and
// returns that directory (the Go module root). Returns "" if none exists.
// Used by autoTest so `go test` runs from the module root even when this
// process was started from a different directory (e.g. /root).
func findGoModuleRoot(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(abs, "go.mod")); err == nil {
			return abs
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return ""
		}
		abs = parent
	}
}

// filesUnchangedSince reports whether every path in paths has an mtime older
// than t (i.e. none of the files were modified after the cached run). Missing
// files count as unchanged (conservative: avoids re-running on transient
// paths that tool output no longer references).
func filesUnchangedSince(paths []string, t time.Time) bool {
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if fi.ModTime().After(t) {
			return false
		}
	}
	return true
}

// TruncateStr truncates a string to n runes, appending "..." if truncated.
func TruncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// EstimateTokens returns a rough token count for a string.
// Uses ~4 chars per token heuristic (reasonable for English/prose).
// For code/tool JSON results this is conservative (code is denser).
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	n := len([]rune(s))/4 + 1
	if n < 1 {
		n = 1
	}
	return n
}

// estimateMessageTokens calculates the token cost of a single message including
// structural overhead (role markers, tool call metadata, etc.).
func estimateMessageTokens(msg provider.Message) int {
	total := EstimateTokens(msg.Content)
	total += 4 // overhead for role + message structure
	for _, tc := range msg.ToolCalls {
		total += EstimateTokens(tc.ID)
		total += EstimateTokens(tc.Function.Name)
		total += EstimateTokens(tc.Function.Arguments)
		total += 3 // structural overhead
	}
	if msg.ToolCallID != "" {
		total += 2
	}
	return total
}

// estimateSessionEntryTokens returns the estimated token cost for a session entry.
func estimateSessionEntryTokens(e session.Entry) int {
	if e.Tokens > 0 {
		return e.Tokens
	}
	return EstimateTokens(e.Content) + 4
}
