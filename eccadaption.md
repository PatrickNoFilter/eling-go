# ELING — ECCADAption (End-of-Conversation Message Constraint Dictation & Adaptive Persistence Integration)

> **Design change**: the agent must dictate its **own final user-facing message** under a strict
> **per-pipeline output constraint** (word/length/paragraph budget + role/format policy), then
> **auto-learn** the computed facts of the exchange into durable memory and **self-edit the fired
> end-of-session/end-of-turn bookkeeping** (session metadata + MCP config + permissions policy) so a
> session's closing artifacts are empirically accurate. Rationale / prior-art notes are in
> `docs/eccadaption_faq.md`.

This document evolves, in order: **P1 Output Constraints for End Messages**, **P2 Self-Validated Post-Session Persistence (Session/MCP/Permissions)**, **P3 Rust-style `NewType` scaffolding**, and **D** decision evidence. Each phase links theory (the section it elaborates) to implementation (file/line anchors in this repo). All state edits route through a **single brain `Ecca` layer** so the flow is observable and revertible.

---

## P1 — Output Shaping for End Messages

> **Status: DONE** — `internal/layers/shaping.go` pump (`d4253dd`), `OutputConfig`/`output` block with
> `end_message_runes` / `end_message_paras` / `end_message_no_md` (`9c8dc77`), agent choke-point wiring
> `Agent.shapeEndMessage` → `end_message_produce` hook (`2613fd2`); tests in `internal/agent/output_shaping_test.go`.
> All defaults zero → pure passthrough on fresh installs.

**Theory**: The final assistant message in a turn is *state* to future turns (resume/context). Enforce a
numeric budget and a format policy *just-in-time*, at the single choke point where the final string is
produced, so the user-facing closing message is predictable and does not blow the plan budget.

**Scope**: internal layer + one wire into the agent's final-message production.

### 1.1 New layer file `internal/layers/shaping.go`

Define the policy and a pure pump:

```go
// EndMessagePolicy caps the agent's final assistant message.
// Fill := policy(Filled); FillAndVerify returns err if the buffer violates the policy.
package layers

type FillPolicy struct {
    MaxRunes      int  // hard ceiling on the final message length
    MinRunes      int  // floor; 0 disables (used by P3 tests)
    MaxPara       int  // max paragraph count (blank-line split)
    DisallowMKD   bool // forbid markdown bullet bolding "**…**" tokens
    DisallowAster // no top-level "* " bullets
}

// EndMessageWrap is a message with its final content.
type EndMessageWrap struct {
    content string
    used    int // consumed runes against the budget (debug/audit)
    ok      bool
    note    string // audit trail string
}
```

Hook candidates (see `internal/layers/hooks.go:70`) — add **two** constants to the `AllHooks` list:

```go
HookEndMessageProduce = "end_message_produce" // fired before final msg is committed
```

Pump (call only from the agent final call):

```go
// NewEndMessage builds the final tokenized message under policy; ok=false means
// caller should fall back to the current (legacy) path.
func NewEndMessage(p EndMessagePolicy, msg string) (EndMessageWrap, bool)
```

Implementation rules, grounded in `internal/layers/hooks.go` and `brain.go`:

1. Measure `len([]rune(msg))` (runes, not bytes — user-visible count).
2. If `len > p.MaxRunes && p.MaxRunes > 0`, truncate at the last space before the cap and append
   the constant trailer `"\n… (truncated to respect output budget)"`. Set `wrap.used = maxRunes`.
3. Split on `"\n\n"`; if paragraphs `> p.MaxParas && p.MaxParas > 0`, keep the first `p.MaxParas`
   paragraphs and append `"\n… (paragraphs trimmed to budget)"`.
4. If `p.DisallowMarkdown`, `strings.ReplaceAll(msg, "** ", "**")` and drop lines starting with
   `"- "` (list `DisallowTrailingAsterisk`) before returning.
