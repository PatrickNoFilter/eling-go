// Package tools provides a dynamic tool registry inspired by jcode's tool system.
// Tools can be registered, unregistered, listed, and hot-reloaded at runtime.
package tools

import (
	"fmt"
	"runtime/debug"
	"sync"

	"eling/internal/logger"
)

// DefaultRegistry is the global tool registry instance.
// Tools self-register via init() functions using this registry.
var DefaultRegistry = NewRegistry()

// Tool defines an executable tool/function that the agent can call.
type Tool struct {
	Name        string                                                 `json:"name"`
	Description string                                                 `json:"description"`
	Version     string                                                 `json:"version"`
	Category    string                                                 `json:"category"` // system, skill, mcp, user
	Execute     func(args map[string]interface{}) (interface{}, error) `json:"-"`
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
func (r *Registry) Execute(name string, args map[string]interface{}) (result interface{}, err error) {
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("tool %q not found", name)
	}

	// Panic-safe execution: catch panics in tool code and return as error
	defer func() {
		if r := recover(); r != nil {
			stack := string(debug.Stack())
			logger.Global().Error("Tool %q panicked: %v\nStack:\n%s", name, r, stack)
			logger.WriteCrashReport(fmt.Errorf("tool %q panicked: %v", name, r), stack)
			result = nil
			err = fmt.Errorf("tool %q panicked: %v", name, r)
		}
	}()

	return t.Execute(args)
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
