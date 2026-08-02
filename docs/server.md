# Server Subsystem — `internal/server`

The **daemon / ACP mode**: exposes a long-running ELING agent over HTTP+SSE so
any client (curl, the TUI via `--daemon-url`, another device on the LAN) can
talk to the same brain. Activated with `eling serve` (or `--server`).

## Files

| File | Purpose |
|------|---------|
| `server.go` | `Server`, `NewServer`, HTTP+SSE handlers |
| `server_test.go` | Daemon tests (fake upstream via injectable `AgentFactory`) |

## Design

- **One `*agent.Agent` per `session_id`** — sequential chats to the same id
  continue the same in-memory conversation (agent holds message history).
- **Per-session run lock** (`sessionAgent.runMu`) — serializes `AskStream`
  turns so two concurrent HTTP requests for the same session can never
  interleave tool execution or session-history writes.
- **Auth** — `Authorization: Bearer <token>`. Empty token = loopback-only,
  no auth (Termux-friendly default).
- **SSE events** — `message` (delta), `tool_call`, `done` (final text),
  `error`.
- **Injectable `AgentFactory`** — tests substitute a fake upstream provider
  (httptest) without real credentials.

## Endpoints (typical)

- `POST /chat` (or `/v1/chat`) — submit a prompt; streams SSE events back
- `/health` — version + liveness (uses the `version` string)
- `/stats` — agent stats (A5: tool success-rate, latency, provider calls)

## Related

- [`agent.md`](./agent.md) — the agent each session wraps
- [`session.md`](./session.md) — per-session persistence
- [`tui.md`](./tui.md) — remote TUI via `--daemon-url`