5. Always return `ok = true`; never hard-fail the turn.

### 1.2 Budget defaulting

Add to `internal/config/config.go` (`Config` struct body, after `Agent` block, keep parallel to the
existing discrete structs):

```go
Output OutputConfig `yaml:"output"`
```

and define:

```go
type OutputConfig struct {
  // EndMessage* governs the *final assistant message* of each completed turn.
  EndMessageRunes     int  `yaml:"end_message_runes"`      // default 0 => no cap
  EndMessageParas     int  `yaml:"end_message_paras"`      // default 0 => no cap
  EndMessageMarkdown  bool `yaml:"end_message_no_md"`      // default false
}
```

Fill defaults in `DefaultConfig()` (`config.go:250` region):
`EndMessageRunes: 0, EndMessageParas: 0, EndMessageDisallowMarkdown: false` (opt-in; preserves current
behavior on fresh installs).

### 4.3 Agent choke point — `internal/agent/agent.go`

In the `Run` path that builds the final `fullResponse` (the branch ending in the `return fullResponse, nil`
path where `appendWithReasoning` is called and `autoLearn` + `updateConversationSummary` are spawned —
around `agent.go:1471–1484`):

1. After the provider returns, call the new layer:

```go
wc := layers.NewEndMessage(layers.EndMessagePolicy{
    MaxRunes:          a.cfg.Agent.Output.EndRunes,
    MaxParas:          a.cfg.Agent.Output.EndMessageParas,
    DisallowMarkdown:  a.cfg.Agent.Output.EndMessageDisallow,
}, fullResponse)
full = wc.content
```

2. Only when `a.cfg.Agent.Output.EndRunes > 0 || …` is non-default; otherwise skip the layer
   entirely (zero overhead for the common case).
3. **Append a hook `end_message_produce`** via `a.fireHook(layers.HookEndMessageProduce, …)` with
   `{"before_len": N, "after_len": M, "policy": …}` so audit layers can observe the truncation event.

### Tests
- `internal/layers/shaping_test.go`: rune truncation counts, paragraph trimming, markdown stripping,
  zero-policy passthrough (no-op when all fields default).
- `internal/agent/agent_test.go` (or a new `output_test.go`): set `EndRunes: 20`, feed a 50-rune
  response, assert returned `full` length `<= 20`.

---

## P2 — Session/MCP/Permissions persistence wiring (validated before persist)

> **Status: DONE** — `Manager.Save/SaveAll` re-verify token totals via `verifyTotals()` (`0fc5b59`),
> MCP `ManagerFromConfig`/`Reset` config reload (`a2bc3ea`), `PermissionsConfig → PermPolicy` bridge
> asserted by `TestPermPolicyFromConfig` (`384f9d5`).

**Theory**: When a turn completes, the facts the agent derived **must be writable** (no lost learning),
and the end-of-session bookkeeping that records *what it learned / what it opened* must recompute its
own numbers from verified state rather than trusting stale values.

### P2.1 Session metadata correctness — `internal/session/session.go`, `agent/session naming`
- `Manager.Save(name)` (`session.go:232`) should flush in-memory `Entries` (including `SetLastEntryTokens`)
  **before** writing. Audit `SaveAll`/`Save` to guarantee `total_tokens` metadata matches the sum of the
  entries after the final append — a small `verifyTotals()` that recomputes and logs if mismatched
  (does not hard-fail; goes to audit).

### P2.2 MCP config reload — `internal/mcp/mcp.go` + `internal/config/config.go`
- `MCPConfig` (`config.go:172`) and `MCPServerConfig` (`config.go:178`: Name/Command/Args/Env) already
  exist. Add loader helper in `internal/mcp/mcp.go`:

```go
// ManagerFromConfig builds a Manager from cfg.MCP; applies connectTimeout when nonzero.
func ManagerFromConfig(cfg config.MCPConfig) (*Manager, error)
```

