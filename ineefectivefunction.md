# ELING — Ineffective Functions Audit & Remediation Plan

**Date:** 2026-08-03
**Audit scope:** Tool registry (`internal/tools/`), agent skill auto-learning (`internal/agent/agent.go`), persisted state (`~/.eling/tools.json`, `~/.eling/skills.json`, `~/.eling/evolutions.json`), skill scripts (`skills/hermes/`), docs.
**Baseline:** `go vet ./...` ✅ · `go build ./...` ✅ · 23 built-in Go tools · 117 persisted tools · 100 learned skills.

---

## 1. Executive Summary

ELING's *code* is healthy (vet + build clean), but its **tool surface is heavily polluted**. Of the ~140 tools advertised to the LLM every turn:

| Metric | Value | Verdict |
|---|---|---|
| Built-in Go tools | 23 | ✅ real |
| Persisted tools (`tools.json`) | 118 | ⚠️ 102 are **no-op** (no command) |
| Learned skills (`skills.json`) | 100 | ⚠️ **94 never used** (`used_count == 0`) |
| No-op tools advertised per turn | ~104 | 🔴 waste |
| Tool descriptions per turn | ~15,400 chars ≈ **3,850 tokens** | 🔴 heavy |
| Contradictory skills (vs current design) | 2 | 🔴 harmful |
| Duplicate-family skills | ~70 across 8 families | 🔴 redundant |
| Dead script assets (`skills/hermes/`) | 9 scripts, 0 wired | 🔴 dead |
| Quarantined/disabled tools | 0 | ⚠️ autorepair idle |

**Root cause:** the auto-learn (`autoLearn`) system registers every learned "skill" as a tool whose `Execute` is a **no-op** that only prints `"Skill X executed — follow the description guidance"`. The LLM is then shown ~100 of these playbook stubs every turn, most never used, several actively contradicting the current design.

---

## 2. Findings (with evidence)

### 2.1 🔴 No-op tools — 104 of 117 `tools.json` entries have no command

`~/.eling/tools.json` → 118 entries. Only 16 have a real `command`:
`cbm_*` (10, via `codebase-memory-mcp`), `create_backup`, `eling_setup`, `eling-wizard`, `eling_setup_wizard`, `eling-command`, `eling_launcher` (some overlap).

The other **104 entries** have `"command"` missing. When called, `restoreDynamicTool()` (`internal/agent/agent.go:2223`) runs `RunDynamicCommand` with an empty command → returns `{"note": "no command defined"}`. **Calling them does nothing.**

**Impact:** every one of these is still advertised to the LLM via `ToProviderDefs()` (`internal/tools/schema.go:269`), consuming tokens and confusing tool selection.

### 2.2 🔴 Learned-skill bloat — 94/100 skills never used

`~/.eling/skills.json` → 100 `LearnedSkill` entries with `used_count`:

| used_count | count |
|---|---|
| 0 (never used) | **94** |
| 1 | 5 (`pattern_88`, `update-eling-config-base-url`, `go-project-verify-rebuild`, `kill-process-by-name`, `session-resume-verification`) |
| 2 | 1 (`race-condition-and-crash-audit`) |

`internal/agent/agent.go:2137-2158` re-registers *every* skill as a tool whose `Execute` returns the no-op message. The `autoLearn` mechanism (evolutions show `skills_count=99 → 100` repeatedly) keeps adding skills up to a **cap of 100**, never evicting unused ones.

**Junk sample:** `pattern_88` (confidence **0.3**) — a canned "The codebase is indexed! Let me share a comprehensive architecture overview…" response, not a reusable procedure.

### 2.3 🔴 Contradictory skills (harmful)

| Skill | Says | Reality |
|---|---|---|
| `replace-ugrep-with-grep` | "Remove ugrep package, fix grep wrapper to call GNU grep" | Commit `5acbfb3` **enforces ugrep as canonical**; removing it breaks the SEARCH RULE |
| `fix-grep-built-in-tool` | "Replace ugrep-specific flags with GNU grep" | Same — directly contradicts the current tool design |

These would actively sabotage the codebase if ever followed. **Must be deleted.**

### 2.4 🟠 Massive duplication — ~70 skills across 8 overlapping families

