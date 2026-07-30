# Quick Wins - Recent Improvements

## 1. Fixed REPL Mode Commands

**Before:** The REPL mode (non-TTY) only supported `/quit`. Any other command showed "Commands: /quit".

**After:** Full command support in REPL mode:
- `/help`, `/stats`, `/tools`, `/skills`, `/memory`, `/recall <query>`, `/save`, `/session`, `/providers`, `/clear`, `/quit`

## 2. Graceful Shutdown

**Added:** Signal handling for SIGINT/SIGTERM that saves state before exiting.

## 3. Auto-Save Timer

**Added:** Background goroutine that saves state every 5 minutes when `session.auto_save` is true (default).

## 4. Fixed `math_eval` Skill

**Before:** Only parsed a single float value (e.g., `fmt.Sscanf("42", "%f", &result)`), didn't actually evaluate expressions.

**After:** Full safe arithmetic expression evaluator using Go's AST parser:
- Supports `+`, `-`, `*`, `/`, `%`, parentheses
- Supports math functions: `abs()`, `sqrt()`, `sin()`, `cos()`, `round()`, `floor()`, `ceil()`
- Supports constants: `pi`, `e`
- Safe: only allows mathematical operations, no code execution

## 5. Added `/sessions` Command

**Added:** Both TUI and REPL now support listing saved sessions with `/sessions`.

## 6. Added `create_backup` Tool

**New tool:** Creates a timestamped ZIP backup of the eling project, excluding compiled binaries, existing zips, .git, node_modules, vendor, cache directories, and other non-essential files.

## 7. Added `codebase-intelligence` Skill

**New tool:** Meta-skill that documents the codebase analysis tools available via the codebase-memory-mcp integration.

## 8. Major Consolidation — Reduced Duplicate Functionality (~540 lines removed)

**Scope:** Four groups of duplicate/redundant functionality were consolidated across the codebase:

### A. Dead Code & Skills
- **Removed:** `internal/skills/` package (Go package `skills` completely gone)
- **Moved:** MCP skill from old `skills` package to `internal/mcp/skill/skill.go` (package `mcpskill`)
- **Removed:** `SkillManager` struct — `ListSkills()` now returns `[]tools.Tool` from the ToolRegistry (`category:"skill"`)
- **Simplified:** `main.go` imports `mcpskill` directly — 7 references updated

### B. Auto-Learning Unified
- **Renamed:** `learnFromExchange()` → `autoLearn()` (single LLM-based skill learning function)
- **Removed:** Old pattern-based `autoLearn()` (the Python port replaced the legacy heuristic)
- **Removed:** `detectPromptType()` — no longer needed after unification

### C. Memory Consolidation
- **Removed:** `saveConversationToMemory()` + `AddAssistantMessage` + `AddUserMessage` + `NewConversation` — conversation saving handled automatically by Brain's Facts layer
- **Removed:** `StartDecay()` / `StopDecay()` from in-memory memory — unified under `FactsLayer.ApplyDecay()` (SQL exponential time-decay)
- **No race conditions:** The old in-memory decay had channel-close race conditions; SQL-based decay is thread-safe

### D. Semantic Search Integration
- **Added:** `BrainQuery` hook injected into the 8-layer Brain — semantic search now queries all layers via RRF fusion
- **Removed:** 6 dead functions: `searchMemoryItems`, `MemoryItemData`, `ItemsData`, `SetMemoryItems`, `SemanticIndexSave`, `SemanticIndexLoad`
- **Retained:** `AddToSemanticIndex` (needed by the `semantic_index` tool)
- **Local fallback preserved:** Trigram-based fallback still in place for offline operation

**Impact:** ~540 lines removed, ~482 lines moved/relocated, ~50 lines added. Zero external behavior changes. `go build ./...` compiles clean.

## 9. Created Documentation

**Added:** `docs/README.md` with architecture overview and project structure documentation.
**Added:** `docs/ARCHITECTURE.md` (564 lines) — full system architecture, pipeline flow, thread safety model.
**Added:** `docs/API.md` — configuration schema, CLI flags, provider API compatibility, state storage.
**Added:** `docs/DEVELOPMENT.md` — developer setup, workflow, adding tools, thread safety guidelines.
**Added:** `docs/TOOLS.md` — complete reference for all 20+ built-in tools.
