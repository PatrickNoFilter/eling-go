# ELING - Auto-Learning Evolving AI Agent

## Architecture Overview

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

### 2. TUI Layer (Bubbletea)
- **Header**: Status bar, model info, memory usage, MCP status
- **Body**: Scrollable viewport for conversation history and tool output
- **Input**: Bottom text entry with command handling

### 3. Agent Core (internal/agent/)
- Auto-learning: `autoLearn()` uses LLM to extract reusable skills from every exchange
- Evolving: Agent can register new tools and skills at runtime
- Self-reflection: Periodic conversation summarization for long context
- Turn timeout prediction: Self-adaptive based on history

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
