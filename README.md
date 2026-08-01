# 🧠 ELING — Auto-Learning Evolving AI Agent

> **E**volving **L**earning **I**ntelligent **N**eural **G**o Agent

ELING is a **self-improving terminal AI agent** written in Go — a production-grade conversation AI with persistent memory, dynamic tool execution, multi-provider LLM support, session management, MCP protocol integration, automatic code review, and autonomous skill learning.

Inspired by [jcode](https://github.com/1jehuang/jcode) and battle-tested with **DeepSeek V4 Flash** (compatible with any OpenAI API), ELING runs as a beautiful 3-panel Bubbletea TUI or as a non-interactive CLI.

---

## ✨ Features

### 🤖 Core AI
| Feature | Details |
|---------|---------|
| **Multi-Provider** | DeepSeek, OpenAI, Groq, Anthropic, or any OpenAI-compatible API |
| **Auto-Retry & Fallback** | Exponential backoff + automatic provider failover + key rotation |
| **Self-Adaptive Timeout** | Learns from past turn durations to predict optimal timeout |
| **Streaming Responses** | Real-time token-by-token streaming in TUI |
| **Tool Calling** | Full function-calling with multi-round tool execution |
| **Context Budgeting** | Intelligent token budget management with history trimming |
| **LLM Summarization** | Periodic LLM-generated conversation summaries for long context |

### 🧠 Memory & Learning
| Feature | Details |
|---------|---------|
| **Short-Term Memory** | Recent conversation context window |
| **Long-Term Memory** | Persistent store with FactsLayer (SQLite + BM25 FTS5) and trust scoring |
| **🧠 8-Layer Brain** | Builtin · Blackbox · Facts · Code · KB · Obsidian · Notion · Continuum — fused via RRF |
| **Semantic Search** | Vector embedding-based meaning search via BrainQuery hook (RRF fusion across all layers) + local trigram fallback |
| **Auto-Learning** | `autoLearn()` — LLM judges if a response is worth memorizing as a reusable skill |
| **Conversation Indexing** | Every turn indexed via Brain's Facts layer (automatic, no separate config needed) |
| **Memory Decay** | FactsLayer.ApplyDecay() — exponential time-decay on SQL data (unified, no duplicate in-memory decay) |

### 🏗️ 8-Layer Memory Architecture (Adapted from [PatrickNoFilter/eling](https://github.com/PatrickNoFilter/eling))

ELING now implements the complete 8-layer memory architecture from the Python eling-agent, ported to Go:

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

| Layer | What it stores | How it's queried | Persistence |
|------|---------------|-------------------|-------------|
| **📡 Continuum** | Multi-agent knowledge, agent registry | SQL queries | `continuum.db` — shared across agents |
| **🧠 Notion** | Permanent pages, project plans (optional) | Notion API | Cloud — human-viewable |
| **📝 Obsidian** | Local Markdown notes, daily logs | File content search | Local filesystem |
| **📚 KB** | Articles, docs, long-form knowledge | FTS5 full-text search | Local SQLite |
| **🕸️ Code** | Function symbols, structs, interfaces | SQL LIKE search | Local SQLite |
| **💎 Facts** | Short facts, preferences, observations | BM25 FTS5 + optional embeddings | Local SQLite |
| **🔎 Blackbox** | Agent telemetry events, efficiency scores | SQL queries | Local SQLite |
| **⚡ Builtin** | Agent identity, user profile | In-memory load | Flat files |

Results from all layers are fused using **RRF (Reciprocal Rank Fusion)** — the same algorithm used by the Python version — giving you unified search results ranked by relevance across every memory layer.

### 🛠 Dynamic Tool System
| Tool | Description |
|------|-------------|
| `bash` | Execute shell commands with timeout and output limits |
| `read` / `write` / `edit` | Full file ops with **auto-backup** (timestamped `.bak`, rotation keeps 5) |
| `ls` | Directory listing with metadata |
| `grep` | Pattern search with regex, file type filter (**ugrep** 7.5.0 — fuzzy `-Z`, archives `-z`, JSON/CSV output, `--bool`, smart case `-S`) |
| `web_search` | DuckDuckGo search (with fallback endpoints + timeout prediction) |
| `web_fetch` | URL content fetch via curl (preflight probe + adaptive max-time) |
| `register_tool` | Dynamically create new bash-wrapping tools (use `type=skill` for skills) |
| `create_backup` | Timestamped ZIP backups |
| `semantic_search` | Meaning-based vector search over indexed content |
| `semantic_index` | Add content to the search index |
| `codebase-intelligence` | Meta-skill orchestrating analysis tools |
| `eling_setup` | Configure provider/API key/model from within conversation |
| `ocr_review` / `ocr_scan` / `ocr_health` | Alibaba Open Code Review integration |

### 🖥 Terminal UI (TUI)
- **3-panel layout**: Header (status bar), Body (scrollable conversation), Input
- **Scrolling marquee banner**: Pink animated ticker at the top (`✦ ELING — Auto-Learning Evolving AI Agent ✦`) scrolls continuously
- **Syntax-highlighted** tool output with color-coded status (Catppuccin Mocha theme)
- **Thinking indicators**: Reasoning text, tool call progress, elapsed timers
- **Active tool tracking**: See running commands with live timers
- **Scrollable history**: Viewport with pgup/pgdn
- **Paste-safe input**: Multi-line pastes are held in the input box — newlines never auto-send (bracketed-paste + burst detection)
- **Plan Mode UI**: Drafted plans render as a checklist with **y = approve / n = reject / Esc = skip** (no tools run before approval)
- **Session-aware**: Displays current session, token usage, memory stats

### 📋 Session Management
- Named sessions with save/resume (`--resume`, `--last`, `--session-name`)
- Auto-save every 5 minutes and on graceful shutdown
- Conversation summary compression for long-running sessions
- Interruption recovery: Ctrl+C saves context, never loses your query
- Session listing with metadata (timestamps, token counts)

### 🔌 MCP Protocol
- Full **Model Context Protocol** client (JSON-RPC 2.0 over stdio)
- Connect external tool servers: filesystem, database, custom tools
- Dynamic tool discovery from MCP servers
- Configurable in `config.yaml`

### 📦 Open Code Review (OCR)
- Integrated [Alibaba Open Code Review](https://github.com/alibaba/open-code-review)
- `ocr_review` — Review git diffs, commits, or branch ranges
- `ocr_scan` — Full-file scan of entire directories
- `ocr_health` — Check CLI version and LLM connection
- Structured JSON output with line-level findings

### 🛡 Safety & Reliability
- **Panic recovery**: All tool execution and agent operations are panic-safe
- **Crash detection**: Detects previous crashes on startup (SIGBUS, SIGSEGV)
- **PID file management**: Single-instance enforcement with graceful kill
- **Tool output limits**: 512 KiB cap per command, 256 KiB per tool result
- **UTF-8 safe**: Rune-aware truncation prevents splitting multi-byte chars
- **Auto-backup before write/edit**: Every `write`/`edit` snapshots the existing file to `*.bak.<timestamp>` (rotation keeps the last 5; configurable via `ELING_BACKUP_DIR` / `ELING_BACKUP_KEEP`)
- **Web timeout prediction**: `web_fetch`/`web_search` do a fast DNS+TCP preflight probe (dead hosts fail in ~1.5s) and adapt `--max-time` per host based on observed latency/failure history
- **Graceful shutdown**: Saves state on SIGTERM, SIGINT
- **Auto-save**: Periodic state persistence (configurable)
- **Fatal signal handler**: Catches SIGBUS/SIGSEGV for crash reporting

---

## 🚀 Quick Start

### Prerequisites
- **Go 1.21+** (1.25 recommended)
- **An API key** from DeepSeek, OpenAI, or any compatible provider

### Install & Run

```bash
# Clone and build
git clone https://github.com/yourusername/eling.git
cd eling
go build -o eling .

# Set your API key
export DEEPSEEK_API_KEY="sk-your-key-here"

# Run the TUI
./eling
```

### First Run Wizard

ELING's interactive setup wizard can be launched three ways — they are all **the same wizard**:

```bash
./eling setup          # Same as eling-wizard (delegates to eling-wizard.sh)
./eling-wizard.sh      # The interactive setup wizard
./eling-setup          # Alias with the same interactive flow
```

`eling setup` runs the exact same wizard as `eling-wizard` (icons, banner,
review step, connection test). Recommended flow — **choose provider first, then enter the API key**:

```bash
./eling setup
```

The interactive flow walks through, in order:
1. **Provider** — pick from a menu (opencode-zen, opencode-zen-free, deepseek-direct, openai, groq, tokenrouter, or custom)
2. **API Key** — enter the key for the provider you just selected (with a provider-specific hint for where to get one)
3. **Model / Base URL** — confirm or override the provider defaults
4. **System prompt** and **max context** (agent-level settings)
5. **Review** — verify everything before saving
6. **Test** — optional live API connection check

> 💡 `eling setup` **delegates to `eling-wizard.sh`** whenever it's found — all three entry points
> (`./eling setup`, `./eling-wizard.sh`, `./eling-setup`) run the identical interactive flow.
> Extended flags the wizard doesn't support (`--add-provider`, `--test`, `--dedupe`) fall through
> to the built-in Go implementation in `internal/cli/setup.go`.

Non-interactive / quick setup (same flags for both commands):

```bash
./eling setup --list                          # View current config
./eling setup --quick --provider openai --api-key "sk-..." --model gpt-4o
./eling-wizard.sh --quick --provider groq --model llama-3.3-70b --api-key "gsk-..."
```

Add an extra provider without touching the current config (extended flag, built-in setup):

```bash
./eling setup --add-provider   # interactive: provider first, then its API key
# or fully non-interactive:
./eling setup --add-provider --provider groq --model llama-3.3-70b --api-key "gsk-..." --base-url "https://api.groq.com/openai/v1"
```

Other extended flags:

```bash
./eling setup --test            # Live API connection check after saving
./eling setup --dedupe          # Remove exact-duplicate providers (same model + base_url + api_key)
./eling setup --list            # Verify what's currently configured
```

Or configure via command line:

```bash
./eling --api-key "sk-..." --model "deepseek-v4-flash"
```

---

## 📖 Usage Guide

### Command-Line Flags

```bash
# Basic
./eling                                           # Interactive TUI mode
./eling --run "list all files in current dir"     # Non-interactive (single query)
./eling --version                                 # Show version

# API & Model
./eling --api-key "sk-..."                        # Set API key
./eling --model "gpt-4o"                          # Override model

# Sessions
./eling --resume my_session                       # Resume a named session
./eling --last                                    # Resume most recent session
./eling --session-name "code-review-2025"         # Name current session
./eling --list-sessions                           # List all saved sessions

# Config
./eling --config /path/to/config.yaml             # Use custom config

# Plan Mode (draft + approve before tools execute)
./eling --plan "deploy the service"               # Draft a plan, wait for approval, then execute
./eling --plan                                    # Enable plan mode for the whole session
```

### TUI Commands

| Command | Description |
|---------|-------------|
| `/help` | Show all available commands |
| `/stats` | Agent statistics (conversations, skills, memory, tokens) |
| `/tools` | List all registered tools |
| `/skills` | Show learned skills |
| `/memory` | Show recent memories |
| `/recall <query>` | Search memories by keyword |
| `/session` | Show current session info |
| `/save` | Save state immediately |
| `/sessions` | List all saved sessions |
| `/resume <name>` | Resume a saved session |
| `/providers` | List configured providers |
| `/provider <name>` | Switch to a different provider |
| `/plan` | Toggle plan mode (draft + approve before tools execute) |
| `/mcp` | Show MCP server status |
| `/mcp_connect <name> <cmd>` | Connect an MCP server |
| `/evolve` | Trigger evolution/self-improvement |
| `/config` | Show current configuration |
| `/clear` | Clear the screen |
| `/quit` | Exit |

### Direct Tool Invocation

Type tool calls directly in the input box:

```
bash(cmd=ls -la)
read(path=main.go, max_lines=50)
web_search(query=Go programming best practices)
grep(query=function, path=./internal, type=go)
write(path=hello.txt, content=Hello World)
edit(path=file.go, old_string=old code, new_string=new code)
```

### Keyboard Shortcuts

| Key | Action |
|-----|--------|
| `Enter` | Send message |
| `Alt+Enter` | New line in input |
| `Paste` | Multi-line pasted text is **held** in the input box — newlines inside a paste never auto-send; press `Enter` afterwards to send |
| `PgUp` / `PgDn` | Scroll conversation |
| `↑` / `↓` | Input history navigation |
| `Ctrl+C` | Interrupt current response (safe — context preserved) |
| `Ctrl+D` | Exit |

> 💡 **Paste safety:** ELING detects paste bursts (both bracketed-paste mode and
> plain terminals). While pasting, `Enter` inserts a newline instead of
> submitting, so you can paste long multi-line text/code, review it in the
> input box, and only send it when you deliberately press `Enter`. A hint line
> above the input shows `pasting… newlines are held` during the paste and
> `multiline input — Enter to send` afterwards.

---

## 📂 Project Structure

```
eling/
├── main.go                        # Entry point — CLI flags, signal handling, crash detection
├── go.mod / go.sum                # Go module dependencies
├── README.md                      # This file
├── DESIGN.md                      # Architecture design overview
├── eling-wizard.sh                # Interactive setup wizard
├── eling-setup                    # Setup alias (same interactive flow)
├── start.sh                       # Launcher with OS signal trapping (SIGBUS/SIGSEGV crash reports)
├── rebuild.sh                     # Atomic rebuild (mv not cp — safe on overlayfs/proot)
├── kill-eling.sh                  # Graceful shutdown helper
├── docs/
│   ├── README.md                  # Documentation index
│   ├── ARCHITECTURE.md            # Full system architecture (564 lines)
│   ├── API.md                     # Config schema, CLI flags, provider compatibility
│   ├── DEVELOPMENT.md             # Developer guide
│   ├── TOOLS.md                   # Complete tool reference (20+ tools)
│   ├── QUICK_WINS.md              # Recent improvements log
│   └── hermes-skills-adaptation.md
├── internal/
│   ├── agent/
│   │   ├── agent.go               # Core agent: Ask, streaming, tool loop, autoLearn, sessions
│   │   ├── memory.go              # Memory: short/long-term, recall, FactsLayer-backed decay
│   │   └── memory_test.go         # Memory unit tests
│   ├── cli/
│   │   ├── cli.go                 # CLI subcommand handling
│   │   └── setup.go               # Built-in setup wizard (--add-provider, --test, delegation)
│   ├── config/
│   │   └── config.go              # YAML config loader, defaults, provider config
│   ├── layers/                    # 🧠 8-Layer Memory Architecture (RRF fusion)
│   │   ├── layers.go              # Layer interface, Brain orchestrator, RRF fusion
│   │   ├── builtin.go             # Layer 1: MEMORY.md / USER.md (always-on)
│   │   ├── blackbox.go            # Layer 2: Flight recorder + 11-metric scoring
│   │   ├── facts.go               # Layer 3: SQLite + BM25 hybrid with trust scoring
│   │   ├── code.go                # Layer 4: Codegraph symbol intelligence
│   │   ├── kb.go                  # Layer 5: FTS5 knowledge corpus
│   │   ├── obsidian.go            # Layer 6: Local Markdown vault access
│   │   ├── notion.go              # Layer 7: Notion API sync (optional)
│   │   ├── continuum.go           # Layer 8: Multi-agent orchestration hub
│   │   ├── hooks.go               # BrainQuery hook interface (semantic search integration)
│   │   ├── think.go               # HRR reasoning engine
│   │   ├── privacy.go             # Privacy filtering for memory layers
│   │   ├── rules.go               # Rule-based memory filtering
│   │   ├── snapshot.go            # Brain state snapshots
│   │   ├── spec_kit.go            # Specification toolkit
│   │   └── verify_on_stop.go      # Stop-time verification
│   ├── logger/
│   │   ├── logger.go              # Crash-safe logger, signal handling, crash reports
│   │   └── logger_test.go
│   ├── markdownify/
│   │   └── markdownify.go         # HTML/document to Markdown converter
│   ├── mcp/
│   │   ├── mcp.go                 # MCP client (JSON-RPC 2.0 stdio), server manager
│   │   ├── skill/
│   │   │   └── skill.go           # MCP skill tool (package mcpskill)
│   │   └── srv/
│   │       └── server.go          # MCP server implementation
│   ├── provider/
│   │   ├── deepseek.go            # Multi-provider LLM client, retry, fallback, key rotation
│   │   └── rotation_test.go       # Key rotation tests
│   ├── session/
│   │   └── session.go             # Session save/resume, manager, metadata
│   ├── tools/
│   │   ├── registry.go            # Dynamic tool registry (thread-safe, category-aware)
│   │   ├── bash.go                # Shell execution with timeout + output limits
│   │   ├── files.go               # read / write / edit / grep / ls (+ auto-backup before write/edit)
│   │   ├── web.go                 # web_search + web_fetch (curl-based, fallback)
│   │   ├── web_timeout.go         # fetchPredictor: preflight probe + adaptive max-time per host
│   │   ├── register.go            # Dynamic tool/skill registration
│   │   ├── backup.go              # create_backup + codebase-intelligence
│   │   ├── schema.go              # JSON parameter schemas for all tools
│   │   ├── semantic.go            # Semantic search (BrainQuery + local trigram)
│   │   ├── setup.go               # eling_setup tool (config management)
│   │   ├── ocr.go                 # Open Code Review integration
│   │   ├── files_backup_test.go   # Auto-backup rotation tests
│   │   ├── web_timeout_test.go    # Timeout predictor tests
│   │   └── register_test.go       # Registration tests
│   └── tui/
│       ├── tui.go                 # 3-panel Bubbletea TUI (marquee banner, paste-safe input)
│       └── paste_test.go          # Paste-burst detection tests
├── skills/
│   └── hermes/                    # Community skill scripts (hermes skills)
│       ├── deep-web-research.sh
│       ├── interactive-prompt-analyzer.sh
│       └── ...
└── scripts/
    ├── install-hermes-skills.sh
    └── agent-integration/         # Agent integration tools
```

---

## ⚙️ Configuration

Full YAML config at `~/.eling/config.yaml` (auto-created on first run):

```yaml
agent:
  default_model: "deepseek-v4-flash-free"
  default_base_url: "https://opencode.ai/zen/v1"
  system_prompt: "You are ELING, an auto-learning evolving AI agent..."
  max_context: 32768              # Token budget for context window
  max_turn_rounds: 0              # Max tool rounds (0 = unlimited)
  max_turn_duration: 0            # Wall-clock timeout (0 = no timeout)
  max_turn_duration_retries: 2   # Retries on timeout
  auto_test: true                 # Auto-run go test on touched files
  learn_from_exchange: true       # LLM-based skill learning (autoLearn)
  providers:
    - name: "opencode-zen"
      model: "deepseek-v4-flash"
      base_url: "https://opencode.ai/zen/v1"
      backup_keys: []             # Additional keys for rotation
      max_retries: 5
      base_delay_sec: 2
      max_delay_sec: 60
      max_budget_sec: 360

ui:
  theme: "default"                # Catppuccin Mocha
  show_memory: true
  show_thinking: true
  verbose_tool_output: true
  max_messages: 500
  timezone: "Local"

memory:
  max_short_term: 50
  max_long_term: 1000
  decay_rate: 0.01                # Per-tick strength decay

mcp:
  enabled: false
  servers: []

session:
  auto_save: true
  save_dir: "~/.eling/sessions"
```

All state is persisted to `~/.eling/`:
- `config.yaml` — Configuration
- `memory.json` — Memory items (short + long term)
- `skills.json` — Learned skills
- `evolutions.json` — Evolution history
- `summary.txt` — Compressed conversation summary
- `tools.json` — Dynamic tool registrations
- `turn_timeout_history.json` — Self-adaptive timeout data
- `sessions/` — Saved conversation sessions
- `*-brain.db` — Brain layer databases (facts, code, kb, blackbox, continuum)
- `eling.log` — Application log
- `crash_report.log` — Panic/bus error crash reports
- `eling.pid` — PID file for single-instance enforcement

---

## 🔧 Provider Setup

### DeepSeek
```bash
export DEEPSEEK_API_KEY="sk-..."
./eling --model "deepseek-chat"
```

### OpenAI
```yaml
# ~/.eling/config.yaml
agent:
  providers:
    - name: "openai"
      model: "gpt-4o"
      base_url: "https://api.openai.com/v1"
```

### Groq (Llama 3, Mixtral)
```yaml
agent:
  providers:
    - name: "groq"
      model: "llama-3.3-70b-versatile"
      base_url: "https://api.groq.com/openai/v1"
```

### OpenRouter
```yaml
agent:
  providers:
    - name: "openrouter"
      model: "anthropic/claude-3.5-sonnet"
      base_url: "https://openrouter.ai/api/v1"
```

### TokenRouter (Kimi K3, many models via one API)
```yaml
agent:
  providers:
    - name: "tokenrouter"
      model: "deepseek/deepseek-v4-flash"
      base_url: "https://api.tokenrouter.com/v1"
```
Grab a free API key at [tokenrouter.com](https://tokenrouter.com), then configure via the setup wizard:
```bash
./eling-wizard.sh          # select "9) TokenRouter"
# or non-interactive:
./eling-wizard.sh --quick --provider tokenrouter --api-key "sk-..." --model "deepseek/deepseek-v4-flash" --base-url "https://api.tokenrouter.com/v1"
# or via eling-setup:
./eling-setup --add-provider --provider tokenrouter --model deepseek/deepseek-v4-flash --base-url https://api.tokenrouter.com/v1 --api-key "sk-..."
```

### Multiple Providers (Automatic Fallback)
```yaml
agent:
  providers:
    - name: "primary"
      model: "deepseek-v4-flash"
      base_url: "https://api.deepseek.com"
    - name: "fallback"
      model: "gpt-4o-mini"
      base_url: "https://api.openai.com/v1"
```

When the primary provider fails with rate limits or transient errors, ELING **automatically falls back** to the next provider in the list.

### API Key Rotation
Configure multiple keys per provider — ELING rotates through them on auth failures:
```yaml
agent:
  providers:
    - name: "deepseek"
      model: "deepseek-chat"
      base_url: "https://api.deepseek.com"
      backup_keys:
        - "sk-backup-key-1"
        - "sk-backup-key-2"
```

---

## 🔌 MCP Integration

Connect external tool servers via the [Model Context Protocol](https://modelcontextprotocol.io):

```bash
/mcp_connect filesystem npx -y @anthropic/mcp-server-filesystem /path/to/files
/mcp_connect database npx -y @anthropic/mcp-server-postgres postgresql://...
```

Or configure in `config.yaml`:
```yaml
mcp:
  enabled: true
  servers:
    - name: "filesystem"
      command: "npx"
      args: ["-y", "@anthropic/mcp-server-filesystem", "/path"]
```

---

## 📦 Open Code Review

ELING integrates [Alibaba Open Code Review](https://github.com/alibaba/open-code-review) (OCR):

```bash
# Install the CLI
npm install -g @alibaba-group/open-code-review

# From ELING TUI — just type:
ocr_review(preview=true)                       # See what would be reviewed
ocr_review(repo=".", background="fix bugs")    # Full review of workspace
ocr_scan(path="./src")                         # Full-file scan
ocr_health                                     # Check status
```

---

## 🏗 Architecture Overview

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
│  │  Web Timeout Prediction | Auto-Backup (write/edit)  │ │
│  │  DeepSeek reasoning_content persistence             │ │
│  └─────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────┘
```

### Key Architecture Decisions

1. **Thread-safe by design**: `sync.RWMutex` on Agent, `sync.Mutex` on Memory, `sync.RWMutex` on Registry — consistent locking hierarchy prevents deadlocks
2. **Panic-safe everywhere**: All tool execution, state saving, and conversation handling has `recover()` with crash logging
3. **Bounded memory**: Tool output capped at 512 KiB, tool results at 256 KiB, context window managed by token budget
4. **Self-healing**: Auto-retry with exponential backoff, provider fallback, key rotation, adaptive timeout
5. **No context loss**: Ctrl+C saves conversation state; interrupted prompts are preserved
6. **8-Layer Brain with RRF fusion**: All memory searches query every layer and fuse results using Reciprocal Rank Fusion — the same algorithm adapted from the Python eling
7. **Unified skill management**: `ListSkills()` returns `[]tools.Tool` from the ToolRegistry (`category:"skill"`) — no separate `SkillManager`
8. **No duplicate memory decay**: FactsLayer.ApplyDecay() handles all memory decay; the old in-memory `StartDecay()`/`StopDecay()` were removed during consolidation
9. **Usage-based skill eviction**: Skills track their `UsedCount` and are evicted by lowest usage (not just age) when the 100-skill cap is reached
10. **Auto-backup before every mutation**: `write`/`edit` snapshot the original file (`*.bak.<timestamp>`) with rotation (last 5) — no more lost code on bad edits; configurable via `ELING_BACKUP_DIR` / `ELING_BACKUP_KEEP`
11. **Timeout prediction for web tools**: `fetchPredictor` runs a fast DNS+TCP preflight (dead hosts fail in ~1.5s) and adapts curl `--max-time` per host from recorded latency/failure history — slow or dead hosts can no longer hang the agent
12. **Reasoning-content persistence**: DeepSeek `reasoning_content` is stored with assistant messages and passed back on tool-loop follow-ups and session resume (DeepSeek thinking mode rejects assistant messages that omit it)
13. **Stale-message guard in TUI**: generation counters (`genMsg`) discard messages from old goroutines after a new query is submitted — no more late tool results bleeding into the wrong conversation

---

## 🧪 Tech Stack

| Component | Library | Version |
|-----------|---------|---------|
| **Language** | Go | 1.25 |
| **TUI Framework** | [Bubbletea](https://github.com/charmbracelet/bubbletea) | v1.3.10 |
| **UI Components** | [Bubbles](https://github.com/charmbracelet/bubbles) | v1.0.0 |
| **Styling** | [Lipgloss](https://github.com/charmbracelet/lipgloss) | v1.1.0 |
| **Search** | [ugrep](https://github.com/Genivia/ugrep) | 7.5.0 (all `grep` calls — fuzzy, archives, JSON/CSV, `--bool`) |
| **Config** | [yaml.v3](https://pkg.go.dev/gopkg.in/yaml.v3) | v3.0.1 |
| **Spinner** | [spinner](https://github.com/briandowns/spinner) | v1.23.2 |
| **LLM Provider** | Any OpenAI-compatible API | — |
| **MCP Protocol** | JSON-RPC 2.0 over stdio | — |
| **OCR** | [@alibaba-group/open-code-review](https://github.com/alibaba/open-code-review) | v1.8.0 |

---

## 🔬 Benchmarking

ELING includes a built-in benchmark suite:

```bash
cd eling
go test ./internal/benchmark/ -v -bench=.
```

Benchmarks measure:
- Response quality (BLEU, ROUGE, BERTScore concepts)
- Tool execution accuracy
- Memory recall precision/recall
- Session save/resume reliability
- Multi-turn conversation coherence

---

## 🤝 Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing`)
3. Run tests (`go test ./...`)
4. Build (`go build -o eling .`)
5. Commit with clear messages
6. Open a Pull Request

### Development Commands

```bash
go build -o eling .              # Build
go run .                         # Run without building
go test ./...                    # Run all tests
go test -v ./internal/agent/     # Test specific package
go vet ./...                     # Static analysis
```

---

## 📜 License

MIT License — see [LICENSE](LICENSE) for details.

---

## 🙏 Acknowledgments

- [jcode](https://github.com/1jehuang/jcode) — The next generation coding agent harness (primary inspiration)
- [Eling (PatrickNoFilter)](https://github.com/PatrickNoFilter/eling) — 8-layer memory architecture, Blackbox flight recorder, HRR reasoning, Facts layer with BM25 hybrid, Obsidian vault access, Notion sync, Continuum multi-agent orchestration, and document-to-Markdown (adapted to Go)
- [Alibaba Open Code Review](https://github.com/alibaba/open-code-review) — Code review integration
- [Charmbracelet](https://charm.sh/) — Bubbletea, Bubbles, Lipgloss (beautiful TUI)
- [DeepSeek](https://deepseek.com/) — LLM API provider
