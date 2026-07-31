# ELING Architecture Documentation

## System Architecture

ELING follows a **modular layered architecture** with clear separation of concerns. Each subsystem communicates through well-defined interfaces, making the system extensible, testable, and maintainable.

```
┌─────────────────────────────────────────────────────────────────┐
│                        CLI Layer (main.go)                      │
│  Flag parsing · Config loading · Signal handling · PID mgmt     │
│  Crash detection · Graceful shutdown · Auto-save timer          │
└──────────┬──────────────────────────────────────────────────────┘
           │ agent.New(cfg) · ag.Ask() · ag.AskStream()
           │ ag.LoadState() · ag.SaveState() · ag.ListSessions()
┌──────────▼──────────────────────────────────────────────────────┐
│                        Agent Core (agent/)                      │
│                                                                  │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │                    Ask Pipeline                          │   │
│  │                                                         │   │
│  │  buildContext() → buildMessages() → runToolLoop()        │   │
│  │                                        ↓                 │   │
│  │  autoLearn() ← LLM skill learning                        │   │
│  │  updateConversationSummary()                            │   │
│  └──────────────────────────────────────────────────────────┘   │
│                                                                  │
│  ┌────────────┬──────────────┬────────────────┬──────────────┐  │
│  │ Provider   │ Memory       │ Tool Registry  │ Session Mgr  │  │
│  │ Manager    │ (Short+Long) │ (Thread-safe)  │ (Save/Resume)│  │
│  │ (Fallback) │              │                │              │  │
│  └────────────┴──────────────┴────────────────┴──────────────┘  │
│                                                                  │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  Subsystems: MCP Client · mcpskill · Brain (8-Layer)    │   │
│  │  Auto-Learning · Evolution · Turn Timeout History        │   │
│  └──────────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────────┘
```

---

## Layer 1: CLI & Entry Point (`main.go`)

### Responsibilities
- Parse command-line flags
- Load configuration from file or defaults
- Initialize crash-safe logger
- Detect previous crashes (PID file staleness, bus errors)
- Enforce single-instance (kill previous PID)
- Create agent instance and run appropriate mode (TUI, REPL, non-interactive)
- Handle OS signals (SIGTERM graceful shutdown, SIGBUS/SIGSEGV crash reporting)
- Auto-save state every 5 minutes

### Crash Safety Architecture
```
┌─────────────────────────────────────────────────────┐
│                  Crash Protection                    │
│                                                     │
│  checkCrashOnStartup()                              │
│    ├── PID file staleness detection                 │
│    └── Bus error / segfault detection (dmesg)       │
│                                                     │
│  recoverWithStack(ag)                               │
│    ├── Catches all panics in main goroutines        │
│    ├── Logs to crash_report.log                     │
│    ├── Prints stack trace to stderr                 │
│    └── safeSaveState(ag) with 5s timeout           │
│                                                     │
│  Fatal Signal Handler (goroutine)                   │
│    ├── Catches SIGBUS, SIGSEGV (hardware faults)    │
│    ├── Logs crash details                           │
│    └── Resets handler and re-raises for core dump   │
└─────────────────────────────────────────────────────┘
```

### Key Design Decisions
- **PID file** at `~/.eling/eling.pid` ensures only one instance runs per user
- **Fatal signal handler** catches hardware faults that Go's `recover()` cannot
- **Safe state save** uses a goroutine with timeout to prevent deadlock if panic happened while holding mutex
- **Clean shutdown marker** written to log so subsequent startups know the previous exit was clean

---

## Layer 2: Agent Core (`internal/agent/agent.go`)

### Ask Pipeline (the heart of ELING)

