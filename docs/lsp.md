# LSP Subsystem — `internal/lsp`

Instant-diagnostics Language Server Protocol client that gives the agent
real-time compiler/analyzer feedback while editing code (Qwen-code steal,
Phase 3), plus **whole-project symbol rename (A6, oh-my-pi steal)** — the
`lsp_rename` tool. Configured at agent boot via `lsp.Configure(...)` —
best-effort: missing server binaries are silently skipped.

## Files

| File | Purpose |
|------|---------|
| `lsp.go` | `Server`, `Manager`, diagnostics + rename plumbing |
| `lsp_test.go` | Unit tests |
| `real_gopls_test.go` | Integration test against real `gopls` |
| `exec_helper_test.go` | Subprocess exec helper tests |
| `../tools/lsp_rename.go` | `lsp_rename` tool + applyEdit safety-net handler (A6) |

## Key Types

- **`Config`** — `{Enabled bool, Servers []ServerConfig}`;
  `DefaultConfig()` provides sensible defaults.
- **`Server`** — one LSP language server process:
  - `readLoop()` — reads JSON-RPC responses off the server; **acks
    server-initiated requests** (`respond`) so `workspace/applyEdit` never blocks
  - `send(method, params, wantReply)` — JSON-RPC request/response
  - `handleDiagnostics(params)` — routes `textDocument/publishDiagnostics`
  - `handleApplyEdit(params)` — routes `workspace/applyEdit` → `TextEdit`s →
    tools-layer hook (backup + lock safety net)
  - `didOpenOrChange(path, content)` — push buffer contents to the server
  - `getDiagnostics(path)` — current diagnostics for a file
  - `rename(path, line, col, newName)` — `textDocument/rename` → `[]TextEdit`
  - `stop()` — graceful shutdown
- **`Manager`** — `NewManager(cfg)`, `Enabled()`, `Diagnostics(path,
  content)` (blocks until diagnostics arrive or timeout), `Rename(path, line,
  col, newName)` (0-based LSP positions, UTF-16 columns), `serverFor(lang)`.

## How the Agent Uses It

After editing a file, the agent asks `Manager.Diagnostics(path, content)` for
compiler feedback and feeds it into the next model turn — catching errors
before the user sees them. Auto-test memoization (`autoTestCache`) prevents
re-running `go test` on every round.

For renames, the agent calls the `lsp_rename` tool (file + 0-based position +
new name); gopls computes every reference across the project and edits land
through `lockFile` + `backupFile` + binary guard — the identical safety net as
`edit`/`write`. Server-pushed `workspace/applyEdit` refactors take the same
path via `SetApplyEditHandler`.

## Related

- [`agent.md`](./agent.md) — boot wiring (`lsp.Configure`)
- [`tools.md`](./tools.md) — file edits trigger diagnostics
