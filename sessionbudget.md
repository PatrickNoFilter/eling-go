# Session Budget — Implementation Plan

**Date:** 2026-08-08
**Status:** Proposal (pending approval)
**Scope:** Add a session-scoped resource budget to ELING. This is the aggregate
safety net that does **not** exist today. Per-turn timeouts and per-tool budgets
already exist; this plan adds the *session-level* bounds.

## Implementation status (updated 2026-08-08)

| Step | Status | Commit |
|---|---|---|
| 1. Config fields (`max_duration_sec`/`max_turns`/`idle_timeout_sec`) | ✅ done | `52c160a` |
| 2. `internal/budget` package (Enforce/BeginTurn/EndTurn, `Exceeded`) | ✅ done | `743fb7b` |
| 3. Root deadline — `--run` + REPL `replCtx` | ✅ done | `94fc20d` |
| 4. Turn-count bucket — REPL loop | ✅ done | `94fc20d` |
| 5. Idle stopwatch — **REPL done; TUI pending** | 🟡 partial | `94fc20d` |
| 6. CLI exposure — `/session` live display done; `config` CLI keys + `/sessionbudget` pending | 🟡 partial | `94fc20d` |
| P2 follow-up — `ELING_SESSION_MAX_DURATION_SEC` env override for automate/benchmark | ⬜ pending | — |

All knobs default `0 = off`; fresh-install behavior unchanged (verified by
`go build ./... && go vet ./... && go test ./...`).

---

## 1. Why this exists (the gap)

Every timeout/budget ELING has today is **per-turn** (one `Ask`/`AskStream`
call) or **per-tool** (one tool invocation). There is **no aggregate bound**
across a whole process / session. Concretely:

| Dimension | Existing bound | Gap |
|---|---|---|
| Wall-clock per turn | `max_turn_duration` (self-adaptive retries) | **No total process wall-clock** |
| Tool rounds per turn | `max_turn_rounds` (falls back to `MaxInt32`) | unlimited |
| Tool-loop msgs per turn | `maxMessagesInToolLoop = 100` (internal) | per-loop only |
| Context window | `max_context` (32K) + trimming | trims messages, not turns |
| **Aggregate wall-clock / turns / idle** | — | **absent** |

### Evidence (current source)
- The top-level turns run against a **deadline-less** root context:
  - `main.go:617` → `ag.Ask(context.Background(), prompt)` (single `--run`)
  - `main.go:793` → `ag.Ask(replCtx, input)` (REPL loop; `replCtx` only cancelled
    by SIGINT/SIGTERM, so a chatty session never bounds itself)
  - `internal/tui/tui.go:1018` → `ag.Ask(ctx, text, ...)` (interactive TUI)
  - `internal/automate/automate.go:246` → `a.Ask(ctx, goal)` (automated runner)
  - `internal/benchmark/executor.go:43` → `e.ag.Ask(ctx, tc.Input)` (benchmark)
- Per-turn timeout gate lives inside `Ask`/`AskStream`, applied **per call**
  (`internal/agent/agent.go:500` and `:1371`). `defaultMaxTurnDuration = 0`
  (`agent.go:382`) means **no timeout by default** even per-turn.
- `SessionConfig` currently holds **only** `AutoSave` + `SaveDir`
  (`internal/config/config.go:193-196`). There is no session budget config.
- The process's only automatic exit paths are user signals (SIGINT/SIGTERM → `replCancel()`
  at `main.go:755-760` and the crash handler). Nothing auto-bounds idle or aggregate time.
- The auto-save timer runs `ag.SaveState()` every 5 min (`main.go:571-587`), so an
  idle/terminated session already has a recent checkpoint — safe to auto-save+exit.

### Architectural rationale (do NOT add a single global wall-clock)
A blanket always-on wall-clock timeout is the *wrong* tool:
- It would kill legitimate long interactive sessions for no benefit.
- The correct answer is a **scoped, opt-in session budget** — three orthogonal
  knobs, all `0 = off` so fresh-install behavior is unchanged.
