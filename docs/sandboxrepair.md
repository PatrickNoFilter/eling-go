# sandboxrepair.md — Sandbox Observability & Repair Plan

**Status:** ✅ Implemented — commit `4f82a06` on branch `eling/exp/d6-sandbox-observability` (worktree, awaiting merge-to-main approval). WS-1..WS-4 all landed; `go build`, `go vet`, `go test ./internal/tools/` green.
**Owner:** ELING agent
**Scope:** `internal/tools/sandbox.go` + `internal/tools/bash.go`
**Tie-ins:** `stealing.md` D-phase, proot/Termux direction, `diagnose-slow-backup-zip`
**License guard:** MIT repo, same tree; default behavior must remain byte-identical.

---

## 1. Objective

Make the bash-tool sandbox **observable and honest**. Today most sandbox failure
paths are silent (`return`, `continue`, `_ =`), and the single most dangerous one —
network isolation being dropped — is **invisible to the agent and to logs**. This plan:

1. Exposes whether network isolation actually applied (`network_isolation` field).
2. Turns every silent failure into an **observable counter** on the existing
   registry `Stats()` (bug indicator, no new logging subsystem).
3. Carries sandbox state through the one error path that currently loses it.
4. Records the two entropy/home fallbacks once (not per-call, to avoid spam).

Default sandbox behavior is **unchanged**; `sandbox_test.go`, `timeout_test.go`,
and the registry `stats_test.go` must stay green.

---

## 2. Grounded error inventory (read from current source, 2026-08-07)

### sandbox.go
| # | Location | Path | Current handling | Signal today |
|---|----------|------|------------------|--------------|
| E1 | `newSandboxDir` `rand.Read` err (≈:93) | entropy unavailable | fallback to `run-<nanots>`, **no log** | ❌ silent |
| E5 | `sandboxRoot` `os.UserHomeDir` err (≈:83) | home unset | returns `/.eling/sandbox` (empty prefix) | ❌ silent |
| E2 | `cleanupSandbox` `os.ReadDir(root)` err (≈:118) | read root fails | `return` | ❌ silent |
| E3 | `cleanupSandbox` `e.Info()` err (≈:128) | stat fails | `continue` | ❌ silent |
| E4 | `cleanupSandbox` `os.RemoveAll(...)` (≈:143) | prune fails | `_ =` **swallowed** | ❌ silent |
| E5b | `realHome` `os.UserHomeDir` err (≈:203) | home unreadable | fallback `/root` | ❌ silent |
| E6 | `wrapNetworkIsolation` `LookPath("unshare")` err (≈:258) | no `unshare` / no priv | **returns command unchanged** | ❌ silent |

> **Note a self-contradiction:** the `wrapNetworkIsolation` docstring says
> *"Never silently drop the guard"*, but the function returns the command
> unchanged whenever `unshare` is absent. The intent and the code disagree —
> this is the core bug this plan fixes.

### bash.go
| # | Location | Current handling | Signal today |
|---|----------|------------------|----------------|
| E8 | `os.MkdirAll(sandboxDir)` err (bash:201) | surfaces `Err("sandbox: create sandbox dir: …")` | ✅ surfaced |
| E9 | `destructiveCommand` match block (bash:214) | `blocked:true` in result | ✅ surfaced |
| E10 | `cmd.Run()` non-`ExitError` (bash:283) | `return nil, fmt.Errorf("bash execution failed: %w", err)` — **loses `sandbox` in buffer** | ⚠️ note |
| E11 | timer timeout (bash:368) | `timed_out:true` | ✅ surfaced |
| E12 | ctx cancel (`bashExecuteCtx`) | `bash aborted` | ✅ surfaced |
| E13 | truncated output (>512 KiB) | `… truncated …` appended | ✅ surfaced, **not counted** |

**Summary:** 12 failure paths total; **5 well-surfaced** (E8–E12), **7 silent**
(E1–E6), of which the two structural gaps are **E6** (net isolation goes dark)
and **E10** (error path strips sandbox context). Truncation (E13) is surfaced in
text but never counted.

---

## 3. Workstreams

### WS-1 (core) — expose real network-isolation state
**File:** `sandbox.go`
- Refactor `wrapNetworkIsolation(command string) string` →
  `wrapNetworkIsolation(command string) string` + a returned bool, or compute a
  global once at `SetSandbox`.
