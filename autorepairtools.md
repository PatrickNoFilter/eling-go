# 🔧 Auto-Repair Tools Mechanism — `autorepairtools.md`

**Status:** PLAN — Phase 0 ✅ DONE, Phase 1 ✅ DONE (probe-first fixers live), Phase 2 ✅ DONE (quarantine + `autorepair`/`tools-health` CLI + TUI indicator), Phase 3 ✅ DONE (autofix enablement + exponential backoff + commit guard + docs). All 4 phases implemented and committed (`3e0dade`, `d8f0bf1`, `307773e`, Phase 3).
**Owner:** ELING
**Branch:** main
**Grounded in:** `internal/tools/registry.go` (Registry, Tool, ExecuteContext, Execute, Stats, metrics), `internal/tools/register.go` (DynamicTool persistence → `state/tools.json`), tool init() self-registration per file (`bash.go`, `files.go`, `web.go`, `web_timeout.go`, `sandbox.go`, `ocr.go`, `setup.go`, `semantic.go`, `worktree.go`, `backup.go`, `schema.go`).

---

## 1. Problem Statement

Today ELING has a **dynamic tool registry** with per-tool metrics (`toolMetrics`:
calls, failures, latency, last_call) and a robust timeout/panic-guard in
`ExecuteContext`. However there is **no decision layer** that:

1. **Detects** when a tool is actually broken (failing repeatedly / panicking /
   timing out / returning structurally-invalid output) vs. just failing on bad
   input.
2. **Classifies** the breakage (env missing, binary missing, dependency not
   installed, MCP server down, config drift, logic bug, transient network).
3. **Decides** whether that breakage is **autofixable** (we can repair it
   ourselves), **advisory** (we can only warn + mitigate), or **fatal/quarantine**
   (disable the tool and surface it to the user).
4. **Repairs** autonomously (reinstall binary, fix config, restart MCP server,
   re-verify deps) **or** quarantines safely.

So ELING currently fails silently in a loop on broken tools (grep→ugrep drift,
MCP count 0, OCR hang, web DNS 403, stale sandbox binary — all real incidents
this session), with no single mechanism to detect → decide → fix → verify.

---

## 2. Goal

A **ToolAutoRepair subsystem** (`internal/autorepair/`) that observes every
`ExecuteContext` call, classifies failures into breakage classes, scores
"repairability", and triggers a bounded, idempotent repair or a safe quarantine —
elivering decision transparency to the user.

---

## 3. Non-goals (explicit)

- Not a static analyzer of tool source code.
- Not replacing manual user decisions when a tool is *irreversibly* misconfigured
  (license keys, refunds, etc.).
- Not auto-uninstalling tools without a user-confirmation path.
- No infinite retry loops — bounded attempts + exponential backoff.

---

## 4. Architecture

New package: `internal/autorepair/`

```
internal/autorepair/
  detector.go      // classifyClass(): error string → Class; decideVerdict()
  judge.go         // judge() funnel; SnapshotOf/Dashboard/Summary/DisabledTools
  repairer.go      // Fixer recipes, repairability(), Repair() probe→gate→fix→post-probe
  quarantiner.go   // safe disable + persist + manual re-enable
  hook.go          // RecordFailure/RepairTool/Quarantine/Reenable package entry points
  autofix_db.go    // knowledge table: tool → known fix / probe command
  state.go         // Engine struct; rolling window; quarantine map; retry/backoff config
  phase3.go        // commit guard (gitTreeDirty) + SanitizeUTF8 hardening
  *_test.go        // autorepair_test / repair_test / quarantiner_test / phase3_test
```

> Note: the plan originally named `classifier.go`/`registry_hook.go`/`health.go`; the
> implementation consolidated decision logic into `judge.go` and the registry hook
> into `hook.go` (no separate `health.go` / `breaker.go` / `classifier.go` files exist).
> Docs below reflect the **actual** file layout.

