# D2 — Verify → Repair Loop (`internal/verify`)

The **verify → repair loop** (short *D2*, plan codename "DeepCode heist") stops
the agent from declaring success over broken code. After any editing round, the
agent runs real verification evidence against the files it just changed. A
**failed verification is never reported as success** — the failure is fed back
as the next user message so the model repairs it instead of moving on.

## How it works

Every `Ask` / `AskStream` turn is wrapped by a lightweight, per-turn state
machine (`verify.New`). Three hooks drive it:

- **`Reset()`** — called at the start of every turn. Clears per-turn state so
  evidence never leaks across turns.
- **`Round(toolCtx, calls)`** — called after each tool round in the tool loops
  (`runToolLoop` and `runStreamToolLoop`). It:
  1. detects the files edited this round (the `edit`, `write`, and `lsp_rename`
     tools),
  2. selects **evidence** for the task,
  3. runs it. On dirty/failing evidence it returns a `[Verification failed — …]`
     repair prompt that the agent injects as the next user message. On clean
     evidence it returns `""` (no interruption).
- **`Final(toolCtx)`** — called right before the final answer is returned.
  Attaches an **Evidence block** (command, exit code, status) to the answer so
  PASS / STILL-FAILING is reported honestly.

Both code paths — the non-streaming `Ask`/`runToolLoop` and the streaming
`AskStream`/`runStreamToolLoop` — are wired identically.

## Evidence taxonomy

The evidence to run is chosen per task type (folded in from D5):

| Task (touched files)              | Evidence                                           |
|-----------------------------------|---------------------------------------------------|
| Go code (`.go` w/ `go.mod`)       | `go test ./...` in the module root                |
| Go single file (no `go.mod`)      | LSP diagnostics for the file                      |
| Python / TypeScript / JS          | LSP diagnostics (gopls in the static-evidence role) |
| Docs / markdown only              | *no* evidence (a docs-only change is low-risk)    |
| No verifiable edits               | *no* evidence                                     |

A **timeout** is *inconclusive* (never a failure) — a slow suite is not broken
code.

## Repair budget

The number of failed-verification repair rounds is **bounded and separate** from
the global turn-round cap, so repair prompting can never balloon into an
unbounded loop:

- `verify.max_rounds` — how many failing rounds prompt a repair (default `2`).
  When the budget is exhausted the final prompt says so explicitly and the next
  `Round` returns without prompting again.
- `verify.timeout_sec` — per-evidence-run wall-clock timeout (default `60`).

## Configuration

```yaml
verify:
  enabled: true      # verify→repair loop ON by default (D2)
  max_rounds: 2       # repair iterations before honest-failure reporting
  timeout_sec: 60     # per-run evidence timeout
  evidence: auto      # "auto" = per-task evidence taxonomy
```

## Opt-outs

Three, independent paths skip auto-verification:

1. **`--no-verify`** CLA flag — equivalent to `verify.enabled: false`. The
   verifier is *commissioned disabled* at construction; the per-turn
   plan-mode logic can **never** resurrect it (`Enable()` is a no-op).
2. **`verify.enabled: false`** in `config.yaml` — same commissioning result.
3. **Plan mode** — when the user is explicitly gating execution with a draft →
   approve plan, auto-verification opts out *for that turn*. The next non-plan
   turn restores it (for a commissioned agent).

In every opt-out case there is **no Evidence block and no verification delay**.

## Tests

- `internal/verify/verify_test.go` — the loop semantics: extraction, task
  selection, disabled/no-op paths, real `go test` evidence (PASS / FAIL /
  timeout-is-inconclusive), repair-budget bounding, LSP static evidence, Final
  re-checking stale failing evidence, Reset.
- `internal/agent/verify_wiring_test.go` — the agent wiring: commissioning,
  `--no-verify` persistence across a full `Ask`, plan-mode opt-out + restore,
  and the `verifyToolCalls` reducer.