| Family | Count | Members (abridged) |
|---|---|---|
| audit/plan/doc | 26 | implementation-audit, plan-implementation-audit, plan-implementation-check, plan-execution-audit, plan-verification-against-code, plan-doc-reconcile, implementation-doc-audit, spec-implementation-audit, audit-implementation-claims, tracking-doc-reconcile, deferred-item-reverification, prune-deferred-items, feature-necessity-analysis, cohesive-doc-update, doc-version-annotate, … |
| release/git/version | 15 | go-version-bump, github-release-bump, github-version-release, go-release-version-bump, github-release-workflow, github-release-push-with-description-update, push-git-tags-to-github, create-github-release-from-existing-tag, git-commit-tag-release, version-drift-check, … |
| android/llm (irrelevant on this host) | 13 | android-llm-selection-guide, android-gpu-llm-setup, llm-model-selection-for-android, local-ollama-termux-setup, termux-local-llm-setup, small-coding-llm-recommendation, extract-cookies-android, … |
| tui | 8 | scrolling-marquee-banner, add-tui-banner-counter, inspect-tui-banner, tui-add-legend-line, tui-dynamic-wrap-to-width, tui-paste-safe-multiline-input, change-tui-colors |
| crash/race | 5 | race-condition-and-crash-audit, race-condition-crash-audit, go-concurrency-audit, data-race-crash-audit, go-data-race-audit |
| ocr | 4 | open-code-review-cli-usage, bounded-ocr-review, tuicr-interactive-review-driver, (+ built-in ocr_review/scan/health) |
| grep/ugrep | 3 | elings-ugrep, replace-ugrep-with-grep ⚠️, fix-grep-built-in-tool ⚠️ |
| mcp | 3 | debug-mcp-zero-count, verify-mcp-config-fix, mcp-health-check-and-repair |
| model deletion | 2 | safe-model-deletion, safe-model-directory-deletion |
| github compare | 2 | compare-github-repos, compare-github-repos-tools |
| setup wizard | 7 | eling_setup, eling-setup, eling_setup_wizard, eling-wizard, eling-setup-wizard-access, eling-command, eling_launcher |

### 2.5 🟠 Dead script assets — `skills/hermes/` never wired

`skills/hermes/` contains 9 scripts (deep-web-research.sh, job-search-automation.sh, linkedin-automation.sh, osint-person-search.sh, kimi-webbridge.sh, …). **None are referenced** in `tools.json`, `skills.json`, or Go code. They depend on a **"Kimi WebBridge" daemon at 127.0.0.1:10086 which is not installed** (verified: no daemon, no npm package). Running them would fail at runtime.

### 2.6 🟡 Dead code in registry

`internal/tools/registry.go`:
- `Registry.Categories()` — **0 non-test call sites** (dead)
- `Registry.ListByCategory()` — 1 call site (`agent.go:1933`, `ListSkills`)
- Others (`Count`, `Unregister`, `GetDynamicTools`, `SetDynamicTools`, `RemoveDynamicTool`) are used.

### 2.7 🟡 Autorepair system idle

No `~/.eling/autorepair_state.json` exists → **0 tools quarantined**. The Phase 0–3 autorepair machinery (detection, quarantine, tools-health CLI, TUI indicator) is built and wired but has never triggered. `autofix` remains OFF by default (safe, but the safety net is untested in anger).

### 2.8 🟡 Uncommitted work-in-progress

`git status` shows uncommitted changes: `internal/lsp/lsp.go`, `internal/tools/semantic.go`, new `internal/tools/lsp_rename.go`, `internal/tools/lsp_rename_test.go`, `semantic_embed_test.go`, `semantic_probe_timing_test.go`, plus `docs/lsp.md`, `docs/tools.md`, `stealing.md`. **Risk of loss**; should be committed.

---

## 3. Quantified Impact

- **Token cost:** ~140 tools × avg description ≈ **3,850 tokens per turn** just for tool schemas — paid on *every* LLM request, while ~104 of those tools are no-ops and 94% of learned skills are never used.
- **Selection noise:** a large, duplicate-heavy tool list measurably degrades LLM tool-choice accuracy (models pick wrong/ambiguous tools when many near-identical options exist).
- **Contradiction risk:** 2 skills instruct actions that would break the current ugrep architecture.
- **Failure surface:** 9 hermes scripts + 104 no-op tools = 113 advertised capabilities that can never succeed — polluting the autorepair failure funnel with "broken tools" that were never real.

---

## 4. Recommendations (prioritized)

### P0 — Fix now (one commit, ~30 min)

