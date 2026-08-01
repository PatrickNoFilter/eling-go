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

### PHASE 1 — Git Worktrees + Sandboxed Bash  `[✅ DONE 2026-08-01 v0.3.0]`

**Goal:** Every bash command runs in an isolated sandbox dir; `git` operations use
auto-created worktrees so experiments never touch the main working tree.

**Status:** Implemented & committed (`feat: sandbox + git worktrees`, v0.3.0). New
`internal/tools/sandbox.go` (171 LOC: per-invocation dirs, env scrub w/ controlled-var
override, 15 destructive-pattern guards, best-effort `unshare -n`), `internal/tools/worktree.go`
(250 LOC: create/list/remove/merge), `sandbox_test.go` (7 tests). `allow_host: true` opt-in
escape hatch on the bash tool. Config: `sandbox.enabled/root/max_output/timeout_sec/guard_mode`
(default **on**). TUI header shows `🏝️ snd on`. Fixed a real env-var bug found by tests:
duplicate `HOME`/`PATH` from host env overrode sandbox values (execve: last wins).

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

### PHASE 3 — LSP Integration  `[✅ DONE 2026-08-01 v0.2.4]`

**Goal:** After the agent edits a file, run language-server diagnostics and feed them back
before the model continues.

**Status:** Implemented & committed (`91890e8 feat: lsp integration`, v0.2.4). New `internal/lsp`
package (minimal JSON-RPC 2.0 LSP client over stdio, Content-Length framed), wired into
both tool loops after `HookPostToolUse`. `write`/`edit` results gain a compact `[lsp]`
section (file:line:col SEV: message, capped at 20). `lsp.KillAll()` mirrors
`tools.KillRunningTools()` on TUI Ctrl+C. 17 new tests pass (12 lsp + 5 agent).

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

### PHASE 4 — Daemon/ACP Mode  `[✅ DONE 2026-08-01 v0.2.5]`

**Goal:** `eling serve` — long-running agent accessible over HTTP+SSE so any client
(TUI, curl, another device) can talk to it.

**Status:** Implemented & committed (`feat: eling serve daemon`, v0.2.5). New
`internal/server` package (316 LOC: Server, auth, health/sessions/chat handlers,
SSE streaming), `cmdServe` in `internal/cli/cli.go` with `--addr`/`--token` flags
and graceful SIGINT/SIGTERM shutdown (saves all sessions), `ServerConfig`
(`server.enabled/addr/token`) in config, `Agent.SessionName()` accessor, and
6 new tests (health, 401 auth, SSE chat stream, session continuity, 400s, 404).

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
- [x] `curl -N -X POST http://127.0.0.1:8765/v1/chat -d '{"prompt":"hi"}'` streams a reply
- [x] Two sequential chats to same session_id continue the conversation
- [x] Wrong token → 401
- [x] `go test -race ./internal/server/...` clean (6 tests pass)

**Effort:** M (1–2 days) · **Risk:** medium (concurrency, SSE framing)

---

### PHASE 5 — User-Defined Hooks  `[✅ DONE 2026-08-01 539c18f]`

**Goal:** Let users attach shell scripts to the existing 7 lifecycle events — the Qwen Code
hook model on top of ELING's internal `fireHook` system.

**Status:** Implemented & committed (`539c18f fix: autoTest reliability + feat: user-defined
hooks`, Phase 5 of qwen heist). New `internal/hooks/hooks.go` (173 LOC): `RegisterUserHooks`
bridges `config.yaml` → `hooks.scripts.<event>` to Brain hook handlers (5s timeout per script,
JSON context on stdin, failures logged & swallowed — never crash the agent), `CheckVeto`
wired into BOTH tool loops (`runToolLoop` + `runStreamToolLoop`) so `pre_tool_use` scripts
can veto calls via `{"block":true,"reason":"..."}`. Unknown hook names warn instead of
silently never firing. 7 tests pass (`internal/hooks/hooks_test.go`).

**Design:**

1. **Config:** `hooks: { scripts: { pre_tool_use: ["/path/script.sh", ...], post_tool_use: [...], error_occurred: [...] } }`
   parsed in `internal/config/config.go` into `HooksConfig{ Scripts map[string][]string }`.
2. **Bridge in `internal/hooks/hooks.go`:**
   - `RegisterUserHooks(brain, scripts)` — for each configured script, registers a
     `layers.HookHandler` closure that runs it via `exec.CommandContext` with the hook
     context JSON on stdin (`{"tool_name":"bash","arguments":"{...}","duration_ms":123}`).
   - 5s timeout per script (`scriptTimeout`); stderr captured; stdout capped at 64 KiB;
     panic-recover mirrors `fireHook` (`agent.go:285`).
3. **Pre-tool gate:** scripts emitting `{"block": true, "reason": "..."}` on stdout for
   `pre_tool_use` veto the tool call before execution (`hooks.CheckVeto` + blocked result
   returned to the model so it knows why).
4. Documented in `README.md` + `docs/` (see Hooks section).
5. Same commit also fixed the autoTest tool (see below).

**Files touched:** `internal/hooks/hooks.go` (new), `internal/hooks/hooks_test.go` (new),
`internal/config/config.go` (HooksConfig), `internal/agent/agent.go` (wire bridge in
`SetBrain`, veto gate in both tool loops), `README.md`, `docs/*`.

**Acceptance:**
- [x] A `post_tool_use` script appends a marker file after any `edit` — marker appears (test: `TestRegisterUserHooksFiresPostToolUse`)
- [x] A `pre_tool_use` script returning `{"block":true}` prevents the tool call (test: `TestPreToolUseVeto`)
- [x] Missing script path → warning, no crash (test: `TestMissingScriptDoesNotCrash`)

**Effort:** S–M (1 day) · **Risk:** low

---

## 🗺️ Roadmap

| Phase | Feature | Effort | Suggested order | Commit |
|-------|---------|--------|-----------------|--------|
| 1 | Git Worktrees + Sandbox | M | 3rd (needs stability) | `feat: sandbox + git worktrees` ✅ **v0.3.0 (2026-08-01)** |
| 2 | Plan Mode | S | **1st** ⚡ quick win | `feat: plan mode gating` ✅ **v0.2.3 (2026-08-01)** |
| 3 | LSP Integration | M | 2nd | `feat: lsp diagnostics feedback` ✅ **v0.2.4 (2026-08-01)** |
| 4 | Daemon/ACP Mode | M | 4th | `feat: eling serve daemon` ✅ **v0.2.5 (2026-08-01)** |
| 5 | User-Defined Hooks | S–M | 5th (leverages existing system) | `feat: user-defined hooks` ✅ **539c18f (2026-08-01)** |

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
