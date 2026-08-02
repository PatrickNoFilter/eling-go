# 🧬 STEALING.md — Feature Heist Plan (Qwen Code + oh-my-pi)

> **Mission:** Port the most valuable capabilities from
> [QwenLM/qwen-code](https://github.com/QwenLM/qwen-code) (26.5k ⭐, Apache-2.0, TypeScript)
> **and** [can1357/oh-my-pi](https://github.com/can1357/oh-my-pi) (21.2k ⭐, Rust + TypeScript)
> into ELING (Go, Termux-native).
>
> **Part I** (below) = Qwen Code heist (Phases 1–5, all ✅ DONE).
> **Part II** (appended 2026-08-02) = oh-my-pi adoption list — candidates, not yet implemented.
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

---
---

# 🤖 PART II — oh-my-pi Adoption List (candidates, not yet implemented)

> **Source:** [can1357/oh-my-pi](https://github.com/can1357/oh-my-pi) (21.2k ⭐, fork of
> badlogic/pi-mono). 6,055 files; ~55k LOC Rust core (`crates/`) + TypeScript packages
> (`packages/`); Bun runtime; Bazel build. Clone on-device at `/root/oh-my-pi` (depth-1, 163 MB).
>
> **What omp does better than us:** tool-call *reliability* (hash-anchored edits, benchmark-tuned
> prompts per model), IDE-grade code intelligence (14 LSP ops, 28 DAP ops), persistent execution
> kernels that loop back into the agent's own tools, a 40+ provider model catalog, and a
> docs-per-subsystem map (~90 docs) that makes a 55k-LOC codebase navigable.
>
> **Rule:** same as Part I — reimplement ideas, never copy code. ELING is Go single-binary by
> design; omp's Rust natives (search/grep/shell) are **already equivalent** in ELING via ugrep 7.5.0.

## 📊 Ranked Adoption List (Value/Effort)

| # | Adoption | Effort | Value | ELING anchor (today) |
|---|----------|--------|-------|----------------------|
| A1 | **Hash-anchored edits** (`hashline`-style) | S | 🔥🔥🔥 | `internal/tools/files.go` (edit = string match) |
| A2 | **Model catalog** (40+ providers, per-model tuning) | S–M | 🔥🔥🔥 | `internal/provider/deepseek.go` + wizard |
| A3 | **Persistent shell session** (`pi-shell`-style) | M | 🔥🔥 | `internal/tools/bash.go` + `sandbox.go` |
| A4 | **Kernel→tool loopback** (Python/Bun kernel calls agent tools) | M | 🔥🔥 | `internal/server/` (eling serve daemon, Phase 4) |
| A5 | **`eling stats` dashboard** (`omp stats`-style) | S | 🔥🔥 | session JSONL + `internal/benchmark/metrics.go` |
| A6 | **LSP rename wiring** (`willRenameFiles` / `applyEdit`) | S–M | 🔥🔥 | `internal/lsp/lsp.go` (gopls already wired) |
| A7 | **Docs-per-subsystem** (1 doc per `internal/` package) | S (docs) | 🔥🔥 | README + DESIGN.md only |
| A8 | **Benchmark gate for prompt/tool changes** (`metaharness`-style) | S–M | 🔥 | `internal/benchmark/` (already exists) |
| A9 | DAP integration (28 debug ops) | L | 🔥 | reuse LSP JSON-RPC transport — **v2** |
| A10 | Mnemosyne-style learnings file read at session start | S | 🔥 | `internal/agent/memory.go` + `internal/skills/` |

---

### A1 — Hash-anchored edits (hashline-style)  `[IMPLEMENTED 2026-08-02]`

**What omp does:** every edit references the exact source content hash of the region being
replaced. Edits land first-attempt: benchmark shows **+5pp over str_replace, −61% tokens** wasted
on retry loops.

**Why we wanted it:** ELING's `edit` tool (`internal/tools/files.go`) matched `old_string`
exactly and failed with "old_string not found" on the slightest drift (whitespace, partial line,
duplicate text). Every retry burned tokens and API calls.

**What shipped (v1.2.0 of the edit tool):**
1. **`read` now returns `hash`** (SHA-256 hex of the file) — the anchor the model echoes back.
2. **`edit` accepts `source_hash`** — optional; when present, the file's hash is verified before
   replacing. Mismatch aborts with *expected vs computed* hashes + "re-read and retry" guidance,
   so the model self-corrects in one step instead of looping.
3. **`edit` accepts `occurrence`** (1-based, default 1) — targets the n-th match explicitly.
   Kills the old `strings.Replace(...,1)` first-match ambiguity. Result reports
   `occurrence` + `total_occurrences`; out-of-range occurrence errors list the max.
4. **Drift hint** — if `old_string` isn't found but matches after whitespace/line-ending
   normalization, the error says so ("re-copy exact bytes from read output") instead of a bare
   not-found.
5. **Per-file mutex** (`lockFile`, keyed by absolute path) — `edit` and `write` now serialize
   their whole read→verify→backup→write cycle. Verified with a 20-goroutine lost-update test.
6. **Edit result returns the new file `hash`** — chained edits pass it back as `source_hash`
   without a re-read round-trip.

**Tests:** `internal/tools/files_anchor_test.go` (12 new cases: occurrence targeting, hash
mismatch/match, chained hashes, drift hint, concurrent serialization, binary guard, read hash).
Full suite + `go vet ./...` green.

**Not adopted yet (deferred):** `apply_patch`-style multi-hunk edits (one call, N hunks) — cut
round-trips on big refactors. Worth doing after A2 (model catalog) lands.

**Files touched:** `internal/tools/files.go`, `internal/tools/schema.go`,
`internal/tools/files_anchor_test.go`.


---

### A2 — Model catalog (40+ providers, per-model metadata)  `[candidate]`

**What omp does:** a catalog package knows every provider's models: context window, pricing
tier, reasoning support, default base URLs, API-key env vars — the agent picks the right model
for the job and the CLI auto-fills config.

**Why we want it:** our provider layer is one hand-written `deepseek.go` + config keys. The
setup-wizard pain ("tokenrouter wizard not working") exists partly because defaults are
hard-coded in one place.

**Plan:**
1. New `internal/provider/catalog.go` — static table: `provider → models[] → {context_window,
   supports_reasoning, tier, default_base_url, env_var}`. Seed with the providers we actually
   use (opencode-zen, tokenrouter, qwen, deepseek, openai-compatible) + common free ones.
2. `eling setup` wizard reads catalog for auto-fill; `validate` does a live
   `GET {base}/models` check (pattern: `diagnose-api-provider-setup`).
3. Add `providers.<name>.model_aliases` so one logical model ("deepseek-v4-flash") maps per
   provider without config duplication.

**Files:** `internal/provider/catalog.go` (new), `internal/provider/deepseek.go`,
`internal/config/*`, setup wizard scripts.

---

### A3 — Persistent shell session (pi-shell-style)  `[candidate]`

**What omp does:** a persistent PTY shell keeps cwd, env, and shell state between calls —
no more re-`cd` + re-export chains, huge token savings.

**Why we want it:** every ELING bash call is a fresh `exec.Command` in a sandbox dir; multi-step
workflows re-state context each time.

**Plan:**
1. Per-session persistent shell: `internal/tools/shell.go` holding a `*exec.Cmd` with `StdinPipe`
   + PTY (use `creack/pty` — pure Go, works on Termux) per `sessionID`.
2. Commands run via `bash -ic "<cmd>"` against the live shell; `pwd`/env snapshot returned with
   each result so the model sees state.
3. **Sandbox intact:** persistent shell lives inside the session sandbox dir (`sandbox.go`
   already provides it); `reset` tool to kill & recreate.
4. Config flag `shell.persistent: true` (default **off** until battle-tested; on = opt-in).

**Files:** `internal/tools/shell.go` (new), `internal/tools/sandbox.go`,
`internal/tools/registry.go`, `internal/session/session.go`.

---

### A4 — Kernel→tool loopback (Python/Bun kernels call agent tools)  `[candidate]`

**What omp does:** persistent Python/Bun kernels can call back into the agent's own tools
(read/search/edit) over a loopback HTTP channel — code can inspect and modify the repo itself.

**Why we want it:** ELING's `eling serve` daemon (`internal/server/`, Phase 4 ACP mode) already
exposes HTTP+SSE — the loopback substrate exists for free.

**Plan:**
1. Add `python`/`node` execution tool that starts a persistent kernel per session
   (`internal/tools/kernel.go`) and exposes `ELING_TOOL_LOOPBACK=http://127.0.0.1:<port>` env.
2. Kernel code can `requests.post(loopback + "/tool", json={name, args})` to call registered
   tools (read/edit/grep) with the same auth the daemon uses.
3. Guard: loopback bound to 127.0.0.1 only; tools whitelist (no bash-in-bash by default).

**Files:** `internal/tools/kernel.go` (new), `internal/server/`, `internal/tools/registry.go`.
**Risk:** nested execution → phase-gate behind config flag, v2 default off.

---

### A5 — `eling stats` dashboard (omp stats-style)  `[candidate]`

**What omp does:** `omp stats` shows tokens, sessions, tool-call success rates, per-model spend.

**Plan:** new `internal/stats/` package aggregating session JSONL (`internal/session/`) +
`internal/benchmark/metrics.go` output; `eling stats` CLI subcommand with a compact table:
sessions, total tokens, tool calls, success %, avg latency, per-provider spend estimate.

**Files:** `internal/stats/*` (new), `internal/cli/`, `internal/benchmark/metrics.go`.

---

### A6 — LSP rename wiring (willRenameFiles / applyEdit)  `[candidate]`

**What omp does:** 14 LSP ops incl. safe rename (`willRenameFiles` → `workspace/applyEdit`).

**Why we want it:** Phase 3 gave us diagnostics feedback via gopls
(`internal/lsp/lsp.go`); rename is the natural next step and makes refactors land cleanly
instead of string-swapping.

**Plan:** add `textDocument/rename` + `workspace/applyEdit` + `willRenameFiles` handling to
`internal/lsp/lsp.go`, exposed as an `lsp_rename` tool. Reuse existing gopls transport
(JSON-RPC stdio). Tests exist (`real_gopls_test.go`).

**Files:** `internal/lsp/lsp.go`, `internal/tools/registry.go`, tests.

---

### A7 — Docs-per-subsystem  `[candidate — documentation only]`

**What omp does:** ~90 docs files mapping 1:1 to subsystems — you read the doc *before* the
source and the monorepo stays navigable at 55k LOC.

**Plan:** one doc per `internal/` package in `docs/`:
`docs/agent.md`, `docs/tools.md`, `docs/skills.md`, `docs/mcp.md`, `docs/tui.md`,
`docs/provider.md`, `docs/lsp.md`, `docs/benchmark.md`, `docs/session.md`, `docs/server.md`.
Each: purpose, entry points, key types, invariants (e.g. "every tool call has a timeout").
Mirrors the `cohesive-doc-update` pattern already used on README/DESIGN.

**Files:** `docs/*.md` (new, ~10 files).

---

### A8 — Benchmark gate for prompt/tool changes (metaharness-style)  `[candidate]`

**What omp does:** a metaharness benchmarks prompt variants per model; prompts are tuned against
real task suites before shipping.

**Plan:** `internal/benchmark/` already exists (cases, executor, metrics, report) — add a
`baselines.json` + make `./rebuild.sh` (or `eling bench`) compare against it when
prompt/tool-schema files change; fail the build on >X% regression. Mirrors the discipline that
kept omp's edit success at +5pp.

**Files:** `internal/benchmark/`, `rebuild.sh`.

---

### A9 — DAP integration (28 debug ops)  `[v2 candidate]`

**What omp does:** full Debug Adapter Protocol — breakpoints, stepping, watches.

**Why v2:** heavier than LSP (needs a debug server per language), and Termux debugging UX is
limited. The transport (JSON-RPC over stdio) is identical to LSP, so A6 should land first and
DAP can reuse the same plumbing.

---

### A10 — Mnemosyne-style learnings file  `[candidate]`

**What omp does:** an explicit memory backend the agent reads at session start.

**Plan:** ELING already has `internal/agent/memory.go` + skill auto-learn/forget (cap 100) +
semantic index. Only gap: a **structured `~/.eling/learnings.md`** (top 20 durable lessons)
injected into the system prompt at boot, promoted automatically from skill usage counts.
Small, high-leverage.

**Files:** `internal/agent/memory.go`, `internal/skills/`.

---

## ❌ Not Adoptable / Out of Scope (oh-my-pi)

- **Rust natives core** — ELING is Go single-binary; perf-critical search already covered by ugrep.
- **Bun runtime / TS surface** — architectural non-fit (Termux + Go binary is the whole point).
- **collab-web, browser, marketplace/extensions, pi-voice** — need GUI/desktop or heavy deps; v2+.
- **Vouch-based PR review CI** — process, not code (and ELING is single-dev).

## 🧪 Testing & Safety (same rules as Part I)

1. `./rebuild.sh` mandatory before commit; `go test -race ./...` for A3/A4 (concurrency).
2. `create_backup` before each phase.
3. A1–A2 are quick wins (S) → land first. A3–A4 behind config flags, default off.
4. Update `docs/` (A7) in the same commit as any phase.
5. Bump version via `go-version-bump` on milestones.

---

# ✅ Verification Audit (2026-08-02) — race / double-function / effectiveness

Audited every candidate against the real codebase (`/root/eling`). Result per item:
🟢 safe · 🟡 amend before implementing · 🔴 reframe (would duplicate or regress).

| # | Race | Double-func | Effective? | Verdict |
|---|------|-------------|------------|---------|
| A1 | 🟡 `editExecute` is read-modify-write with **no lock** (`files.go:289`) | 🟢 no `applyPatch`/hash func exists | 🟢 **real win**: `strings.Replace(...,1)` replaces *first occurrence* — ambiguous when old_str repeats; hash anchoring fixes it | 🟡 amend (see below) |
| A2 | 🟢 static table | 🔴 **`setupPresets()` already exists** (`cli/setup.go:341`, 6 providers) + wizard `catalogEntry` | 🟡 two catalogs = drift (grep-wrapper déjà vu) | 🔴 reframe as **extraction**, not new file |
| A3 | 🔴 **`cleanupSandbox()` prunes `run-*` dirs by mtime** — can delete an active session shell's cwd; no per-shell serialization → concurrent cmds interleave on one PTY; `destructiveCommand` guard bypassed per-cmd; `scrubEnv` changes shell env | 🟢 no `shell.go` | 🟡 bash -ic + PTY prompt-boundary parsing is hard; env differs from fresh exec | 🔴 needs 3 fixes below |
| A4 | 🟡 kernel orphans on `Server.Shutdown` (only HTTP stopped, `server.go:102`); port TOCTOU if picked manually | 🟢 no `kernel.go`; no `/tool` route | 🟢 `Registry.Execute` is RWMutex-guarded — loopback is feasible | 🟡 amend (see below) |
| A5 | 🟢 brain/session reads are safe | 🔴 **`eling stats` EXISTS** (`cli.go:64` → `cmdStats` brain stats) + `/stats` TUI (`main.go:664` → `GetStats` returns tokens/tools/sessions) | 🔴 new `internal/stats/` + new command = shadow conflict | 🔴 reframe as **extend cmdStats/GetStats** |
| A6 | 🟢 `writeMu` (lsp.go:98) + `mu` already serialize stdin & seq | 🟢 no `Rename` func | 🟢 gopls transport + `real_gopls_test.go` ready | 🟢 but apply edits via `backupFile`/`editExecute`, not raw `applyEdit` |
| A8 | 🟢 build-time only | 🟢 no `bench` subcommand; no baselines.json | 🟡 gate in `rebuild.sh` = **slower builds** (user already flagged build time) + needs API key | 🟡 opt-in `--bench` flag, never default |
| A10 | 🟢 boot-time read; separate file | 🟢 `memory.go` has Remember/Recall only | 🟢 | 🟢 use atomic write + rotation |

## 🔧 Amendments locked in (must be applied when implementing)

1. **A1** — add per-file `sync.Mutex` map in `files.go` around read-modify-write; hash verify also
   returns **occurrence index** when old_str matches N times (kills first-match ambiguity).
2. **A2** — DELETE `setupPresets()` + wizard `catalogEntry`; move table into
   `internal/provider/catalog.go`; `cli/setup.go` consumes it. One source of truth.
3. **A3** — (a) per-shell mutex serializes every command; (b) persistent shells register their
   sandbox dir with `cleanupSandbox` exclusion list; (c) re-run `destructiveCommand` per command
   inside the shell; (d) keep `shell.persistent` default **off**.
4. **A4** — (a) kill kernels in `Server.Shutdown`; (b) port via `net.Listen("127.0.0.1:0")` +
   pass actual port (no TOCTOU); (c) auth token as header, never in child env.
5. **A5** — extend `cmdStats` (`cli.go:790`) + `Agent.GetStats` (`agent.go:1780`) with
   tool-success %, latency, per-provider spend. No new `internal/stats/`, no new subcommand.
6. **A8** — `eling bench --gate <baselines.json>` opt-in; `rebuild.sh` untouched.
7. **A6** — `lsp_rename` result goes through `backupFile()` + `editExecute` so backups/rotation
   are preserved.

## 📚 Reference (Part II)

- oh-my-pi repo: https://github.com/can1357/oh-my-pi — local clone: `/root/oh-my-pi`
- Key omp subsystems to study before implementing:
  - `packages/hashline` (A1), `packages/model-catalog` (A2), `crates/pi-shell` (A3),
    `packages/metaharness` (A8), `packages/mnemopi` + `docs/mnemosyne-memory-backend.md` (A10),
    `packages/natives` + `crates/pi-natives` (perf core — read for ideas only)
- omp docs map: `docs/` in `/root/oh-my-pi` (~90 files, 1:1 per subsystem)