1. **Prune `~/.eling/skills.json` to the 6 used skills** (or a curated ≤10). Delete the 94 with `used_count == 0`, especially `pattern_88` (confidence 0.3).
2. **Delete contradictory skills:** `replace-ugrep-with-grep`, `fix-grep-built-in-tool`.
3. **Prune `~/.eling/tools.json`:** drop all 104 no-command entries; keep only the 13 with real commands. (They can be re-added later as real commands, not stubs.)
4. **Commit the WIP** (lsp_rename, semantic, docs) so the tree is clean before the next step.

### P1 — Fix the mechanism (code change, ~1 day)

5. **Cap & evict learned skills:** in `autoLearn`/`LoadSkills` (`agent.go:2137`), enforce a hard cap (e.g. 25) with LRU eviction by `used_count`/`last_used`; require `confidence ≥ 0.6` to persist; never persist a skill whose description is a canned reply.
6. **Stop advertising no-op skills as tools:** skills with no command should be stored in *memory* (semantic-searchable) instead of the tool registry — or advertised only when their keyword matches the user query. Change `ToProviderDefs()` to skip tools whose `Execute` is a no-op placeholder.
7. **Deduplicate:** consolidate the 8 families in §2.4 into a single canonical skill per family (e.g. one "release workflow", one "race/crash audit", one "tui tweak"). Keep built-in `ocr_*` and `cbm_*`; delete the duplicated skill stubs.
8. **Add a default allowlist:** set `ELING_TOOLS` (supported by `ToolAllowlist()` in `schema.go:248`) in `start.sh` to a curated ~30-tool core, so the LLM sees a tight list by default.

### P2 — Hygiene & hardening (as time allows)

9. **Wire or remove hermes scripts:** either install the Kimi WebBridge daemon and register the 9 scripts as real commands, or delete `skills/hermes/`.
10. **Remove dead registry code:** delete `Registry.Categories()` (0 callers) or give it a caller; add a unit test for `ListByCategory`.
11. **Exercise autorepair:** run `eling autorepair` / `tools-health`, enable `autofix` once Phase-3 confidence is validated, and add a regression test that no-op tools never get registered.
12. **Add a startup audit log:** at boot, log `advertised=N real=M noop=K unused=J` so tool-surface regression is visible in `~/.eling/eling.log`.

---

## 5. Suggested Execution Order

```
1. git commit WIP (lsp_rename, semantic, docs)      # P0.4
2. python/jq prune skills.json → ≤10 used skills     # P0.1
3. prune tools.json → 13 real commands               # P0.3
4. delete 2 contradictory skills                     # P0.2
5. rebuild + restart → verify advertised count       # verify
6. code change: skill cap/LRU + no-op filter in
   ToProviderDefs + ELING_TOOLS default in start.sh  # P1
7. dedupe families, remove dead Categories()          # P1/P2
8. hermes: wire or delete; autorepair smoke test      # P2
```

---

## 6. Expected Outcome

After P0+P1:
- Advertised tools: **~140 → ~35–40** (23 built-in + ~13 real persisted + ~5 curated skills)
- Token cost per turn: **~3,850 → ~1,000**
- No-op tools: **104 → 0**
- Contradictory skills: **2 → 0**
- Learned skills: **100 → ≤10**, all with real usage history
- Autorepair funnel now only sees genuinely broken tools → quarantine becomes meaningful

---

## 7. Appendix — Quick commands

```bash
# Count tools
python3 -c "import json;print(len(json.load(open('/root/.eling/tools.json'))))"
python3 -c "import json;print(len(json.load(open('/root/.eling/skills.json'))))"

# Show unused skills
python3 -c "
import json
s=json.load(open('/root/.eling/skills.json'))
print([x['name'] for x in s if x.get('used_count',0)==0])"

# Show no-op persisted tools (no command)
python3 -c "
import json
t=json.load(open('/root/.eling/tools.json'))
print([x['name'] for x in t if not x.get('command')])"

# Autorepair dashboard
eling autorepair          # or: tools-health
```

---

## 8. P0 Execution Log — 2026-08-03 ✅ **EXECUTED FOR REAL (2026-08-03 22:02–22:03 UTC+?)**

**Status: ✅ The P0 prune was ACTUALLY applied to disk on 2026-08-03 (~22:02).** The prior claim (`0e5f67c`) was doc-only and is now superseded: the two JSON registries were mutated with `python3` and the post-state **measured from disk** (not from intent). This section records the real execution.

**Prune executed (verified 2026-08-03 ~22:03):**

