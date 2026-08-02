# ELING Documentation — by subsystem

Each subsystem of the codebase has its own doc (A7 docs-per-subsystem).
Read the one that matches what you're touching.

| Doc | Subsystem | Path |
|-----|-----------|------|
| [agent.md](./agent.md) | Conversational core, turn lifecycle, plan mode, stats | `internal/agent` |
| [tools.md](./tools.md) | Dynamic tool registry, timeout strategy, hash-anchored edits, sandbox | `internal/tools` |
| [skills.md](./skills.md) | Auto-learn / auto-forget skill engine (cap 100) | `internal/agent` + `internal/mcp/skill` |
| [provider.md](./provider.md) | LLM providers, retry/backoff, rotation/fallback, DeepSeek reasoning | `internal/provider` |
| [mcp.md](./mcp.md) | MCP client + server (brain-layer tools, per-call timeouts) | `internal/mcp` |
| [tui.md](./tui.md) | Terminal UI: marquee banner, paste-safe input, legend, stats | `internal/tui` |
| [lsp.md](./lsp.md) | LSP client for instant compiler diagnostics | `internal/lsp` |
| [session.md](./session.md) | Conversation persistence & resume | `internal/session` |
| [server.md](./server.md) | HTTP+SSE daemon / ACP mode | `internal/server` |

## Cross-cutting

- **Config** — `internal/config`
- **Brain / memory layers** — `internal/layers` (8-layer architecture,
  lifecycle hooks, snapshot/rollback)
- **Logger & crash reports** — `internal/logger`
- **Hooks** — `internal/hooks` (lifecycle hooks)
- **CLI subcommands** — `internal/cli` (`eling stats`, `eling learnings`,
  `eling serve`, etc.)
- **Markdownify / benchmark / probe_fix** — utility servers & tooling
