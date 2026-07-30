# ELING Configuration & API Reference

## Configuration File

Location: `~/.eling/config.yaml` (auto-created, customizable via `ELING_CONFIG` env var or `--config` flag)

### Full Schema

```yaml
agent:
  # ── Model Settings ──
  default_model: "deepseek-v4-flash-free"      # Default LLM model
  default_base_url: "https://opencode.ai/zen/v1" # Default API base URL
  system_prompt: "You are ELING..."             # System prompt for the AI

  # ── Context & Token Management ──
  max_context: 32768                            # Token budget for context window
  max_turn_rounds: 0                            # Max tool-call rounds (0 = unlimited)
  max_turn_duration: 0                          # Wall-clock timeout per turn in seconds (0 = no timeout)
  max_turn_duration_retries: 2                 # How many times to retry on timeout

  # ── Auto Features ──
  auto_test: true                               # Auto-run go test on touched files
  learn_from_exchange: true                     # LLM-based skill learning from conversations (maps to autoLearn())
  save_conversation: true                       # [Legacy] Conversation saving handled automatically by Brain's Facts layer

  # ── Providers ──
  providers:
    - name: "opencode-zen"                     # Provider name (any string)
      model: "deepseek-v4-flash"               # Model identifier
      base_url: "https://opencode.ai/zen/v1"   # API endpoint
      api_key: ""                              # API key (usually from env var)
      backup_keys: []                          # Additional keys for rotation
      # Optional retry config overrides:
      max_retries: 5                           # Max retries (default: 5)
      base_delay_sec: 2                        # Initial backoff in seconds (default: 2)
      max_delay_sec: 60                        # Max backoff in seconds (default: 60)
      max_budget_sec: 360                      # Total retry budget in seconds (default: 360)

ui:
  theme: "default"                             # UI theme
  show_memory: true                            # Show memory stats in header
  show_thinking: true                          # Show reasoning/thinking text
  verbose_tool_output: true                    # Show full tool args/results
  max_messages: 500                            # Max messages in viewport
  timezone: "Local"                            # Display timezone

memory:
  max_short_term: 50                           # Max short-term memory items
  max_long_term: 1000                          # Max long-term memory items
  decay_rate: 0.01                             # Per-tick strength decay (0.0-1.0)

mcp:
  enabled: false                               # Enable MCP servers
  servers: []                                  # List of MCP server configs
    # - name: "filesystem"
    #   command: "npx"
    #   args: ["-y", "@anthropic/mcp-server-filesystem", "/path"]
    #   env: {"KEY": "VALUE"}

session:
  auto_save: true                              # Auto-save every 5 minutes
  save_dir: "~/.eling/sessions"                # Session save directory
```

---

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `DEEPSEEK_API_KEY` | Primary API key for LLM providers | — |
| `ELING_CONFIG` | Path to config file | `~/.eling/config.yaml` |

---

## CLI Flags Reference

```
Usage: eling [flags]

Flags:
  --api-key <key>        DeepSeek API key (or set DEEPSEEK_API_KEY env var)
  --model <name>         Override the default model
  --config <path>        Path to config file
  --resume <name>        Resume a named session
  --last                 Resume the most recent session
  --list-sessions        List all saved sessions (alias: --sessions)
  --session-name <name>  Name the current session
  --run <query>          Run a single query non-interactively
  --version              Print version and exit
```

---

## Provider API Compatibility

ELING is compatible with any **OpenAI-compatible** API. This includes:

| Provider | Base URL | Notes |
|----------|----------|-------|
| **OpenAI** | `https://api.openai.com/v1` | Models: gpt-4o, gpt-4o-mini, gpt-4-turbo |
| **DeepSeek** | `https://api.deepseek.com` | Models: deepseek-chat, deepseek-v4-flash |
| **OpenCode** | `https://opencode.ai/zen/v1` | Models: deepseek-v4-flash-free |
| **Groq** | `https://api.groq.com/openai/v1` | Models: llama3-70b, mixtral-8x7b |
| **OpenRouter** | `https://openrouter.ai/api/v1` | Models: claude-3.5, gpt-4o, gemini-2.0 |
| **Together** | `https://api.together.xyz/v1` | Models: llama-3.3-70b, mixtral |
| **Anyscale** | `https://api.endpoints.anyscale.com/v1` | Models: llama-2, mixtral |
| **Fireworks** | `https://api.fireworks.ai/inference/v1` | Models: llama-3, mixtral |
| **Perplexity** | `https://api.perplexity.ai` | Models: sonar-pro, sonar-small |

