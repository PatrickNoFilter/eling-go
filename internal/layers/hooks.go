// Package layers implements an 8-layer memory architecture for the ELING agent.
//
// Hooks system — 15 lifecycle hooks for memory-aware agent behavior.
// Adapted from Python eling's hooks.py by PatrickNoFilter.
package layers

import (
	"context"
	"log"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ── Hook name constants ─────────────────────────────────────────────────────

const (
	HookSessionStart         = "session_start"
	HookPreUserMessage       = "pre_user_message"
	HookPostUserMessage      = "post_user_message"
	HookPreToolUse           = "pre_tool_use"
	HookPostToolUse          = "post_tool_use"
	HookPostAssistantMessage = "post_assistant_message"
	HookDecisionMade         = "decision_made"
	HookFileEdit             = "file_edit"
	HookVerifyRequest        = "verify_request"
	HookErrorOccurred        = "error_occurred"
	HookCompaction           = "compaction"
	HookSessionEnd           = "session_end"
	HookIdle30Min            = "idle_30min"
	HookSyncStart            = "sync_start"
	HookSyncComplete         = "sync_complete"
	HookSyncError            = "sync_error"
)

// AllHooks is the canonical list of all hook names.
var AllHooks = []string{
	HookSessionStart,
	HookPreUserMessage,
	HookPostUserMessage,
	HookPreToolUse,
	HookPostToolUse,
	HookPostAssistantMessage,
	HookDecisionMade,
	HookFileEdit,
	HookVerifyRequest,
	HookErrorOccurred,
	HookCompaction,
	HookSessionEnd,
	HookIdle30Min,
	HookSyncStart,
	HookSyncComplete,
	HookSyncError,
}

// ── Types ───────────────────────────────────────────────────────────────────

// HookHandler is a function that handles a hook event.
// It receives the hook name and a context map, and returns a result (or nil).
type HookHandler func(hookName string, ctx map[string]interface{}) interface{}

// HookRegistry is a thread-safe registry of named hook handlers.
// Each hook can have multiple handlers; all fire in registration order.
type HookRegistry struct {
	mu       sync.RWMutex
	handlers map[string][]HookHandler
}

// NewHookRegistry creates a new HookRegistry with empty handler lists for all hooks.
func NewHookRegistry() *HookRegistry {
	h := &HookRegistry{
		handlers: make(map[string][]HookHandler),
	}
	for _, name := range AllHooks {
		h.handlers[name] = []HookHandler{}
	}
	return h
}

// Register registers a handler for a hook. Unknown hooks log a warning.
func (hr *HookRegistry) Register(hookName string, handler HookHandler) {
	hr.mu.Lock()
	defer hr.mu.Unlock()
	if _, ok := hr.handlers[hookName]; !ok {
		log.Printf("[hooks] warning: unknown hook %q, ignoring", hookName)
		return
	}
	hr.handlers[hookName] = append(hr.handlers[hookName], handler)
}

// Unregister removes a specific handler from a hook.
// NOTE: Go does NOT allow comparing non-nil function values with ==/!=,
// so this uses a reflect-based approach. Panics if handler is nil.
func (hr *HookRegistry) Unregister(hookName string, handler HookHandler) {
	hr.mu.Lock()
	defer hr.mu.Unlock()
	handlers, ok := hr.handlers[hookName]
	if !ok {
		return
	}
	// Build a new list excluding the given handler.
	// We compare the underlying function pointer via reflection.
	target := runtime.FuncForPC(reflect.ValueOf(handler).Pointer())
	var newHandlers []HookHandler
	for _, h := range handlers {
		if h == nil {
			continue
		}
		ptr := runtime.FuncForPC(reflect.ValueOf(h).Pointer())
		if ptr == target {
			continue // skip the matching handler
		}
		newHandlers = append(newHandlers, h)
	}
	hr.handlers[hookName] = newHandlers
}

// Fire fires all handlers for a hook. Returns list of return values.
// All panics are caught and logged (hooks never crash the caller).
func (hr *HookRegistry) Fire(hookName string, ctx map[string]interface{}) []interface{} {
	hr.mu.RLock()
	handlers, ok := hr.handlers[hookName]
	hr.mu.RUnlock()
	if !ok || len(handlers) == 0 {
		return nil
	}

	var results []interface{}
	for _, handler := range handlers {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[hooks] handler for %q panicked: %v", hookName, r)
					results = append(results, nil)
				}
			}()
			result := handler(hookName, ctx)
			results = append(results, result)
		}()
	}
	return results
}