### 4.1 Failure classification (`detector.go`)

Every tool failure flows through a classifier. Inputs:
- tool `Name`, `Category` (system | skill | mcp | user/dynamic)
- error string from `ExecuteContext` (panic / timeout / err)
- result shape (from `Result.Success` / `Result.Error`, or dynamic tool `exit_err`, `stderr`)
- call counter at failure time (from `metrics`)

Output = a `ToolFailure` object:

```go
type Class int
const (
  ClassTransient   Class = iota // network blip, timeout, rate limit — no code change needed
  ClassMissingDep              // binary/engine/binary missing (e.g. ocr not installed, ugrep gone)
  ClassConfigDrift             // config/env/base_url wrong, MCP disabled, provider revoked
  ClassContractViolation   // returns malformed JSON / wrong schema — logic/schema bug
  ClassLogicBug             // deterministic wrong output (rendering, parsing)
  ClassCrash                // panic / data-race / OOM
  ClassUnknown
)
```

Rule of thumb for **"broken"** (the core question this plan answers):
- **Broken (needs a decision)** = ≥ `N` failures in a rolling window with a **non-input-dependent** confidence signal (same error string, timeout/panic, empty/garbage result on valid args). Default `N=3`.
- **Not broken (no action):** single failure, error is clearly input-validation ("name is required"), marginal latency, transient timeout that recovers.

### 4.2 Repairability decision (`judge.go` + `repairer.go`)

Each `ToolFailure` yields a `RepairVerdict`:

```
              ┌──────────────────────────────┐
failure ──▶   │  Repairable?                │
              │  score 0..1 (confidence)    │
              └──────────────┬──────────────┘
                             │
        ┌────────────────────┼─────────────────────┐
        ▼                    ▼                     ▼
   AUTO-FIX (score≥0.75)  ADVISORY (0.4–0.75)   QUARANTINE (<0.4 or policy)
   run idempotent fix;    surface report to      disable tool in registry,
   re-probe; if still     user, suggest manual    log verdict, persist a
   broken → escalate      action                 caller-facing message
```

