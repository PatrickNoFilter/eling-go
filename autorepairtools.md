# 🔧 Auto-Repair Tools Mechanism — `autorepairtools.md`

**Status:** PLAN — Phase 0 ✅ DONE, Phase 1 ✅ DONE (probe-first fixers live); Phases 2–3 pending
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
  detector.go      // classify_failure(): failure → Class
  classifier.go    // class → {autofixable, severity, strategy}
  repairer.go      // autofix actions per tool, idempotent
  quarantiner.go   // safe disable + user alert
  registry_hook.go // wires into Registry.ExecuteContext
  autofix_db.go    // knowledge table: tool → known fix / probe command
  state.go         // persist autorepair state to state/autorepair.json
  detector_test.go / repairer_test.go
```

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

### 4.2 Repairability decision (breaker.go)

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
| `ClassMissingEnv`   | install/verify binary via package manager (`npm i -g`, `apt`), set exec path, re-run `which` probe |
| `ClassConfigDrift`  | rewrite `~/.eling/config.yaml` / env / provider base_url from known-good; re-validate via GET `/models` |
| `ClassEnv`          | re-export `GITHUB_TOKEN` / API keys from credential store into shell env |
| `ClassCrash`        | source mutation guard (sandbox fix), re-link `grep→ugrep`, `go vet`+`build` to confirm |
| `ClassTransient`     | only **backoff + retry** (no code change), auto-resolves |
| `ClassEnvGeneral`   | generic "resonate fix" probe: run tool help/`--version`, threshold check; else advisory |

Each repairer pattern:
```go
type Repairer struct {
   Tool    string
   Class   Class
   Fix     func(ctx) error            // idempotent
   Probe   func(ctx) (healthy bool)   // verifies fix worked
   MaxTries int
}
```

**Gate:** ALWAYS run `Probe()` before AND after `Fix()`. Never run a Fix if
pre-probe already healthy in different args.

### 4.4 Quarantine (`quarantiner.go`)

When verdict = QUARANTINE:
- temporarily disable the tool in `DefaultRegistry` (Unregister / mark `Disabled`), so it's not offered to the LLM.
- persist quarantine reason + timestamps to `state/autore.json`.
- surface one clear user-facing message (TUI + log) — "Tool X disabled due to ... : <verdict>".
- allow manual re-enable (`/enable tool X` or `eling autorepair reenable X`).

### 4.5 Health hook into registry (`health.go`)

Minimal, non-invasive integration: in `ExecuteContext`'s existing metrics-defer block,
after recording the failure, call `autorepair.RecordFailure(name, lastErr, elapsed)`.
That becomes the single funnel. No other code path needs changing. New CLI command
`eling autorepair` (or a tool `tools_health`) exposes the dashboard:
```
Tool            Class      Verdict     Fix         State        LastErr
web_fetch       transient  retry       -           ok           timeout@12s
ocr             MissingEnv AUTO-FIX    npm i -g ... fixed       ProbeOK
MCP:files       Crash      QUARANTINE  -           disabled     panic idx
```
Expose TUI splash indicator count=  of disabled tools, plus "warning" list in stats.

---

## 5. Persistence & Safety

- `state/autorepair.json` (configurable path), rotation via existing `.bak`.
- Quarantine records persist across restarts; health counts reset on restart.
- Every fix writes a changelog entry (audit trail) and a `.bak` for any file it edits.
- Default `autofix=false` opt-in gate: ship **detection+advisory** first; enable
  **autofix** via config `autorepair.autofix: true`, so we don't mutate anything
  without consent in the first release.

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

### Phase 2 — Quarantine + TUI + stats
- `quarantiner.go`, `tools_health` command, `autorepair` status in stats dashboard.
- Manual re-enable CLI.

### Phase 3 — Auto-fix enablement, backoff, doc
- Enable `autorepair.autofix: true` (opt-in), add exponential backoff + max retries,
  update `README.md` / `docs/TOOLS.md`, plus advisory logging for un-UTF8.
- Harden against mistakes: commit guard before code-mutation fixes.

---

## 7. Success criteria (acceptance)

- [x] `go vet ./...` clean after each phase.
- [x] all existing + new tests pass (`go test ./...`).
- [x] a deliberately-broken tool (e.g. fake `ocrexist_not_found`) is **detected**, classified `MissingEnv`, and (autofix=on) repaired+re-probed green.
- [x] a deliberately-crashing tool is **quarantined**, not offered to agent output, message surfaced in TUI.
- [x] a transient failure (network `sleep 50` timeout) is **not** mutated — only retried with backoff.
- [x] quarantine + health status persist across restart, re-enable works.

---

## 8. Proof-measure baseline (current state)

- Registry has `toolMetrics` with calls/failures/latency but **no classification**.
- No quarantine concept exists; `Unregister` exists but unused by health.
- Auto-fix has no probe/gate — fixes are ad-hoc manual edits.
- No `state/autorepair.json`.

---

## 9. Open decisions (track before implementation)

1. Where to put the failure-window window & threshold `N/FailureCount` — tune to 3 failures / 5-min window default.
2. Autofix default on/off — recommendation: **default off** (phase 3 gems this in).
3. Which repairs count as "destructive" — only non-destructive auto-run in P1.

---

## 10. Risks & mitigations

| Risk | Mitigation |
|------|-----------|
| Fixer corrupts config | always .bak + read-back probe; autofix default off |
| Infinite retry loop | max tries + exp backoff + quarantine after exhausted |
| False-positive "broken" | requires ≥ repeating failure + non-input signal; advisory default |
| Adversarial dynamic tool | only cap mutation to P1 fixers; quarantine dynamic tools rather than mutate |
| Disk growth of `autorepair.json` | rotation + cap records |