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

## 9. Skill Auto-Learn Fixes — UsedCount Tracking, Restart Restoration, Persistence

**Three critical fixes to the skill auto-learn system:**

### A. `UsedCount` Now Tracks Actual Usage
**Before:** `UsedCount` was set to 0 when a skill was created but never incremented. The eviction algorithm always fell back to `LearnedAt` (oldest-first FIFO), ignoring which skills were actually useful.

**After:** `incrementSkillUsedCount()` is called after all 3 tool execution paths (`Ask()`, `runStreamToolLoop()`, `UseTool()`). The eviction algorithm now has meaningful `UsedCount` data — skills that are actually used get priority, and unused skills are evicted first.

### B. Skills Restored into Tool Registry on Restart
**Before:** `LoadState()` restored `a.skills` from `skills.json` but never re-registered them into the Tool Registry. Learned skills persisted in JSON but became invisible to the LLM after restart.

**After:** In `LoadState()`, after restoring `a.skills`, each skill is registered into `ToolRegistry` as a category `"skill"` tool — exactly like `autoLearn()` does during live learning. Skills are visible and callable after restart.

### C. Auto-Learned Skills Now Persist as Dynamic Tools
**Before:** `autoLearn()` registered skills in the tool registry but never called `tools.AddDynamicTool()`. Skills were lost on restart because they weren't written to `tools.json`.

**After:** In `autoLearn()`, after registering in the tool registry, the skill is also saved via `tools.AddDynamicTool()` with category `"skill"`. Skills survive application restarts — they're re-loaded from `tools.json` alongside user-defined tools.

**Impact:** The skill system now works end-to-end: skills are learned, tracked by actual usage, survive restarts, and are intelligently evicted (least-used first).

## 10. Created Documentation

**Added:** `docs/README.md` with architecture overview and project structure documentation.
**Added:** `docs/ARCHITECTURE.md` (564 lines) — full system architecture, pipeline flow, thread safety model.
**Added:** `docs/API.md` — configuration schema, CLI flags, provider API compatibility, state storage.
**Added:** `docs/DEVELOPMENT.md` — developer setup, workflow, adding tools, thread safety guidelines.
**Added:** `docs/TOOLS.md` — complete reference for all 20+ built-in tools.

## 11. Grep Switched to ugrep 7.5.0

**Before:** The `grep` tool used GNU grep (via `/usr/local/bin/grep` → `/usr/bin/grep`).

**After:** All `grep` calls (tool + TUI display + wrapper) now resolve to **ugrep 7.5.0**, unlocking:
- Fuzzy search (`-Z`), compressed-archive search (`-z`)
- JSON / CSV output, file-type filters (`-t`)
- Boolean operators (`--bool`), smart case (`-S`), multi-line matching (`-U`)

## 12. Paste-Safe Multi-Line Input (TUI)

**Before:** Pasting multi-line text into the TUI auto-sent it line-by-line (each pasted `\n` submitted the buffer), corrupting long code pastes.

**After:** ELING detects paste bursts (both bracketed-paste events and fast key sequences on plain terminals) and **holds newlines** while pasting — `Enter` inserts a newline instead of submitting. The input shows `pasting… newlines are held` then `multiline input — Enter to send`; you review the paste and deliberately press `Enter` to send. (`pasteBurstWindow` 60 ms, `pasteGrace` 350 ms — see `internal/tui/paste_test.go`)

## 13. Scrolling Marquee Banner (TUI)

**Added:** A pink animated ticker at the top of the TUI — `✦ ELING — Auto-Learning Evolving AI Agent ✦ Adaptive • Intelligent • Autonomous ✦` — scrolls continuously across the display width (rune-safe, wraps seamlessly). It sits above the status header, which was also recolored from green to light blue for a softer Catppuccin look.

## 14. Auto-Backup Before `write` / `edit`

**Before:** `write`/`edit` overwrote files with no safety net — a bad edit destroyed the original content permanently.

**After:** Every `write`/`edit` snapshots the existing file to `*.bak.<timestamp>` before mutating (no-op skip if content is identical). Rotation keeps the last **5** backups per file; `ELING_BACKUP_DIR` mirrors backups to a central directory, `ELING_BACKUP_KEEP` overrides rotation count. Covered by `internal/tools/files_backup_test.go`.

## 15. Web Timeout Prediction (`fetchPredictor`)

**Before:** `web_fetch`/`web_search` hung on slow or dead hosts until curl's full `--max-time` expired (10s+ per attempt, worse with fallbacks).

**After:** New `internal/tools/web_timeout.go` adds a three-part predictor (web tools now v2.1.0):
1. **Preflight probe** — fast DNS + TCP reachability check; dead hosts fail in ~1.5s
2. **Adaptive max-time** — per-host curl `--max-time` derived from recorded latency/failure history
3. **Outcome recording** — every fetch feeds the model, so predictions improve over time

