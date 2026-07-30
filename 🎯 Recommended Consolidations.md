# 🎯 Recommended Consolidations — Execution Plan

> **Status: ✅ Groups A, B, C COMPLETE | 🚧 Group D IN PROGRESS | Last updated: 2025-07-30**

```text
Progress:  ████████████░░░░░░  75% (3 of 4 groups done)
Groups:    A ✅ B ✅ C ✅ D 🚧
```

**Goal:** Eliminate all redundant/duplicate/dead code while preserving **every externally visible behavior** — every tool the LLM can call, every TUI command, every CLI flag, every API endpoint, every persistence mechanism.

**Strategy:** 4 parallelizable execution groups. Groups A, B, C are **independent** (different files/different concerns). Group D depends on understanding `Brain.Query()` return type.

**Pre-flight (before any group):**
```bash
cd /root/eling && go build ./... && go test ./... && git add -A && git commit -m "checkpoint before consolidation"
```

---

## 🔍 What We Found — 5 Redundancy Clusters

| Cluster | Systems | Impact | Risk |
|---------|---------|--------|------|
| **🔴 R1** Triple conversation storage | `agent.Memory.Remember()` + `saveConversationToMemory()` + `Brain.Store()` (via hooks) | 3× writes of same data, 2× decay systems drift | 🟢 |
| **🔴 R2** Dead skill system | `skills.Manager` (256 lines) — 3 builtins already in `tools.Registry`, **plus** 482-line `mcp_skill.go` that depends on this package | 256 dead lines + cross-registration in 4 methods | 🟢 |
| **🔴 R3** Two auto-learning paths | `autoLearn()` (pattern-based, creates `LearnedSkill` entries **never exposed to LLM**) + `learnFromExchange()` (LLM-based, creates callable tools) | Double creation of evolutions, dead entries, wasted goroutines | 🟡 |
| **🟡 R4** Two decay systems | `agent.Memory.StartDecay()` (flat subtraction) + `FactsLayer.ApplyDecay()` (exponential time-decay) | Drift between systems, 50 lines dead code | 🟡 |
| **🟡 R5** Two semantic indexes | `tools.SemanticIndex` (in-memory trigram, 300+ lines) + `FactsLayer` (SQLite FTS5+HRR) | Duplicate storage, different search results, dual persistence | 🔴 |

---

## ⚡ Fast-Track Execution Plan

```
Day 1:  Group A — Dead Code & Skills (🟢 Low risk)        ← biggest win, least risk
        Includes: MCP skill relocation, skills.Manager removal, register_skill merge

Day 1:  Group B — Auto-Learning Unification (🟡 Med risk)  ← independent of A

Day 2:  Group C — Memory Consolidation (🟡 Med risk)       ← independent of A & B

Day 3+: Group D — Semantic Search Unification (🔴 High risk) ← only if needed
```

---

# 🚀 Group A — Dead Code & Skills Cleanup (🟢 ~160 lines changed)

**What:** Move MCP skill out of `skills` package, remove `skills.Manager` entirely, merge `register_skill` into `register_tool`, clean up dead code.

**Files:** `internal/skills/mcp_skill.go`, `internal/skills/skills.go`, `internal/skills/skills_test.go`, `internal/agent/agent.go`, `internal/tools/register.go`, `internal/tui/tui.go`, `main.go`

---

### A0. Move MCP Skill out of `skills` package ⚠️ CRITICAL — MUST DO FIRST ✅

The 482-line `internal/skills/mcp_skill.go` is in `package skills` and provides 7 exported functions used by `main.go`. Before removing the `skills` package, this file must be relocated.

**Create new package:**

```bash
mkdir -p internal/mcp/skill
cp internal/skills/mcp_skill.go internal/mcp/skill/skill.go
```

**Edit `internal/mcp/skill/skill.go` — change package declaration (line 18):**

```go
// BEFORE:
package skills

// AFTER:
package mcpskill
```

**Update imports in `main.go` (line 23):**