```
User Prompt
    │
    ▼
buildContext(prompt)
    ├── Substring memory recall (fast)
    ├── Semantic search recall (meaning-based)
    └── Tool list injection (compact reference)
    │
    ▼
buildMessages(prompt)
    ├── System prompt + conversation summary
    ├── Session history (trimmed to token budget)
    │   ├── Always keeps minimum recent entries
    │   └── Older entries summarized via LLM
    └── Current user prompt
    │
    ▼
runToolLoop(ctx, prov, messages, toolDefs, maxDuration)
    │
    ├── Round 1: Chat() → response
    │   ├── No tool calls → return final answer
    │   └── Has tool calls → execute each tool
    │       ├── Tool result capped at 256 KiB
    │       └── Messages trimmed to 100 max
    │
    ├── Round 2: Chat(response + tool results) → ...
    │   └── (repeat until no more tool calls or max rounds)
    │
    ├── Wall-clock timeout check (self-adaptive)
    │   ├── estimateTurnDuration(prompt) — history-based prediction
    │   └── On timeout: retry with 2x elapsed time
    │
    └── On success:
        ├── Record turn duration (for future prediction)
        ├── Save to session (user + assistant entries)
        ├── autoLearn() — LLM judges if response is worth memorizing as a skill
        └── updateConversationSummary() — context compression
```

### Skill Auto-Learn Lifecycle

```
autoLearn() (LLM-judged exchange)
    │
    ├── Creates skill → registers in ToolRegistry (category:"skill")
    ├── Persists via tools.AddDynamicTool() → tools.json   ← survives restart
    ├── LoadState() re-registers skills.json entries into ToolRegistry
    └── incrementSkillUsedCount() on every tool execution path
        (Ask, runStreamToolLoop, UseTool)
    │
    └── Eviction (100-skill cap): evicts LOWEST UsedCount first
        (usage-based, not just oldest LearnedAt)
```

Key behaviors:
- `UsedCount` is incremented after all 3 tool-execution paths — eviction uses real usage data
- Auto-learned skills persist to `tools.json` alongside user-defined tools
- On restart, `LoadState()` re-registers restored skills into the Tool Registry (visible & callable)

### Reasoning-Content Persistence

DeepSeek thinking-mode models return a `reasoning_content` field alongside the answer. ELING stores `lastReasoning` on every round (even empty) and:
1. Persists it with assistant session entries (`AppendWithReasoning`)
2. Passes it back in tool-loop follow-up assistant messages (DeepSeek rejects messages that omit it in thinking mode)
3. Streams it to the TUI as "thinking" text (`ToolCallEvent.Reasoning`)

### Self-Adaptive Timeout Mechanism

```
Past Turn Records (up to 100)
    │
    ▼
estimateTurnDuration(prompt)
    ├── Filter records with similar prompt length (±20%)
    ├── Average their durations
    ├── Add 50% safety margin
    └── Minimum 30 seconds
    │
    ▼
If timeout occurs on actual turn:
    ├── elapsed = time since start
    ├── newTimeout = elapsed × 2
    ├── min 60 seconds
    └── Retry up to MaxTurnDurationRetries (default 2)
```

### Provider Fallback Chain

```
chatWithRetry()
    │
    ├── Try primary provider (with retries)
    │   ├── Exponential backoff: 2s, 4s, 8s + 30% jitter
    │   ├── Max retries: provider-level (5) + agent-level (2)
    │   └── If budget exceeded → try next provider
    │
    ├── Try fallback provider 1
    │   └── (same retry logic)
    │
    ├── Try fallback provider 2...
    │
    └── If ALL providers fail → clear error message
```

### Message Budget Management

```
Token Budget = MaxContext (default 32768)
    │
    ├── System prompt tokens (deducted)
    ├── Conversation summary tokens (deducted)
    ├── Current user prompt tokens (deducted)
    │
    ├── Reserve 1/3 budget for recent history
    │   ├── Always keep ≥ 4 recent entries
    │   └── Then fill with history up to budget
    │
    └── If history exceeds budget → older entries dropped
        (But summary preserves their essence)
```

---

## Layer 3: Memory System (`internal/agent/memory.go`)

### Memory Architecture