- Enforcement lives at the **root context** in `main.go` + two light checks in
  the REPL/TUI loops, layered **on top of** (not replacing) the per-turn logic.

---

## 2. Design

### 2.1 New config (three knobs, all `0` = off)

Extend `SessionConfig` (`internal/config/config.go:193`):

```go
type SessionConfig struct {
    AutoSave bool   `yaml:"auto_save"`
    SaveDir  string `yaml:"save_dir"`

    // MaxDurationSec is a total wall-clock cap for the whole process/session,
    // enforced as a root context.WithTimeout. 0 = off. Mainly for
    // --run, automate, serve, benchmark (unattended surfaces).
    MaxDurationSec int `yaml:"max_duration_sec"`

    // MaxTurns caps the number of user Ask turns in a session. 0 = off.
    MaxTurns int `yaml:"max_turns"`

    // IdleTimeoutSec auto-saves and exits after N seconds with no user
    // activity. 0 = off. Works for REPL and TUI (interactive surfaces).
    IdleTimeoutSec int `yaml:"idle_timeout_sec"`
}
```

Defaults in `DefaultConfig()` (`config.go:227`) remain all-zero (off) so existing
behavior is preserved. Verified by config test.

### 2.2 Enforcement layers

| Layer | Where | What it enforces |
|---|---|---|
| A. Root deadline | `main.go` root context (`--run`, REPL, TUI) | `MaxDurationSec` |
| B. Turn-count bucket | `main.go` REPL loop + `tui.go` | `MaxTurns` |
| C. Idle stopwatch | REPL loop + `tui.go` + TUI model | `IdleTimeoutSec` |
| D. Per-turn (unchanged) | `internal/agent/agent.go` | existing turn timeouts |

Layers B and C count/elapse **only** between turns (not while a turn is running),
so an active long turn is never killed by the session knobs — that remains the
per-turn timeout's job.

---

## 3. Concrete changes (listed, atomic, each commit-able)

### 3.1 Config fields
- `internal/config/config.go`
  - Extend `SessionConfig` struct (above).
  - Add `Session: SessionConfig{ ... }` entries to `DefaultConfig()` — all zero.
  - *(No schema migration needed: zero values = off, spotless.)*
- `internal/config/config_test.go`
  - Add test: default session budget all-zero; roundtrip nonzero values.

### 3.2 Root deadline (Layer A — unattended surfaces)
- `main.go`
  - After `flag.Parse()` / config load, if `cfg.Session.MaxDurationSec > 0`,
    build `timeoutCtx, timeoutCancel := context.WithTimeout(context.Background(),
    time.Duration(cfg.Session.MaxDurationSec)*time.Second)`.
  - Use `timeoutCtx` as the parent for:
    - `main.go:617` → `ag.Ask(timeoutCtx, prompt)` (non-interactive)
    - REPL: derive `replCtx` from `timeoutCtx` instead of `context.Background()`
      (`main.go:755`).
  - Only `defer timeoutCancel()` (don't force-exit on the non-interactive path
    before printing / saving — let the Ask return a deadline-exceeded error,
    which the existing error branch at `main.go:622-626` already handles).
- `internal/automate/automate.go` + `internal/benchmark/executor.go`
  - They build their own `ctx` (deadline-less) before calling `a.Ask(ctx, ...)`.
    A follow-up step can wire an `ELING_SESSION_MAX_DURATION_SEC` env override so
    automated surfaces inherit the budget without config plumbing.
  *(Mark as a P2 follow-up, not a blocker.)*

### 3.3 Turn-count bucket (Layer B)
- `main.go` REPL loop (the `for` at `main.go:~770`): before each turn,
  increment `turnCount`; if `MaxTurns > 0 && turnCount > MaxTurns`, print a
  message like `session limit reached (max N turns)` and `replDone`.
- `internal/tui/tui.go:1018` path: keep a counter in the TUI model; when the
  budget is hit, send an app-level quit (`p.Quit()`), the same way `/quit`
  already works.
- `MaxTurns` counts **user turns**, not tool rounds (tool rounds are already
  bounded by `max_turn_rounds` / `maxMessagesInToolLoop`).