```go
// BEFORE:
    "eling/internal/skills"

// AFTER:
    "eling/internal/mcp/skill"
```

**Update all 7 function references in `main.go` (lines 225-262):**

```go
// Line 225:
// BEFORE:
    skillCfg := skills.DefaultMCPSkillConfig()
// AFTER:
    skillCfg := mcpskill.DefaultMCPSkillConfig()

// Line 232:
// BEFORE:
    skills.RegisterMCPSkill(skillCfg)
// AFTER:
    mcpskill.RegisterMCPSkill(skillCfg)

// Line 236:
// BEFORE:
    if err := skills.MCPSkillStart(); err != nil {
// AFTER:
    if err := mcpskill.MCPSkillStart(); err != nil {

// Line 243:
// BEFORE:
    status, _ := skills.MCPSkillStatus()
// AFTER:
    status, _ := mcpskill.MCPSkillStatus()

// Line 246:
// BEFORE:
    skills.MCPSkillStop()
// AFTER:
    mcpskill.MCPSkillStop()

// Line 252:
// BEFORE:
    if err := skills.MCPSkillStart(); err != nil {
// AFTER:
    if err := mcpskill.MCPSkillStart(); err != nil {

// Line 262:
// BEFORE:
    skills.MCPSkillStop()
// AFTER:
    mcpskill.MCPSkillStop()
```

**Remove import from `agent.go` (line 28) — only used for `skills.Manager` type:**

```go
// BEFORE:
    "eling/internal/skills"
// AFTER: (remove this line entirely)
```

**Verify MCP skill still works:**

```bash
cd /root/eling && go build ./...
# Test MCP skill is registered
go run . --help 2>&1 | grep mcp-server
# Expected: "mcp-server" appears in available commands
```

---

### A1. Remove `internal/skills/skills.go` and `internal/skills/skills_test.go` ✅

These files contain `Manager`, `Skill`, 3 built-ins (`echo`, `math_eval`, `web_search`). All 3 are already served by `tools.Registry`:
- `echo` → `tools/register.go` (the `echo` tool)
- `math_eval` → `tools/schema.go` (the `math_eval` tool)
- `web_search` → `tools/web.go` (the real web search)

```bash
rm internal/skills/skills.go internal/skills/skills_test.go
```

---

### A2. Remove `SkillManager` field from Agent struct ✅

**Edit `/root/eling/internal/agent/agent.go`:**

```go
// BEFORE (line 95):
    // Skills/Plugins (dynamic plugin/skill manager)
    SkillManager *skills.Manager

// AFTER: remove both lines
```

---

### A3. Remove `skMgr` creation and `SkillManager` init from `NewAgent()` ✅

```go
// BEFORE (line 199):
    skMgr := skills.NewManager()

// AFTER: remove this line
```

```go
// BEFORE (line 210):
    SkillManager:    skMgr,

// AFTER: remove this line
```

---

### A4. Remove the skills bridge loop in `NewAgent()` (lines 223-241) ✅

```go
// BEFORE:
    // Bridge skills into the tool registry so the LLM can call them.
    // Skip web_search since tools/web.go already provides a working version.
    for _, sk := range skMgr.List() {
        if sk.Name == "web_search" {
            continue // tools/web.go has the real web_search
        }
        // ... (entire for loop)
    }

// AFTER: remove this entire for loop
```

---

### A5. Change `ListSkills()` to query `ToolRegistry` ✅

```go
// BEFORE:
// ListSkills returns all registered skills.
func (a *Agent) ListSkills() []skills.Skill {
    return a.SkillManager.List()
}

// AFTER:
// ListSkills returns all registered skills (skills are tools with category "skill").
func (a *Agent) ListSkills() []tools.Tool {
    return a.ToolRegistry.ListByCategory("skill")
}
```