- Wire the manager to honor a config edit **while a session is running** (replaces stale plan). Expose:
  `Manager.Reset(cfg)` reloads servers from config without dropping existing sessions.

### P2.3 Permissions persistence — `internal/tools/permissions.go`, `config.PermissionsConfig`
- `config.PermissionsConfig` (`config.go:38`) already models default/rules/projects and is built into
  the registry via `permPolicyFromConfig` (`main.go:900`) → `tools.PermPolicy` → `ModeFor`
  (`permissions.go:69`).
- Add `permPolicyFromConfig` test to `permissions_test.go` to assert expert mapping (default→mode,
  project trust outranks rules), so a config error is caught before a session auto-rotates.

### Decision hook — persist learned facts at session end
- After the successful final turn, post to `learnings.Append` (`internal/learnings/learnings.go:47`)
  with a `-" learning: <summary>"` line premixing only factual/verifiable statements extracted from the
  exchange (no secrets, no monetary figures). Gate on `a.cfg.Agent.LearnFromExchange`
  (`agent.go:2995`) so behavior is opt-in.

---

## P3 — Rust-style `guardrails` scaffolding

> **Status: DONE** — `internal/layers/guardrails.go` (P3.1, commit `94c1313`),
> `GuardrailsConfig` block (commit `806272d`), agent wiring (commit `d038c56`).

**Theory**: P1/P2 facts must be enforced by construction; add a `guardrails` module that, like Rust
`Miri`/`compiler builtins` (compiler enforced runtime checks), pans to a set of whitebox checks and a
hard-enum `GuardrailsAssert` that can be evaluated as a compile-time unit. Modelling scaffolding only —
all wiring inert by default, enable-opt per config.

### 3.1 New layer `internal/layers/guardrails.go`

```go
type GuardrailID int

const (
    GuardrailEndMessageUnderBudget GuardrailID = iota // witness: shaping.go
    GuardrailSessionTokenMonotonic                     // witness: session save drift
    GuardrailOpenersMatchesPerms      // witness: permissions projection
    GuardrailMCPserverMatchesConfig // witness: Manager reset consistency
)

type GuardrailsAssert struct {
    ID      GuardrailID
    Violation string
    Witness string // file:line provenance
}
```

- Expose `func AssertAll() []GuardrailsAssert` scanning each of the four witnesses and returning the
  list of violated invariants; the **describe** handler prints a human table.

### 4.2 Integrate
- Wire `guardrails` into the selected persist/emit paths of P1/P2 as a *soft audit* (log-only when
  `guardrails.audit=true`) and *hard veto* when `guardrails.strict=true` (config `GuardrailsConfig`
  block under `Config`). Defaults: both `false`.

---

## D — Evidence / Session doc index

> **Status: DONE** — acceptance (build/vet/test green at each phase) recorded in the P1–P3 commits above;
> user-facing docs synced in the README/docs commit (`docs: sync README + docs with P1/P2/P3 feature set`).

| Internal SES | Build sketch |
|---|---|
| `internal/layers/shaping.go` | P1 pump (free, stateless) |
| `internal/layers/guardrails.go` | P3 shared enum + assert runner |
| `internal/agent/agent.go` (`Run`/`send`) | P1 single choke-point call |
| `internal/session/session.go` | P2.1 token-total drift audit |
| `internal/mcp/mcp.go` | P2.2 manager reload |
| `internal/tools/permissions.go`, `main.go` | P2.3 policy + test hook |
| `internal/config/config.go` | new `Output` and `Guardrails` blobs |

### Acceptance
- Build `go build ./...` green at each of P1→P2→P3 (small commit at each done step;
  tree never red at commit).
- `go vet ./...` clean; `go test ./...` passes with the new P1/P2/P3 unit tests.
- Opt-in defaults (all new behaviors default **off**) so a fresh install behaves exactly as today.

---

*Document authored to extend the codebase; each numbered section is an atomic, revertible step.*