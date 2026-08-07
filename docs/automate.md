# D4 — Scheduled Automations (`internal/automate`)

The **scheduled automations** subsystem (short *D4*, plan codename "DeepCode
heist") turns saved workflows into *standing* jobs the daemon owns. A job runs
headlessly on a 5-field crontab schedule — nightly `go test ./...`, weekly
docs-freshness scans, or any natural-language goal — and appends its output to
`~/.eling/automations.log`.

## How it works

Three pieces:

- **Cron parser** (`ParseCron`) — a dependency-free 5-field crontab parser
  (`min hour dom mon dow`). Supports `*`, `*/n`, `N`, `N-M`, comma lists, and
  range+step (`N-M/n`); day-of-month/day-of-week follow cron's OR semantics when
  both are restricted. `Cron.Next(after)` finds the next matching time (bounded
  4-year lookahead, minute granularity).
- **Scheduler** (`Scheduler.Start`) — a single-goroutine ticker that scans the
  job list every `tick`. For each enabled job whose cron time has arrived it
  launches the run in a goroutine. **Overlap guard:** if the previous run of the
  same job is still in-flight when a tick fires, that run is skipped and logged —
  a job is never executed twice concurrently.
- **Runner** — the headless execution abstraction:
  - *Command jobs* (`Command`) run via `/bin/sh -c` (no agent involved).
  - *Goal jobs* (`Goal`) run through a freshly-created agent (`agent.New` +
    `Ask`) — session-less, mirroring the CLI `--run` path.

## CLI

```
eling automate add <name> <command|goal> --schedule "0 2 * * *"   # add a job (use --goal for a goal)
eling automate list                                               # show jobs + scheduler state
eling automate remove <name>                                      # delete a job
eling automate run <name>                                         # run a job once, now
eling automate enable|disable <name>                              # toggle a job
eling automate enable-scheduler|disable-scheduler                 # toggle the daemon scheduler
eling automate logs [n]                                           # last n lines of automations.log
```

Jobs persist in `config.yaml` under `automate.jobs[]`, each carrying `name`,
`command` xor `goal`, `schedule`, `enabled`, and the `last_run`/`last_status`
bookkeeping written after every fire.

## Daemon integration

`eling serve` starts the scheduler when `automate.enabled` is true (it is
**off by default**):

- scheduler `Start(30 * time.Second)` alongside the HTTP server;
- on shutdown, in-flight jobs are cancelled before the server stops.

## Output & failures

- Every run appends a line to `~/.eling/automations.log`
  (`[RFC3339] job "<name>" ran: <truncated output>` / `FAILED: <err>`).
- `LastRun` / `LastStatus` are persisted to config so `eling automate list`
  reflects reality across restarts.
- Failed runs are logged and recorded, never silently retried; overlap skips
  are logged explicitly.

## Testing

`internal/automate/automate_test.go` covers cron parse/match (every, step, dow,
invalid), the overlap guard, due-fire + status persistence, disabled jobs, and
the scheduler-off path; `internal/config/config_test.go` guards the config
round-trip (including old configs without an `automate:` key).