**No changes needed for consumers:**
- `tui.go:1096` — iterates `.Name` and `.Description`, which exist on `tools.Tool`
- `main.go:621` — iterates `.Name` and `.Description`, which exist on `tools.Tool`
- Both use `agent.TruncateStr(sk.Description, 60)` — works with any struct having `.Description string`

---

### A6. Remove `SkillManager.Register()` calls from `AddPluginFromCommand()`, `AddSkill()`, `restoreDynamicTool()` ✅

**Edit `AddPluginFromCommand()` (lines 1541-1549) — remove the "Also register as a skill" block:**

```go
// BEFORE:
    a.ToolRegistry.Register(tool)

    // Also register as a skill
    _ = a.SkillManager.Register(skills.Skill{
        Name:        name,
        Description: description,
        ...
    })
    tools.AddDynamicTool(tools.DynamicTool{
        Name:        name,
        Description: description,
        Category:    cat,
        Command:     command,
    })
    return nil

// AFTER:
    a.ToolRegistry.Register(tool)
    tools.AddDynamicTool(tools.DynamicTool{
        Name:        name,
        Description: description,
        Category:    cat,
        Command:     command,
    })
    return nil
```

**Edit `AddSkill()` (lines 1561-1600) — remove `a.SkillManager.Register(...)` and its error check:**

```go
// BEFORE:
    err := a.SkillManager.Register(skills.Skill{
        Name:        name,
        Description: description,
        ...
    })
    if err != nil {
        return err
    }
    // Also register in tool registry so the LLM can call it
    ...

// AFTER (simplified):
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
```

**Edit `restoreDynamicTool()` (lines 1785-1796) — remove the "Also restore in SkillManager" block:**

```go
// BEFORE:
    a.ToolRegistry.Register(tool)

    // Also restore in SkillManager for skills and plugins
    if cat == "skill" || cat == "plugin" {
        _ = a.SkillManager.Register(skills.Skill{
            Name:        tool.Name,
            Description: tool.Description,
            ...
        })
    }

// AFTER:
    a.ToolRegistry.Register(tool)
```

---

### A7. Merge `register_skill` into `register_tool`

**Edit `internal/tools/register.go`:**

1. Remove the `register_skill` tool registration (lines 78-86)
2. Add a `type` parameter to `register_tool` description
3. In `registerToolExecute()`, add type detection:

```go
// In the tool definition's ArgsSchema, add:
"type": {
    "type": "string",
    "description": "Type of registration: 'tool' (default) or 'skill'",
    "enum": ["tool", "skill"]
},

// In registerToolExecute(), add after getting name/description:
toolType, _ := args["type"].(string)
if toolType == "" {
    toolType = "tool"
}
cat := "dynamic"
if toolType == "skill" {
    cat = "skill"
}
// Use `cat` instead of hardcoded "dynamic" when registering the tool
```

---

### ✅ Group A Verification

```bash
# Compile check
cd /root/eling && go build ./...

# No SkillManager references remain
grep -rn 'SkillManager' internal/ --include='*.go'
# Expected: 0 results

# No skills package import in agent.go
grep '"eling/internal/skills"' internal/agent/agent.go
# Expected: 0 results

# ListSkills returns []tools.Tool
grep -A2 'func.*ListSkills' internal/agent/agent.go
# Expected: return a.ToolRegistry.ListByCategory("skill")

# No register_skill tool
grep 'register_skill' internal/tools/register.go
# Expected: 0 results (only register_tool remains)

# MCP skill still works
grep -rn 'mcpskill\.' main.go
# Expected: 7 matches (MCPSkillStart, MCPSkillStop, etc.)

# Test
cd /root/eling && go test ./...

# Binary builds and runs
go build -o /dev/null .
```

---

# 🚀 Group B — Auto-Learning Unification (🟡 ~80 lines changed)

**What:** Remove pattern-based `autoLearn()` (creates unused `LearnedSkill` entries with meaningless `Pattern` fields never exposed to LLM). `learnFromExchange()` is the real auto-learning system (LLM-decides, creates callable tools). Rename it to `autoLearn()`.