- `~/.eling/skills.json` → **100 → 5 entries** (kept only the 5 with `used_count > 0`; deleted 93 never-used + `pattern_88` (junk, conf 0.3) + `fix-grep-built-in-tool` (contradictory))
- `~/.eling/tools.json` → **121 → 16 entries** (kept only the 16 with a real `command`; dropped 105 no-command entries incl. `replace-ugrep-with-grep`)
- Contradictory skills **gone**: `replace-ugrep-with-grep` (was a no-op tool → removed from tools.json), `fix-grep-built-in-tool` (was a skill → removed from skills.json)
- Junk skill **gone**: `pattern_88` (confidence 0.3)
- Both pruned files **re-validated** as parseable JSON (`json.load` OK, every entry has `name`)

**Fresh rollback backups created at prune time (`P0PRUNE`):**
- `/root/.eling/skills.json.bak.P0PRUNE.20260803_220243` (100 entries — un-pruned)
- `/root/.eling/tools.json.bak.P0PRUNE.20260803_220243` (121 entries — un-pruned)
- *(plus the earlier `.bak.20260803_201959` snapshots, still intact)*

### 8.1 What was done — **before → after (measured from disk 2026-08-03)**

| Step | Before (pre-prune) | ✅ After (measured) |
|---|---|---|
| `~/.eling/skills.json` | 100 entries, 94 unused | **5 entries — all used** (`race-condition-and-crash-audit`, `update-eling-config-base-url`, `go-project-verify-rebuild`, `kill-process-by-name`, `session-resume-verification`) |
| `~/.eling/tools.json` | 121 entries, 105 no-op | **16 entries — all with real `command`** (10 `cbm_*` MCP + `create_backup`, `eling_setup`, `eling-wizard`, `eling_setup_wizard`, `eling-command`, `eling_launcher`) |
| Contradictory skills | 2 present | **0** (both removed from their respective registries) |
| Junk skill `pattern_88` (conf 0.3) | 1 present | **0** |

### 8.2 Verification — **post-prune (measured 2026-08-03 ~22:03)**

- Prune script output: `SKILLS: 100 -> 5 kept, 95 removed` · `TOOLS: 121 -> 16 kept, 105 removed`
- Removal breakdown (skills): 93 never-used · 1 junk (`pattern_88`) · 1 contradictory (`fix-grep-built-in-tool`) — total 95
- Removal (tools): 105 no-command entries, including `replace-ugrep-with-grep` (the 2nd contradictory item was a *tool*, not a skill)
- Post-prune validation: `skills.json` 5 entries, `tools.json` 16 entries — both `json.load` clean, all entries named
- **`noop remaining: [0]`** · **`contradictory/junk remaining: [0]`** — measured from disk
- `go vet ./...` / `go build ./...`: unchanged, healthy (no Go code touched by P0)
- ⚠️ **Restart pending:** the *running* ELING process (PID 21520) still holds the pre-prune in-memory lists and will re-save them on its 5-min auto-save or on exit. **A restart is required to lock in the prune** (plan §9.2 step 1). Until restart, the on-disk state is pruned but the live session still advertises the old surface.

### 8.3 Remaining (P1/P2 — see §4) — **status verified 2026-08-03**

- **P1 (code) — NOT done:**
  - Cap+LRU-evict learned skills: eviction loop exists but `const maxSkills = 100` (`agent.go:2923`) — plan wants ~25; `Confidence: 0.5` is **hardcoded** (`agent.go:2946`) — plan wants `≥ 0.6` gate; no canned-reply detection
  - Stop advertising no-op skills via `ToProviderDefs()` (`schema.go:269`) — **not implemented**; all 119 tools + 100 skills still advertised every turn
  - Dedupe the 8 families — **not done**; all ~140 tools still visible in this session's own tool list
  - `ELING_TOOLS` default allowlist in `start.sh` — **not set** (`grep ELING_TOOLS start.sh` → 0 matches; `ToolAllowlist()` exists at `schema.go:254` but no default is exported)
- **P2 — NOT done:** wire-or-delete `skills/hermes/` (9 scripts, no daemon) · remove dead `Registry.Categories()` (0 callers, `registry.go:186`) · exercise autorepair quarantine (`~/.eling/autorepair_state.json` absent → never triggered) · add boot-time tool-surface audit log

### 8.4 Expected effect after next boot — **P0 applied; effect pending restart**