Response payloads now include a `timeout_prediction` object with host, latency, and fail counts. Covered by `internal/tools/web_timeout_test.go`.

## 16. DeepSeek Reasoning-Content Persistence

**Fixed:** DeepSeek thinking-mode rejects assistant messages that omit `reasoning_content` — resumed sessions and tool-loop follow-ups used to fail.

**After:** `lastReasoning` is stored on every round (even empty) and:
- persisted with assistant session entries (`AppendWithReasoning`)
- passed back in tool-loop follow-up messages
- streamed to the TUI as thinking text

## 17. TUI Stale-Message Guard

**Fixed:** After submitting a new query, late tool results/errors from the previous query's goroutine could still render in the conversation.

**After:** Messages are wrapped with a generation counter (`genMsg`); the TUI discards any message whose generation doesn't match the current submit generation.

## 18. Rebuild & Launcher Scripts

- **`rebuild.sh`** — builds to a temp file then atomically `mv`s over `./eling` (never `cp`). On overlayfs/proot, `cp` truncates the running inode → SIGBUS (exit 135); `mv` swaps inodes safely.
- **`start.sh`** — launcher that wraps the binary and catches fatal OS signals (SIGBUS/SIGSEGV/SIGABRT/SIGILL/SIGFPE), writing a crash report with overlayfs-specific guidance.
- **`kill-eling.sh`** — graceful shutdown helper (SIGTERM, not SIGKILL).

## 19. Setup Wizard Upgrades — `--add-provider`, `--dedupe`, `--test`, TokenRouter

**Before:** The wizard only overwrote the single default provider; adding a second provider meant hand-editing `config.yaml`.

**After:**
- **`--add-provider`** — add a new provider without touching the current config. Interactive mode now prompts **provider first, then that provider's own API key** (never reuses the existing key). Also works fully non-interactive: `./eling setup --add-provider --provider groq --model llama-3.3-70b --api-key gsk-... --base-url https://api.groq.com/openai/v1`
- **`--dedupe`** — scans the `providers:` section and removes exact duplicates (same `model` + `base_url` + `api_key`), keeping the first occurrence; prints what was removed.
- **`--test`** — live API connection check after saving.
- **TokenRouter provider** added to the interactive menu (`9) tokenrouter` → `deepseek/deepseek-v4-flash` @ `https://api.tokenrouter.com/v1`); free keys at tokenrouter.com.
- `eling setup` now **delegates to `eling-wizard.sh`** whenever it's found — the three entry points (`./eling setup`, `./eling-wizard.sh`, `./eling-setup`) run the exact same interactive flow. Extended flags the wizard doesn't support (`--add-provider`, `--test`, `--dedupe`) fall through to the built-in Go implementation in `internal/cli/setup.go`.
- **Usage tip:** always `./eling setup --list` before editing to verify the config was written correctly.

## 20. ECCADAption — Output Shaping, Validated Persistence, Rust-Style Guardrails

**Added:** three-plan-phase feature set (see `eccadaption.md` at repo root) with all new behaviors **opt-in and default-off**, preserving fresh-install behavior exactly:

### A. P1 — End-message output shaping (`internal/layers/shaping.go`)
- New `EndMessagePolicy` + `NewEndMessage` pump: rune cap (with truncation trailer), paragraph cap, markdown-strip — applied at a single choke point in the agent (`Agent.shapeEndMessage`, agent.go).
- Config: `output.end_message_runes` / `output.end_message_paras` / `output.end_message_no_md` (all default 0/false).
- Fires new `end_message_produce` lifecycle hook with `{before_len, after_len, note}` when shaping actually fires.

### B. Self-Validated Persistence (P2)
- Session: `verifyTotals()` recomputes `total_tokens` on save and logs drift (audit-only, never hard-fails).
- MCP: `ManagerFromConfig(cfg)` + `Manager.Reset(cfg)` reload configured servers mid-session without dropping sessions.
- Permissions: `PermissionsConfig → PermPolicy` bridge asserted by `TestPermPolicyFromConfig` (default/mode mapping, invalid-rule dropping, resolution order rule > project trust > default).

### C. Rust-style Guardrails (P3)
- `internal/layers/guardrails.go`: four white-box invariants (`end_message_under_budget`, `session_token_monotonic`, `openers_match_perms`, `mcp_server_matches_config`) exposed as `GuardrailID` + `GuardrailsAssert{Violation, Witness}` + `AssertAll()`/`DescribeAll()`. Zero witness ⇒ zero asserts ⇒ fully inert.
- Config: `guardrails.audit` (log-only) / `guardrails.strict` (hard-veto). Wired into the end-message emit path first.

**Impact:** zero behavior change until configured; each phase verified with `go build`, `go vet`, and `go test` before commit (P1: `9c8dc77`+`d4253dd`+`2613fd2`, P2: `0fc5b59`+`a2bc3ea`+`384f9d5`, P3: `806272d`+`94c1313`+`d038c56`).
