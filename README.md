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
| **Long-Term Memory** | Persistent store with strength decay and automatic forgetting |
| **Semantic Search** | Vector embedding-based meaning search (OpenAI-compatible embeddings) |
| **Auto-Learning** | Extracts patterns from interactions; learns reusable skills |
| **LLM Skill Learning** | `learnFromExchange` — LLM judges if a response is worth memorizing |
| **Conversation Indexing** | Every turn saved to semantic index for future recall |
| **Memory Decay** | Unused memories weaken over time and are pruned |

### 🛠 Dynamic Tool System
| Tool | Description |
|------|-------------|
| `bash` | Execute shell commands with timeout and output limits |
| `read` / `write` / `edit` | Full file operations |
| `ls` | Directory listing with metadata |
| `grep` | Pattern search with regex, file type filter |
| `web_search` | DuckDuckGo search (with fallback endpoints) |
| `web_fetch` | URL content fetch via curl |
| `register_tool` | Dynamically create new bash-wrapping tools at runtime |
| `register_skill` | Register named skills/plugins |
| `create_backup` | Timestamped ZIP backups |
| `semantic_search` | Meaning-based vector search over indexed content |
| `semantic_index` | Add content to the search index |
| `codebase-intelligence` | Meta-skill orchestrating analysis tools |
| `eling_setup` | Configure provider/API key/model from within conversation |
| `ocr_review` / `ocr_scan` / `ocr_health` | Alibaba Open Code Review integration |

### 🖥 Terminal UI (TUI)
- **3-panel layout**: Header (status bar), Body (scrollable conversation), Input
- **Syntax-highlighted** tool output with color-coded status (Catppuccin Mocha theme)
- **Thinking indicators**: Reasoning text, tool call progress, elapsed timers
- **Active tool tracking**: See running commands with live timers
- **Scrollable history**: Viewport with pgup/pgdn
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

ELING has an interactive setup wizard:

```bash
./eling-wizard.sh
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
| `PgUp` / `PgDn` | Scroll conversation |
| `↑` / `↓` | Input history navigation |
| `Ctrl+C` | Interrupt current response (safe — context preserved) |
| `Ctrl+D` | Exit |

---

## 📂 Project Structure

```
eling/
├── main.go                        # Entry point, CLI flags, crash handling
├── go.mod / go.sum                # Go module dependencies
├── README.md                      # This file
├── DESIGN.md                      # Architecture design overview
├── eling-wizard.sh                # Interactive setup wizard
├── start.sh                       # Quick start script
├── test_rotation.sh               # API key rotation test
├── docs/
│   ├── README.md                  # Documentation index
│   ├── QUICK_WINS.md              # Recent improvements log
│   └── hermes-skills-adaptation.md
├── internal/
│   ├── agent/
│   │   ├── agent.go               # Core agent: Ask, streaming, tool loop, auto-learn, sessions
│   │   ├── memory.go              # Memory system: short/long-term, strength decay, recall
│   │   ├── memory_test.go         # Memory tests
│   │   └── save_conversation_test.go
│   ├── config/
│   │   └── config.go              # YAML config loader, default config, provider config
│   ├── logger/
│   │   ├── logger.go              # Crash-safe logger, signal handling, crash reports
│   │   └── logger_test.go
│   ├── mcp/
│   │   └── mcp.go                 # MCP client (JSON-RPC 2.0 stdio), server manager
│   ├── provider/
│   │   ├── deepseek.go            # Multi-provider LLM client, retry, fallback, rotation
│   │   └── rotation_test.go       # Key rotation tests
│   ├── session/
│   │   └── session.go             # Session save/resume, manager, metadata
│   ├── skills/
│   │   ├── skills.go              # Plugin/skill manager (legacy)
│   │   └── skills_test.go
│   ├── tools/
│   │   ├── registry.go            # Dynamic tool registry (thread-safe)
│   │   ├── bash.go                # Bash execution with timeout
│   │   ├── files.go               # read/write/edit/grep/ls
│   │   ├── web.go                 # web_search + web_fetch (curl-based)
│   │   ├── register.go            # Dynamic tool/skill registration
│   │   ├── backup.go              # create_backup + codebase-intelligence
│   │   ├── schema.go              # JSON parameter schemas for all tools
│   │   ├── semantic.go            # Vector embedding search engine
│   │   ├── setup.go               # eling_setup tool (config management)
│   │   ├── ocr.go                 # Open Code Review integration
│   │   ├── register_test.go
│   │   └── rotation.go            # API key rotation tool (deprecated)
│   └── tui/
│       └── tui.go                 # 3-panel Bubbletea TUI (1344 lines)
├── skills/
│   └── hermes/                    # Community skill scripts
│       ├── deep-web-research.sh
│       ├── interactive-prompt-analyzer.sh
│       └── ...
└── scripts/
    └── install-hermes-skills.sh
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
  learn_from_exchange: true       # LLM-based skill learning
  save_conversation: true         # Index every turn to semantic search
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
- `semantic_index.json` — Vector embeddings for semantic search
- `turn_timeout_history.json` — Self-adaptive timeout data
- `sessions/` — Saved conversation sessions
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
│  │  HEADER: Model | Session | Tokens | Mem% | MCP   │    │
│  ├──────────────────────────────────────────────────┤    │
│  │  BODY: Scrollable conversation log + tool output │    │
│  ├──────────────────────────────────────────────────┤    │
│  │  INPUT: > text entry · tool(args) · /command    │    │
│  └──────────────────────────────────────────────────┘    │
└──────────────────────┬───────────────────────────────────┘
                       │