```
┌────────────────────────────────────────────┐
│              Memory                         │
│                                            │
│  Short-Term (recent context)               │
│  ├── Max: 50 items (configurable)          │
│  ├── First-in, first-out                   │
│  └── Overflow → consolidated to long-term  │
│                                            │
│  Long-Term (persistent store)              │
│  ├── Max: 1000 items (configurable)        │
│  ├── Strength: 0.0–1.0                     │
│  ├── Decays over time (configurable rate)  │
│  ├── Reinforced on recall (+0.1)           │
│  └── Weakest pruned when full              │
│                                            │
│  Each item:                                │
│  ├── ID (nanotimestamp + counter)          │
│  ├── Content (text)                        │
│  ├── Category (fact, preference, skill)    │
│  ├── Tags (for filtering)                  │
│  ├── CreatedAt                             │
│  ├── Accessed (counter)                    │
│  └── Strength (decay + reinforcement)      │
└────────────────────────────────────────────┘
```

### Memory Operations

| Operation | Description | Lock |
|-----------|-------------|------|
| `Remember(content, category, tags)` | Store new item → short-term → overflow to long-term | Write |
| `Recall(query)` | Search by substring or tag match | Write (strength boost) |
| `Recent(n)` | Return last n items (deep copy) | Read |
| `Len()` | Total item count | Read |
| `Stats()` | Memory usage statistics | Read |
| `forgetWeakest()` | Remove bottom 10% by strength | Write |

### Thread Safety
- `sync.RWMutex` on all operations
- `Recent()` returns deep copies to prevent slice aliasing
- Decay is handled by FactsLayer.ApplyDecay() (SQL-level, not in-memory)

---

## Layer 4: Dynamic Tool System (`internal/tools/`)

### Registry Architecture

```
┌──────────────────────────────────────────────┐
│             Tool Registry                     │
│  sync.RWMutex                                │
│                                              │
│  tools: map[name] → Tool                     │
│  categories: map[category] → [tool names]    │
│                                              │
│  Operations:                                 │
│  ├── Register(tool)                          │
│  ├── Unregister(name)                        │
│  ├── Get(name) → (Tool, bool)               │
│  ├── List() → []Tool                         │
│  ├── ListByCategory(cat) → []Tool            │
│  ├── Count() → int                           │
│  ├── Categories() → []string                 │
│  ├── Execute(name, args) → (result, error)  │
│  └── ToProviderDefs() → []ToolDef            │
└──────────────────────────────────────────────┘
```

### Tool Categories

| Category | Description | Examples |
|----------|-------------|----------|
| `system` | Built-in, always available | bash, read, write, grep, web_search |
| `skill` | Learned or registered skills | pattern_coding, user-defined |
| `plugin` | Command-based plugins | Community skill scripts |
| `dynamic` | Runtime-registered tools | User-created via register_tool |
| `mcp` | MCP server tools | filesystem, database |

### Panic-Safe Execution

Every tool execution is wrapped in `recover()`:
```
Execute(name, args)
    ├── Lookup tool in registry (read lock)
    ├── Defer recover() → logs panic, returns error
    └── Execute tool function
```

### Tool Result Limits
- **Bash output**: 512 KiB max (stdout + stderr combined)
- **Tool result string**: 256 KiB max (rune-aware truncation)
- **Running commands**: Tracked for Ctrl+C kill

### Search: ugrep 7.5.0
All `grep` tool calls resolve to **ugrep** via the `/usr/local/bin/grep` wrapper:
- Fuzzy search (`-Z`), compressed archives (`-z`), JSON/CSV output
- File-type filters (`-t`), boolean operators (`--bool`), smart case (`-S`), multi-line (`-U`)
- Internal invocation: `grep -rn -I -F` (or `-E` for regex) with `-m 5000` per-file cap, `.git`/`node_modules`/`vendor` excluded, 1 MB output cap, 10 s timeout

