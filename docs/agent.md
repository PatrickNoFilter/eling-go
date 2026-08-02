# Agent Subsystem — `internal/agent`

The **Agent** is ELING's conversational core. It owns providers, memory, brain
layers, tools, MCP, sessions, skills, and the turn lifecycle.

## Files

| File | Purpose |
|------|---------|
| `agent.go` | `Agent` struct, `New()`, `Ask`/`AskStream`, tool loop, provider fallback, stats |
| `memory.go` | Short/long-term memory (`Memory`, `NewMemory`) |
| `plan_test.go` | Plan-mode tests (Qwen-code steal Phase 2) |
| `autotest_test.go` | Auto-test memoization tests |
| `lsp_test.go` | LSP-in-agent integration tests |
| `sanitize_test.go` | Output sanitization tests |

## Key Types

- **`Agent`** — the main object. Created with `agent.New(cfg)`. Notable fields:
  - `providers *provider.Manager` — model provider rotation
  - `memory *Memory` — short/long-term memory
  - `Brain *layers.Brain` — 8-layer memory architecture
  - `ToolRegistry *tools.Registry` — **the global `tools.DefaultRegistry`**
  - `MCP *mcp.Manager`, `Sessions *session.Manager`
  - `skills []LearnedSkill`, `evolutions []Evolution` — auto-learned skills
  - `PlanEnabled atomic.Bool` — plan mode toggle (Qwen-code steal Phase 2)
  - `turnTimeoutHist []TurnTimeoutRecord` — self-adaptive timeout prediction
  - `providerStats map[string]*ProviderStat` — A5 per-provider call metrics

## Turn Lifecycle

1. **`Ask(ctx, prompt, onToolCall...)`** — non-streaming turn. Calls
   `chatWithRetry` (non-stream) or routes through the tool loop.
2. **`AskStream(ctx, prompt, onChunk, onToolCall...)`** — streaming turn;
   SSE-style deltas via `onChunk`, tool events via `onToolCall`.
3. **`runToolLoop` / `runStreamToolLoop`** — executes model-requested tool
   calls, auto-runs `go test` after file edits (memoized via `autoTestCache`),
   appends results, loops until the model answers.
4. **`chatStreamWithRetry`** — provider call with retry/backoff and provider
   fallback. Records per-provider metrics (A5) via `recordProviderCall`.

## Plan Mode (Phase 2)

When `PlanEnabled`, `Ask()` drafts a plan with tools stripped and waits for
approval via `PlanApprover` (TUI-attached callback; nil = auto-approve) before
executing tools.

## Stats (A5)

`GetStats()` returns brain + runtime metrics:

- `conversations`, `learned_skills`, `evolutions`, `memory_items`
- `tools_available`, `mcp_servers`, `model`, `session`, `token_budget`
- `tool_calls`, `tool_failures`, `tool_success_rate`, `tool_avg_latency_ms`
  (merged from `ToolRegistry.Stats()`)
- `provider_calls` — per-provider `{calls, failures, success_rate,
  avg_latency_ms, last_call}`

## Skills

See [`skills.md`](./skills.md). The auto-learn/forget engine lives here
(`LearnedSkill`, `maxSkills = 100` hard cap at `agent.go:2868`).

## Related

- [`provider.md`](./provider.md) — provider manager + retry
- [`session.md`](./session.md) — conversation persistence
- [`tools.md`](./tools.md) — tool registry & timeout strategy
