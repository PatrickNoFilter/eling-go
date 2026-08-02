# Session Subsystem — `internal/session`

Conversation persistence: every turn is stored so ELING can **resume** past
conversations (`--resume`, `--last`, `--session-name`), including streaming
reasoning content and tool calls.

## Files

| File | Purpose |
|------|---------|
| `session.go` | `Session`, `Manager`, `Entry`, persistence |
| `session_test.go` | Tests |

## Key Types

- **`Session`** — one conversation: name, model, `Entries []Entry`, metadata.
  - `Entry` — `{Role, Content, Reasoning, ToolCalls, Tokens, Timestamp}`.
  - Reasoning stored so DeepSeek thinking mode survives resume.
- **`Manager`** — `NewManager(saveDir)`; methods:
  - `Create(name, model)` / `Get(name)` / `GetCopy(name)` (copy-safe reads)
  - `Append(name, role, content, toolCalls...)` /
    `AppendWithReasoning(name, role, content, reasoning, toolCalls...)`
  - `List()` / `Save(name)` / `LastEntry(name)` / `SetLastEntryTokens`
  - `GetEntriesCopy(name)` — safe snapshot for stats/history

## Stats

`Agent.GetStats().conversations` = `len(entries)/2` (user+assistant pairs);
`total_tokens_used` comes from `Metadata["total_tokens"]`.

## Thread Safety

`GetCopy` / `GetEntriesCopy` return deep clones so concurrent readers
(TUI stats, daemon) never race with the agent's writer.

## Related

- [`agent.md`](./agent.md) — `Agent.Sessions` usage, resume flow
- [`server.md`](./server.md) — per-session agents in daemon mode