// HasHandlers checks if a hook has any registered handlers.
func (hr *HookRegistry) HasHandlers(hookName string) bool {
	hr.mu.RLock()
	defer hr.mu.RUnlock()
	handlers, ok := hr.handlers[hookName]
	return ok && len(handlers) > 0
}

// TotalHandlers returns the total number of registered handlers across all hooks.
func (hr *HookRegistry) TotalHandlers() int {
	hr.mu.RLock()
	defer hr.mu.RUnlock()
	count := 0
	for _, hs := range hr.handlers {
		count += len(hs)
	}
	return count
}

// Reset removes all handlers (for testing).
func (hr *HookRegistry) Reset() {
	hr.mu.Lock()
	defer hr.mu.Unlock()
	for k := range hr.handlers {
		hr.handlers[k] = []HookHandler{}
	}
}

// ── Built-in handler factories (the 15 default behaviors) ──────────────────

// makeSessionStartHandler creates a handler for HookSessionStart.
func makeSessionStartHandler(brain *Brain) HookHandler {
	return func(name string, ctx map[string]interface{}) interface{} {
		log.Printf("[hooks] session_start — warming caches")
		info := map[string]interface{}{
			"timestamp": time.Now().Format(time.RFC3339),
		}
		fl := brain.FactsLayer()
		if fl != nil {
			stats := fl.Stats()
			info["facts_count"] = stats["total_facts"]
			// Pre-warm: get top concepts
			results, _ := fl.Query(context.Background(), "", 3)
			concepts := make([]string, 0, len(results))
			for _, r := range results {
				if len(r.Content) > 80 {
					concepts = append(concepts, r.Content[:80])
				} else {
					concepts = append(concepts, r.Content)
				}
			}
			info["top_concepts"] = concepts
		}
		// Check KB layer
		for _, l := range brain.layers {
			if l.Name() == "kb" {
				info["kb_available"] = true
			}
		}
		return info
	}
}

// makePreUserMessageHandler creates a handler for HookPreUserMessage.
func makePreUserMessageHandler(brain *Brain) HookHandler {
	return func(name string, ctx map[string]interface{}) interface{} {
		userMsg, _ := ctx["content"].(string)
		if userMsg == "" {
			return map[string]interface{}{"injected": false, "reason": "no content"}
		}
		fl := brain.FactsLayer()
		if fl == nil {
			return map[string]interface{}{"injected": false, "reason": "no facts layer"}
		}
		results, _ := fl.Query(context.Background(), userMsg, 5)
		memories := make([]map[string]interface{}, 0, len(results))
		for _, r := range results {
			memories = append(memories, map[string]interface{}{
				"content": r.Content,
				"score":   r.Score,
				"_layer":  "facts",
			})
		}
		return map[string]interface{}{"injected": true, "memories": memories}
	}
}

// makePostUserMessageHandler creates a handler for HookPostUserMessage.
func makePostUserMessageHandler(brain *Brain) HookHandler {
	return func(name string, ctx map[string]interface{}) interface{} {
		content, _ := ctx["content"].(string)
		source, _ := ctx["source"].(string)
		if source == "" {
			source = "user_prompt"
		}
		if content == "" {
			return map[string]interface{}{"indexed": false}
		}
		fl := brain.FactsLayer()
		if fl == nil {
			return map[string]interface{}{"indexed": false}
		}
		err := fl.Store(context.Background(), Item{
			Content:  content,
			Category: "user_prompt",
			Source:   source,
		})
		if err != nil {
			return map[string]interface{}{"indexed": false, "error": err.Error()}
		}
		return map[string]interface{}{"indexed": true}
	}
}

// makePreToolUseHandler creates a handler for HookPreToolUse.
func makePreToolUseHandler(brain *Brain) HookHandler {
	return func(name string, ctx map[string]interface{}) interface{} {
		toolName, _ := ctx["tool_name"].(string)
		args, _ := ctx["arguments"].(string)
		query := toolName
		if args != "" {
			query += " " + args
		}
		if len(query) > 200 {
			query = query[:200]
		}
		if query == "" {
			return map[string]interface{}{"recalled": false}
		}
		fl := brain.FactsLayer()
		if fl == nil {
			return map[string]interface{}{"recalled": false}
		}
		results, _ := fl.Query(context.Background(), query, 3)
		return map[string]interface{}{"recalled": true, "results": results}
	}
}

