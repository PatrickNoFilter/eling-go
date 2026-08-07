// Package tools provides a dynamic tool registry inspired by jcode's tool system.
// Tools can be registered, unregistered, listed, and hot-reloaded at runtime.
package tools

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"eling/internal/autorepair"
	"eling/internal/logger"
)

// DefaultRegistry is the global tool registry instance.
// Tools self-register via init() functions using this registry.
var DefaultRegistry = NewRegistry()

// DefaultToolTimeout is the fallback budget applied to any tool that does not
// declare its own Timeout. It guarantees no tool can hang the agent forever,
// even tools that predate the timeout system.
const DefaultToolTimeout = 5 * time.Minute

// Tool defines an executable tool/function that the agent can call.
type Tool struct {
	Name        string                                                 `json:"name"`
	Description string                                                 `json:"description"`
	Version     string                                                 `json:"version"`
	Category    string                                                 `json:"category"` // system, skill, mcp, user
	Execute     func(args map[string]interface{}) (interface{}, error) `json:"-"`
	// ExecuteCtx is the optional context-aware variant. When set, ExecuteContext
	// uses it so callers can cancel long-running tools (e.g. web_fetch) via a
	// parent context deadline instead of blocking until the tool's own timeout.
	ExecuteCtx func(ctx context.Context, args map[string]interface{}) (interface{}, error) `json:"-"`
	// Timeout is the maximum wall-clock budget for this tool. When 0 the
	// DefaultToolTimeout (5 min) is used. Tools that implement ExecuteCtx
	// receive a context carrying this deadline; plain-Execute tools are run
	// under a goroutine + timer guard so they cannot block past the budget.
	Timeout time.Duration `json:"-"`
	// Noop marks a placeholder/stub tool that performs no real work (e.g. a
	// learned-skill stub or a persisted dynamic tool with no command). Noop
	// tools are hidden from ToProviderDefs() so they are never advertised to
	// the LLM, consuming tokens and polluting tool selection. Runtime-only.
	Noop bool `json:"-"`
}

// Result wraps a tool execution result.
type Result struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

// toolMetrics tracks runtime call statistics for one tool (A5 stats dashboard).
type toolMetrics struct {
	Calls        int64
	Failures     int64
	TotalLatency time.Duration
	LastCallAt   time.Time
}

// Registry manages all available tools dynamically.
// Inspired by jcode's tool/mod.rs dynamic registry.
type Registry struct {
	mu         sync.RWMutex
	tools      map[string]Tool
	categories map[string][]string // category -> tool names

	// metrics tracks per-tool runtime call statistics (A5). Populated by
	// ExecuteContext on every call so the stats dashboard can report
	// tool-success %, average latency, and per-tool breakdowns.
	metrics map[string]*toolMetrics

	// disabled marks tools quarantined by the auto-repair subsystem so they are
	// not offered to the LLM (List) nor executable (Execute/ExecuteContext).
	// They stay registered in memory — SetDisabled(false) re-enables them
	// without losing the definition (manual re-enable path).
	disabled map[string]bool

	// perm is the D6 per-tool permission policy. Empty/inactive => everything
	// is allowed (historical behavior). projectDir is the working directory
	// used to resolve project trust levels.
	perm        PermPolicy
	projectDir  string
	// gate, when non-nil, is consulted for every tool whose resolved mode is
	// PermAsk. A nil gate degrades "ask" to "allow" (safe in headless runs —
	// serve, --run, automate — where no human is present to answer a prompt).
	// The gate is invoked exactly once per ExecuteContext call.
	gate PermissionGate
}

// PermissionGate inspects a single tool call whose resolved mode is "ask" and
// decides whether to allow it. Returning true runs the tool; false blocks it
// with a "denied at ask prompt" error. Errors are treated as blocks.
type PermissionGate func(name string, args map[string]interface{}) (bool, error)

// NewRegistry creates a new tool registry and registers built-in tools.
func NewRegistry() *Registry {
	r := &Registry{
		tools:      make(map[string]Tool),
		categories: make(map[string][]string),
		metrics:    make(map[string]*toolMetrics),
		disabled:   make(map[string]bool),
	}
	r.registerBuiltins()
	return r
}

// SetPermissions installs the D6 per-tool permission policy. projectDir is the
// working directory used to resolve project trust; pass "" when unknown.
// Passing an empty policy disables gating entirely (historical behavior).
func (r *Registry) SetPermissions(p PermPolicy, projectDir string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.perm = p
	r.projectDir = projectDir
}

// SetPermissionGate installs the interactive "ask" prompt handler. When nil,
// tools whose resolved mode is "ask" run without prompting (headless-safe).
func (r *Registry) SetPermissionGate(g PermissionGate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gate = g
}

// PermissionModeFor resolves the permission mode for a tool call without
// executing it. Useful for CLI introspection (`eling permission list`).
func (r *Registry) PermissionModeFor(name string) (mode, reason string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.perm.ModeFor(name, r.projectDir)
}

// PermissionPolicy returns the currently installed policy (for introspection).
func (r *Registry) PermissionPolicy() PermPolicy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.perm
}

