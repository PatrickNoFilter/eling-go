# LSP Subsystem — `internal/lsp`

Instant-diagnostics Language Server Protocol client that gives the agent
real-time compiler/analyzer feedback while editing code (Qwen-code steal,
Phase 3). Configured at agent boot via `lsp.Configure(...)` — best-effort:
missing server binaries are silently skipped.

## Files

| File | Purpose |
|------|---------|
| `lsp.go` | `Server`, `Manager`, diagnostics plumbing |
| `lsp_test.go` | Unit tests |
| `real_gopls_test.go` | Integration test against real `gopls` |
| `exec_helper_test.go` | Subprocess exec helper tests |

## Key Types

- **`Config`** — `{Enabled bool, Servers []ServerConfig}`;
  `DefaultConfig()` provides sensible defaults.
- **`Server`** — one LSP language server process:
  - `readLoop()` — reads JSON-RPC responses off the server
  - `send(method, params, wantReply)` — JSON-RPC request/response
  - `handleDiagnostics(params)` — routes `textDocument/publishDiagnostics`
  - `didOpenOrChange(path, content)` — push buffer contents to the server
  - `getDiagnostics(path)` — current diagnostics for a file
  - `stop()` — graceful shutdown
- **`Manager`** — `NewManager(cfg)`, `Enabled()`, `Diagnostics(path,
  content)` (blocks until diagnostics arrive or timeout), `serverFor(lang)`.

## How the Agent Uses It

After editing a file, the agent asks `Manager.Diagnostics(path, content)` for
compiler feedback and feeds it into the next model turn — catching errors
before the user sees them. Auto-test memoization (`autoTestCache`) prevents
re-running `go test` on every round.

## Related

- [`agent.md`](./agent.md) — boot wiring (`lsp.Configure`)
- [`tools.md`](./tools.md) — file edits trigger diagnostics