// makePostToolUseHandler creates a handler for HookPostToolUse.
func makePostToolUseHandler(brain *Brain) HookHandler {
	return func(name string, ctx map[string]interface{}) interface{} {
		toolName, _ := ctx["tool_name"].(string)
		result, _ := ctx["result"].(string)
		if result == "" && toolName == "" {
			return map[string]interface{}{"stored": false}
		}
		observation := "Tool [" + toolName + "] returned: "
		if len(result) > 300 {
			observation += result[:300]
		} else {
			observation += result
		}
		fl := brain.FactsLayer()
		if fl == nil {
			return map[string]interface{}{"stored": false}
		}
		err := fl.Store(context.Background(), Item{
			Content:  observation,
			Category: "tool_observation",
			Tags:     []string{toolName},
		})
		if err != nil {
			return map[string]interface{}{"stored": false, "error": err.Error()}
		}
		return map[string]interface{}{"stored": true}
	}
}

// makePostAssistantMessageHandler creates a handler for HookPostAssistantMessage.
func makePostAssistantMessageHandler(brain *Brain) HookHandler {
	return func(name string, ctx map[string]interface{}) interface{} {
		content, _ := ctx["content"].(string)
		if content == "" || len(content) < 20 {
			return map[string]interface{}{"facts_stored": 0}
		}
		fl := brain.FactsLayer()
		if fl == nil {
			return map[string]interface{}{"facts_stored": 0}
		}
		err := fl.Store(context.Background(), Item{
			Content:  content,
			Category: "assistant_reply",
		})
		if err != nil {
			return map[string]interface{}{"facts_stored": 0, "error": err.Error()}
		}
		return map[string]interface{}{"facts_stored": 1}
	}
}

// makeDecisionMadeHandler creates a handler for HookDecisionMade.
func makeDecisionMadeHandler(brain *Brain) HookHandler {
	return func(name string, ctx map[string]interface{}) interface{} {
		content, _ := ctx["content"].(string)
		correction, _ := ctx["correction"].(string)
		fl := brain.FactsLayer()
		if fl == nil {
			return map[string]interface{}{"decided": false}
		}
		if correction != "" {
			err := fl.Store(context.Background(), Item{
				Content:  correction,
				Category: "correction",
				Tags:     []string{"decision"},
				Trust:    0.95,
			})
			if err != nil {
				return map[string]interface{}{"corrected": false, "error": err.Error()}
			}
			return map[string]interface{}{"corrected": true}
		}
		if content != "" {
			err := fl.Store(context.Background(), Item{
				Content:  content,
				Category: "decision",
				Trust:    0.9,
			})
			if err != nil {
				return map[string]interface{}{"decided": false, "error": err.Error()}
			}
			return map[string]interface{}{"decided": true}
		}
		return map[string]interface{}{"decided": false}
	}
}

// makeFileEditHandler creates a handler for HookFileEdit.
func makeFileEditHandler(brain *Brain) HookHandler {
	return func(name string, ctx map[string]interface{}) interface{} {
		filePath, _ := ctx["file_path"].(string)
		if filePath == "" {
			return map[string]interface{}{"reindexed": false, "verify_tracked": false}
		}
		RecordEdit(filePath)
		brain.FireHook(HookVerifyRequest, map[string]interface{}{
			"changed_paths": GetChangedPaths(),
		})
		return map[string]interface{}{"reindexed": false, "verify_tracked": true}
	}
}

// makeErrorOccurredHandler creates a handler for HookErrorOccurred.
func makeErrorOccurredHandler(brain *Brain) HookHandler {
	return func(name string, ctx map[string]interface{}) interface{} {
		errorStr, _ := ctx["error"].(string)
		tool, _ := ctx["tool_name"].(string)
		contextStr, _ := ctx["context"].(string)
		content := "ERROR [" + tool + "]: " + errorStr
		if contextStr != "" {
			truncated := contextStr
			if len(truncated) > 200 {
				truncated = truncated[:200]
			}
			content += " | Context: " + truncated
		}
		fl := brain.FactsLayer()
		if fl == nil {
			return map[string]interface{}{"stored": false}
		}
		tagStr := "error"
		if tool != "" {
			tagStr = "error," + tool
		}
		err := fl.Store(context.Background(), Item{
			Content:  content,
			Category: "error",
			Tags:     strings.Split(tagStr, ","),
		})
		if err != nil {
			return map[string]interface{}{"stored": false, "error": err.Error()}
		}
		return map[string]interface{}{"stored": true}
	}
}