**Files:** `internal/agent/agent.go`

---

### B1. Remove old `autoLearn()` function body (lines 2268-2333)

This function:
- Calls `a.memory.Remember()` (redundant — conversation is already stored via `Brain.Store()` hooks)
- Creates `LearnedSkill` entries with `Pattern: "prompt_type=..."` that are **never exposed to the LLM** as callable tools
- Records duplicate evolution entries (the new `autoLearn` also records evolutions)

Remove the entire function signature + body.

---

### B2. Update goroutine calls in `Ask()` (line 437-438)

```go
// BEFORE:
            go a.autoLearn(prompt, finalContent)
            go a.learnFromExchange(prompt, finalContent)
// AFTER:
            go a.autoLearn(prompt, finalContent)    // learnFromExchange renamed to autoLearn
```

---

### B3. Update goroutine calls in `AskStream()` (line 1052-1053)

```go
// BEFORE:
            go a.autoLearn(prompt, fullResponse)
            go a.learnFromExchange(prompt, fullResponse)
// AFTER:
            go a.autoLearn(prompt, fullResponse)    // learnFromExchange renamed to autoLearn
```

---

### B4. Rename `learnFromExchange()` → `autoLearn()` (line 2334)

```go
// BEFORE:
func (a *Agent) learnFromExchange(prompt, response string) {
// AFTER:
func (a *Agent) autoLearn(prompt, response string) {
```

---

### B5. Remove `detectPromptType()` function (line 2691)

This function is only called by the old `autoLearn()` which is now removed. Delete the entire function.

---

### B6. (Optional) Check if `Pattern` field from `LearnedSkill` struct is still used

```bash
grep -rn '\.Pattern' internal/agent/ --include='*.go' | grep -v '_test'
```
If `Pattern` is only read in the old `autoLearn()` (now removed), remove it from the `LearnedSkill` struct definition.

---

### ✅ Group B Verification

```bash
# Compile
cd /root/eling && go build ./...

# Only ONE autoLearn function
grep -n 'func.*autoLearn\|func.*learnFromExchange' internal/agent/agent.go
# Expected: 1 match — "func (a *Agent) autoLearn(prompt, response string) {"

# Only TWO calls to autoLearn (one in Ask, one in AskStream)
grep -n 'go a.autoLearn\|go a.learnFromExchange' internal/agent/agent.go
# Expected: 2 matches — both "go a.autoLearn(...)"

# No detectPromptType
grep -n 'detectPromptType' internal/agent/agent.go
# Expected: 0 results

# Test
cd /root/eling && go test ./...
```

---

# 🚀 Group C — Memory Consolidation (🟡 ~100 lines changed)

**What:** Remove `saveConversationToMemory()` (redundant with `Brain.Store()` via `HookPostAssistantMessage` + session persistence). Remove `agent.Memory.StartDecay()` / `StopDecay()` (redundant with `FactsLayer.ApplyDecay()`). Keep `FactsLayer` as canonical long-term store. Keep `agent.Memory` for short-term `/recall` command.

**Files:** `internal/agent/agent.go`, `internal/agent/memory.go`

---

### C1. Remove `saveConversationToMemory()` goroutine calls

**In `Ask()` (line 439):**
```go
// BEFORE:
            go a.autoLearn(prompt, finalContent)
            go a.saveConversationToMemory(prompt, finalContent)
            go a.updateConversationSummary()
// AFTER:
            go a.autoLearn(prompt, finalContent)
            go a.updateConversationSummary()
```

**In `AskStream()` (line 1054):**
```go
// BEFORE:
            go a.autoLearn(prompt, fullResponse)
            go a.saveConversationToMemory(prompt, fullResponse)
            go a.updateConversationSummary()
// AFTER:
            go a.autoLearn(prompt, fullResponse)
            go a.updateConversationSummary()
```

---

### C2. Remove `saveConversationToMemory()` function (lines 2540-2599)

Remove the entire function and its helper `inferConversationTags()` (lines ~2496-2540).