- In `bashExecute` (bash.go), attach `result["network_isolation"]` =
  `true`/`false` reflecting whether `unshare` was present *and* used.
- The proot/Termux case (no `unshare`) now visibly reports `network_isolation:false`
  instead of silently pretending isolation. This is the single highest-value fix.
- Guard includes the current code path unchanged when `unshare` is present.

**Tests:** new `sandbox_test.go` case asserting `network_isolation` is set on the
result map and reports `false` when `unshare` is absent from PATH.

### WS-2 (core) — observability counters on the existing `Stats()`
**File:** `sandbox.go` + `bash.go` (register counter via the existing stats map
already read by `stats_test.go`).

New counters (keys as they'd appear in `Stats().ToolCalls`/per-tool stats or a
dedicated `sandbox` block):

| Key | Incremented when |
|-----|-------------------|
| `sandbox_invocations` | any sandboxed bash invocation starts |
| `sandbox_net_unshare_missing` | E6 — `unshare` not available/privileged |
| `sandbox_cleanup_remove_failed` | E4 — `os.RemoveAll` returns error |
| `sandbox_dir_create_failed` | E8 — `MkdirAll` fails |
| `sandbox_entropy_fallback` | E1 — `rand.Read` fails |
| `sandbox_truncated_output` | E11/truncation indicator |
| `sandbox_home_fallback` | E5/E5b — home lookup failed |

Each is a pure counter call; zero impact on the hot path. These become the
"bug indicator" the user asked for — drift in a counter diagnoses sandbox debt
without new logging infra.

**Tests:** extend `stats_test.go` (or add `sandbox_observability_test.go`)
asserting counters reflect a crafted failure (e.g. unshare absent).

### WS-3 — carry sandbox context on the error path (E10)
**Change:** `bash.go` — in the `non-ExitError` branch (fork/missing bash), wrap
the returned error with structure so callers aren't in the dark about
sandboxing:

```go
return nil, fmt.Errorf("bash execution failed (sandbox=%v, net_isolation=%v): %w",
    sandboxed, netIsolated, err)
```

### WS-4b — one-time fallback notes (E1, E5)
- Guard with a `sync.Once` log-once stderr note when entropy or home lookup
  falls back — recorded once, not spammed per call.
- Wire the same events into the counters from WS-2.

---

## 4. Guarded / verification gate

Work in a worktree (`sandbox-observability`), not the main tree:
- `go vet ./...`
- `go test ./internal/tools/ -run 'Sandbox|Timeout|Stats' -v`
- Default path byte-identical: existing destructive-block, timeout, ctx-cancel,
  and perm-pol tests must pass unchanged.
- Compare binary diff expectations: no new external deps (only stdlib).

---

## 5. Connection to the pluggable-backend seam (from earlier discussion)

`network_isolation` (WS‑1) is the first slice of the proposed pluggable
`runtime.exec(source, {backend})` seam discussed for the proot/container/Termux
direction. Landing WS‑1 now explicitly does **not** throwaway work — the
identifier becomes the field a future `prootBackend`/`containerBackend` reports
instead of `unshare`. WS‑4 lays groundwork by making each backend's launch path
observable.

---

## 6. Acceptance criteria (definition of done)

- [ ] `bashExecute` results always carry `sandbox:bool` **and** (when sandboxed)
      `network_isolation:bool`.
- [ ] Non-`ExitError` paths include `sandbox=`/`net_isolation=` context (WS‑4).
- [ ] All silent paths (E1–E6) now increment a counter in `Stats()`.
- [ ] `go vet` + the sandbox/timeout/stats test suites pass.
- [ ] Default (unshare-present) path emits identical results to today.

---

## 7. Deliberately out of scope (keep the PR small)

- Remote/Cloudflare `computer` backend (future; needs its own design).
- Anything touching `web`, `backup`, `ocr`, `semantic` — those are different
  surfaces, not `bash`/exec.
- Leaky full `ExecBackend` interface — only the `network_isolation` identifier
  is added now; full reflection deferred.

---

## 8. Decision needed from maintainer

1. Use the existing `Stats()` counters **or** a dedicated stderr logger for the
   fallback notes (recommend: counters everywhere + `sync.Once` note for E1/E5).
2. `Detail` the identifiers go under the existing stats — **go ahead and
   implement WS‑1 + WS‑2 as one atomic commit** in a worktree, or write-up only?