// makeCompactionHandler creates a handler for HookCompaction.
func makeCompactionHandler(brain *Brain) HookHandler {
	return func(name string, ctx map[string]interface{}) interface{} {
		summary, _ := ctx["summary"].(string)
		if summary == "" {
			return map[string]interface{}{"stored": false}
		}
		fl := brain.FactsLayer()
		if fl == nil {
			return map[string]interface{}{"stored": false}
		}
		err := fl.Store(context.Background(), Item{
			Content:  summary,
			Category: "session_summary",
			Tags:     []string{"compaction"},
		})
		if err != nil {
			return map[string]interface{}{"stored": false, "error": err.Error()}
		}
		return map[string]interface{}{"stored": true}
	}
}

// makeSessionEndHandler creates a handler for HookSessionEnd.
func makeSessionEndHandler(brain *Brain) HookHandler {
	return func(name string, ctx map[string]interface{}) interface{} {
		summary, _ := ctx["summary"].(string)
		if summary == "" {
			return map[string]interface{}{"logged": false}
		}
		fl := brain.FactsLayer()
		if fl == nil {
			return map[string]interface{}{"logged": false}
		}
		err := fl.Store(context.Background(), Item{
			Content:  summary,
			Category: "session_summary",
			Tags:     []string{"session_end"},
		})
		if err != nil {
			return map[string]interface{}{"logged": false, "error": err.Error()}
		}
		return map[string]interface{}{"logged": true}
	}
}

// makeIdle30MinHandler creates a handler for HookIdle30Min.
func makeIdle30MinHandler(brain *Brain) HookHandler {
	return func(name string, ctx map[string]interface{}) interface{} {
		fl := brain.FactsLayer()
		if fl == nil {
			return map[string]interface{}{"error": "no facts layer"}
		}
		// Snapshot before bulk operations
		snapshotMeta := map[string]interface{}{}
		snapshotInfo, err := fl.CreateSnapshot("idle_30min_maintenance")
		if err == nil {
			snapshotMeta["snapshot_id"] = snapshotInfo.ID
		}
		// Apply forgetting decay
		decayResult := fl.ApplyDecay(0.01)
		// Memory evolution pass
		evolution := fl.Evolve(0.65)
		return map[string]interface{}{
			"snapshot":      snapshotMeta["snapshot_id"],
			"decay":         decayResult,
			"contradictions": 0,
			"evolved":       evolution["merged"],
		}
	}
}

// makeVerifyRequestHandler creates a handler for HookVerifyRequest.
func makeVerifyRequestHandler(brain *Brain) HookHandler {
	return func(name string, ctx map[string]interface{}) interface{} {
		changedRaw, _ := ctx["changed_paths"].([]string)
		if len(changedRaw) == 0 {
			return map[string]interface{}{"nudge": nil, "reason": "no changed paths"}
		}
		nudge := BuildVerifyNudge()
		return map[string]interface{}{
			"nudge":               nudge,
			"changed_paths_count": len(changedRaw),
			"needs_verification":  nudge != "",
		}
	}
}

// makeNoopHandler creates a no-op handler for hooks with no default logic.
func makeNoopHandler(brain *Brain) HookHandler {
	return func(name string, ctx map[string]interface{}) interface{} {
		return map[string]interface{}{"handled": false, "hook": name}
	}
}

// ── Default registration ───────────────────────────────────────────────────

// RegisterDefaultHooks creates and registers all 15 built-in handlers on a Brain.
func RegisterDefaultHooks(brain *Brain) *HookRegistry {
	registry := brain.Hooks

	factories := map[string]func(brain *Brain) HookHandler{
		HookSessionStart:         makeSessionStartHandler,
		HookPreUserMessage:       makePreUserMessageHandler,
		HookPostUserMessage:      makePostUserMessageHandler,
		HookPreToolUse:           makePreToolUseHandler,
		HookPostToolUse:          makePostToolUseHandler,
		HookPostAssistantMessage: makePostAssistantMessageHandler,
		HookDecisionMade:         makeDecisionMadeHandler,
		HookFileEdit:             makeFileEditHandler,
		HookVerifyRequest:        makeVerifyRequestHandler,
		HookErrorOccurred:        makeErrorOccurredHandler,
		HookCompaction:           makeCompactionHandler,
		HookSessionEnd:           makeSessionEndHandler,
		HookIdle30Min:            makeIdle30MinHandler,
		HookSyncStart:            makeNoopHandler,
		HookSyncComplete:         makeNoopHandler,
		HookSyncError:            makeNoopHandler,
	}

	for hookName, factory := range factories {
		registry.Register(hookName, factory(brain))
	}

	return registry
}