### Required API Features
For full functionality, the provider must support:
1. **Chat completions** (`POST /v1/chat/completions`)
2. **Function/tool calling** (tools array in requests)
3. **Streaming** (SSE for real-time responses) — optional but recommended
4. **Embeddings** (`POST /v1/embeddings`) — for semantic search

---

## State Storage

All state is persisted under `~/.eling/`:

| File | Content | Format |
|------|---------|--------|
| `config.yaml` | Configuration | YAML |
| `memory.json` | Memory items (short + long term) | JSON |
| `skills.json` | Learned skills | JSON |
| `evolutions.json` | Evolution history | JSON |
| `summary.txt` | Compressed conversation summary | Text |
| `tools.json` | Dynamic tool registrations | JSON |
| `turn_timeout_history.json` | Self-adaptive timeout data | JSON |
| `*-brain.db` | Brain layer databases (facts, code, kb, blackbox, continuum) | SQLite |
| `sessions/*.json` | Saved conversation sessions | JSON |
| `eling.log` | Application log | Text |
| `crash_report.log` | Panic/bus error crash reports | Text |
| `eling.pid` | PID file (single-instance) | Text |

---

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success / clean exit |
| 1 | Error (config, API key, agent creation failure) |

---

## API Rate Limits & Retry Behavior

### Retry Strategy
```
Error Occurred
    ├── Non-retryable (400, 401, 403, 404, 422)
    │   └── Return immediately to user
    │
    ├── Rate limited (429)
    │   ├── Parse Retry-After header
    │   ├── Wait with jitter
    │   └── Retry up to MaxRetries
    │
    ├── Server error (500, 502, 503, 504)
    │   ├── Exponential backoff: 2s → 4s → 8s → ... → 60s max
    │   ├── +30% random jitter
    │   └── Retry up to MaxRetries
    │
    ├── Timeout (408)
    │   └── Same as server error
    │
    ├── Auth failure (401)
    │   ├── Rotate to next backup key
    │   └── Retry with new key
    │
    └── All retries exhausted
        └── Fall back to next provider (if available)
```

### Key Rotation Algorithm
```
Auth Error (401)
    │
    ├── Log failure for current key
    ├── Select next key from BackupKeys[] (round-robin)
    │   ├── Keys tried in order: [primary, backup1, backup2, ...]
    │   └── After all keys tried → return auth error
    │
    └── Retry request with new key
        ├── Success → continue (key works)
        └── Fail → try next key
```

---

## MCP Protocol Reference

### Supported Methods

| Method | Direction | Description |
|--------|-----------|-------------|
| `initialize` | Client → Server | Handshake and version negotiation |
| `tools/list` | Client → Server | List available tools |
| `tools/call` | Client → Server | Execute a tool |
| `resources/list` | Client → Server | List available resources |
| `resources/read` | Client → Server | Read a resource |
| `notifications/...` | Server → Client | Async notifications |

### JSON-RPC 2.0 Format

**Request:**
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/list",
  "params": {}
}
```

**Response:**
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "tools": [
      {
        "name": "read_file",
        "description": "Read file contents",
        "inputSchema": {
          "type": "object",
          "properties": {
            "path": { "type": "string" }
          },
          "required": ["path"]
        }
      }
    ]
  }
}
```

**Error Response:**
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "error": {
    "code": -32603,
    "message": "Internal error",
    "data": {}
  }
}
```

---

## Agent Callback Events

The agent emits `ToolCallEvent` callbacks during `Ask()` and `AskStream()` for UI rendering:

```go
type ToolCallEvent struct {
    SeqID      int                    // Sequential tool call ID
    Name       string                 // Tool name
    Args       map[string]interface{} // Tool arguments
    ResultText string                 // Tool result
    Error      string                 // Error message (if failed)
    Reasoning  string                 // Model's chain-of-thought text
    IsStart    bool                   // true = tool starting, false = completed
    IsThinking bool                   // true = model reasoning between rounds
}
```

---

## Embedding API

Used by `semantic_search` and `semantic_index` tools.

**Endpoint:** `POST /v1/embeddings` (same base URL as chat)

**Request:**
```json
{
  "model": "text-embedding-ada-002",
  "input": "text to embed"
}
```

**Response:**
```json
{
  "object": "list",
  "data": [
    {
      "object": "embedding",
      "index": 0,
      "embedding": [0.001, -0.002, ...]
    }
  ],
  "model": "text-embedding-ada-002"
}
```

**Caching:** Embeddings are cached in memory (up to 1000 entries) to reduce API calls.
**Persistence:** Semantic search queries the 8-layer Brain (SQLite-backed) via RRF fusion, with an in-memory local trigram fallback for offline operation.