// SetDisabled marks a tool disabled (quarantined) or re-enabled. Disabled tools
// are hidden from List() (so the LLM is never offered them) and refused by
// Execute/ExecuteContext (so a stale agent turn cannot call them). The Tool
// definition is retained, so SetDisabled(false) restores it without re-creating.
func (r *Registry) SetDisabled(name string, disabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if disabled {
		r.disabled[name] = true
	} else {
		delete(r.disabled, name)
	}
}

// IsDisabled reports whether a tool is currently disabled (quarantined).
func (r *Registry) IsDisabled(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.disabled[name]
}

// Disabled returns the names of all currently disabled (quarantined) tools.
func (r *Registry) Disabled() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.disabled))
	for n := range r.disabled {
		if _, ok := r.tools[n]; ok {
			out = append(out, n)
		}
	}
	return out
}

// Register adds a tool, replacing any existing tool with the same name.
func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name] = t
	r.categories[t.Category] = append(r.categories[t.Category], t.Name)
}

// Unregister removes a tool by name.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.tools[name]; ok {
		// Remove from category list
		cat := t.Category
		names := r.categories[cat]
		for i, n := range names {
			if n == name {
				r.categories[cat] = append(names[:i], names[i+1:]...)
				break
			}
		}
		delete(r.tools, name)
	}
}

// Get retrieves a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns all registered tools.
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		if r.disabled[t.Name] {
			continue // quarantine: hide disabled tools from the LLM
		}
		result = append(result, t)
	}
	return result
}

// ListByCategory returns tools in a specific category.
func (r *Registry) ListByCategory(category string) []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := r.categories[category]
	result := make([]Tool, 0, len(names))
	for _, name := range names {
		if t, ok := r.tools[name]; ok {
			result = append(result, t)
		}
	}
	return result
}

// Categories returns all tool categories.
func (r *Registry) Categories() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cats := make([]string, 0, len(r.categories))
	for c := range r.categories {
		cats = append(cats, c)
	}
	return cats
}

// Count returns the number of registered tools.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}

// Stats returns runtime call metrics for every tool executed in this process
// (A5 stats dashboard). Includes totals plus a per-tool breakdown:
//
//	tool_calls, tool_failures, tool_success_rate, tool_avg_latency_ms,
//	per_tool: {name: {calls, failures, success_rate, avg_latency_ms, last_call}}
//
// Safe to call concurrently with ExecuteContext.
func (r *Registry) Stats() map[string]interface{} {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var totalCalls, totalFailures int64
	var totalLatency time.Duration
	perTool := make(map[string]map[string]interface{}, len(r.metrics))
	for name, m := range r.metrics {
		totalCalls += m.Calls
		totalFailures += m.Failures
		totalLatency += m.TotalLatency
		rate := 1.0
		avgMs := 0.0
		if m.Calls > 0 {
			rate = float64(m.Calls-m.Failures) / float64(m.Calls)
			avgMs = float64(m.TotalLatency.Milliseconds()) / float64(m.Calls)
		}
		perTool[name] = map[string]interface{}{
			"calls":          m.Calls,
			"failures":       m.Failures,
			"success_rate":   rate,
			"avg_latency_ms": avgMs,
			"last_call":      m.LastCallAt.Format(time.RFC3339),
		}
	}

	rate := 1.0
	avgMs := 0.0
	if totalCalls > 0 {
		rate = float64(totalCalls-totalFailures) / float64(totalCalls)
		avgMs = float64(totalLatency.Milliseconds()) / float64(totalCalls)
	}
	return map[string]interface{}{
		"tool_calls":          totalCalls,
		"tool_failures":       totalFailures,
		"tool_success_rate":   rate,
		"tool_avg_latency_ms": avgMs,
		"per_tool":            perTool,
	}
}

// Execute runs a tool by name with the given arguments.
// Panics during tool execution are caught, logged, and returned as errors
// so the agent can continue functioning.
//
// It delegates to ExecuteContext with a background context so every call
// path (agent, MCP server, external) gets the same timeout budget strategy:
// no tool can block the caller past its Timeout / DefaultToolTimeout.
func (r *Registry) Execute(name string, args map[string]interface{}) (result interface{}, err error) {
	return r.ExecuteContext(context.Background(), name, args)
}