**Repairability score** = weighted sum:
- has known fix in `autopatch_db` (+0.5)
- fix is idempotent + testable (probe) (+0.2)
- no destructive side-effects (doesn't delete data) (+0.2)
- cost/low-risk (re-install package vs. edit production config) (+0.1)

### 4.3 Auto-fix repairers (`repairer.go`)

Layered fixers, each **idempotent** and **probe-first**:

| Failure class       | Autofix strategy (examples grounded in real ELING issues) |
|---------------------|-------------------------------------------------------------|
| `ClassMissingDep`   | install/verify binary via package manager (`npm i -g @alibaba-group/open-code-review` for ocr, `apt-get install -y ugrep`), re-run `which` probe |
| `ClassConfigDrift`  | rewrite `~/.eling/config.yaml` / provider base_url from known-good; re-validate via GET `/models`; repair `/usr/local/bin/grep` wrapper → ugrep (with `.bak`) |
| `ClassCrash`        | source mutation guard (commit guard in Phase 3), re-link `grep→ugrep`, `go vet`+`build` to confirm; quarantine on panic |
| `ClassTransient`     | only **backoff + retry** (no code change), auto-resolves |
| `ClassContractViolation` / `ClassLogicBug` / `ClassUnknown` | advisory — manual review (no safe auto-recipe) |

Each repairer pattern (actual `Fixer` struct in `repairer.go`):
```go
type Fixer struct {
   Tool        string        // "" = wildcard (any tool of Class)
   Class       Class
   Summary     string        // human description of the fix
   Probe       func() error  // nil = healthy (pre + post gate)
   Fix         func() error  // idempotent repair
   Destructive bool          // never auto-run; surfaced as advisory
   MutatesCode bool          // commit-guard gated in Phase 3
}
```

**Gate:** ALWAYS run `Probe()` before AND after `Fix()`. Never run a Fix if
pre-probe already healthy. Destructive recipes are never auto-run even with
autofix on; code-mutating fixes are refused while the git tree is dirty
(Phase 3 commit guard).

### 4.4 Quarantine (`quarantiner.go`)

When verdict = QUARANTINE:
- temporarily disable the tool in `DefaultRegistry` (mark `Disabled` via `Registry.SetDisabled`), so it's not offered to the LLM and refused by `ExecuteContext`.
- persist quarantine reason + timestamps to `~/.eling/autorepair_state.json` (atomic temp+rename write).
- surface one clear user-facing message (TUI `⚠️ q <n> [names]` + log) — "Tool X disabled due to ... : <verdict>".
- allow manual re-enable (`eling autorepair reenable X`).

### 4.5 Health hook into registry (`hook.go`)

Minimal, non-invasive integration: in `ExecuteContext`'s existing metrics-defer block,
after recording the failure, call `autorepair.RecordFailure(name, lastErr, elapsed)`.
That becomes the single funnel. No other code path needs changing. New CLI command
`eling autorepair` (or a tool `tools-health` / `health`) exposes the dashboard:
```
Tool            Class      Verdict     Fix         State        LastErr
web_fetch       transient  retry       -           ok           timeout@12s
ocr             missing_dep autofix    npm i -g ... fixed       ProbeOK
MCP:files       crash      quarantine  -           disabled     panic idx
```
Expose TUI splash indicator count = of disabled tools, plus "warning" list in stats.

---

## 5. Persistence & Safety

- `~/.eling/autorepair_state.json` (configurable path via `Engine.statePath`), rotation via existing `.bak`.
- Quarantine records persist across restarts; health counts reset on restart.
- Every fix writes a changelog entry (audit trail) and a `.bak` for any file it edits.
- Default `autofix=false` opt-in gate: ship **detection+advisory** first; enable
  **autofix** via config `autorepair.autofix: true`, so we don't mutate anything
  without consent in the first release.
- Phase 3 hardening: exponential backoff (base 500 ms, doubling, cap 30 s) + max
  retries (default 3) bound every repair; code-mutation fixes are refused while
  the git working tree is dirty (commit guard); non-UTF-8 error bytes are
  sanitized before recording.

---

## 6. Phases

### Phase 0 — Instrumentation (small, low-risk) ✅ DONE
- Add `autorepair` package with `RecordFailure`, classifier, `RepairVerdict`.
- Hook into `ExecuteContext` defer block (metrics branch) to funnel failures.
- Ship **detection + classification only** (no mutation). `go vet`, tests pass.

**Phase 0 deliverables (committed):**
- `internal/autorepair/state.go` — `Class`/`Verdict` enums, `Engine`, rolling
  failure window (default 5m), repeated-identical-error counter.
- `internal/autorepair/detector.go` — `classifyClass()` maps error strings →
  `Class`; `decideVerdict()` → `Verdict` (broken = specific class AND repeated
  threshold; crash always quarantines; transient always retry).
- `internal/autorepair/judge.go` — `judge()` funnel, `SnapshotOf`/`Dashboard`/
  `DisabledTools`/`Summary` for dashboards.
- `internal/autorepair/hook.go` — `RecordFailure` package entry (funnel).
- `internal/tools/registry.go` — hooked the `ExecuteContext` defer block: after
  metric recording, failed calls are fed to `autorepair.RecordFailure`.
- `internal/autorepair/autorepair_test.go` — 7 tests (classifier signal map,
  single-failure-not-broken, repeated-escalation, crash-quarantine, dashboard,
  concurrent records, defaults).
- Status: `go vet ./...` clean, `go build ./...` clean, `go test ./internal/tools/`
  + `./internal/autorepair/` pass. (Race detector unsupported in this sandbox VMA;
  verified via concurrent-record test + mutex discipline.)

### Phase 1 — Probe-first fixers for the 3 safest classes ✅ DONE
- Implement `repairer.go` + `autofix_db.go` for:
  - `MissingBinary` (which→install→which-probe)
  - `ConfigDrift` (config read-back + `/models` HTTP probe)
  - `Env` (re-export token probes)
- Each fixer is idempotent + Probe-gated; `autofix` flag default **false**.

**Phase 1 deliverables (committed):**
- `internal/autorepair/repairer.go` — `Fixer` recipe type (Tool/Class/Summary/Probe/Fix/Destructive),
  `repairability()` weighted score, `Repair()` probe→(gate)→fix→post-probe funnel, `RepairAll()`,
  `RegisterFixer(s)`, `SetAutofix`/`AutofixEnabled`, `SummaryLines`/`Compact`.
- `internal/autorepair/autofix_db.go` — `buildBuiltinFixers()`: grounded recipes for
  `ocr` (npm -g install, `probeExecutable`), `grep`→`ugrep` (MissingBinary + ConfigDrift wrapper
  repair with `.bak`), provider `/models` probe (`probeProviderModels`), env token probe
  (advisory fetch, non-destructive).
- `internal/autorepair/hook.go` — extended with `RepairTool` / `RepairAllTools` / `SetAutofixEnabled`.
- **Key safety fix:** `Repair().Tried` is now set **only when a fix is actually attempted** (autofix on +
  probe unhealthy), never when autofix is off. This preserves the "autofix off = pure advisory, no mutation"
  guarantee that Phase 1 explicitly requires.
- `internal/autorepair/repair_test.go` — **8 new Phase-1 tests**:
  autofix-off-is-advisory (no mutation), probe-first-verifies (no-op on healthy), autofix-on
  applies+verifies, destructive-never-auto-runs, exact-beats-wildcard, no-recipe-is-advisory,
  repairability-scoring, autofix-toggle.
- Status: `go vet ./...` clean, `go build ./...` clean, `go test ./internal/autorepair/`
  (15 tests) + `./internal/tools/` pass.

> Note: destructive + env-token recipes are surfaced as **advisory** (never auto-run), even with
> autofix enabled. Actual destructive/config-write autofix stays gated for Phase 3.

### Phase 2 — Quarantine + TUI + stats ✅ DONE
- `quarantiner.go`, `tools_health` command, `autorepair` status in stats dashboard.
- Manual re-enable CLI.

**Phase 2 deliverables (committed `307773e`):**
- `internal/autorepair/quarantiner.go` — quarantine records persisted to
  `~/.eling/autorepair_state.json` (atomic temp+rename), idempotent `LoadState`,
  `Reenable`, sorted `Quarantined()` listing, `CountQuarantined` for the TUI.
- `internal/tools/registry.go` — `SetDisabled`/`IsDisabled`/`Disabled`; `List()`
  hides disabled tools from the LLM; `ExecuteContext` refuses disabled tools;
  QUARANTINE verdict from the hook disables + persists + logs.
- `internal/cli/cli.go` — `eling autorepair` dashboard + `reenable <tool>` +
  `autofix on|off`; `tools-health` / `health` aliases.
- TUI header shows `⚠️ q <n> [tool names]` for disabled tools.
- `quarantiner_test.go` — persistence round-trip, re-enable, listing (fixed the
  boom_tool/web_tool assertion bug).

### Phase 3 — Auto-fix enablement, backoff, doc ✅ DONE
- Enable `autorepair.autofix: true` (opt-in), add exponential backoff + max retries,
  update `README.md` / `docs/TOOLS.md`, plus advisory logging for un-UTF8.
- Harden against mistakes: commit guard before code-mutation fixes.

**Phase 3 deliverables (committed):**
- `internal/autorepair/phase3.go` — `gitTreeDirty`/`workingTreeDir` commit guard
  (stub-able via `commitGuardCheck` var) + `SanitizeUTF8` (replaces invalid bytes
  with U+FFFD so dashboards/state never carry malformed encodings).
- `internal/autorepair/state.go` — `maxRetries` (default 3) + `backoffBase`
  (default 500 ms) + `backoff()` exponential doubling capped at 30 s; `SetMaxRetries`.
- `internal/autorepair/repairer.go` — `Fixer.MutatesCode` flag; `Repair()` now runs a
  bounded retry loop (Fix → post-probe → backoff → retry, `Attempts` reported);
  destructive + code-mutating recipes never auto-run; commit guard blocks dirty-tree
  code fixes.
- `internal/config/config.go` — `AutorepairConfig{Autofix bool, MaxRetries int}`
  under `autorepair:` YAML (default `autofix: false`).
- `main.go` — wires `autorepair.SetAutofixEnabled(cfg.Autorepair.Autofix)`,
  `SetMaxRetries`, `LoadQuarantineState()` at startup (both CLI and server paths).
- `internal/autorepair/hook.go` — `RecordFailure` sanitizes non-UTF-8 error strings
  before recording; `SetMaxRetries` package entry.
- `internal/autorepair/phase3_test.go` — 8 tests: retry-with-backoff heals,
  exhausts-budget → advisory, commit guard blocks/allows code mutation,
  SanitizeUTF8, RecordFailure sanitizes, backoff sequence + cap, SetMaxRetries.
- `docs/TOOLS.md` — Auto-Repair & Tool Health section (config, CLI, pipeline,
  safety guarantees).

---

## 7. Success criteria (acceptance)

- [x] `go vet ./...` clean after each phase.
- [x] all existing + new tests pass (`go test ./...`).
- [x] a deliberately-broken tool (e.g. fake `exec: "flakyfix": executable file not found`) is **detected**, classified `missing_dep`, and (autofix=on) repaired+re-probed green (verified in `repair_test.go` / `phase3_test.go`).
- [x] a deliberately-crashing tool is **quarantined**, not offered to agent output, message surfaced in TUI (verified in `autorepair_test.go` / `quarantiner_test.go`).
- [x] a transient failure (network `sleep 50` timeout) is **not** mutated — only retried with backoff.
- [x] quarantine + health status persist across restart, re-enable works (verified in `quarantiner_test.go`).
- [x] retries are bounded: fix that never heals exhausts `maxRetries` and escalates to advisory, never loops forever (`phase3_test.go`).
- [x] code-mutation fixes are blocked while the git tree is dirty (commit guard) (`phase3_test.go`).

---

## 8. Proof-measure baseline (current state)

- Registry has `toolMetrics` with calls/failures/latency but **no classification**.
- No quarantine concept exists; `Unregister` exists but unused by health.
- Auto-fix has no probe/gate — fixes are ad-hoc manual edits.
- No `state/autorepair.json`.

---

## 9. Open decisions (track before implementation)

1. Where to put the failure-window window & threshold `N/FailureCount` — **RESOLVED**: rolling window default 5 min, threshold 3 repeated identical errors (`Engine.New(0,0)` → `window=5m, threshold=3` in `state.go`).
2. Autofix default on/off — **RESOLVED**: default **off**; opt-in via config `autorepair.autofix: true` (wired in `main.go` via `SetAutofixEnabled`).
3. Which repairs count as "destructive" — **RESOLVED**: `Fixer.Destructive` (never auto-run, advisory) and `Fixer.MutatesCode` (commit-guard gated in Phase 3); env-token recipes are advisory (never inject secrets automatically).

---

## 10. Risks & mitigations

| Risk | Mitigation |
|------|-----------|
| Fixer corrupts config | always .bak + read-back probe; autofix default off |
| Infinite retry loop | max tries + exp backoff + quarantine after exhausted |
| False-positive "broken" | requires ≥ repeating failure + non-input signal; advisory default |
| Adversarial dynamic tool | only cap mutation to P1 fixers; quarantine dynamic tools rather than mutate |
| Disk growth of `autorepair.json` | rotation + cap records |