┌──────────────────────▼───────────────────────────────────┐
│                    Agent Core (Go)                        │
│  ┌──────────┬──────────────┬────────────────┬──────────┐ │
│  │Provider  │ Memory       │ Tool Registry  │ Sessions │ │
│  │Manager   │ Short+Long   │ (Dynamic,      │ Save/    │ │
│  │(Multi)   │ Term, Decay, │ Thread-safe)   │ Resume   │ │
│  │          │ Embeddings   │ 22+ tools      │          │ │
│  ├──────────┴──────────────┴────────────────┴──────────┤ │
│  │  MCP Client (JSON-RPC 2.0) | Config (YAML)          │ │
│  │  Auto-Learning | Evolution | Crash Recovery         │ │
│  │  Self-Adaptive Timeout | Key Rotation               │ │
│  └─────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────┘
```

### Key Architecture Decisions

1. **Thread-safe by design**: `sync.RWMutex` on Agent, `sync.Mutex` on Memory, `sync.RWMutex` on Registry — consistent locking hierarchy prevents deadlocks
2. **Panic-safe everywhere**: All tool execution, state saving, and conversation handling has `recover()` with crash logging
3. **Bounded memory**: Tool output capped at 512 KiB, tool results at 256 KiB, context window managed by token budget
4. **Self-healing**: Auto-retry with exponential backoff, provider fallback, key rotation, adaptive timeout
5. **No context loss**: Ctrl+C saves conversation state; interrupted prompts are preserved

---

## 🧪 Tech Stack

| Component | Library | Version |
|-----------|---------|---------|
| **Language** | Go | 1.25 |
| **TUI Framework** | [Bubbletea](https://github.com/charmbracelet/bubbletea) | v1.3.10 |
| **UI Components** | [Bubbles](https://github.com/charmbracelet/bubbles) | v1.0.0 |
| **Styling** | [Lipgloss](https://github.com/charmbracelet/lipgloss) | v1.1.0 |
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
- [Alibaba Open Code Review](https://github.com/alibaba/open-code-review) — Code review integration
- [Charmbracelet](https://charm.sh/) — Bubbletea, Bubbles, Lipgloss (beautiful TUI)
- [DeepSeek](https://deepseek.com/) — LLM API provider