// ExecuteContext runs a tool with context support. If the tool registered an
// ExecuteCtx variant it is used (allowing cancellation / deadline propagation);
// otherwise it falls back to the plain Execute (which cannot be cancelled).
// Panics during tool execution are caught, logged, and returned as errors.
//
// Timeout strategy (v0.4.0):
//   - Every tool has a wall-clock budget: its own Timeout field, or the
//     DefaultToolTimeout (5 min) fallback. No tool can hang the agent forever.
//   - If the caller's context already carries an earlier deadline (e.g. the
//     turn's max_duration), that deadline wins and is used unchanged.
//   - Context-aware tools receive a ctx with the budget applied, so they can
//     cancel mid-flight (curl, bash, ocr, ...).
//   - Plain-Execute tools run under a goroutine + timer guard; on expiry the
//     tracked subprocesses are SIGKILLed (KillRunningTools) and a timeout
//     error is returned instead of blocking the turn indefinitely.
func (r *Registry) ExecuteContext(ctx context.Context, name string, args map[string]interface{}) (result interface{}, err error) {
	r.mu.RLock()
	t, ok := r.tools[name]
	dis := r.disabled[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("tool %q not found", name)
	}
	if dis {
		return nil, fmt.Errorf("tool %q is disabled (quarantined); re-enable with `eling autorepair reenable %s`", name, name)
	}

	// D6: per-tool permission gate. Runs before dispatch so a "deny" call is
	// blocked without ever entering the tool, and an "ask" call can prompt the
	// user via the interactive gate (installed by the CLI; nil here in headless
	// runs degrades "ask" to "allow"). The empty/inactive policy yields
	// "allow" for every tool, so a fresh install is unaffected.
	if mode, reason := r.perm.ModeFor(name, r.projectDir); mode != PermAllow {
		if mode == PermDeny {
			return nil, fmt.Errorf("tool %q blocked by permission policy (%s); adjust with `eling permission set %s allow`", name, reason, name)
		}
		// PermAsk: consult the gate (if any). Exactly one call per execution.
		r.mu.RLock()
		g := r.gate
		r.mu.RUnlock()
		if g != nil {
			ok, gerr := g(name, args)
			if gerr != nil {
				return nil, fmt.Errorf("tool %q permission gate error: %v", name, gerr)
			}
			if !ok {
				return nil, fmt.Errorf("tool %q blocked by permission policy (%s; denied at ask prompt)", name, reason)
			}
		}
		// nil gate -> run (headless-safe, preserves behavior).
	}

	start := time.Now()
	var panicked bool
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			stack := string(debug.Stack())
			logger.Global().Error("Tool %q panicked: %v\nStack:\n%s", name, r, stack)
			logger.WriteCrashReport(fmt.Errorf("tool %q panicked: %v", name, r), stack)
			result = nil
			err = fmt.Errorf("tool %q panicked: %v", name, r)
		}
		// A5: record runtime metrics for the stats dashboard. Runs on every
		// exit path (success, error, timeout, panic) because err is the named
		// return value — panic recovery above updates it before we get here.
		r.mu.Lock()
		m := r.metrics[name]
		if m == nil {
			m = &toolMetrics{}
			r.metrics[name] = m
		}
		m.Calls++
		m.TotalLatency += time.Since(start)
		m.LastCallAt = time.Now()
		if err != nil {
			m.Failures++
		}
		r.mu.Unlock()

		// Auto-repair funnel (Phase 0 — detection/classification only, no
		// mutation, plus Phase 2 quarantine). Funnels every failed tool call
		// into the autorepair engine so it can ask "is this tool broken?". When
		// the engine returns a QUARANTINE verdict (crash, or fatal repeated
		// breakage), disable the tool here so it is no longer offered to the
		// LLM, persist the reason, and surface a single user-facing message.
		if err != nil {
			errm := err.Error()
			if len(errm) > 4000 {
				errm = errm[:4000]
			}
			cf := autorepair.RecordFailure(name, errm, time.Since(start), panicked)
			if cf.Verdict == autorepair.VerdictQuarantine {
				r.SetDisabled(name, true)
				autorepair.Quarantine(name, cf.Class.String(), cf.Reason, errm)
				logger.Global().Info("Tool %q DISABLED (quarantined): %s", name, cf.Reason)
			}
		}
	}()

	// Derive the tool's wall-clock budget. An explicit earlier deadline on the
	// caller's context (turn max_duration, parent cancel) always wins.
	budget := t.Timeout
	if budget <= 0 {
		budget = DefaultToolTimeout
	}
	execCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
	}

	if t.ExecuteCtx != nil {
		return t.ExecuteCtx(execCtx, args)
	}

	// Plain Execute cannot be cancelled internally: guard it with a goroutine
	// and enforce the budget here. On expiry we SIGKILL tracked subprocesses
	// (curl, bash, ocr, ...) and fail fast instead of blocking forever.
	type execResult struct {
		result interface{}
		err    error
	}
	done := make(chan execResult, 1)
	go func() {
		res, e := t.Execute(args)
		done <- execResult{res, e}
	}()

	select {
	case <-execCtx.Done():
		KillRunningTools()
		if ctx.Err() != nil {
			return nil, fmt.Errorf("tool %q aborted: %v", name, ctx.Err())
		}
		return nil, fmt.Errorf("tool %q timed out after %v (budget exceeded)", name, budget)
	case d := <-done:
		return d.result, d.err
	}
}

// OK returns a successful result.
func OK(data interface{}) Result {
	return Result{Success: true, Data: data}
}

// Err returns an error result.
func Err(msg string) Result {
	return Result{Success: false, Error: msg}
}

// registerBuiltins registers all built-in system tools.
func (r *Registry) registerBuiltins() {
	// All built-in tool registration happens via init from their respective files.
	// This function is intentionally minimal - tools register themselves.
	_ = r // tools register via Register() in their init()
}
