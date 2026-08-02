// Package tools provides a dynamic tool registry inspired by jcode's tool system.
// Tools can be registered, unregistered, listed, and hot-reloaded at runtime.
package tools

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

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
}

// Result wraps a tool execution result.
type Result struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

// Registry manages all available tools dynamically.
// Inspired by jcode's tool/mod.rs dynamic registry.
type Registry struct {
	mu         sync.RWMutex
	tools      map[string]Tool
	categories map[string][]string // category -> tool names
}

// NewRegistry creates a new tool registry and registers built-in tools.
func NewRegistry() *Registry {
	r := &Registry{
		tools:      make(map[string]Tool),
		categories: make(map[string][]string),
	}
	r.registerBuiltins()
	return r
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
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("tool %q not found", name)
	}

	defer func() {
		if r := recover(); r != nil {
			stack := string(debug.Stack())
			logger.Global().Error("Tool %q panicked: %v\nStack:\n%s", name, r, stack)
			logger.WriteCrashReport(fmt.Errorf("tool %q panicked: %v", name, r), stack)
			result = nil
			err = fmt.Errorf("tool %q panicked: %v", name, r)
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