**Also remove the `saveTurnCounter` field from Agent struct (line 119):**
```go
// BEFORE (line 119):
    // saveTurnCounter is an atomic counter for rate-limiting conversation
    // memory saves to every 3rd turn (see saveConversationToMemory).
    saveTurnCounter atomic.Int32

// AFTER: remove these 3 lines
```

---

### C3. Remove `agent.Memory.StartDecay()` and `StopDecay()` references

**Remove from `NewAgent()` (line 197):**
```go
// BEFORE:
    // Start memory strength decay goroutine if a decay rate is configured
    if cfg.Memory.DecayRate > 0 {
        mem.StartDecay(10*time.Minute, cfg.Memory.DecayRate)
    }

// AFTER: remove these 5 lines
```

**Remove from `LoadState()` (lines 1666, 1715):**
```go
// Line 1666:
    // Stop any existing decay goroutine on the current memory before replacing it.
    if a.memory != nil {
        a.memory.StopDecay()
    }
// Remove these 4 lines

// Line 1715:
    // Restart memory decay on the loaded memory
    if a.cfg.Memory.DecayRate > 0 {
        a.memory.StartDecay(10*time.Minute, a.cfg.Memory.DecayRate)
    }
// Remove these 4 lines
```

---

### C4. Remove `StartDecay()` and `StopDecay()` from `internal/agent/memory.go`

Remove:
- `decayStop chan struct{}` field from `Memory` struct
- `decayCancel context.CancelFunc` field from `Memory` struct
- `StartDecay()` method (lines 232-278)
- `StopDecay()` method (lines 280+)
- `decayOnce()` method (if it exists as a separate method)

The `decayOnce` logic is embedded in `StartDecay` goroutine (lines 260-266). Remove all of it.

---

### C5. Keep `DecayRate` in config (do NOT remove)

The `DecayRate` config field (at `internal/config/config.go:68`) is **still used by `FactsLayer.ApplyDecay()`** via `layers.DefaultDecayRate` and by `internal/cli/cli.go` for CLI configuration. Only the `agent.Memory` usage is removed. Leave the config field in place.

---

### C6. Verify `a.memory.Remember()` is only called from correct places

After Group B removes the old `autoLearn()` (which called `a.memory.Remember()` at line 2282), verify:

```bash
grep -n '\.Remember(' internal/agent/agent.go
# Expected: only calls from /remember command handler and LoadState, NOT from auto-learning
```

If the new `autoLearn()` (formerly `learnFromExchange()`) calls `a.memory.Remember()`, verify it doesn't — the LLM-based autoLearn creates skills via `ToolRegistry`, not via `Memory.Remember()`.

---

### ✅ Group C Verification

```bash
# Compile
cd /root/eling && go build ./...

# No saveConversationToMemory references
grep -rn 'saveConversationToMemory\|saveTurnCounter\|inferConversationTags' internal/ --include='*.go'
# Expected: 0 results

# No StartDecay/StopDecay references in agent.go
grep -n 'StartDecay\|StopDecay' internal/agent/agent.go
# Expected: 0 results

# No StartDecay/StopDecay methods in memory.go
grep -n 'func.*StartDecay\|func.*StopDecay' internal/agent/memory.go
# Expected: 0 results

# /recall still works (agent.Memory still exists)
grep -n 'func.*Recall\|func.*Memory' internal/agent/agent.go | head -5
# Expected: GetMemory() and /recall handler still present

# Test
cd /root/eling && go test ./...
```

---

# 🚀 Group D — Semantic Search Unification (🔴 ~300 lines changed, HIGH RISK)

**What:** Make `semantic_search` tool query `Brain.Query()` (which searches `FactsLayer` via BM25+FTS5+HRR) instead of maintaining a separate in-memory trigram index. Remove independent `SemanticIndex` storage/persistence.

