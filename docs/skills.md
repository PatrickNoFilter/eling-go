# Skills Subsystem — auto-learn / auto-forget

ELING **auto-learns skills** from tool usage patterns and **auto-forgets**
unused ones, keeping the skill list bounded and relevant. The engine lives in
`internal/agent` (`LearnedSkill`, evolution records); the `internal/skills/`
package is intentionally minimal (skills are first-class tools with category
`"skill"`).

## Lifecycle

1. **Learn** — when a tool is used successfully enough (tracked by
   `incrementSkillUsedCount`), the agent records a `LearnedSkill` with a
   generated description (gist of the successful usage).
2. **Cap (hard limit = 100)** — `const maxSkills = 100` at
   `agent.go:2868`. When the list is full, the least-recently-used / least
   valuable skill is evicted (forget) before a new one is added.
3. **Forget** — unused skills decay and are dropped, keeping the registry
   bounded and the TUI list clean.
4. **Evolution** — `evolutions []Evolution` records skill upgrades; the
   `eling evolve` CLI command runs the brain's evolution pass.

## Skill as a Tool

Each skill is a `tools.Tool` with `Category: "skill"` registered in the
`ToolRegistry`. The agent can call a skill like any other tool.

## CLI / TUI Surface

- `/skills` — list learned skills (TUI/REPL)
- `eling skills`-equivalent via `Agent.ListSkills()` →
  `ToolRegistry.ListByCategory("skill")`
- `GetStats().learned_skills` — count shown in the stats dashboard

## MCP Skill Bridge

`internal/mcp/skill/skill.go` exposes skills over MCP so external clients
(another ELING, IDE, daemon) can list/invoke learned skills.

## Related

- [`agent.md`](./agent.md) — `LearnedSkill`, `incrementSkillUsedCount`
- [`mcp.md`](./mcp.md) — skill bridge server
