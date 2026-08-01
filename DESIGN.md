# ELING - Auto-Learning Evolving AI Agent

## Architecture Overview

```
┌──────────────────────────────────────────────────────────┐
│                    TUI (Bubbletea 3-Panel)               │
│  ┌──────────────────────────────────────────────────┐    │
│  │  MARQUEE: ✦ ELING — Auto-Learning… (scrolling)   │    │
│  │  HEADER: Model | Session | Tokens | Mem% | MCP   │    │
│  ├──────────────────────────────────────────────────┤    │
│  │  BODY: Scrollable conversation log + tool output │    │
│  ├──────────────────────────────────────────────────┤    │
│  │  INPUT: > text entry · tool(args) · /command    │    │
│  │         (paste-safe: multi-line held until Enter)│    │
│  └──────────────────────────────────────────────────┘    │
└──────────────────────┬───────────────────────────────────┘
                       │
┌──────────────────────▼───────────────────────────────────┐
│                    Agent Core (Go)                        │
│  ┌──────────┬──────────────┬────────────────┬──────────┐ │
│  │Provider  │ Memory       │ Tool Registry  │ Sessions │ │
│  │Manager   │ Short+Long   │ (Dynamic,      │ Save/    │ │
│  │(Multi)   │ Term, Brain  │ Thread-safe)   │ Resume   │ │
│  │          │ 8-Layer RRF  │ 20+ tools      │          │ │
│  ├──────────┴──────┬───────┴────────────────┴──────────┤ │
│  │  🧠 8-Layer Brain (RRF Fusion)                      │ │
│  │  Builtin · Blackbox · Facts · Code · KB             │ │
│  │  Obsidian · Notion · Continuum                      │ │
│  ├─────────────────┬───────────────────────────────────┤ │
│  │  MCP Client (JSON-RPC 2.0) · mcpskill tool          │ │
│  │  Auto-Learning (autoLearn) | Evolution               │ │
│  │  Self-Adaptive Timeout | Key Rotation               │ │
│  └─────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────┘
```

## Components

### 1. CLI Layer (main.go)
- Flag parsing, config loading, signal handling, PID management
- Crash detection / graceful shutdown / auto-save
- `eling setup` subcommand — built-in wizard (delegates to `eling-wizard.sh`; extended flags `--add-provider`, `--test`, `--dedupe` handled by `internal/cli/setup.go`)

### 2. TUI Layer (Bubbletea)
- **Marquee banner**: Pink animated ticker (`✦ ELING — Auto-Learning Evolving AI Agent ✦`) scrolls continuously above the header
- **Header**: Status bar, model info, memory usage, MCP status (recolored light blue for Catppuccin)
- **Body**: Scrollable viewport for conversation history and tool output (generation-counter guard discards stale messages from old query goroutines)
- **Input**: Bottom text entry with command handling — **paste-safe** (bracketed-paste + burst detection hold newlines; a paste never auto-sends, `Enter` submits deliberately)

### 3. Agent Core (internal/agent/)
- Auto-learning: `autoLearn()` uses LLM to extract reusable skills from every exchange
- Evolving: Agent can register new tools and skills at runtime
- Self-reflection: Periodic conversation summarization for long context
- Turn timeout prediction: Self-adaptive based on history
- Plan Mode: `--plan` flag / `/plan` TUI command — drafts a numbered plan with **tools stripped** (a plan-only system suffix), then gates execution behind user approval (y/N/Esc); approved plans persist to the session and are re-injected as a system message so the model follows them
- Instant LSP diagnostics: `internal/lsp` — a minimal JSON-RPC 2.0 (Content-Length framed) LSP client over stdio; one server per language (`gopls`/`pyright-langserver`/`typescript-language-server`), started lazily on first `write`/`edit`; after `HookPostToolUse` the agent appends a compact `[lsp] file.go:12:5 ERR: ...` section to the tool result so the model self-corrects. Best-effort: missing binaries silently skip; `lsp.KillAll()` mirrors `tools.KillRunningTools()` on TUI shutdown
- Skill lifecycle: Skills track `UsedCount`; the 100-skill cap evicts **least-used** (not oldest); auto-learned skills persist to `tools.json` and re-register into the Tool Registry on restart
- Reasoning persistence: DeepSeek `reasoning_content` stored per round and passed back on tool-loop follow-ups and session resume

### 4. Provider (internal/provider/)
- Multi-provider LLM client (DeepSeek, OpenAI, Groq, any OpenAI-compatible)
- Streaming responses, function/tool calling, automatic fallback
- Key rotation on auth failures, exponential backoff retry