**⚠️ Risk:** `semantic_search` is a tool the LLM calls directly. The implementation must maintain equivalent or better search quality. `Brain.Query()` returns `[]layers.Result` with `Content` and `Score` fields; the existing tool returns `[]SemanticResult` with `Content`, `Score`, `Category`, `Tags`. We need an adapter.

**Files:** `internal/tools/semantic.go`, `internal/agent/agent.go`, `internal/layers/layers.go`

**Strategy:** Don't remove the trigram fallback — keep it as a local offline option. Add `Brain.Query()` as the primary backend when available.

---

### D1. Add Adapter Type in `internal/tools/semantic.go`

Add near the top of the file (after imports):

```go
// BrainSearchFn is an adapter that wraps Brain.Query() for use by semantic_search.
// Set by agent.go at startup. Returns results sorted by relevance.
type BrainSearchFn func(query string, limit int) ([]SearchResult, error)

// SearchResult is a unified result from any backend (Brain or local trigram).
type SearchResult struct {
    Content  string
    Score    float64
    Category string
    Tags     []string
    Source   string
}

// BrainQuery is the package-level hook injected by agent.go.
// When nil, semantic_search falls back to local trigram search.
var BrainQuery BrainSearchFn
```

---

### D2. Refactor `semanticSearchExecute()` to use Brain query first

In `semanticSearchExecute()` (line 548), add Brain query at the top:

```go
// In semanticSearchExecute, BEFORE the existing trigram search:
    // Try Brain query first (more accurate, FTS5+HRR hybrid scoring)
    if BrainQuery != nil {
        limit := 5
        if l, ok := args["limit"].(float64); ok && l > 0 {
            limit = int(l)
        }
        results, err := BrainQuery(query, limit)
        if err == nil && len(results) > 0 {
            return OK(map[string]interface{}{
                "query":   query,
                "results": results,
                "total":   len(results),
            }), nil
        }
        // Fall through to local search if Brain returns nothing
    }

    // ... existing trigram search code (keep as fallback) ...
```

---

### D3. Inject Brain.Query in `NewAgent()` — add to `internal/agent/agent.go`

In `NewAgent()`, after `a.Brain` is initialized (around line 220):

```go
// Wire up semantic_search tool to use Brain.Query
tools.BrainQuery = func(query string, limit int) ([]tools.SearchResult, error) {
    ctx := context.Background()
    results, err := a.Brain.Query(ctx, query, limit)
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
```

---

### D4. Remove `SetMemoryItems()`, `AddToSemanticIndex()`, `SemanticIndexSave()`, `SemanticIndexLoad()`

**Remove calls from agent.go:**
- Line 194: `tools.SetMemoryItems(mem.ItemsData())` in `NewAgent()`
- Line 1712: `tools.SetMemoryItems(a.memory.ItemsData())` in `LoadState()`
- Line 1744: `_ = tools.SemanticIndexLoad(filepath.Join(a.stateDir, "semantic_index.json"))` in `LoadState()`
- Line 1900: `_ = tools.SemanticIndexSave(filepath.Join(a.stateDir, "semantic_index.json"))` in `SaveState()`
- Line 2570: `tools.AddToSemanticIndex(tools.SemanticIndexItem{...})` in old `saveConversationToMemory()` — already removed in Group C

**Remove functions from `internal/tools/semantic.go`:**
- `SetMemoryItems()` (line 307)
- `AddToSemanticIndex()` (line 331)
- `SemanticIndexSave()` (line 370)
- `SemanticIndexLoad()` (line 396)
- `cachedMemoryItems`, `cachedMemoryItemsMu` globals
- `MemoryItemData` type (if only used by these functions)
- `searchMemoryItems()` function (line 618)
- `cachedIndex`, `cachedIndexMu` globals (if only used by removed functions)

**Keep:**
- `localEmbedding()` — still useful for offline fallback
- `getEmbedding()` — still useful for offline fallback
- `cosineSimilarity()` — still useful for offline fallback
- `embeddingCache` — still useful for API efficiency
- `SemanticResult` type — still used by local search fallback
- `SemanticSearch()` function (line 360) — still used by local search
- `semanticSearch()` function (line 436) — still used by local search
- `semanticSearchExecute()` — refactored to try Brain first, fall back to local
- `semanticIndexExecute()` — keep `semantic_index` tool for explicit indexing