### Auto-Backup Before Mutation
Every `write`/`edit` snapshots the existing file to `*.bak.<timestamp>` before mutating (no-op when content is identical):
- Rotation keeps the last **5** backups per file (`ELING_BACKUP_KEEP` overrides)
- `ELING_BACKUP_DIR` mirrors backups to a central directory (source path preserved)
- Covered by `internal/tools/files_backup_test.go`

### Web Timeout Prediction (`internal/tools/web_timeout.go`)
`web_fetch` / `web_search` (v2.1.0) use a three-part predictor:
1. **Preflight probe** — fast DNS + TCP reachability check; dead hosts fail in ~1.5s
2. **Adaptive max-time** — per-host curl `--max-time` derived from recorded latency/failure history
3. **Outcome recording** — every fetch feeds the model, so predictions improve over time

Responses include a `timeout_prediction` object (host, latency, fail counts). Covered by `internal/tools/web_timeout_test.go`.

---

## Layer 5: Provider System (`internal/provider/deepseek.go`)

### Multi-Provider Architecture

```
┌──────────────────────────────────────────────┐
│           Provider Manager                   │
│                                              │
│  providers: map[name] → *Provider            │
│  default: string                            │
│                                              │
│  Operations:                                 │
│  ├── AddProvider(name, provider)             │
│  ├── SetDefault(name)                        │
│  ├── GetDefault() → *Provider               │
│  ├── Get(name) → (*Provider, bool)          │
│  └── List() → []string                      │
└──────────────────────────────────────────────┘
```

### Provider Request Flow

```
Chat(ctx, messages, toolDefs...)
    │
    ├── Build HTTP request (OpenAI-compatible JSON)
    ├── Set headers (Authorization, Content-Type)
    │
    ├── Send with retry loop:
    │   ├── Attempt HTTP POST
    │   ├── On transient error (408, 429, 5xx):
    │   │   ├── Parse Retry-After header
    │   │   ├── Exponential backoff + jitter
    │   │   └── Respect budget limit
    │   ├── On auth error (401):
    │   │   └── Rotate to next API key (round-robin)
    │   └── On non-retryable error:
    │       └── Return immediately
    │
    └── Parse response → ChatResponse
        ├── Content (text)
        ├── ToolCalls (function calls)
        └── Reasoning (e.g. DeepSeek reasoning_content)
```

### Retry Configuration

| Parameter | Default | Description |
|-----------|---------|-------------|
| `MaxRetries` | 5 | Maximum retry attempts |
| `BaseDelay` | 2s | Initial backoff delay |
| `MaxDelay` | 60s | Upper bound for backoff |
| `MaxBudget` | 360s | Total wall-clock retry budget |

### Key Rotation

When a provider returns 401 (Unauthorized):
1. Log the failure for the current key
2. Select the next key from `BackupKeys[]` (round-robin)
3. Retry the request with the new key
4. If all keys exhausted, return auth error

---

## Layer 6: Session System (`internal/session/session.go`)

### Session Architecture

```
┌──────────────────────────────────────────────┐
│           Session Manager                    │
│  sync.RWMutex                               │
│                                              │
│  saveDir: ~/.eling/sessions/                │
│  sessions: map[name] → *Session             │
│                                              │
│  Each Session:                              │
│  ├── ID (UUID)                              │
│  ├── Name (user-friendly)                   │
│  ├── CreatedAt / UpdatedAt                  │
│  ├── Model (provider model)                 │
│  ├── Entries[] (conversation turns)         │
│  │   ├── Role: user/assistant              │
│  │   ├── Content (text)                    │
│  │   ├── Timestamp                         │
│  │   ├── Tokens (estimated)               │
│  │   └── ToolCalls (optional)              │
│  └── Metadata (key-value store)            │
│      ├── summary                           │
│      ├── total_tokens                      │
│      └── ...                               │
└──────────────────────────────────────────────┘
```

### Session Operations