### 5. Memory System
- **Short-term**: Conversation context window (configurable, default 50 items)
- **Long-term**: Persistent store backed by FactsLayer (SQLite + BM25 FTS5)
- **🧠 8-Layer Brain**: Builtin · Blackbox · Facts · Code · KB · Obsidian · Notion · Continuum
- **RRF Fusion**: Results from all layers fused using Reciprocal Rank Fusion
- **Semantic Search**: Vector embeddings via BrainQuery hook + local trigram fallback
- **Auto-Learning**: Extracts skills from interactions via LLM

### 6. Tool System (internal/tools/)
- Dynamic thread-safe registry (20+ built-in tools)
- Categories: system, skill, plugin, dynamic, mcp
- Panic-safe execution with bounded output limits (512 KiB bash output, 256 KiB tool results)
- Skills stored as tools with `category:"skill"` in ToolRegistry — no separate SkillManager
- `ListSkills()` returns `[]tools.Tool` directly from registry
- Search via **ugrep 7.5.0** (all `grep` calls — fuzzy `-Z`, archives `-z`, JSON/CSV, `--bool`, smart case `-S`)
- **Auto-backup before mutation**: every `write`/`edit` snapshots the file to `*.bak.<timestamp>` (rotation keeps 5; `ELING_BACKUP_DIR` / `ELING_BACKUP_KEEP` configurable)
- **Web timeout prediction** (`internal/tools/web_timeout.go`): fast DNS+TCP preflight probe (dead hosts fail in ~1.5s) + adaptive curl `--max-time` per host from recorded latency/failure history

### 6b. Setup Wizard (`eling setup` → `eling-wizard.sh` / `eling-setup`)
- Same interactive flow from `./eling setup`, `./eling-wizard.sh`, or `./eling-setup` (delegates to the wizard script when found)
- Provider menu includes: opencode-zen, opencode-zen-free, deepseek-direct, openai, groq, tokenrouter, custom
- Extended flags beyond the wizard: `--add-provider` (add without touching current config), `--test` (live connection check), `--dedupe` (removes exact-duplicate providers: same model + base_url + api_key)

### 7. MCP Client (internal/mcp/)
- Connect to external Model Context Protocol servers
- JSON-RPC 2.0 over stdio
- Dynamic tool discovery from MCP servers
- `mcpskill` tool for managing connections from conversation

### 8. Session Management (internal/session/)
- Named sessions with save/resume
- Auto-save every 5 minutes
- Conversation summary compression

### 9. 8-Layer Memory Architecture (internal/layers/)

```
📡 Layer 8: CONTINUUM   — multi-agent orchestration hub (shared continuum.db)
🧠 Layer 7: NOTION     — online brain, persistent, human-readable (optional)
📝 Layer 6: OBSIDIAN   — local Markdown vault, project notes, daily logs
📚 Layer 5: KB         — FTS5 knowledge corpus for long-form knowledge
🕸️ Layer 4: CODE       — codegraph symbol intelligence
💎 Layer 3: FACTS      — SQLite + BM25 hybrid with trust scoring
🔎 Layer 2: BLACKBOX   — flight recorder + telemetry + 11-metric efficiency scoring
⚡ Layer 1: BUILTIN    — MEMORY.md / USER.md (always-on, zero setup)
```

Adapted from [PatrickNoFilter/eling](https://github.com/PatrickNoFilter/eling) (Python).

## Key Libraries
- `charm.land/bubbletea` — TUI framework
- `charm.land/charm/bubbles` — UI components
- `charm.land/charm/lipgloss` — Styling
- `github.com/mark3labs/mcp-go` — MCP protocol
- `modernc.org/sqlite` — Pure-Go SQLite for Brain layers
- `gopkg.in/yaml.v3` — Configuration
- `ugrep` 7.5.0 — Search engine powering all `grep` tool calls (fuzzy, archives, JSON/CSV, `--bool`)

## Reliability Tooling
- **`rebuild.sh`** — atomic rebuild (builds to temp, then `mv` — never `cp`, which truncates the running inode on overlayfs/proot and causes SIGBUS)
- **`start.sh`** — launcher trapping fatal OS signals (SIGBUS/SIGSEGV/SIGABRT/SIGILL/SIGFPE) and writing crash reports with overlayfs guidance
- **`kill-eling.sh`** — graceful shutdown helper (SIGTERM, never SIGKILL)
- **Auto-backup** — every `write`/`edit` snapshots the original file before mutation (`*.bak.<timestamp>`, rotation keeps 5)
