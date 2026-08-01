# 🧬 STEALING.md — Qwen Code Feature Heist Plan

> **Mission:** Port the most valuable capabilities from [QwenLM/qwen-code](https://github.com/QwenLM/qwen-code)
> (26.5k ⭐, Apache-2.0, TypeScript) into ELING (Go, Termux-native).
>
> **Rule:** We *reimplement ideas*, never copy code. Apache-2.0 permits derivative work, but
> ELING's Go architecture is different enough that clean-room reimplementation is the right call.
>
> **Golden rule:** `create_backup` before every phase. One phase per commit. Tests must pass after each phase.
>
> **Note (2026-08-01):** Phase 3 "SubAgent Delegation" was **removed from v1 scope** — too risky
> (nested LLM API budget, race conditions, free-tier rate limits). Deferred to v2.

---

## 📊 Current State (audited 2026-08-01)

| Capability | ELING today | Qwen Code | Gap |
|-----------|-------------|-----------|-----|
| Agent loop | `internal/agent/agent.go` (`Ask`, `AskStream`, `runToolLoop`, `runStreamToolLoop`) | Gemini-CLI lineage | — |
| Tool registry | `internal/tools/registry.go` (`Execute`/`ExecuteContext`, 17 static builtins + dynamic + MCP) | ACP tool protocol | — |
| Sessions | `internal/session/session.go` (save/resume/list) | session mgmt | — |
| MCP | `internal/mcp/mcp.go` (406 LOC, 2 servers) | MCP + SDKs | daemon surface |
| Hooks | ✅ **`fireHook` + `layers.Brain.FireHook`** — 7 events: `HookSessionStart`, `HookPreUserMessage`, `HookPostUserMessage`, `HookPostAssistantMessage`, `HookErrorOccurred`, `HookPreToolUse`, `HookPostToolUse` | Hooks (user-defined shell) | **user-defined hook scripts** |
| Sandbox | ❌ `internal/tools/bash.go` runs `exec.Command` directly (512 KiB output cap only) | Sandbox + Git Worktrees | **BIG** |
| SubAgents | ❌ single-agent loop | SubAgents/Teams/Workflows | **deferred — v2 (too risky)** |
| LSP | ❌ | LSP integration | medium |
| Plan Mode | ❌ (agent just executes) | Plan Mode | small |
| Daemon | ❌ (CLI/TUI only) | `qwen serve` (ACP/HTTP+SSE) | medium |
| Computer Use | ❌ | cua-driver | out of scope (needs desktop) |
| IM channels | ❌ | Telegram/DingTalk/WeChat | out of scope (v2) |
| SDKs | ❌ | TS/Python/Java | out of scope (v2) |

---

## 🎯 The Heist — 5 Features, Ranked by Value/Effort

---

### PHASE 1 — Git Worktrees + Sandboxed Bash  `[biggest safety win]`

**Goal:** Every bash command runs in an isolated sandbox dir; `git` operations use
auto-created worktrees so experiments never touch the main working tree.

**Design:**

1. **Sandbox wrapper in `internal/tools/bash.go`**
   - New `SandboxConfig{ Enabled bool; Root string; MaxOutput; Timeout }`.
   - When enabled, commands execute with:
     - `cmd.Dir = sandboxRoot` (per-session dir under `~/.eling/sandbox/<sessionID>/`)
     - environment scrub: `PATH` locked, `HOME` pointed at sandbox, `ELING_SANDBOX=1`
     - read-only bind of project dir via `mount --bind ro` (Linux) — **skip if not root**
     - network block optional via `unshare -n` when available (best-effort, ignore failure)
   - Commands that must touch the real tree (`git add`, `rebuild.sh`) get an explicit
     `allow_host: true` arg on the tool schema — **opt-in, never default**.

2. **Git Worktree tool** — new `internal/tools/worktree.go`
   - `worktree_create {base_branch} {name}` → `git worktree add ../eling-wt-<name> -b eling/exp/<name>`
   - `worktree_list`, `worktree_remove {name}`, `worktree_merge {name} {target}` (git merge + delete)
   - Register in `registerBuiltins()` (`internal/tools/registry.go:197`).
   - Worktree roots live in `~/.eling/worktrees/`, never inside the repo.

3. **Config flag:** `sandbox.enabled: true` in `config.yaml` (default **on** in Termux root env; off in constrained envs).

**Files touched:** `internal/tools/bash.go`, `internal/tools/worktree.go` (new), `internal/tools/registry.go`,
`internal/config/*`, `internal/agent/agent.go` (thread sandboxRoot via ctx), `internal/session/session.go` (store sandboxRoot).

**Acceptance:**
- [ ] `bash {cmd:"rm -rf /root/eling"}` fails or is confined; real dir untouched
- [ ] `worktree_create` + `worktree_merge` round-trip on a test commit
- [ ] All 58 existing tests still pass (`go test ./...`)
- [ ] TUI shows `🏝️ sandbox: on` in header

**Effort:** M (1–2 days) · **Risk:** low-medium (mount/unshare are best-effort)

---

### PHASE 2 — Plan Mode  `[✅ DONE 2026-08-01 v0.2.3]`

**Goal:** Gate execution behind a plan/approval step — the agent drafts a plan, the user
approves, *then* tools execute.

**Status:** Implemented & committed (`feat: plan mode gating`, v0.2.3). `--plan` flag,
`/plan` TUI command, `draftPlan()` with tools stripped, TUI y/N/Esc checklist, session
persistence, plan re-injection via system message. 5 dedicated tests pass
(`internal/agent/plan_test.go`).

**Design:**

1. New `--plan` CLI flag (`internal/cli/cli.go`) + `/plan` TUI command (`internal/tui/tui.go`).
2. In `Agent.Ask()` (`agent.go:302`): when plan mode is on and prompt has no plan,
   - first LLM call runs with **tools stripped** (`toolDefs = nil`) and a system suffix:
     `"Respond ONLY with a numbered execution plan. No code. No tool calls."`
   - capture plan text; render in TUI as a checklist; wait for `y/N`/`Enter` from user.
   - on approval, re-enter `Ask()` normally with tools enabled, prepending the approved plan
     to the message history (so the model follows it).
3. Persist plan approval in session (`internal/session/session.go` — `Plan string` field).
4. `Ctrl+C` during plan review aborts; `Esc` skips plan mode for this turn.

**Files touched:** `internal/cli/cli.go`, `internal/tui/tui.go`, `internal/agent/agent.go`, `internal/session/session.go`, `internal/provider/deepseek.go` (no change needed — just don't pass tools).

**Acceptance:**
- [ ] `eling --plan "deploy service"` shows plan checklist, does NOT run tools before approval
- [ ] Approving runs the plan; rejecting aborts with "plan rejected"
- [ ] Plan visible in saved session JSON

**Effort:** S (half day) · **Risk:** very low

---

### PHASE 3 — LSP Integration  `[instant diagnostics]`

**Goal:** After the agent edits a file, run language-server diagnostics and feed them back
before the model continues.

**Design:**

1. **New package `internal/lsp/lsp.go`** — thin JSON-RPC client over stdio:
   - `type Server struct { cmd *exec.Cmd; conn io.ReadWriteCloser }`
   - `Start(lang string) error` — pick server by extension: `gopls` (Go), `pyright-langserver` (Py),
     `typescript-language-server` (TS). Auto-skip if binary missing (best-effort, no hard dep).
   - `DidOpen(path)` → `PublishDiagnostics` capture; `DidChange(path, content)`.
   - `Diagnostics(path) []Diagnostic{ Severity, Message, Range }`.
2. **Hook it into the tool loop** — after `fireHook(HookPostToolUse)` in `runToolLoop`
   (`agent.go:679`) and `runStreamToolLoop` (`agent.go:1345`): if the tool was `write`/`edit`/`bash`
   touching a known source file, call `lsp.Diagnostics(path)` and append a compact
   `[lsp] file.go:12:5 ERR: undefined: foo` line to the tool result sent back to the model.
3. **Config:** `lsp.enabled: true`, `lsp.servers: {go: gopls, python: pyright-langserver, typescript: typescript-language-server}`.
4. Server lifecycle: start lazily on first edit, kill on agent shutdown (`KillRunningTools` pattern in bash.go).

**Files touched:** `internal/lsp/lsp.go` (new), `internal/agent/agent.go`, `internal/config/*`, `internal/tools/files.go` (return file path in result).

**Acceptance:**
- [ ] Editing a `.go` file with a syntax error yields a `[lsp]` diagnostic in the next model turn
- [ ] Missing server binary → silent skip, no crash
- [ ] `go vet` clean; all 58 existing tests pass

**Effort:** M (1–2 days) · **Risk:** low (best-effort skip)

---

### PHASE 4 — Daemon/ACP Mode  `[multi-client agent]`

**Goal:** `eling serve` — long-running agent accessible over HTTP+SSE so any client
(TUI, curl, another device) can talk to it.

**Design:**

1. **New package `internal/server/server.go`**:
   - `POST /v1/chat` `{session_id?, prompt}` → streams SSE events: `message`, `tool_call`, `done`, `error`.
   - `GET /v1/sessions`, `GET /v1/sessions/{id}` (reuse `internal/session/session.go`).
   - `GET /v1/health` → `{version, providers, tools, mcp_servers}`.
   - Auth: `Authorization: Bearer <token from config>` (default `127.0.0.1` bind only).
2. **Reuse the agent loop:** wrap `Agent.AskStream` (`agent.go:1030`) — one `Agent` per session,
   map of `sessionID → *Agent`, mutex-guarded (mirror `runningCmds` pattern in bash.go).
3. **CLI:** `eling serve --addr 127.0.0.1:8765` in `internal/cli/cli.go`; TUI flag `--daemon-url`
   so the TUI can attach to a remote daemon instead of spawning its own agent.
4. **Termux-friendly:** bind `127.0.0.1` by default; expose `--addr 0.0.0.0:8765` for LAN use with token.

**Files touched:** `internal/server/server.go` (new), `internal/cli/cli.go`, `internal/tui/tui.go`,
`internal/config/*`, `internal/session/session.go`.

**Acceptance:**
- [ ] `curl -N -X POST http://127.0.0.1:8765/v1/chat -d '{"prompt":"hi"}'` streams a reply
- [ ] Two sequential chats to same session_id continue the conversation
- [ ] Wrong token → 401
- [ ] `go test -race ./internal/server/...` clean

**Effort:** M (1–2 days) · **Risk:** medium (concurrency, SSE framing)

---

### PHASE 5 — User-Defined Hooks  `[extend, don't build — ELING already has hooks!]`

**Goal:** Let users attach shell scripts to the existing 7 lifecycle events — the Qwen Code
hook model on top of ELING's internal `fireHook` system.

**Design:**

1. **Config:** `hooks: { pre_tool_use: ["/path/script.sh", ...], post_tool_use: [...], error_occurred: [...] }`
   parsed in `internal/config/*` into `map[string][]string`.
2. **Bridge in `internal/layers/hooks.go`** (or new `internal/hooks/hooks.go`):
   - Register a `layers.HookHandler` that, for each configured script, runs it via `exec.Command`
     with the hook context JSON on stdin (`{"tool":"bash","args":{...},"duration_ms":123}`).
   - 5s timeout per script; stderr captured; failures logged, never crash the agent
     (mirror the recover pattern in `fireHook`, `agent.go:285`).
3. **Document the 7 events** in `README.md` + `docs/` with examples (e.g., `post_tool_use`
   script that runs `go vet` after any `edit` on a `.go` file).
4. Scripts that output `{"block": true, "reason": "..."}` on stdin-result for `pre_tool_use`
   can veto a tool call (pre-tool gate — new capability).

**Files touched:** `internal/hooks/hooks.go` (new), `internal/config/*`, `internal/agent/agent.go` (wire bridge in `New`),
`README.md`, `docs/*`.

**Acceptance:**
- [ ] A `post_tool_use` script appends a marker file after any `edit` — marker appears
- [ ] A `pre_tool_use` script returning `{"block":true}` prevents the tool call
- [ ] Missing script path → warning, no crash

**Effort:** S–M (1 day) · **Risk:** low

---

## 🗺️ Roadmap

| Phase | Feature | Effort | Suggested order | Commit |
|-------|---------|--------|-----------------|--------|
| 1 | Git Worktrees + Sandbox | M | 3rd (needs stability) | `feat: sandbox + git worktrees` |
| 2 | Plan Mode | S | **1st** ⚡ quick win | `feat: plan mode gating` ✅ **v0.2.3 (2026-08-01)** |
| 3 | LSP Integration | M | 2nd | `feat: lsp diagnostics feedback` |
| 4 | Daemon/ACP Mode | M | 4th | `feat: eling serve daemon` |
| 5 | User-Defined Hooks | S–M | 5th (leverages existing system) | `feat: user-defined hooks` |

**Suggested sprint:** Phase 2 → 3 → 1 → 4 → 5 (quick wins first, big-ticket items
after the safety net from Phase 1 exists).

**Removed from v1:** ~~Phase 3 SubAgent Delegation~~ — too risky (nested LLM API budget,
race conditions, free-tier rate limits). Revisit in v2.

---

## 🧪 Testing & Safety Per Phase

1. `./rebuild.sh` (go vet + build + tests) — **mandatory before commit**
2. `go test -race ./...` for phases 1, 4 (concurrency-heavy)
3. `create_backup` → zip to `/root/eling_backup_<ts>.zip`
4. Update `README.md` + `DESIGN.md` + `docs/` in the same commit
5. Bump version via `go-version-bump` (v0.2.3 → v0.3.0 at Phase 1 — sandbox milestone)
6. `git commit` with conventional message; tag on milestone (Phase 1 or 4)

## ❌ Explicitly Out of Scope (v1)

- Computer Use / CUA driver (needs desktop GUI — Termux is headless)
- IM channels (Telegram/WeChat bots) — candidate for v2
- SDKs (TS/Python/Java) — v2
- SubAgent delegation / Agent Arena — v2 (requires nested agent infra we deliberately deferred)

---

## 📚 Reference

- Qwen Code repo: https://github.com/QwenLM/qwen-code (Apache-2.0)
- ELING agent loop: `internal/agent/agent.go`
- ELING tool registry: `internal/tools/registry.go`
- ELING hooks (already exist): `internal/layers/*` + `fireHook` in `internal/agent/agent.go:285`
- ELING bash tool (sandbox target): `internal/tools/bash.go`