| Operation | Description |
|-----------|-------------|
| `Create(name, model)` | New session |
| `Append(name, role, content)` | Add entry |
| `Save(name)` | Persist to JSON |
| `Load(name)` | Load from disk |
| `List()` | All session names |
| `Resume(name)` | Load and return context |
| `UpdateSessionName(old, new)` | Rename |

### Persistence Format

Sessions are saved as individual JSON files:
```
~/.eling/sessions/
├── session_1700000000.json
├── my_code_review.json
└── debug_session.json
```

---

## Layer 7: MCP Client (`internal/mcp/mcp.go`)

### MCP Architecture

```
┌──────────────────────────────────────────────┐
│           MCP Manager                        │
│  sync.RWMutex                               │
│                                              │
│  servers: map[name] → *Server               │
│                                              │
│  Each Server:                               │
│  ├── Name                                   │
│  ├── Command + Args + Env                   │
│  ├── cmd (*exec.Cmd)                       │
│  ├── stdin / stdout / stderr pipes          │
│  ├── pending: map[id] → response channel    │
│  └── notifCh → client notifications         │
│                                              │
│  Operations:                                │
│  ├── Connect(ctx, name, cmd, args, env)     │
│  ├── Disconnect(name)                       │
│  ├── ListTools(ctx, name) → []Tool          │
│  ├── CallTool(ctx, name, tool, args)        │
│  └── List() → []string                     │
└──────────────────────────────────────────────┘
```

### JSON-RPC 2.0 Protocol

```
Request:  {"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}
Response: {"jsonrpc":"2.0","id":1,"result":{"tools":[...]}}

Request:  {"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read","arguments":{"path":"/file"}}}
Response: {"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"..."}]}}
```

---

## Layer 8: Terminal UI (`internal/tui/tui.go`)

### 3-Panel Layout

```
┌──────────────────────────────────────────────────────────┐
│  MARQUEE: ✦ ELING — Auto-Learning Evolving AI Agent ✦    │  ← scrolling ticker (pink)
│  HEADER:  Model | Session | Tokens | Mem% | MCP          │  ← light blue (Catppuccin)
├──────────────────────────────────────────────────────────┤
│  BODY: Scrollable conversation + tool output             │
│        (syntax-highlighted, color-coded status)          │
├──────────────────────────────────────────────────────────┤
│  INPUT: > text · tool(args) · /command                   │
│         paste-safe: multi-line held until deliberate Enter│
└──────────────────────────────────────────────────────────┘
```

### Paste-Safe Input

Pasting multi-line text no longer auto-sends each line. The TUI detects paste bursts two ways:

1. **Bracketed-paste events** — terminals that emit `\x1b[200~` / `\x1b[201~` markers are handled directly.
2. **Burst detection** — plain terminals: key sequences arriving faster than `pasteBurstWindow` (60 ms) are treated as a paste; a `pasteGrace` (350 ms) hold period keeps newlines as literal input after the burst.

While pasting, `Enter` inserts a newline instead of submitting; the input shows `pasting… newlines are held`, then `multiline input — Enter to send` afterwards. Only a deliberate final `Enter` submits the buffer. Covered by `internal/tui/paste_test.go`.

### Stale-Message Guard (`genMsg`)

Each query submission increments a **generation counter**. Every message rendered into the viewport is wrapped in `genMsg{gen, msg}`; the Update loop discards any message whose generation doesn't match the current submission. This prevents late tool results/errors from a previous query's goroutine bleeding into the new conversation.

### Thinking / Tool Progress

- Reasoning text (including DeepSeek `reasoning_content`) streams into the body as thinking
- Running tools show live elapsed timers; status colors reflect success/failure
- Viewport supports `PgUp` / `PgDn` scrolling

---

## Thread Safety Model

