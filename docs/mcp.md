# MCP Subsystem — `internal/mcp`

ELING's Model Context Protocol layer. Lets ELING act as an **MCP client**
(connect to external MCP servers) and as an **MCP server** (expose its brain
layers + skills to other MCP clients).

## Files

| File | Purpose |
|------|---------|
| `mcp.go` | `Manager` — connect/list/disconnect MCP servers |
| `srv/server.go` | **MCP server** — exposes ~28 brain-layer tools to clients |
| `srv/timeout_test.go` | Per-call timeout guard tests |
| `skill/skill.go` | Skills-over-MCP bridge |

## Client side (`Manager`)

- `mcp.NewManager()`, `Connect(ctx, name, command, args, env)`,
  `List()`, `Disconnect`.
- Servers configured in `cfg.MCP.Servers` (when `cfg.MCP.Enabled`).
- `Agent.GetStats().mcp_servers` reports the connected count.

## Server side (`srv/server.go`)

- `eling --mcp` runs ELING as an MCP server (`--agent-id` for continuum).
- Exposes the 8-layer brain tools (facts, memory, rules, snapshots, etc.),
  skills, and continuum operations — **~28 tools**.
- **Per-call timeout guard** — every MCP tool call runs under a wall-clock
  budget via `executeToolWithTimeout`; `mcpToolTimeout()` assigns strict
  budgets (fast lookups 10s, local ops 20–30s, network/heavy 60s). On budget
  expiry: SIGKILL tracked subprocesses + return a timeout error.
- `--mcp-verify` checks the server is alive and lists tools.

## Verification / Debug

- `mcp-health-check-and-repair` skill — verify binaries, live tool calls.
- `debug-mcp-zero-count` skill — checks `mcp.enabled`, config parity.
- `verify-mcp-config-fix` skill — config edits with timestamped backups.

## Related

- [`agent.md`](./agent.md) — `Agent.MCP` integration
- [`skills.md`](./skills.md) — skill bridge
- [`layers.md`](./layers.md) — the brain layers exposed as MCP tools