---

### D5. Remove `ItemsData()` from `internal/agent/memory.go` if no longer needed

After removing `SetMemoryItems()`, check if `ItemsData()` is used elsewhere:

```bash
grep -rn 'ItemsData' internal/ --include='*.go'
```

If only used by the now-removed calls, remove the `ItemsData()` method and the `tools.MemoryItemData` type (if defined in `semantic.go`). **But keep `MemoryItemData`** if it's used by the local search fallback's `searchMemoryItems()` — wait, we're removing `searchMemoryItems()` too. So if `MemoryItemData` is only used by `SetMemoryItems` and `searchMemoryItems`, remove it.

---

### ✅ Group D Verification

```bash
# Compile
cd /root/eling && go build ./...

# No SetMemoryItems/AddToSemanticIndex references
grep -rn 'SetMemoryItems\|AddToSemanticIndex\|SemanticIndexSave\|SemanticIndexLoad' internal/ --include='*.go'
# Expected: 0 results

# BrainQuery is injected
grep -n 'BrainQuery\s*=' internal/agent/agent.go
# Expected: 1 match (in NewAgent)

# Test semantic_search still works
cd /root/eling && go test ./...

# Functional test
echo '{"query":"test query"}' | go run . --run-tool semantic_search 2>&1 | head -20
# Expected: returns results (either from Brain or local fallback)
```

---

## 📊 Impact Summary

| Group | Lines Changed | Risk | Behavior Change | Rollback |
|-------|:------------:|:----:|:---------------:|:--------:|
| **A** Dead Code & Skills | ~160 removed + 482 moved | 🟢 | None — skills still show in `/skills`, MCP server still works | `git checkout -- internal/agent/agent.go internal/tools/register.go main.go` |
| **B** Auto-Learning | ~80 removed | 🟡 | None — `learnFromExchange()` (renamed `autoLearn()`) still creates callable skills; dead pattern-learning removed | `git checkout -- internal/agent/agent.go` |
| **C** Memory | ~100 removed | 🟡 | None — `/recall` still works via `agent.Memory`, Brain.Store() hooks handle persistence | `git checkout -- internal/agent/agent.go internal/agent/memory.go` |
| **D** Semantic Search | ~200 removed + ~50 added | 🔴 | None — `semantic_search` tool returns results from Brain.Query() with local trigram fallback | `git checkout -- internal/tools/semantic.go internal/agent/agent.go` |
| **Total** | **~540 removed + ~532 moved + ~50 added** | | **Zero external behavior changes** | |

---

## 🔄 Rollback Strategy

Each group is independently revertible:

```bash
# Before starting each group:
cd /root/eling && git add -A && git commit -m "checkpoint before group <letter>"

# If a group causes issues, revert it:
git revert HEAD

# Or for individual file recovery:
git checkout HEAD~1 -- internal/agent/agent.go

# Full restore to pre-consolidation state:
git checkout <pre-consolidation-commit-hash>
```

---

## 📋 Pre-Flight Checklist (run before each group)

```bash
# 1. Verify current state compiles
cd /root/eling && go build ./... && go test ./...

# 2. Create git checkpoint
cd /root/eling && git add -A && git commit -m "checkpoint before group <letter>"

# 3. Note current binary size
go build -o /dev/null . 2>&1; ls -lh eling 2>/dev/null || echo "no binary"
```

## ✅ Post-Group Checklist (run after each group)

```bash
# 1. Compile
cd /root/eling && go build ./...

# 2. Run tests
cd /root/eling && go test ./...

# 3. Verify key behaviors (adapt per group)
go run . --help 2>&1 | head -5
echo "/skills" | go run . --interactive 2>&1 | head -20
```
