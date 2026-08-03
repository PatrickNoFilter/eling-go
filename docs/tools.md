# Tools Subsystem — `internal/tools`

Dynamic tool registry + the individual tools ELING can invoke. Inspired by
jcode's tool registry. **This is the most security-critical package** — every
tool call goes through here.

## Files

| File | Purpose |
|------|---------|
| `registry.go` | `Tool`, `Registry`, timeout strategy, runtime metrics (A5) |
| `register.go` | Built-in tool registration |
| `files.go` | Read/write/edit files — **hash-anchored edits (A1)** |
| `bash.go` | Shell execution (sandboxed) |
| `web.go` / `web_timeout.go` | HTTP fetch with adaptive timeouts |
| `backup.go` | Timestamped backups + rotation |
| `ocr.go` | OpenCodeReview integration |
| `lsp_rename.go` | **LSP symbol rename (A6)** — gopls/pyright/TS rename → backup+lock edits |
| `sandbox.go` | Phase-1 bash sandbox (namespaces, guards) |
| `worktree.go` | Phase-1 git worktree management |
| `semantic.go` | Semantic search index |
| `schema.go` | Tool argument schemas |
| `setup.go` | Setup helpers |

## Registry & Timeout Strategy (v0.4.0)

Every tool has a **wall-clock budget**:

- `Tool.Timeout` or `DefaultToolTimeout` (5 min) fallback
- Caller context deadline wins if earlier
- `ExecuteCtx` tools receive a ctx with the budget (can cancel mid-flight)
- Plain `Execute` tools run under a goroutine + timer guard; on expiry
  `KillRunningTools()` SIGKILLs tracked subprocesses

`Registry.ExecuteContext` also **records runtime metrics (A5)**:

```go
r.Stats() → {
  tool_calls, tool_failures, tool_success_rate, tool_avg_latency_ms,
  per_tool: { name: {calls, failures, success_rate, avg_latency_ms, last_call} }
}
```

## Hash-Anchored Edits (A1 — oh-my-pi steal)

`files.go` implements the highest-value oh-my-pi adoption:

- **`source_hash` verify** — `read` returns a content hash; edits only apply
  if the current file hash matches, preventing silent corruption from stale
  context
- **`occurrence` targeting** — replace the Nth occurrence of a pattern
- **`replaceNth`** — surgical Nth-occurrence replacement
- **`lockFile`** — per-file mutex serializing concurrent edits
- Tests: `files_anchor_test.go` (12 cases, passes)

## Sandbox (Phase 1)

`SetSandbox(SandboxSettings{...})` configures the bash sandbox (default on):
namespace isolation, max-output caps, timeout, guard mode.

## Related

- [`agent.md`](./agent.md) — the agent uses `tools.DefaultRegistry`
- [`server.md`](./server.md) — MCP/daemon tool dispatch