```
Lock Hierarchy (no deadlock risk — consistent ordering):
┌─────────────────────────────────────────────────────┐
│ 1. Agent.mu (sync.RWMutex)                          │
│    ├── Used for: state access, config, session mgmt │
│    ├── Read lock: Ask(), buildContext(), GetStats()  │
│    └── Write lock: SetSessionName(), SaveState()     │
│                                                     │
│ 2. Memory.mu (sync.Mutex)                           │
│    ├── Used for: all memory read/write operations   │
│    └── Never held while Agent.mu is held            │
│                                                     │
│ 3. Session.Manager.mu (sync.RWMutex)                │
│    ├── Used for: session CRUD operations            │
│    └── Held under Agent.mu read lock                │
│                                                     │
│ 4. Provider.Manager.mu (sync.RWMutex)               │
│    ├── Used for: provider management                │
│    └── Held under Agent.mu read lock                │
│                                                     │
│ 5. Agent.turnTimeoutMu (sync.RWMutex)               │
│    ├── Standalone (never nested under Agent.mu)     │
│    └── Used for: turn timeout history               │
│                                                     │
│ 6. Registry.mu (sync.RWMutex)                       │
│    ├── Standalone (never nested under Agent.mu)     │
│    └── Used for: tool registry operations           │
└─────────────────────────────────────────────────────┘
```

### Key Rules
1. **Never** acquire Agent.mu while holding another lock
2. **Never** call back into Agent from a tool execution (deadlock risk)
3. Memory operations use their own lock, separate from Agent
4. Tool registry is completely independent of Agent locking
5. All channel operations are non-blocking (buffered channels)

---

## 🧠 Layer 8: 8-Layer Memory Architecture (`internal/layers/`)

Adapted from [PatrickNoFilter/eling](https://github.com/PatrickNoFilter/eling) (Python) — the complete layered memory system ported to Go.

### Architecture Overview

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

### Layer Interface

```go
type Layer interface {
    Name() string
    Priority() int
    Query(ctx context.Context, q string, limit int) ([]Result, error)
    Store(ctx context.Context, item Item) error
    Close() error
}
```

### Brain Orchestrator (RRF Fusion)

```go
brain := layers.NewBrain(
    layers.NewBuiltinLayer(stateDir),
    blackboxLayer,
    factsLayer,
    codeLayer,
    kbLayer,
    obsidianLayer,
    notionLayer,
    continuumLayer,
)

results, err := brain.Query(ctx, "what did I learn about caching", 10)
```

Results from all layers are fused using **Reciprocal Rank Fusion (RRF)**:
```
RRF score = 1 / (60 + rank) for each result in each layer
```

### Layer Details

| Layer | File | DB | Key Feature |
|-------|------|----|-------------|
| **Builtin** | `builtin.go` | Flat files (MEMORY.md, USER.md) | Always-on identity context |
| **Blackbox** | `blackbox.go` | `blackbox.db` (SQLite) | 11-metric efficiency scoring |
| **Facts** | `facts.go` | `facts.db` (SQLite + FTS5) | BM25 + optional embeddings |
| **Code** | `code.go` | `code.db` (SQLite) | Auto-indexes Go functions/structs |
| **KB** | `kb.go` | `kb.db` (SQLite + FTS5) | Long-form knowledge storage |
| **Obsidian** | `obsidian.go` | Filesystem (`.md` files) | Local Markdown vault access |
| **Notion** | `notion.go` | Notion API (cloud) | Optional online persistence |
| **Continuum** | `continuum.go` | `continuum.db` (SQLite) | Multi-agent orchestration |

### Blackbox 11 Efficiency Metrics

| Metric | What it measures |
|--------|-----------------|
| Redundant reads | Files read twice without changes between |
| Cache hit ratio | Terminal output reuse vs. re-execution |
| Read amplification | Lines read per line written |
| Retry waste | Bash/compile failures retried |
| Yield density | Edits per tool call |
| Token efficiency | Total tokens used |
| Edit efficiency | Edits per file open |
| Test success | Passes per test run |
| Commit frequency | Commits per hour |
| Context window utilization | Proportion of context actually used |
| Subagent overhead | Orchestration cost of subagents |
