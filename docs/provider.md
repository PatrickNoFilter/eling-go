# Provider Subsystem — `internal/provider`

Manages the LLM providers ELING talks to: OpenAI-compatible chat completions,
streaming, retry/backoff, provider rotation/fallback, and DeepSeek-specific
reasoning content.

## Files

| File | Purpose |
|------|---------|
| `deepseek.go` | `Provider` (Chat/ChatStream), `Manager`, retry config |
| `rotation_test.go` | Provider rotation/fallback tests |

## Key Types

- **`Provider`** — one model endpoint. `provider.New(ProviderConfig{Name,
  Model, BaseURL, APIKey, BackupKeys})`. Methods:
  - `ChatStream(ctx, messages, onChunk, tools...)` → `(content, reasoning,
    toolCalls, err)` — streaming with tool-call support
  - `Chat(...)` — non-streaming
  - `GetRetryConfig()` / `SetRetryConfig(rc)` — per-provider retry tuning
- **`Manager`** — holds all providers, `AddProvider`, `SetDefault`, `Get`,
  `List`, fallback ordering via `Agent.getProvidersForFallback`.

## Retry & Fallback

- Per-provider retry config: `MaxRetries`, `BaseDelay` (jittered exponential
  backoff), `MaxDelay`, `MaxBudget`.
- `provider.IsRetryable(err)` decides whether a failure is retryable.
- `provider.RetryBudgetExceeded(err)` triggers provider **fallback** — the
  agent tries the next provider in rotation (e.g. primary busy → backup).
- The retry loop lives in `Agent.chatStreamWithRetry`
  (`internal/agent/agent.go`).

## DeepSeek Reasoning Content

DeepSeek thinking mode returns `reasoning_content`. The agent:

1. Persists it with the assistant session entry
   (`Session.AppendWithReasoning`).
2. Passes it back on resume — DeepSeek **rejects** assistant messages that
   omit it in thinking mode.
3. Handles custom JSON marshaling so reasoning survives round-trips
   (`deepseek-reasoning-content-handling` skill).

## A5 Metrics

Every `ChatStream` attempt is recorded by the agent (`recordProviderCall`) —
see [`agent.md`](./agent.md) → `provider_calls` in `GetStats()`.

## Related

- [`agent.md`](./agent.md) — provider usage + fallback loop
- [`config.md`](./config.md) — provider config schema
