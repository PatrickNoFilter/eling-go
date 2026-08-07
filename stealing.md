# 🧬 STEALING.md — Feature Heist Plan (Qwen Code + oh-my-pi)

> **Mission:** Port the most valuable capabilities from
> [QwenLM/qwen-code](https://github.com/QwenLM/qwen-code) (26.5k ⭐, Apache-2.0, TypeScript)
> **and** [can1357/oh-my-pi](https://github.com/can1357/oh-my-pi) (21.2k ⭐, Rust + TypeScript)
> into ELING (Go, Termux-native).
>
> **Part I** (below) = Qwen Code heist (Phases 1–5, all ✅ DONE).
> **Part II** (appended 2026-08-02) = oh-my-pi adoption list — **6 ✅ implemented (A1, A2, A5, A6, A7, A10),
> 1 ⏳ deferred (A3)**. Re-audited 2026-08-02, A6 landed 2026-08-03.
> **Part III** (appended 2026-08-04) = DeepCode heist — D1–D7 candidates; sprint order D1 → D2 → D4 → D6 → D3. **All landed 2026-08-07** (D1 `49372ca`, D2 `b7d26e4`, D4 `8f733ae`, D6 `bdf1818`+`c6117eb`, D7 `a019885`, D3 `26a2a2f`). D3 gated default off.
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
> **What omp does better than us:** tool-call *reliability* (hash-anchored edits), IDE-grade code
> intelligence (14 LSP ops — we adopted rename, A6), persistent execution shells (A3), a 40+
> provider model catalog, and a docs-per-subsystem map (~90 docs) that makes a 55k-LOC codebase
> navigable. Kernels→tool loopback, DAP, and benchmark-gated prompts were **audited and dropped**
> (2026-08-03): loopback = risky nested execution; DAP = heavy, weak Termux UX; benchmark gate =
> slower builds + API cost on free tier.
>
> **Rule:** same as Part I — reimplement ideas, never copy code. ELING is Go single-binary by
> design; omp's Rust natives (search/grep/shell) are **already equivalent** in ELING via ugrep 7.5.0.

## 📊 Ranked Adoption List (Value/Effort)

| # | Adoption | Status | Effort | Value | ELING anchor (today) |
|---|----------|--------|--------|-------|----------------------|
| A1 | **Hash-anchored edits** (`hashline`-style) | ✅ | S | 🔥🔥🔥 | `internal/tools/files.go` (hash anchor + occurrence) |
| A2 | **Model catalog** (40+ providers, per-model tuning) | ✅ | S–M | 🔥🔥🔥 | `internal/provider/catalog.go` (single source, v0.4.3) |
| A3 | **Persistent shell session** (`pi-shell`-style) | ⏳ | M | 🔥🔥 | `internal/tools/bash.go` + `sandbox.go` |
| A5 | **`eling stats` dashboard** (`omp stats`-style) | ✅ | S | 🔥🔥 | `Registry.Stats()` + `GetStats` + `eling stats` CLI (persisted snapshot) — **v0.4.4** |
| A6 | **LSP rename wiring** (`willRenameFiles` / `applyEdit`) | ✅ | S–M | 🔥🔥 | `internal/lsp/lsp.go` + `internal/tools/lsp_rename.go` (lsp_rename tool, 2026-08-03) |
| A7 | **Docs-per-subsystem** (1 doc per `internal/` package) | ✅ | S (docs) | 🔥🔥 | `docs/` — 9 subsystem docs + README index |
| A10 | Mnemosyne-style learnings file read at session start | ✅ | S | 🔥 | `internal/learnings/` (journal ✓, system-prompt injection at boot ✓) — **v0.4.4** |

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
round-trips on big refactors. A2 (model catalog) has landed (v0.4.3), so this is the next
natural quick win.

**Files touched:** `internal/tools/files.go`, `internal/tools/schema.go`,
`internal/tools/files_anchor_test.go`.


---

### A2 — Model catalog (40+ providers, per-model metadata)  `[IMPLEMENTED 2026-08-02 v0.4.3]`

**What omp does:** a catalog package knows every provider's models: context window, pricing
tier, reasoning support, default base URLs, API-key env vars — the agent picks the right model
for the job and the CLI auto-fills config.

**Why we wanted it:** our provider layer was one hand-written `deepseek.go` + config keys. The
setup-wizard pain ("tokenrouter wizard not working") existed partly because defaults were
hard-coded in one place.

**What shipped (commits `500907d` + `36dce32`, v0.4.3):**
1. **`internal/provider/catalog.go`** — single-source static table: `provider → models[] →
   {context_window, supports_reasoning, tier, default_base_url, env_var}` covering
   opencode-zen, tokenrouter, qwen, deepseek, openai-compatible + common free providers.
2. **`setupPresets()` DELETED** from `cli/setup.go` — the wizard now consumes `catalog.go`
   (amendment #2 applied: one source of truth, no dual-catalog drift).
3. **`catalog_test.go`** — drift-guard test (`TestCatalogMatchesSetupPresets`) fails if wizard
   presets ever diverge from the catalog again.

**Files:** `internal/provider/catalog.go` (new), `internal/provider/catalog_test.go` (new),
`internal/provider/deepseek.go`, `internal/cli/setup.go`, setup wizard scripts.

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

### A5 — `eling stats` dashboard (omp stats-style)  `[IMPLEMENTED 2026-08-02 v0.4.4]`

**What omp does:** `omp stats` shows tokens, sessions, tool-call success rates, per-model spend.

**What shipped (amendment #5 — commits `c93e57d`/v0.4.2 first half, `a1ae10f`/v0.4.4 second half):**
1. **`Registry.Stats()`** (`internal/tools/registry.go:165`) — tool_calls, tool_failures,
   tool_success_rate, tool_avg_latency_ms + per-tool breakdown.
2. **`Agent.GetStats`** (`internal/agent/agent.go`) — merges registry stats with
   `providerStatsSnapshot` (per-provider calls/failures/success_rate/avg_latency_ms/last_call);
   also exposes `learnings` count.
3. **`/stats` TUI + REPL commands** (`main.go` / `tui.go`) — iterate `GetStats`, merged fields render.
4. **`cmdStats` CLI extended** (`internal/cli/cli.go`) — `eling stats` now renders a
   **🛠️ Runtime Metrics (last session)** section (nested per_tool + provider breakdown) read
   from the persisted snapshot.
5. **Persistence** (`internal/agent/stats_store.go`): `Agent.SaveStats()` writes live tool +
   provider metrics to `~/.eling/stats.json` on graceful shutdown (defer in `main.go`);
   `LoadStats()` reads it back for the CLI. Fresh installs get a friendly
   "run an interactive session first" hint.
6. **Tests** — `internal/tools/stats_test.go` (existing) + `internal/agent/stats_store_test.go`
   (roundtrip, missing-file, path isolation).

Per amendment #5: no new `internal/stats/` package, no new subcommand — `cmdStats` extended in place.

---

### A6 — LSP rename wiring (willRenameFiles / applyEdit)  `[IMPLEMENTED 2026-08-03]`

**What omp does:** 14 LSP ops incl. safe rename (`willRenameFiles` → `workspace/applyEdit`).

**Why we want it:** Phase 3 gave us diagnostics feedback via gopls
(`internal/lsp/lsp.go`); rename is the natural next step and makes refactors land cleanly
instead of string-swapping.

**What shipped:**
1. **`internal/lsp/lsp.go`** — `textDocument/rename` request (`Server.rename`, `Manager.Rename`,
   package-level `Rename`), server-initiated `workspace/applyEdit` handling (`handleApplyEdit`,
   `parseApplyEdit`, `respond` — the read loop now acks server requests so the server never
   blocks), `TextEdit` type, `SetApplyEditHandler` hook.
2. **`internal/tools/lsp_rename.go`** — registers the `lsp_rename` tool (file_path + 0-based
   line/col + new_name), opens the file via `Diagnostics` first (gopls refuses rename on unseen
   documents), then applies edits through the **same safety net as edit/write**: per-file
   `lockFile` + `backupFile` + `isTextFile` binary guard + reverse-document-order application
   with UTF-16 column mapping (`lspOffset`) so non-ASCII files stay correct.
3. **Server-pushed applyEdit routing** — `SetApplyEditHandler` installed at tools init so
   workspace/applyEdit pushes (rename code actions, refactors) land through backup+lock too.

**Files:** `internal/lsp/lsp.go`, `internal/lsp/lsp_test.go`, `internal/tools/lsp_rename.go`,
`internal/tools/lsp_rename_test.go`.

---

### A7 — Docs-per-subsystem  `[IMPLEMENTED 2026-08-02]`

**What omp does:** ~90 docs files mapping 1:1 to subsystems — you read the doc *before* the
source and the monorepo stays navigable at 55k LOC.

**What shipped (commit `c93e57d`, v0.4.2):** `docs/` now has **9 subsystem docs** + index —
`docs/agent.md`, `docs/tools.md`, `docs/skills.md`, `docs/provider.md`, `docs/mcp.md`,
`docs/tui.md`, `docs/lsp.md`, `docs/session.md`, `docs/server.md` (each: purpose, entry
points, key types, invariants — e.g. "every tool call has a timeout") plus `docs/README.md`
as the index. `docs/benchmark.md` deferred (no benchmark CLI yet).

**Files:** `docs/*.md` (9 subsystem docs + README index), `README.md` (links to `docs/`).

---

### A10 — Mnemosyne-style learnings file  `[IMPLEMENTED 2026-08-02 v0.4.4]`

**What omp does:** an explicit memory backend the agent reads at session start.

**What shipped (commits `c93e57d`/v0.4.2 + `a1ae10f`/v0.4.4):**
1. **`internal/learnings/learnings.go`** (Load / Append / Count / Path, atomic write + rotation
   per amendment) + `internal/learnings/learnings_test.go`.
2. **`eling learnings` CLI** (`cmdLearnings`, `cli.go`: list + `add "lesson"`).
3. **Boot-time count log** (`main.go`: `📓 N learning(s) loaded from ~/.eling/learnings.md`).
4. **✅ System-prompt injection (was the gap):** `Agent` loads learnings in `New()` (boot) into
   `a.learnings`; `buildMessages()` injects a `[Durable learnings from past sessions — apply
   when relevant]` system message into **every turn** (capped to the last 10 entries to protect
   small local-model budgets). `Agent.Learn("...")` persists + refreshes the in-memory slice
   immediately; `GetStats` exposes the `learnings` count.
5. **Tests** — `internal/agent/learnings_inject_test.go` (boot load, buildMessages injection,
   Learn() journal + memory, GetStats count).

**Files:** `internal/learnings/learnings.go` (+test), `internal/cli/cli.go` (learnings cmd),
`internal/agent/agent.go` (boot load + per-turn injection + Learn), `internal/agent/learnings_inject_test.go`.

---

## ❌ Not Adoptable / Out of Scope (oh-my-pi)

- **Rust natives core** — ELING is Go single-binary; perf-critical search already covered by ugrep.
- **Bun runtime / TS surface** — architectural non-fit (Termux + Go binary is the whole point).
- **collab-web, browser, marketplace/extensions, pi-voice** — need GUI/desktop or heavy deps; v2+.
- **Vouch-based PR review CI** — process, not code (and ELING is single-dev).

## 🧪 Testing & Safety (same rules as Part I)

1. `./rebuild.sh` mandatory before commit; `go test -race ./...` for A3 (concurrency).
2. `create_backup` before each phase.
3. A1–A2 are quick wins (S) → land first. A3 behind config flag, default off.
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
| A5 | 🟢 brain/session reads are safe | 🔴 **`eling stats` EXISTS** (`cli.go:64` → `cmdStats` brain stats) + `/stats` TUI (`main.go:664` → `GetStats` returns tokens/tools/sessions) | 🔴 new `internal/stats/` + new command = shadow conflict | 🔴 reframe as **extend cmdStats/GetStats** |
| A6 | 🟢 `writeMu` (lsp.go:98) + `mu` already serialize stdin & seq | 🟢 no `Rename` func | 🟢 gopls transport + `real_gopls_test.go` ready | 🟢 but apply edits via `backupFile`/`editExecute`, not raw `applyEdit` |
| A10 | 🟢 boot-time read; separate file | 🟢 `memory.go` has Remember/Recall only | 🟢 | 🟢 use atomic write + rotation |

**Post-audit status (2026-08-02, updated for v0.4.4):** A2 ✅ (v0.4.3, single-source catalog), A5 ✅
(v0.4.4, Registry.Stats + GetStats + `eling stats` CLI with persisted snapshot), A7 ✅ (v0.4.2, 9 docs),
A10 ✅ (v0.4.4, journal + CLI + system-prompt injection per turn via buildMessages + Learn()). A1
already ✅ above. **A6 ✅ (2026-08-03, lsp_rename tool + applyEdit safety net).** A3 still ⏳.

## 🔧 Amendments locked in (must be applied when implementing)

1. **A1** — add per-file `sync.Mutex` map in `files.go` around read-modify-write; hash verify also
   returns **occurrence index** when old_str matches N times (kills first-match ambiguity).
2. **A2** — DELETE `setupPresets()` + wizard `catalogEntry`; move table into
   `internal/provider/catalog.go`; `cli/setup.go` consumes it. One source of truth.
3. **A3** — (a) per-shell mutex serializes every command; (b) persistent shells register their
   sandbox dir with `cleanupSandbox` exclusion list; (c) re-run `destructiveCommand` per command
   inside the shell; (d) keep `shell.persistent` default **off**.
4. **A5** — extend `cmdStats` (`cli.go`) + `Agent.GetStats` (`agent.go`) with tool-success %,
   latency, per-provider spend. No new `internal/stats/`, no new subcommand. ✅ Applied 2026-08-02.
5. **A6** — `lsp_rename` edits go through `lockFile()` + `backupFile()` + `isTextFile()` (the same
   primitives `editExecute` uses), NOT raw `applyEdit` writes. ✅ Applied 2026-08-03. *(Deviation
   from draft wording: edits are positional LSP ranges, so they bypass `editExecute`'s
   string-occurrence API — but the backup/lock/binary-guard safety net is identical.)*

## 📚 Reference (Part II)

- oh-my-pi repo: https://github.com/can1357/oh-my-pi — local clone: `/root/oh-my-pi`
- Key omp subsystems to study before implementing:
  - `packages/hashline` (A1), `packages/model-catalog` (A2), `crates/pi-shell` (A3),
    `packages/mnemopi` + `docs/mnemosyne-memory-backend.md` (A10),
    `packages/natives` + `crates/pi-natives` (perf core — read for ideas only)
- omp docs map: `docs/` in `/root/oh-my-pi` (~90 files, 1:1 per subsystem)

---
---

# 🧠 PART III — DeepCode Adoption List (candidates, not yet implemented)

> **Source:** [HKUDS/DeepCode](https://github.com/HKUDS/DeepCode) (16.2k ⭐, "Open Agentic Coding" —
> ideas → production-ready code; HKU Data Science lab, same group as LightRAG). Local clone:
> `/root/deepcode` (depth-1, clone before Phase D2 study).
>
> **What DeepCode does better than us:**
> 1. **Evidence-driven completion** — picks the *appropriate* verification for the task (tests /
>    build / static diagnostics / diff), runs it, and **a failed verification is never reported as
>    success** — the failure feeds the next repair iteration.
> 2. **Multi-agent parallelism done safely** — focused agents work in **isolated git worktrees**
>    with explicit conflict surfacing (never silent clobber).
> 3. **Project rules ingestion** — reads the repo's own `AGENTS.md`/`DEEPCODE.md`/`CLAUDE.md` into
>    its loop so engineering rules steer every turn.
> 4. **Scheduled automations** — saved workflows run on cron (regression scans, test-repair, docs).
> 5. **Per-tool permission profiles** — allow/ask/deny per tool + per-project trust.
>
> **Audited 2026-08-04 against `/root/eling`:** our worktree infra (Phase 1), event hooks (Phase 5),
> LSP diagnostics (Phase 3), plan mode (Phase 2), and per-turn learnings injection (A10) are the
> foundations DeepCode builds on — **we're ahead on infra, behind on the verify→repair loop and
> rules self-ingestion**.
>
> **Rule:** same as Parts I & II — reimplement ideas, never copy code. ELING is Go single-binary by
> design; DeepCode's search/shell/static surfaces are **already equivalent** via ugrep 7.5.0 +
> sandbox + LSP.

## 📊 Ranked Adoption List (Value/Effort)

| # | Adoption | Status | Effort | Value | ELING anchor (today) |
|---|----------|--------|--------|-------|----------------------|
| D1 | **Project rules ingestion** (AGENTS.md/CLAUDE.md/DEEPCODE.md → system prompt) | ✅ 2026-08-06 | S | 🔥🔥🔥 | `internal/layers/rules_ingest.go` (new); `agent.go` boot-load + `buildMessages()` inject (A10 path); `cli.go` `eling rules show`/`--refresh` |
| D2 | **Verify→Repair loop** (evidence-driven completion / Loop Engineering) | ✅ 2026-08-06 | S–M | 🔥🔥🔥 | `internal/verify/evidence.go` + `loop.go` (new); `agent.go` `verifyToolCalls()` gate + `Round()`/`Final()` (wired, default ON); `verify.max_rounds` (not global `maxToolRounds`); `--no-verify` + plan-mode opt-out |
| D3 | **Multi-agent parallelism in isolated worktrees** (conflict-surfaced) | ✅ 2026-08-07 | M | 🔥🔥 | `internal/agents/orchestrator.go` (new: bounded 2-agent split, worktree isolation, conflict surfacing, no-silent-merge); `internal/config` `agents` block gated **off** by default |
| D4 | **Scheduled automations** (`eling automate add … --schedule`) | ✅ 2026-08-07 | M | 🔥🔥 | `internal/automate/automate.go` (new: cron parse + overlap-guarded Scheduler); `cli.go` `eling automate …`; `config.go` `automate.jobs[]`; daemon starts scheduler when enabled |
| D5 | **Evidence taxonomy per task type** | ✅ 2026-08-06 (via D2) | — | 🔥 | `internal/verify/evidence.go` selector — Go → `go test ./...`/`go vet`/LSP, docs → diff; **folded into D2** (not standalone) |
| D6 | **Per-tool permission profiles** (allow/ask/deny + project trust) | ✅ 2026-08-07 | M | 🔥 | `internal/tools/permissions.go` (policy + resolution), `internal/config` `permissions` block, `eling permission …` CLI, TUI interactive ask-gate per call |
| D7 | **Atomic commit discipline** (conventional commits + build/test gate) | ✅ 2026-08-06 | XS | 🔥 | default system prompt: only the SEARCH RULE — no commit-workflow rule |

**Suggested sprint:** D1 ✅ → **D7 (XS quick win)** ✅ → D2 ✅ → D4 ✅ → D6 ✅ → D3 ✅ (quick wins first; D3 last — highest risk, gated). **All Part III candidates DONE (2026-08-07).** D2/D4/D6/D7 landed on `main`; D3 gated `/agents.Enabled=false` (safe for default installs; **not recommended on constrained hosts** — see D3 note).

---

### D1 — Project rules ingestion (AGENTS.md/CLAUDE.md/DEEPCODE.md → system prompt)  `[candidate — Phase 1, S]`

**What DeepCode does:** reads the project's own `AGENTS.md`/`DEEPCODE.md` (and Claude-style skill
rules dirs) into the agent loop, so repo-specific engineering rules steer every turn.

**Why we want it:** ELING's `internal/layers/rules.go` (exports `DetectAgents`, `WriteRules`) *writes* AGENTS.md for other agents and
*detects* them for rule generation, but **never reads a project's own `AGENTS.md`/`CLAUDE.md`/
`.cursor/rules` into its own context**. A repo with rules gets ignored — the agent re-learns
conventions by trial.

**Plan:**
1. `internal/layers/rules_ingest.go` (new, package `layers` — agent already imports it, so no
   new package / no import cycle): at `Agent.New()` / session start, probe cwd + `--dir` for
   `AGENTS.md`, `DEEPCODE.md`, `CLAUDE.md`, `.cursor/rules/*.mdc` (first match wins, in that order).
2. **Read-only** ingestion: parse, cap at ~4 KiB / 40 lines (protect small local-model budgets),
   normalize into a `[Project rules — apply when relevant]` block.
3. Inject through the **same `buildMessages()` mechanism A10 uses for learnings** (per-turn system
   message, capped) — no new plumbing in the loop.
4. `eling rules show` CLI + `eling rules --refresh` reload (extend existing `rules.go` surface).

**Files touched:** `internal/layers/rules_ingest.go` (new, package `layers`),
`internal/layers/rules.go` (extend `DetectAgents`/`WriteRules` surface for `eling rules show` /
`--refresh`), `internal/agent/agent.go` (boot load + inject), `internal/cli/cli.go`,
`internal/layers/rules_ingest_test.go` (new).

**Acceptance:**
- [x] Repo with `AGENTS.md` → rules appear in per-turn system messages (test asserts) — `TestBuildMessagesInjectsProjectRules` ✅ 2026-08-06
- [x] Missing rules file → silent skip, no crash — `TestMissingRulesSilentSkip` ✅ 2026-08-06
- [x] `./rebuild.sh` green; full suite passes ✅ 2026-08-06

**Effort:** S (half day) · **Risk:** very low

---

### D2 — Verify→Repair loop (evidence-driven completion / Loop Engineering)  `[✅ DONE 2026-08-06 b7d26e4]`

**Status:** Implemented & committed (`b7d26e4 feat(verify): wire execute-verification verify→repair loop`).
New `internal/verify` package: `evidence.go` (per-task evidence selector — Go edit →
`go test ./...` fallback `go build ./...`, no-tests → `go vet`/LSP diagnostics, docs → diff-only =
the folded D5 taxonomy) + `loop.go` (`Round()` runs evidence after each tool round; failures are
injected as the next user message for repair, bounded by the dedicated `verify.max_rounds`, default 2;
`Final()` appends an honest `Evidence:` block — PASS or STILL-FAILING, never success with failing
evidence). Wired into both agent tool loops via `verifyToolCalls()` (`agent.go`), commissioned from
`cfg.Verify` (default ON; `--no-verify` off, plan‑mode‑gated turns opted out). Per-turn reset so
evidence never leaks across Ask/AskStream turns. New `docs/verify.md` + wiring/commissioning tests
(`verify_wiring_test.go`, `verify_test.go`). Build clean, `go vet` clean, full `go test ./...` green.

**What DeepCode does:** the agent picks *appropriate verification evidence* for the task (test
output, build result, static diagnostics, diff/artifact), runs it, and **a failed verification is
never reported as success** — the failure becomes the input to the next repair iteration until
green (or honestly reported as failing).

**Why we want it:** the #1 waste in coding agents is declaring "done" after edits without checking.
ELING today: `autoTest()` (`agent.go:3088`) runs `go test` but **Go-only and fire-and-forget**
(appends output, no loop); `internal/autorepair/` repairs *broken tools*, not task completion;
`internal/layers/verify_on_stop.go` is a nudge for *other* agents, not a self loop.

**Plan:**
1. **Evidence selector** (`internal/verify/evidence.go`): pick by task — Go edit → `go test ./...`
   (fallback `go build ./...`); edit w/o tests → `go vet`/LSP diagnostics (reuse `internal/lsp`);
   docs → no verify, just diff. *(This is the D5 taxonomy — built here, not separately.)*
2. **Loop gate** (`internal/verify/loop.go`): after tool-loop edits, if `verify.enabled` (default
   on; plan-mode or `--no-verify` opts out), run evidence, parse pass/fail, and on failure inject
   `[verification failed — repair within N rounds]`, reusing `internal/autorepair/state.go`
   maxRetries/backoff (default **2 rounds**), each run time-boxed.
3. **Honest reporting**: final answer includes an `Evidence:` block (command, exit, summary).
   Never claim success with failing evidence.
4. Config: `verify: { enabled: true, max_rounds: 2, timeout_sec: 60, evidence: auto }`.

**Files touched:** `internal/verify/evidence.go` (new), `internal/verify/loop.go` (new),
`internal/agent/agent.go` (wire gate into both tool loops), `internal/config/config.go`,
`internal/verify/verify_test.go` (new).

**Acceptance:**
- [x] Introduce a Go syntax error via edit → next turn contains `[verification failed]` and the agent repairs — ✅ 2026-08-06 (`verify_wiring_test.go`)
- [x] Clean edit → `Evidence: go test … PASS` reported with the answer — ✅ 2026-08-06
- [x] `--no-verify` / plan-mode skip → no evidence block, no delay — ✅ 2026-08-06 (`--no-verify` flag + plan-mode opt-out)
- [x] `go vet` clean; full `go test ./...` green — ✅ 2026-08-06 (`b7d26e4`)

**Effort:** S–M (1–2 days) · **Risk:** low–medium (bounded by max_rounds + timeout) — **implemented**

---

### D3 — Multi-agent parallelism in isolated worktrees  `[✅ DONE 2026-08-07 — 26a2a2f; config gated default off]`

**What DeepCode does:** splits work into focused agents (investigate vs. implement vs. review)
running in **isolated git worktrees**; changes never clobber each other; conflicts are surfaced
explicitly, never silently merged.

**Why we want it:** Part I explicitly deferred SubAgents (nested API budget, race conditions,
free-tier rate limits). DeepCode's answer to "races" is exactly the worktree infra we already
shipped in Phase 1 — so this is the **v2 un-defer**, now de-risked by isolation we already own.

**Plan:**
1. **Orchestrator** (`internal/agents/orchestrator.go`): bounded **2-agent** split —
   `investigator` (read-only: search/read/plan in a read-only worktree) → `implementer` (edits in
   its own worktree). Max 2 concurrent; per-agent token budget cap.
2. **Isolation**: each subagent gets its own `worktree_create`; merge only via `worktree_merge`
   with an explicit `--review` diff report; **never silent merge**.
3. **Conflict surfacing**: if worktrees touch the same file, the merge produces a diff report to
   the main agent instead of auto-resolving.
4. Config: `agents.enabled: false` (default **off** until battle-tested), `agents.max: 2`;
   `go test -race ./internal/agents/...` in the CI gate.

**Files touched:** `internal/agents/orchestrator.go` (new), `internal/agents/agents_test.go` (new),
`internal/tools/worktree.go` (review/conflict report), `internal/config/config.go`,
`internal/agent/agent.go` (entry).

**Acceptance:**
- [x] Two-agent split on a fixture repo: both edit separate files; merge clean — test `TestTwoAgentSeparateFiles` ✅
- [x] Both edit the same file → conflict diff surfaced, no silent overwrite — test `TestSameFileConflict` ✅
- [x] Disabled by default; enabling requires explicit config — test `TestDisabledByDefault` ✅
- [x] `go test -race ./internal/agents/...` green ✅ (build/vet/test all pass)

**Effort:** M ⚡ **landed 2026-08-07 (`26a2a2f`)** · **Risk:** high (nested API budget, concurrency) → **gated, default off** (`agents.Enabled=false`)

> **⚠️ Constrained-host note (proot/Android/Termux):** D3's *parallelism* is I/O-bound and fits, but it defaults **off** for a reason. On this 2-big-core big.LITTLE + ~2.9 GiB available / swap-tight proot box, each sub-agent's local toolchain (`go build ./...` cold is >30 s here) collides onto the same 2 fast cores → CPU saturation + swap thrash. **Do not enable D3 on this device.** If ever enabled on a desktop: keep `agents.max: 2`, with `agents.max: 1` + verify-loop off for weak hosts.

---

### D4 — Scheduled automations  `[✅ DONE 2026-08-07 — 8f733ae]`

**What DeepCode does:** saves a stable workflow and runs it manually or on a schedule (regression
scans, test-repair, docs upkeep).

**Why we want it:** Phase 5 gave us event hooks; a scheduler turns them into *standing* jobs the
daemon owns — nightly `go test ./...` + auto-repair report, weekly docs-freshness scan.

**Plan:**
1. `eling automate add <name> <command|goal> --schedule "0 2 * * *"` (`internal/automate/
   automate.go` + CLI), persisted in `config.yaml` (`automate.jobs[]`).
2. Scheduler in the daemon (`internal/server/server.go`): cron-lite ticker (small internal
   5-field crontab parser — no new heavy dep), **overlap guard** (never run the same job twice
   concurrently; skip + log).
3. Runs reuse the agent loop headlessly (session-less, `--run` style); output logged to
   `~/.eling/automations.log`; failures fire `HookErrorOccurred`.
4. `eling automate list/remove/logs`.

**Files touched:** `internal/automate/automate.go` (new), `internal/cli/cli.go`,
`internal/config/config.go`, `internal/server/server.go`, `internal/automate/automate_test.go` (new) — plus `docs/automate.md` (new, shipped in the D4 docs commit).

**Acceptance:**
- [x] `eling automate add` persists; `list` shows it; daemon fires it at schedule (test with 1-minute schedule) — async CLI + Scheduler.scan fires due jobs; round-trip + firing covered in `automate_test.go`
- [x] Overlap: a slow job running → second tick skipped + logged — `TestSchedulerOverlapGuard`
- [x] `./rebuild.sh` green

**Status (2026-08-07):** Implemented + committed `8f733ae`. New `internal/automate/` package: dependency-free 5-field cron parser (`ParseCron`), `Scheduler` with per-job overlap guard (never runs the same job twice concurrently; skip + log), and `Runner` abstraction (command jobs via `/bin/sh -c`; goal jobs via a freshly-created agent — session-less, mirroring `--run`). CLI added `eling automate add|list|remove|run|enable|disable|logs` plus enable/disable-scheduler. Jobs persist in `config.yaml` `automate.jobs[]` with `LastRun`/`LastStatus` bookkeeping. Daemon (`cmdServe`) starts the scheduler when `automate.enabled`, cancels in-flight jobs on shutdown. Output appended to `~/.eling/automations.log`. Verified with go build + go vet + `go test ./...` (all packages) + `./rebuild.sh`.

**Effort:** M (1–2 days) · **Risk:** medium (daemon lifecycle, time parsing) — resolved as committed

---

### D5 — Evidence taxonomy per task type  `[✅ DONE 2026-08-06 — folded into D2, not standalone]`

DeepCode picks evidence *by task*. Rather than a separate phase, D2's evidence selector (step 1)
**is** the taxonomy — implemented in `internal/verify/evidence.go` (Go edit → `go test ./...`,
no tests → `go vet`/LSP diagnostics; docs → diff-only). Extend the table as new task types appear
(Python → pyright diagnostics via LSP, HTML/JS → eslint via LSP). No separate ticket; tracked
inside D2's `evidence.go`.

---

### D6 — Per-tool permission profiles (allow/ask/deny + project trust)  `[✅ DONE 2026-08-07]`

**What DeepCode does:** per-tool permission levels (allow/ask/deny) and per-project trust, so
destructive or sensitive tools are gated without blanket-approving everything.

**Why we want it:** plan mode gates the *whole turn*; sandbox `guard_mode` blocks a fixed list.
There's no way to say "bash: ask, but read/write/edit: allow" per project.

**Plan:**
1. Config: `permissions: { default: ask, rules: [ {tool: "bash", mode: "ask"}, {tool: "write",
   mode: "allow"}, {tool: "rm", mode: "deny"} ] }` + `projects: { "/root/eling": {trust: full} }`.
2. Enforce in `registry.ExecuteContext` (before dispatch): look up tool → mode; `deny` returns a
   blocked result; `ask` routes to the same TUI y/N gate plan mode uses (reuse the checklist
   renderer); `allow` runs (sandbox still applies).
3. **Default preserves current behavior**: `ask` for `bash`, `allow` for safe tools — no surprise
   gates on fresh install.

**Files touched:** `internal/config/config.go`, `internal/tools/registry.go`, `internal/tui/tui.go`
(ask gate), `internal/tools/permissions_test.go` (new).

**Implemented (2026-08-07):**
- `internal/tools/permissions.go` — `PermPolicy` model + resolution. Exact tool rule > longest-prefix
  project trust > default. A fully-empty policy is `inactive` (all tools allowed — fresh install
  behaviour unchanged). `NewPermPolicy` / `ValidPermMode` / `ModeFor`.
- `registry.go` — `SetPermissions` / `SetPermissionGate` / `PermissionModeFor` / `PermissionPolicy`;
  `ExecuteContext` gates before dispatch under `RLock` (deny → blocked error; ask → consults the
  gate once per call; nil gate degrades ask→allow for headless/serve/automate; allow → runs with
  sandbox still applied).
- `config.go` — `permissions { default, rules[], projects{} }` block, `Active()`.
- CLI — `eling permission list|set|unset|set-default|project|reset` (persists to `eling.yml`).
- TUI — per-call `ask` gate: `permAskMsg` mirrors the plan approver; y/N/Esc/Ctrl+C verdict logged;
  gate installed per submit and cleared on return/cancel.
- 9 unit tests in `internal/tools/permissions_test.go`.

**Acceptance:**
- [ ] `deny` on `bash` blocks with a reason; `allow` on `write` skips the gate
- [ ] `ask` prompts exactly once per call in TUI
- [ ] Fresh config → defaults match today's behavior (no surprise gates)

**Effort:** M (1–2 days) · **Risk:** low (additive; default preserves behavior)

---

### D7 — Atomic commit discipline (conventional commits + build/test gate)  `[IMPLEMENTED 2026-08-06 a019885]`

> ⚠️ **Outlier:** source is a **Claude Code skill**, not DeepCode —
> [bring-shrubbery/atomic-commits](https://github.com/bring-shrubbery/atomic-commits) (MIT, 4
> commits). Kept in Part III as a quick-win sibling candidate so all adoption items live in one list.

**Status (a019885, 2026-08-06):** Added a 5-line `ATOMIC COMMIT DISCIPLINE` paragraph to the
default system prompt in `internal/config/config.go` (next to the SEARCH RULE): plan atomic steps
→ implement ONE logical change → `go build` + `go vet` + `go test` → commit with a conventional
message → repeat; never batch unrelated changes, never leave the tree red. New
`internal/config/config_test.go` asserts the default prompt carries every fragment of the rule and
that the SEARCH RULE survived the edit. Prompt-only, no new gates/deps. `go vet` + full `go test
./...` green.

**Its own discipline applied to the commit:** `feat(prompt): D7 atomic commit discipline in
default system prompt` was itself a single atomic change, built/vetted/tested, then committed with
a conventional message.

**What it does:** enforces the commit workflow as a first-class instruction:
1. Plan work as a **numbered list of atomic steps** before coding.
2. Implement **one logical change** at a time.
3. **Commit immediately after each change** with a conventional commit message (`feat:`, `fix:`,
   `docs:`, `chore:`, …).
4. **Verify the codebase builds and tests pass after every commit.**

**Why we want it:** ELING already *practices* this — the heist golden rule ("one phase per commit;
tests must pass after each phase") and the git log (conventional-style atomic commits like
`docs(deepcode): add Part III`, `chore(backup): limit backup rotation`) prove the habit. But the
discipline lives in the plan doc + session habits, not in the runtime: the default system prompt
contains only the SEARCH RULE. Nothing tells the agent to apply atomic commits on **arbitrary**
projects, so the habit silently degrades outside the heist workflow.

**Plan:**
1. Add a short **"Atomic commit discipline"** paragraph to the default system prompt in
   `internal/config/config.go` (next to the SEARCH RULE), ~5 lines, reimplemented in our own words.
2. Order of operations in the rule: plan atomic steps → implement one change → `go build` + `go vet`
   + `go test` → commit with a conventional message → repeat.
3. No new runtime gates, no new deps — instruction text only (matches the "reimplement, never copy"
   rule; the skill's substance is a 4-rule discipline).

**Files touched:** `internal/config/config.go` (default prompt) + `internal/config/config_test.go`
(assert default prompt contains the rule).

**Acceptance:**
- [ ] Default prompt dump shows the rule (visible via `eling config`)
- [ ] Fresh install behavior unchanged beyond instruction text (no new gates/deps)
- [ ] `go vet` + `go test` green after the change

**Effort:** XS (≤ 0.5 day) · **Risk:** low (prompt-only; additive)

---

## 🟢 Verification Audit (2026-08-04) — race / double-function / effectiveness

Audited every candidate against the real codebase (`/root/eling`). Result per item:
🟢 safe · 🟡 amend before implementing · 🔴 reframe (would duplicate or regress).

| # | Race | Double-func | Effective? | Verdict |
|---|------|-------------|------------|---------|
| D1 | 🟢 read-only boot-time probe; separate file | 🔴 `rules.go` exists but only **writes/detects** for other agents — no self-ingest; no conflict | 🟢 rules steering every turn = real win | 🟢 reuse learnings injection (A10) mechanism; cap size |
| D2 | 🟢 new package; gate in tool loop is sequential | ✅ implemented `b7d26e4` — `internal/verify/evidence.go` + `loop.go`; wired via `verifyToolCalls()`; bounded by `verify.max_rounds` (not global `maxToolRounds`) | 🟢 the #1 waste in coding agents; bounded by max_rounds | ✅ done — cap rounds/timeouts; never claim success with failing evidence |
| D3 | 🔴 nested agent concurrency = race risk (the v1 deferral) | ✅ implemented `26a2a2f` — `internal/agents/orchestrator.go` (bounded 2-agent split, worktree isolation, conflict surfacing, no-silent-merge) | 🟡 real win only if isolation holds | 🟡 gated `agents.Enabled=false`; per-agent budget; `go test -race` |
| D4 | 🟢 scheduler single-goroutine ticker + overlap guard | 🟢 hooks (Phase 5) are event-driven — no scheduler exists | 🟢 | ✅ implemented `8f733ae` — `internal/automate/automate.go`; overlap guard + `~/.eling/automations.log`; no new heavy cron dep |
| D5 | — | — | — | ✅ folded into D2 (`evidence.go`) — done `b7d26e4` |
| D6 | 🟢 additive check before dispatch | 🟢 no per-tool permission infra; plan mode + guard_mode are different layers | 🟢 | 🟢 default `ask`/`allow` preserves behavior |
| D7 | 🟢 prompt-only text; no new state/goroutines | 🟢 no existing commit-workflow rule in `config.go`; distinct from D2 (runtime verify loop) | 🟢 formalizes a habit already practiced | 🟢 quick win (XS); prompt-only, no deps |

## 🔧 Amendments locked in (must be applied when implementing)

1. **D1** — ingestion is **read-only** (never overwrite the user's AGENTS.md); probe order
   `AGENTS.md` → `DEEPCODE.md` → `CLAUDE.md` → `.cursor/rules/*.mdc`; cap 4 KiB/40 lines; inject
   via `buildMessages()` like A10. New code lives in `internal/layers/rules_ingest.go` (package
   `layers`, already imported by the agent) — do **not** create a new `internal/rules` package.
2. **D2** — ✅ applied `b7d26e4`: bounded by the dedicated `verify.max_rounds` field (default 2
   rounds; **not** ELING's global `maxToolRounds`), reuse `internal/autorepair/state.go`
   maxRetries/backoff, `--no-verify` + plan-mode opt-out; each iteration checks `toolCtx.Err()` first
   to honor shutdown/config timeout; final answer always carries an `Evidence:` block; **never report
   success with failing evidence**.
3. **D3** — ✅ applied `26a2a2f`: `agents.enabled` default **off**; max 2 concurrent subagents;
   per-agent token budget cap; merges go through diff review report only (no-silent-merge);
   conflict = surfaced, never auto-merge; `go test -race ./internal/agents/...` mandatory
   (3 tests green: disabled-by-default, clean split, conflict-surfaced). Gated off, so a default
   install is unaffected.
4. **D4** — overlap guard (skip + log concurrent runs of the same job); logs to
   `~/.eling/automations.log`; failures fire `HookErrorOccurred`; no new heavy cron dep.
5. **D6** — additive only: default `ask` for `bash`, `allow` for safe tools → fresh-install
   behavior identical to today; enforce before dispatch in `registry.ExecuteContext`.

## 🧪 Testing & Safety (same rules as Parts I & II)

1. `./rebuild.sh` mandatory before commit; `go test -race ./...` for D2 and D3 (concurrency).
2. `create_backup` before each phase; one phase per commit.
3. ✅ D1 (S) landed first (`49372ca`); D2 (S–M) the headline phase landed second (`b7d26e4`); D4 (M) landed third (`8f733ae`); D6 (M) fourth (`bdf1818` + tui `c6117eb`); D3 (M, gated) fifth and last (`26a2a2f`). **All Part III candidates landed 2026-08-07.**
4. Update `docs/` (A7 subsystem docs — add `docs/verify.md`, `docs/rules.md` etc.) in the same
   commit as any phase. ✅ `docs/verify.md` shipped in `b7d26e4`; `docs/rules.md` in `49372ca`.
5. Bump version via `go-version-bump` on milestones (D2 is a strong v0.5.0 candidate). ✅ **Bumped** — latest tag is `v0.6.0` (released from HEAD `e5a5560`); snapshot-budget phase shipped under that release.

## ❌ Not Adoptable / Out of Scope (DeepCode)

- **Tauri desktop GUI** — architectural non-fit (Termux = TUI/CLI single binary).
- **Paper2Code / Text2Web / Text2Backend** — product use-cases, not agent capabilities (and ELING
  already builds offline HTML apps via its own tooling).
- **Multi-model per-loop orchestration** — already covered by the provider catalog (A2) +
  fallback chain (`getProvidersForFallback`).
- **Reimplementing DeepCode's search/shell/static surfaces** — already equivalent: ugrep 7.5.0 +
  sandbox + LSP (Phase 3).

## 📚 Reference (Part III)

- DeepCode repo: https://github.com/HKUDS/DeepCode — local clone target: `/root/deepcode`
  (clone depth-1 before Phase D2 study)
- Key DeepCode concepts to study before implementing:
  - **Loop Engineering / evidence-driven completion** (D2) — docs on goal loop + verification evidence
  - **Multi-agent worktree orchestration** (D3) — isolated worktrees + conflict surfacing
  - **Context Engineering / project rules** (D1) — `AGENTS.md`/`DEEPCODE.md` ingestion
  - **Automations** (D4) — saved workflows + scheduling
  - **Permissions** (D6) — per-tool allow/ask/deny + project trust