### 3.4 Idle stopwatch (Layer C)
- `internal/tui/tui.go` model:
  - On `Msg` handling reset an `idleTimer`. A `ticker` goroutine wakes every
    ~5s; if `time.Since(lastActivity) > IdleTimeoutSec` then auto-save the
    agent (`ag.SaveState()` — same call as auto-save timer, `main.go:571`) and
    `return cmd.Quit`.
  - Ensure the idle timer is reset on user input msgs, not on agent output
    (a long turn should not count as "idle").
- `main.go` REPL loop:
  - Pair a `lastActivity` + ticker in the `select` already present; on idle
    expiry, `ag.SaveState()` then `goto replDone`.
- Add `IdleTimeoutSec` surfaced in `/session` command output and a small
  `/sessionbudget` command to print live budget state (off by default).

### 3.5 Logging / observability
- Add a shared helper, e.g. in `internal/logger` or a new `internal/budget`:
  - `Budget{ MaxDurationSec, MaxTurns, IdleTimeoutSec int }` struct with
    - `Enforce(ctx) (ctx context.Context, cancel, ok bool)`
    - `BeginTurn()`, `EndTurn()` (turn counters / idle activity markers)
    - Returns a structured `BudgetExceeded` error when any knob fires.
- This makes the whole thing testable in isolation and re-used by REPL/TUI.

---

## 4. Tests (all new in the test suite)
- `config_test.go` — defaults all zero; roundtrip `max_duration_sec` /
  `max_turns` / `idle_timeout_sec`.
- `internal/budget_test.go`:
  - `MaxTurns` exceeds → `BudgetExceeded{turn budget}` after N turns.
  - `MaxDurationSec` → derived context's deadline is before now after the budget.
  - `IdleTimeoutSec` → after advancing a fake clock beyond idle, `IsIdleExceeded`.
  - Zero-config → no deadline, no counters, `IsArmed() == false`.
- REPL end-to-end-ish smoke: run `--run` with `session.max_duration_sec: 1` and a
  slow prompt; assert process exits with the expected deadline error path
  (manual/integration, since it needs a model call).

---

## 5. Rollout / compatibility

- **Backward compatible:** all knobs default `0` → no behavior change.
- **Config parity:** `config` CLI command (`internal/cli/cli.go:1627-3016`) already
  reads+sets `cfg.Session.AutoSave`; add the three new keys the same way.
- **Docs:** extend sample `config.toml`/`config.yaml` template if one is shipped.

---

## 6. Recommended implementation order (atomic commits)

1. **`feat(config): add session budget fields (max_duration_sec, max_turns, idle_timeout_sec)`**
   — config struct + defaults + tests. Green (build+vet+test).
2. **`feat(budget): add internal/budget package (Enforce/BeginTurn/EndTurn, BudgetExceeded)`**
   — unit tests; no callers yet. Green.
3. **`feat(main): enforce session max_duration_sec via root context deadline`**
   — wire `main.go:617` + REPL `replCtx` (`main.go:755`) to a deadline. Green.
4. **`feat(main): enforce session max_turns in REPL loop`**
   — counter + expiry → `replDone`. Green.
5. **`feat(tui): enforce session idle_timeout_sec in TUI model`**
   — idle stopwatch + auto-save + `cmd.Quit()`; plus REPL idle handling. Green.
6. **`feat(cli): expose session budget in /session and /config`**
   — optional nice-to-have. Green.

Each step independently verifiable: `go build ./... && go vet ./... && go test ./...`.

---

## 7. Decision recap (what we chose NOT to do)

- **No single global wall-clock timeout** for interactive sessions — a long
  legitimate session must never be killed by a blanket timer.
- **No auto-termination** when `IdleTimeoutSec` and `MaxDurationSec` are both `0`
  (the default). Abandonment is the user's call; the budget is opt-in.
- `MaxDurationSec` and `IdleTimeoutSec` do **not** cut off an in-flight turn —
  per-turn `max_turn_duration` remains the only thing that does. This keeps
  the layers responsibly separate.