# TUI Subsystem — `internal/tui`

The terminal UI that wraps the agent for interactive use. Bubble Tea /
Lipgloss based. `internal/tui/tui.go` (~49KB) is the single main file plus
focused unit tests (`legend_test.go`, `paste_test.go`).

## Feature Highlights

### Scrolling Marquee Banner
- `bannerText` constant drives an animated marquee header (scrolling text,
  configurable speed).
- `bannerOffset` field tracks the marquee scroll position; rendered on a
  timer tick.

### Paste-Safe Multiline Input
- Detects paste via bracketed-paste events where available; otherwise
  detects rapid key bursts (<60ms apart) to avoid garbling pasted content.
- Multi-line input supported without breaking the TUI.

### Legend & Colors
- Legend line added to the header (key hints), e.g. `legend_test.go`.
- Color scheme is centralized in a style var (e.g. `hdrSty`); changeable via
  the `change-tui-colors` skill.

### Dynamic Wrap-to-Width
- `tui-dynamic-wrap-to-width` skill: text is wrapped to the current terminal
  width using the existing `wrapText()` helper, recalculated on resize.

## Stats Display

The TUI renders `agent.GetStats()` in several views (header summary,
full stats panel). See [`agent.md`](./agent.md) → Stats for the fields —
including A5 tool success-rate / latency / per-provider breakdowns.

## Interactions

- Plan mode toggle (`/plan`) — TUI attaches an interactive `PlanApprover`.
- Tool events streamed live (`ToolCallEvent` with thinking indicators).
- Resize handling re-wraps text and re-renders the marquee.

## Related

- [`agent.md`](./agent.md) — agent API consumed by the TUI
- [`server.md`](./server.md) — `--daemon-url` mode drives the TUI remotely