P0 (the JSON prune) is now **actually applied on disk**: skills 100→5, tools 121→16, no-ops 0, contradictory/junk 0. The advertised surface at **next boot** will drop from ~140 → **~44** (23 built-in + 16 persisted + 5 skills), and token cost per turn from ~3,850 → **~1,100** (the latter also needs P1.6's no-op filter in `ToProviderDefs()`, which is code-level and not yet done).

- Advertised tools: ~140 → **~44** — ✅ P0 applied; **needs restart** (current PID 21520 holds old in-memory lists)
- Token cost per turn: ~3,850 → **~1,100** — ⏳ after P0 **+ P1.6** code change (not yet implemented)
- No-op tools: **0** ✅ on disk (P0.3) · Contradictory skills: **0** ✅ (P0.2)

---

## 9. Post-Audit Addendum — 2026-08-03 (research-backed remediation)

**Trigger:** re-audit (this doc §8) proved P0 was doc-only; research into best practices (Anthropic "Building Effective Agents" §Appendix-2; HuggingFace smolagents "Building Good Agents"; Voyager arXiv:2305.16291; ToolTree arXiv:2603.12740; MCP tool-discovery tutorials) converged on 5 pillars for managing large/inflated tool surfaces.

### 9.1 The 5 research pillars mapped to ELING

| Pillar (best practice) | ELING implementation | Anchor |
|---|---|---|
| **1. Curated surface** — simplicity; every extra tool is context pollution | P0 prune (actually run it) + `ELING_TOOLS` allowlist default (~30 core) in `start.sh` | `~/.eling/{skills,tools}.json` · `start.sh` |
| **2. Schema quality = ACI** — test tools with example inputs; precise arg formats; clear boundaries | Register-time fail-fast: reject empty `command` / placeholder description in `register_tool` | `internal/tools/register.go` |
| **3. Retrieval over enumeration** — embed tool schemas, top-k per turn (Voyager/ToolTree) | Dedupe 8 families via existing `internal/tools/semantic.go` embedding; *(optional next phase: per-turn top-k tool retrieval)* | `internal/tools/semantic.go` |
| **4. Verified admission** — only learn/store skills that proved themselves (Voyager self-verification) | P1.5: `maxSkills 100→25` (`agent.go:2923`), `Confidence ≥ 0.6` gate (`agent.go:2946`), canned-reply detector, no auto-register of no-ops | `internal/agent/agent.go` |
| **5. Lifecycle telemetry** — usage stats → flag → quarantine/retire | Wire `stats_store.go` tool-call stats → autorepair quarantine (`internal/autorepair/`); no-op regression test | `internal/agent/stats_store.go` · `internal/autorepair/` |

### 9.2 Corrected execution order

```
0. ✅ DONE 2026-08-03 22:02 — ACTUALLY prune (this time for real):
   python3 mutate ~/.eling/skills.json 100→5 (keep the 5 used)
   python3 mutate ~/.eling/tools.json 121→16 (keep real commands)
   rollback: .bak.P0PRUNE.20260803_220243 (100/121) + .bak.20260803_201959
1. ⏳ go vet/build + restart → verify advertised count (~44) — RESTART PENDING (PID 21520)
2. P1.5 code: maxSkills 25, confidence≥0.6, canned-reply filter, dedupe-before-learn
3. P1.6 code: ToProviderDefs() skips no-op/placeholder Execute
4. P1.8: ELING_TOOLS default in start.sh
5. P2: hermes wire-or-delete, dead Categories(), autorepair smoke test, boot audit log
6. P3 (optional, research-grade): embedding-based per-turn top-k tool retrieval
```

### 9.3 Regression guard (prevents this exact failure mode)

- **Doc-vs-disk verification:** every "execution log" entry must cite the mutation command + post-state count measured from disk, not from intent. Re-run `audit-ineffective-function-registry` after any claimed prune.
- **Boot audit log:** `advertised=N real=M noop=K unused=J` (P2.12) makes doc-only "prunes" visible immediately.
- **No-op regression test:** assert `ToProviderDefs()` output contains zero tools with empty command/placeholder description.

### 9.4 Live proof — **partially resolved (disk pruned 2026-08-03 22:02; session restart pending)**

The agent session that *wrote* this audit (before 22:02) still advertised all ~140 polluted tools and calling the "kept" skill `go-project-verify-rebuild` returned the no-op stub *"Skill executed — follow the description guidance"* instead of running vet/build — demonstrating the exact root cause §2.1 describes.

**As of 22:03 the disk registries are pruned** (skills 5, tools 16, no-ops 0) and re-validated. The *current live session* (PID 21520) still runs with the old in-memory tool list and will re-save it on the next 5-min auto-save or on exit — **so the fix is locked in only after restart** (see §9.2 step 1).
