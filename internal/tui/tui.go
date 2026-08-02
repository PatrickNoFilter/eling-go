package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"eling/internal/agent"
	"eling/internal/lsp"
	"eling/internal/tools"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	initInputHeight = 1
	maxInputHeight  = 8

	// Paste-burst detection. When key events arrive closer together than
	// pasteBurstWindow they are treated as a paste burst. While a paste is
	// active, Enter inserts a newline into the input buffer instead of
	// submitting, so pasted multi-line text is held for review and only sent
	// when the user deliberately presses Enter after the burst has settled.
	pasteBurstWindow = 60 * time.Millisecond  // max gap between keys to count as one burst
	pasteMinBurst    = 2                      // keys in a row that fast => paste in progress
	pasteGrace       = 350 * time.Millisecond // paste mode persists this long after the last burst key
)

var (
	hdrSty       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#89B4FA"))
	dimSty       = lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086"))
	errSty       = lipgloss.NewStyle().Foreground(lipgloss.Color("#F38BA8"))
	infSty       = lipgloss.NewStyle().Foreground(lipgloss.Color("#89B4FA"))
	usrSty       = lipgloss.NewStyle().Foreground(lipgloss.Color("#74C7EC"))
	txtSty       = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF"))
	toolSty      = lipgloss.NewStyle().Foreground(lipgloss.Color("#F9E2AF"))              // yellow for running
	toolOKSty    = lipgloss.NewStyle().Foreground(lipgloss.Color("#A6E3A1"))              // green for success
	toolFailSty  = lipgloss.NewStyle().Foreground(lipgloss.Color("#F38BA8"))              // red for failure
	timerSty     = lipgloss.NewStyle().Foreground(lipgloss.Color("#6C7086"))              // dim for running timer
	timerOKSty   = lipgloss.NewStyle().Foreground(lipgloss.Color("#A6E3A1"))              // green for success timer
	timerFailSty = lipgloss.NewStyle().Foreground(lipgloss.Color("#F38BA8"))              // red for failure timer
	reasonSty    = lipgloss.NewStyle().Foreground(lipgloss.Color("#89B4FA")).Italic(true) // blue italic for reasoning
	resultSty    = lipgloss.NewStyle().Foreground(lipgloss.Color("#CDD6F4"))              // white for tool results
	planSty      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#CBA6F7"))   // lavender bold for plan header
	diffAddSty   = lipgloss.NewStyle().Foreground(lipgloss.Color("#A6E3A1"))              // green for added lines
	diffDelSty   = lipgloss.NewStyle().Foreground(lipgloss.Color("#F38BA8"))              // red for deleted lines
	diffHdrSty   = lipgloss.NewStyle().Foreground(lipgloss.Color("#89B4FA"))              // blue for diff header
	banSty       = lipgloss.NewStyle().Foreground(lipgloss.Color("#F5C2E7"))              // pink for scrolling banner
)

// bannerText is the repeating message for the scrolling marquee.
const bannerText = "  ✦ ELING — Auto-Learning Evolving AI Agent  ✦  Adaptive • Intelligent • Autonomous  ✦  "

type clockTick struct{}

type respMsg string
type errMsg string

// planMsg carries a drafted execution plan from the Ask goroutine so the
// TUI can render it as a checklist and prompt for y/N/Esc approval.
type planMsg string

// toolCallMsg is sent from the Ask goroutine to signal a tool invocation.
type toolCallMsg agent.ToolCallEvent

// genMsg wraps any tea.Msg with a generation counter to detect stale messages
// from old goroutines that are still running after a new query was submitted.
type genMsg struct {
	gen int
	msg tea.Msg
}

// activeTool tracks a running tool invocation in the viewport.
type activeTool struct {
	seqID     int
	name      string
	msgIdx    int // index into m.messages
	startTime time.Time
	preview   string // code preview snippet
}

// Model is the Bubbletea model for the ELING TUI.
type Model struct {
	agent           *agent.Agent
	vp              viewport.Model
	input           textarea.Model
	messages        []string
	width           int
	height          int
	ready           bool
	loading         bool
	now             time.Time
	startTime       time.Time
	loc             *time.Location // timezone location for clock display
	spinner         spinner.Model
	inputVisible    int                    // current visible height set on the textarea widget
	history         []string               // previously submitted inputs (newest last)
	historyIdx      int                    // -1 = current draft, 0..len-1 = history position
	msgCh           chan tea.Msg           // channel for tool-call / response messages
	cancel          context.CancelFunc     // cancels current AskStream
	activeTools     []activeTool           // currently-in-flight tool calls
	thinkingIdx     int                    // index of the "thinking..." line, -1 when none
	interruptedOnce bool                   // true after first Ctrl+C during loading
	maxMessages     int                    // max messages to keep; 0 = unlimited
	bannerOffset    int                    // scrolling banner position offset
	submitGen       int                    // generation counter incremented on each submit; used to detect stale messages
	lastKeyAt       time.Time              // timestamp of the most recent key event (paste-burst detection)
	pasteBurst      int                    // consecutive keys arriving within pasteBurstWindow
	pasting         bool                   // true while a paste burst is active; Enter inserts newline instead of submitting
	pasteUntil      time.Time              // pasting stays true until this time
	awaitingPlan    bool                   // true while a drafted plan is waiting for y/N/Esc approval
	planCh          chan agent.PlanVerdict // receives the user's plan verdict (non-nil only while awaitingPlan)
}

// NewProgram creates a new Bubbletea program with the given agent and timezone location.
func NewProgram(ag *agent.Agent, loc *time.Location) *tea.Program {
	s := spinner.New()
	s.Style = dimSty

	ti := textarea.New()
	ti.Prompt = "> "
	ti.Placeholder = "Enter to send · Alt+Enter=newline · pasted multi-line text is held until Enter"
	ti.ShowLineNumbers = false
	ti.CharLimit = 0
	ti.MaxHeight = maxInputHeight
	ti.FocusedStyle.CursorLine = lipgloss.NewStyle().Foreground(lipgloss.Color("#74C7EC"))
	ti.FocusedStyle.Base = lipgloss.NewStyle().Foreground(lipgloss.Color("#74C7EC"))
	ti.FocusedStyle.Text = lipgloss.NewStyle().Foreground(lipgloss.Color("#74C7EC")) // light blue text
	ti.FocusedStyle.Placeholder = dimSty
	ti.FocusedStyle.Prompt = lipgloss.NewStyle().Foreground(lipgloss.Color("#74C7EC")).Bold(true) // light blue prompt
	ti.BlurredStyle = ti.FocusedStyle
	ti.SetWidth(80)
	ti.SetHeight(initInputHeight)
	ti.Focus()

	// Default max messages to keep in the viewport; older messages are
	// trimmed when the count exceeds this limit.
	const defaultMaxMessages = 500

	m := Model{
		agent: ag,
		input: ti,
		messages: []string{
			welcomeMsg(80),
			"",
		},
		width:        80,
		height:       24,
		now:          time.Now().In(loc),
		loc:          loc,
		spinner:      s,
		inputVisible: initInputHeight,
		historyIdx:   -1,
		msgCh:        make(chan tea.Msg, 512),
		thinkingIdx:  -1,
		maxMessages:  defaultMaxMessages,
		bannerOffset: 0,
	}
	return tea.NewProgram(m, tea.WithAltScreen())
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.tick(), m.spinner.Tick)
}

func (m Model) tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return clockTick{} })
}

// neededInputLines calculates how many terminal rows the current text needs.
func (m Model) neededInputLines() int {
	val := m.input.Value()
	if val == "" {
		return 1
	}
	textWidth := m.width - 4
	if textWidth < 10 {
		textWidth = 10
	}
	lines := 0
	for _, line := range strings.Split(val, "\n") {
		rc := utf8.RuneCountInString(line)
		if rc == 0 {
			lines++
		} else {
			lines += (rc + textWidth - 1) / textWidth
		}
	}
	if lines < 1 {
		lines = 1
	}
	if lines > maxInputHeight {
		lines = maxInputHeight
	}
	return lines
}

// resizeInput sets the textarea height and the viewport height to match the
// current content. Called only from clockTick so it doesn't interfere with
// the textarea's own keystroke processing.
func (m *Model) resizeInput() {
	needed := m.neededInputLines()
	if needed < 1 {
		needed = 1
	}
	if needed > maxInputHeight {
		needed = maxInputHeight
	}
	if needed == m.inputVisible {
		return
	}
	m.inputVisible = needed
	m.input.SetHeight(needed)

	vpHeight := m.height - 2 - needed
	if vpHeight < 1 {
		vpHeight = 1
	}
	m.vp.Height = vpHeight
	m.vp.SetContent(strings.Join(m.messages, "\n"))
}

// trimMessages removes old messages when the total exceeds maxMessages.
// It always keeps the first message (header) and the most recent ones.
// It also updates stale indices (thinkingIdx, activeTools[].msgIdx) that
// would otherwise point to wrong memory locations and cause SIGBUS.
func (m *Model) trimMessages() {
	if m.maxMessages <= 0 || len(m.messages) <= m.maxMessages {
		return
	}
	oldLen := len(m.messages)
	// Keep the first message (header) and the last maxMessages-1 messages.
	keep := m.maxMessages - 1
	if keep < 0 {
		keep = 0
	}
	// Number of messages removed from the middle (after header, before tail)
	removed := oldLen - (1 + keep) // header(1) + tail(keep)
	if removed < 0 {
		removed = 0
	}

	header := m.messages[0]
	tail := m.messages[oldLen-keep:]
	m.messages = append([]string{header}, tail...)

	// --- Fix stale indices ---

	// Update thinkingIdx: if the thinking line was in the removed portion, clear it.
	if m.thinkingIdx > 0 {
		if m.thinkingIdx <= removed {
			// The thinking line was removed — disable it
			m.thinkingIdx = -1
		} else {
			m.thinkingIdx -= removed
		}
	}

	// Update all active tool msgIdx values
	for i := range m.activeTools {
		if m.activeTools[i].msgIdx > 0 {
			if m.activeTools[i].msgIdx <= removed {
				// This tool's message was removed — mark as unknown
				m.activeTools[i].msgIdx = -1
			} else {
				m.activeTools[i].msgIdx -= removed
			}
		}
	}
}

// handleInputClick positions the cursor in the input textarea based on a
// mouse/touch click at the given display line and column within the input area.
func (m *Model) handleInputClick(displayLine, col int) {
	val := m.input.Value()
	if val == "" {
		m.input.CursorEnd()
		return
	}

	promptWidth := 2 // "> "
	textWidth := m.width - promptWidth
	if textWidth < 1 {
		textWidth = 1
	}

	lines := strings.Split(val, "\n")

	targetRow := len(lines) - 1 // fallback: last row
	targetCol := 0

	// Walk logical rows to find which one displayLine falls on.
	acc := 0
	for ri, line := range lines {
		rc := utf8.RuneCountInString(line)
		wrapped := 1
		if rc > 0 {
			wrapped = (rc + textWidth - 1) / textWidth
		}
		if displayLine < acc+wrapped || ri == len(lines)-1 {
			targetRow = ri
			subLine := displayLine - acc
			if subLine < 0 {
				subLine = 0
			}
			if subLine >= wrapped {
				subLine = wrapped - 1
			}
			// Column within this logical line.
			if subLine == 0 {
				targetCol = max(0, col-promptWidth)
			} else {
				targetCol = subLine*textWidth + max(0, col)
			}
			if targetCol > rc {
				targetCol = rc
			}
			break
		}
		acc += wrapped
	}

	// Navigate cursor: go to first row, then down to targetRow.
	m.input.CursorStart()
	for m.input.Line() > 0 {
		m.input.CursorUp()
	}
	for i := 0; i < targetRow; i++ {
		m.input.CursorDown()
	}
	m.input.SetCursor(targetCol)
}

// Update handles all messages and returns the updated model.
// trackKey feeds a key event into the paste-burst detector and returns the
// updated model plus whether we are currently inside a paste. While pasting,
// Enter must insert a newline into the input buffer instead of submitting, so
// pasted multi-line text is never sent prematurely.
func (m Model) trackKey(k tea.KeyMsg) (Model, bool) {
	now := time.Now()
	dt := now.Sub(m.lastKeyAt)
	m.lastKeyAt = now

	// Explicit paste events (bracketed paste mode) always enter paste mode.
	if k.Paste {
		m.pasting = true
		m.pasteUntil = now.Add(pasteGrace)
		return m, true
	}

	// Fast consecutive keys = paste burst (terminals without bracketed paste
	// deliver pasted newlines as real Enter keypresses).
	if dt <= pasteBurstWindow {
		m.pasteBurst++
		if m.pasteBurst >= pasteMinBurst {
			m.pasting = true
			m.pasteUntil = now.Add(pasteGrace)
		}
	} else {
		m.pasteBurst = 1
	}

	// Paste mode expires after a short grace period of no input.
	if now.After(m.pasteUntil) {
		m.pasting = false
	}
	return m, m.pasting
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Unwrap genMsg and check generation to detect stale messages from
	// old goroutines that are still running after a new query was submitted.
	if gm, ok := msg.(genMsg); ok {
		if gm.gen != m.submitGen {
			// Stale message from a previous submission — drop it
			return m, listenForMsg(m.msgCh)
		}
		msg = gm.msg // unwrap to the inner message for normal processing
	}

	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = v.Width
		m.height = v.Height
		vpHeight := m.height - 2 - m.inputVisible
		if vpHeight < 1 {
			vpHeight = 1
		}
		m.input.SetWidth(v.Width)
		if !m.ready {
			m.vp = viewport.New(v.Width, vpHeight)
			m.vp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#74C7EC"))
			m.vp.MouseWheelEnabled = true
			m.ready = true
		} else {
			m.vp.Width = v.Width
			m.vp.Height = vpHeight
		}
		// Re-wrap the welcome legend to the new terminal width so it never
		// overflows the TUI after a resize.
		if len(m.messages) > 0 {
			m.messages[0] = welcomeMsg(m.width)
		}
		m.vp.SetContent(strings.Join(m.messages, "\n"))

	case tea.KeyMsg:
		// While a drafted plan is awaiting approval, intercept y/N/Enter/Esc.
		// The Ask goroutine is blocked on planCh until the user decides.
		if m.awaitingPlan {
			var verdict agent.PlanVerdict = -1
			switch v.Type {
			case tea.KeyEnter:
				verdict = agent.PlanApprove
			case tea.KeyEsc:
				verdict = agent.PlanSkip
			case tea.KeyCtrlC:
				verdict = agent.PlanReject
				if m.cancel != nil {
					m.cancel()
				}
			case tea.KeyRunes:
				s := strings.ToLower(string(v.Runes))
				switch s {
				case "y", "yes":
					verdict = agent.PlanApprove
				case "n", "no":
					verdict = agent.PlanReject
				}
			}
			if verdict >= 0 {
				m.awaitingPlan = false
				ch := m.planCh
				m.planCh = nil
				if ch != nil {
					select {
					case ch <- verdict:
					default:
					}
				}
			}
			return m, nil
		}

		// Scroll keys work even during loading
		if m.loading {
			if v.Type == tea.KeyCtrlC && m.cancel != nil {
				m.cancel()
				tools.KillRunningTools()
				lsp.KillAll() // stop language servers (Phase 3)
				// Save state immediately so the interrupted prompt is persisted
				_ = m.agent.SaveState()
				m.loading = false
				m.activeTools = nil
				m.messages = removeLine(m.messages, m.thinkingIdx)
				m.thinkingIdx = -1
				m.interruptedOnce = true
				m.messages = append(m.messages, dimSty.Render("  interrupted  (Ctrl+C again to exit)"))
				m.vp.SetContent(strings.Join(m.messages, "\n"))
				m.vp.GotoBottom()
				return m, nil
			}
			if v.Type == tea.KeyCtrlC {
				return m, tea.Quit
			}
			switch v.Type {
			case tea.KeyPgUp:
				m.vp.HalfViewUp()
				return m, nil
			case tea.KeyPgDown:
				m.vp.HalfViewDown()
				return m, nil
			case tea.KeyCtrlUp:
				m.vp.LineUp(3)
				return m, nil
			case tea.KeyCtrlDown:
				m.vp.LineDown(3)
				return m, nil
			case tea.KeyUp:
				m.vp.LineUp(1)
				return m, nil
			case tea.KeyDown:
				m.vp.LineDown(1)
				return m, nil
			}
			if v.Alt {
				// Alt+Up/Down already handled above; this avoids double-handling.
				// But as a safety net, let's just return.
			}
			return m, nil
		}
		// Feed the key into the paste-burst detector. While pasting, Enter
		// inserts a newline instead of submitting (see KeyEnter below) so
		// pasted multi-line text is held in the input buffer for review.
		m, pasting := m.trackKey(v)

		switch v.Type {
		case tea.KeyCtrlC:
			if m.interruptedOnce {
				return m, tea.Quit
			}
			return m, tea.Quit
		case tea.KeyEnter:
			if v.Alt {
				m.input, _ = m.input.Update(tea.KeyMsg{
					Type:  tea.KeyRunes,
					Runes: []rune("\n"),
				})
				return m, nil
			}
			if pasting {
				// Enter is part of a paste burst: keep the newline in the
				// input buffer instead of sending to the agent.
				m.input, _ = m.input.Update(tea.KeyMsg{
					Type:  tea.KeyRunes,
					Runes: []rune("\n"),
				})
				m.resizeInput()
				return m, nil
			}
			m.historyIdx = -1
			return m.submit()
		case tea.KeyTab:
			m.input, _ = m.input.Update(tea.KeyMsg{
				Type:  tea.KeyRunes,
				Runes: []rune("  "),
			})
			return m, nil
		case tea.KeyPgUp:
			m.vp.HalfViewUp()
			return m, nil
		case tea.KeyPgDown:
			m.vp.HalfViewDown()
			return m, nil
		case tea.KeyCtrlUp:
			m.vp.LineUp(3)
			return m, nil
		case tea.KeyCtrlDown:
			m.vp.LineDown(3)
			return m, nil
		}

		// Alt+Up/Down: scroll the conversation viewport.
		if v.Alt {
			switch v.Type {
			case tea.KeyUp:
				m.vp.LineUp(1)
				return m, nil
			case tea.KeyDown:
				m.vp.LineDown(1)
				return m, nil
			}
		}

		// When input is empty, Up/Down scroll the viewport instead of cycling history.
		if m.input.Value() == "" {
			if v.Type == tea.KeyUp {
				m.vp.LineUp(1)
				return m, nil
			}
			if v.Type == tea.KeyDown {
				m.vp.LineDown(1)
				return m, nil
			}
		}

		// Non-Alt Up/Down: cycle input history (only when input is non-empty).
		if !v.Alt {
			if v.Type == tea.KeyUp && len(m.history) > 0 {
				if m.historyIdx == -1 {
					m.historyIdx = len(m.history) - 1
				} else if m.historyIdx > 0 {
					m.historyIdx--
				}
				m.input.SetValue(m.history[m.historyIdx])
				m.input.CursorEnd()
				return m, nil
			}
			if v.Type == tea.KeyDown && m.historyIdx != -1 {
				m.historyIdx++
				if m.historyIdx >= len(m.history) {
					m.historyIdx = -1
					m.input.SetValue("")
				} else {
					m.input.SetValue(m.history[m.historyIdx])
				}
				m.input.CursorEnd()
				return m, nil
			}
		}

		// Any other key resets history position (next Up = newest).
		m.historyIdx = -1

		// All other keys go to the textarea for normal editing.
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.resizeInput()
		return m, cmd

	case tea.MouseMsg:
		me := tea.MouseEvent(v)
		// Ignore invalid/negative coordinates that some terminals send
		if me.Y < 0 || me.X < 0 {
			return m, nil
		}
		// Mouse wheel → viewport scrolling.
		if me.IsWheel() {
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		}
		// Left-click in the input area positions the cursor.
		if me.Action == tea.MouseActionPress && me.Button == tea.MouseButtonLeft {
			inputStartY := 2 + m.vp.Height
			if me.Y >= inputStartY {
				m.handleInputClick(me.Y-inputStartY, me.X)
				return m, nil
			}
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(v)
		return m, cmd

	case clockTick:
		m.now = time.Now().In(m.loc)
		m.bannerOffset++ // advance scrolling banner
		m.resizeInput()
		if !m.ready {
			return m, m.tick()
		}
		// Update elapsed time for all active (running) tool calls
		for i := range m.activeTools {
			at := &m.activeTools[i]
			if at.msgIdx >= 0 && at.msgIdx < len(m.messages) {
				elapsed := m.now.Sub(at.startTime)
				content := fmt.Sprintf("%s (%s)", at.name, fmtElapsed(elapsed))
				m.messages[at.msgIdx] = toolSty.Render(wrapWithPrefix("  \u25b6 ", content, m.width))
			}
		}
		// Update the "thinking..." timer line
		if m.thinkingIdx >= 0 && m.thinkingIdx < len(m.messages) {
			elapsed := m.now.Sub(m.startTime)
			m.messages[m.thinkingIdx] = dimSty.Render(fmt.Sprintf("  thinking... %ds", int(elapsed.Seconds())))
		}
		// Preserve scroll position across trim so the user doesn't jump
		savedOffset := m.vp.YOffset
		m.trimMessages()
		m.vp.SetContent(strings.Join(m.messages, "\n"))
		// If trim shrank content such that our old offset is past the end,
		// snap to bottom instead of showing a blank area
		if savedOffset > 0 && m.vp.ScrollPercent() >= 1.0 {
			m.vp.GotoBottom()
		}
		return m, m.tick()

	case toolCallMsg:
		evt := agent.ToolCallEvent(v)
		if evt.Reasoning != "" {
			// Display reasoning content from the model
			m.messages = removeLine(m.messages, m.thinkingIdx)
			m.thinkingIdx = -1
			// Format reasoning in italic blue
			reasonText := evt.Reasoning
			const maxReasoningLen = 2000
			if len(reasonText) > maxReasoningLen {
				reasonText = reasonText[:maxReasoningLen] + "\n... [reasoning truncated]"
			}
			// Add reasoning section header
			m.messages = append(m.messages, reasonSty.Render("  ── Reasoning ──"))
			// Wrap reasoning text with proper indentation
			for _, line := range strings.Split(wrapText(reasonText, m.width-6), "\n") {
				m.messages = append(m.messages, reasonSty.Render("    "+line))
			}
			m.messages = append(m.messages, dimSty.Render("  ───────────────"))
		} else if evt.IsThinking {
			// Model is reasoning between tool rounds
			if m.thinkingIdx < 0 {
				m.thinkingIdx = len(m.messages)
				m.messages = append(m.messages, dimSty.Render(fmt.Sprintf("  thinking... 0s")))
			}
		} else if evt.IsStart {
			// A new tool call started – clear any thinking line first
			m.messages = removeLine(m.messages, m.thinkingIdx)
			m.thinkingIdx = -1
			idx := len(m.messages)
			preview := toolPreview(evt.Name, evt.Args)
			label := evt.Name
			if preview != "" {
				label += ": " + preview
			}
			m.messages = append(m.messages, toolSty.Render(wrapWithPrefix("  \u25b6 ", label+" (0.0s)", m.width)))
			m.activeTools = append(m.activeTools, activeTool{
				seqID:     evt.SeqID,
				name:      label,
				msgIdx:    idx,
				startTime: time.Now(),
				preview:   preview,
			})
		} else {
			// Tool completed – clear thinking line, update in-place
			m.messages = removeLine(m.messages, m.thinkingIdx)
			m.thinkingIdx = -1
			elapsed := time.Since(m.findToolStart(evt.SeqID))
			msgIdx := m.findToolMsgIdx(evt.SeqID)
			m.removeActiveTool(evt.SeqID)
			if msgIdx >= 0 && msgIdx < len(m.messages) {
				preview := toolPreview(evt.Name, evt.Args)
				label := evt.Name
				if preview != "" {
					label += ": " + preview
				}
				if evt.Error != "" {
					failContent := fmt.Sprintf("%s  %s", label, timerFailSty.Render(fmtElapsed(elapsed)))
					m.messages[msgIdx] = toolFailSty.Render(wrapWithPrefix("  \u2717 ", failContent, m.width))
				} else {
					okContent := fmt.Sprintf("%s  %s", label, timerOKSty.Render(fmtElapsed(elapsed)))
					m.messages[msgIdx] = toolOKSty.Render(wrapWithPrefix("  \u2713 ", okContent, m.width))
				}
				// Render inline diff from result if present
				if evt.ResultText != "" {
					for _, dline := range extractDiff(evt.ResultText) {
						m.messages = append(m.messages, dline)
					}
				}
			}
		}
		// Save auto-scroll state BEFORE SetContent: after SetContent,
		// ScrollPercent() drops below threshold even if user was at bottom,
		// because YOffset stays at the old position while content grew.
		wasAtBottom := m.vp.ScrollPercent() >= 0.99
		m.vp.SetContent(strings.Join(m.messages, "\n"))
		if wasAtBottom {
			m.vp.GotoBottom()
		}
		return m, listenForMsg(m.msgCh)

	case planMsg:
		// A plan draft is ready — render it as a checklist and wait for
		// the user's y/N/Esc verdict (the Ask goroutine is blocked meanwhile).
		m.awaitingPlan = true
		plan := strings.TrimSpace(string(v))
		m.messages = append(m.messages, "", planSty.Render("📋 Proposed plan — approve to execute:"))
		for _, line := range strings.Split(plan, "\n") {
			if line == "" {
				continue
			}
			m.messages = append(m.messages, txtSty.Render("  "+line))
		}
		m.messages = append(m.messages, infSty.Render("  [y] approve   [n] reject   [Esc] skip plan mode"))
		if m.ready {
			wasAtBottom := m.vp.ScrollPercent() >= 0.99
			m.vp.SetContent(strings.Join(m.messages, "\n"))
			if wasAtBottom {
				m.vp.GotoBottom()
			}
		}
		return m, nil

	case respMsg:
		m.loading = false
		m.cancel = nil // reset cancel function
		m.awaitingPlan = false
		m.planCh = nil
		// Remove the thinking line cleanly
		m.messages = removeLine(m.messages, m.thinkingIdx)
		m.thinkingIdx = -1
		elapsed := time.Since(m.startTime)
		elapsedStr := dimSty.Render(fmt.Sprintf("  (%.1fs)", elapsed.Seconds()))
		m.messages = append(m.messages, "", txtSty.Render(wrapText(string(v), m.width-2)), elapsedStr)
		if m.ready {
			wasAtBottom := m.vp.ScrollPercent() >= 0.99
			m.vp.SetContent(strings.Join(m.messages, "\n"))
			if wasAtBottom {
				m.vp.GotoBottom()
			}
		}
		return m, m.tick()

	case errMsg:
		m.loading = false
		m.cancel = nil // reset cancel function
		m.awaitingPlan = false
		m.planCh = nil
		m.messages = removeLine(m.messages, m.thinkingIdx)
		m.thinkingIdx = -1
		elapsed := time.Since(m.startTime)
		elapsedStr := dimSty.Render(fmt.Sprintf("  (%.1fs)", elapsed.Seconds()))
		m.messages = append(m.messages, "", errSty.Render(wrapText("Error: "+string(v), m.width-2)), elapsedStr)
		if m.ready {
			wasAtBottom := m.vp.ScrollPercent() >= 0.99
			m.vp.SetContent(strings.Join(m.messages, "\n"))
			if wasAtBottom {
				m.vp.GotoBottom()
			}
		}
		return m, m.tick()

	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	return m, nil
}

// submit sends the current input text to the agent and clears the input area.
// It launches a goroutine that sends tool-call events and the final response
// through the shared channel, enabling live tool-call display.
func (m Model) submit() (tea.Model, tea.Cmd) {
	text := m.input.Value()
	text = strings.TrimSpace(text)
	// Once the user deliberately submits, paste mode no longer applies.
	m.pasting = false
	m.pasteBurst = 0
	if text == "" {
		return m, nil
	}

	if strings.HasPrefix(text, "/") {
		return m.cmd(text)
	}

	m.history = append(m.history, text)
	m.historyIdx = -1
	m.input.Reset()
	m.inputVisible = initInputHeight
	m.input.SetHeight(initInputHeight)
	vpHeight := m.height - 2 - m.inputVisible
	if vpHeight < 1 {
		vpHeight = 1
	}
	m.vp.Height = vpHeight

	// Wrap user input so it doesn't overflow the viewport
	wrappedInput := wrapText(text, m.width-4)
	// Indent continuation lines with "> " to match prompt
	inputLines := strings.Split(wrappedInput, "\n")
	for i, line := range inputLines {
		if line == "" {
			inputLines[i] = ">"
		} else {
			inputLines[i] = "> " + line
		}
	}
	m.messages = append(m.messages, usrSty.Render(strings.Join(inputLines, "\n")))
	m.loading = true
	m.startTime = time.Now()
	m.activeTools = nil
	m.thinkingIdx = len(m.messages)
	m.messages = append(m.messages, dimSty.Render("  thinking..."))
	m.vp.SetContent(strings.Join(m.messages, "\n"))
	m.vp.GotoBottom()

	// Increment generation counter to detect stale messages from old goroutines
	m.submitGen++
	myGen := m.submitGen

	ag := m.agent
	msgCh := m.msgCh

	// Create a cancellable context for interrupt support
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	// Plan mode: attach an interactive approver so the Ask goroutine can
	// pause with a drafted plan and wait for the user's y/N/Esc verdict.
	if ag.PlanEnabled.Load() {
		planCh := make(chan agent.PlanVerdict, 1)
		m.planCh = planCh
		m.awaitingPlan = false
		ag.SetPlanApprover(func(plan string) agent.PlanVerdict {
			select {
			case msgCh <- genMsg{gen: myGen, msg: planMsg(plan)}:
			case <-ctx.Done():
				return agent.PlanReject
			}
			select {
			case v := <-planCh:
				return v
			case <-ctx.Done():
				return agent.PlanReject
			}
		})
	} else {
		ag.SetPlanApprover(nil)
	}

	// Start a goroutine that sends tool-call events and the final response
	// through the shared channel. The Bubbletea loop reads one message at a
	// time via listenForMsg. Each message is wrapped with the generation
	// counter so stale messages from old goroutines can be detected and dropped.
	go func() {
		defer cancel()
		r, e := ag.Ask(ctx, text, func(evt agent.ToolCallEvent) {
			select {
			case msgCh <- genMsg{gen: myGen, msg: toolCallMsg(evt)}:
			case <-ctx.Done():
				return
			}
		})
		if e != nil {
			select {
			case msgCh <- genMsg{gen: myGen, msg: errMsg(e.Error())}:
			case <-ctx.Done():
			}
		} else {
			select {
			case msgCh <- genMsg{gen: myGen, msg: respMsg(r)}:
			case <-ctx.Done():
			}
		}
	}()

	return m, listenForMsg(m.msgCh)
}

// listenForMsg returns a Cmd that reads one message from the channel.
// Bubbletea calls this repeatedly until the channel returns a nil msg
// (closed or empty).
func listenForMsg(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

// findToolStart returns the start time of an active tool by seqID, or time.Now() as fallback.
func (m *Model) findToolStart(seqID int) time.Time {
	for _, at := range m.activeTools {
		if at.seqID == seqID {
			return at.startTime
		}
	}
	return time.Now()
}

// findToolMsgIdx returns the messages index for an active tool by seqID.
func (m *Model) findToolMsgIdx(seqID int) int {
	for _, at := range m.activeTools {
		if at.seqID == seqID {
			return at.msgIdx
		}
	}
	return -1
}

// removeActiveTool removes a tool from the active list by seqID.
func (m *Model) removeActiveTool(seqID int) {
	for i, at := range m.activeTools {
		if at.seqID == seqID {
			m.activeTools = append(m.activeTools[:i], m.activeTools[i+1:]...)
			return
		}
	}
}

// fmtElapsed formats a duration as "1.2s" with one decimal place.
// toolPreview extracts a short (≤60 chars) snippet from tool call arguments
func toolPreview(name string, args map[string]interface{}) string {
	candidates := []string{"command", "file_path", "path", "url", "query", "old_string", "content", "script"}
	for _, key := range candidates {
		if val, ok := args[key]; ok {
			if s, ok2 := val.(string); ok2 && s != "" {
				return agent.TruncateStr(s, 60)
			}
		}
	}
	return ""
}

func fmtElapsed(d time.Duration) string {
	secs := d.Seconds()
	if secs < 10 {
		return fmt.Sprintf("%.1fs", secs)
	}
	return fmt.Sprintf("%ds", int(secs))
}

func (m Model) cmd(c string) (tea.Model, tea.Cmd) {
	pts := strings.Fields(c)
	n := strings.ToLower(pts[0])
	m.messages = append(m.messages, dimSty.Render("  "+c))

	switch n {
	case "/help":
		m.messages = append(m.messages, infSty.Render(`
  /help      /stats     /memory    /recall q  /tools    /skills
  /session   /sessions  /save      /clear     /run      /retry
  /quit
  /add tool|plugin|skill|mcp ...
  /tokens    /mcp       /mcp_connect <name> <cmd...>
  /providers /provider <name>
  /plan      (toggle plan mode: draft + approve before tools)
  /evolve

  Enter=submit  Alt+Enter=newline  paste w/ newlines=held  /run=submit`[1:]))
	case "/stats":
		for k, v := range m.agent.GetStats() {
			m.messages = append(m.messages, fmt.Sprintf("  %s: %v", k, v))
		}
	case "/memory":
		items := m.agent.GetMemory().Recent(5)
		for _, it := range items {
			m.messages = append(m.messages, fmt.Sprintf("  [%s] %s", it.Category, agent.TruncateStr(it.Content, 60)))
		}
		if len(items) == 0 {
			m.messages = append(m.messages, dimSty.Render("  no memories"))
		}
	case "/recall":
		q := strings.Join(pts[1:], " ")
		if q == "" {
			m.messages = append(m.messages, errSty.Render("  usage: /recall <query>"))
		} else {
			for _, it := range m.agent.GetMemory().Recall(q) {
				m.messages = append(m.messages, fmt.Sprintf("  [%s] %s", it.Category, agent.TruncateStr(it.Content, 80)))
			}
		}
	case "/memorize":
		text := strings.Join(pts[1:], " ")
		if text == "" {
			m.messages = append(m.messages, errSty.Render("  usage: /memorize <text>"))
		} else {
			m.agent.GetMemory().Remember(text, "manual", []string{"memorized"})
			m.messages = append(m.messages, infSty.Render("  memorized"))
		}
	case "/tools":
		for _, t := range m.agent.ListTools() {
			m.messages = append(m.messages, fmt.Sprintf("  %s - %s", t.Name, agent.TruncateStr(t.Description, 60)))
		}
	case "/tokens":
		stats := m.agent.GetStats()
		if tb, ok := stats["token_budget"]; ok {
			m.messages = append(m.messages, fmt.Sprintf("  Token budget (max context): %v", tb))
		}
		if tt, ok := stats["total_tokens_used"]; ok {
			m.messages = append(m.messages, fmt.Sprintf("  Total tokens used: %v", tt))
		}
		if tb, ok := stats["token_budget"].(int); ok && tb > 0 {
			if tt, ok := stats["total_tokens_used"].(int); ok && tt > 0 {
				pct := float64(tt) / float64(tb) * 100.0
				m.messages = append(m.messages, fmt.Sprintf("  Budget used: %.1f%%", pct))
			}
		}
		// Show per-entry token estimates
		s := m.agent.GetSession()
		if s != nil {
			m.messages = append(m.messages, fmt.Sprintf("  Session entries: %d", len(s.Entries)))
			for i, e := range s.Entries {
				if i >= 10 {
					m.messages = append(m.messages, fmt.Sprintf("  ... and %d more", len(s.Entries)-i))
					break
				}
				tok := e.Tokens
				if tok <= 0 {
					tok = agent.EstimateTokens(e.Content) + 4
				}
				m.messages = append(m.messages, fmt.Sprintf("  [%d] %s: ~%d tokens", i, e.Role, tok))
			}
		}
	case "/session":
		s := m.agent.GetSession()
		if s != nil {
			m.messages = append(m.messages, fmt.Sprintf("  %s | %d msgs", s.Name, len(s.Entries)))
		}

	case "/sessions":
		sessions, err := m.agent.ListSessions()
		if err != nil {
			m.messages = append(m.messages, errSty.Render("  error: "+err.Error()))
		} else if len(sessions) == 0 {
			m.messages = append(m.messages, dimSty.Render("  no saved sessions"))
		} else {
			m.messages = append(m.messages, infSty.Render("  Saved sessions:"))
			for _, name := range sessions {
				m.messages = append(m.messages, fmt.Sprintf("  - %s", name))
			}
		}

	case "/resume":
		if len(pts) < 2 {
			m.messages = append(m.messages, errSty.Render("  usage: /resume <session_name>"))
		} else {
			name := pts[1]
			contextStr, err := m.agent.ResumeSession(name)
			if err != nil {
				m.messages = append(m.messages, errSty.Render("  error: "+err.Error()))
			} else {
				m.messages = append(m.messages, infSty.Render(fmt.Sprintf("  resumed session %q", name)))
				m.messages = append(m.messages, dimSty.Render("  "+agent.TruncateStr(contextStr, 100)))
			}
		}

	case "/save":
		if e := m.agent.SaveState(); e != nil {
			m.messages = append(m.messages, errSty.Render("  save failed: "+e.Error()))
		} else {
			m.messages = append(m.messages, infSty.Render("  saved"))
		}
	case "/clear":
		m.messages = []string{
			welcomeMsg(m.width),
			"",
		}
		m.thinkingIdx = -1
		m.activeTools = nil
	case "/retry":
		if len(m.history) == 0 {
			m.messages = append(m.messages, errSty.Render("  nothing to retry"))
		} else {
			lastQuery := m.history[len(m.history)-1]
			m.input.SetValue(lastQuery)
			m.input.CursorEnd()
			m.messages = append(m.messages, dimSty.Render("  retrying last query..."))
			// Submit the last query
			m.historyIdx = -1
			return m.submit()
		}
	case "/run":
		// Use text after /run as the prompt; if empty, fall back to input buffer
		text := strings.TrimSpace(strings.TrimPrefix(c, "/run"))
		if text == "" {
			text = strings.TrimSpace(m.input.Value())
		}
		if text == "" {
			m.messages = append(m.messages, dimSty.Render("  nothing to submit"))
		} else {
			m.input.Reset()
			m.inputVisible = initInputHeight
			m.input.SetHeight(initInputHeight)
			vpHeight := m.height - 2 - m.inputVisible
			if vpHeight < 1 {
				vpHeight = 1
			}
			m.vp.Height = vpHeight
			// Inject text into input so submit() reads it
			m.input.SetValue(text)
			m.input.CursorEnd()
			return m.submit()
		}
	case "/quit":
		return m, tea.Quit

	// --- /add tool|plugin|skill|mcp ---
	case "/add":
		if len(pts) < 2 {
			m.messages = append(m.messages, errSty.Render("  usage: /add tool|plugin|skill|mcp <name> [desc] [cmd...]"))
			break
		}
		sub := strings.ToLower(pts[1])
		switch sub {
		case "tool":
			if len(pts) < 4 {
				m.messages = append(m.messages, errSty.Render("  usage: /add tool <name> <description> <command>"))
				break
			}
			name := pts[2]
			descParts := pts[3:]
			cmdIdx := -1
			for i, p := range descParts {
				if strings.HasPrefix(p, "/") || p == "--" || i == len(descParts)-1 {
					cmdIdx = i
					break
				}
			}
			desc := strings.Join(descParts[:cmdIdx], " ")
			command := strings.Join(descParts[cmdIdx:], " ")
			if desc == "" {
				desc = name
			}
			if command == "" {
				m.messages = append(m.messages, errSty.Render("  usage: /add tool <name> <description> <command>"))
				break
			}
			if err := m.agent.AddToolFromCommand(name, desc, command); err != nil {
				m.messages = append(m.messages, errSty.Render("  error: "+err.Error()))
			} else {
				m.messages = append(m.messages, infSty.Render(fmt.Sprintf("  tool %q registered: %s", name, command)))
			}

		case "plugin":
			if len(pts) < 4 {
				m.messages = append(m.messages, errSty.Render("  usage: /add plugin <name> <description> <command>"))
				break
			}
			name := pts[2]
			descParts := pts[3:]
			cmdIdx := -1
			for i, p := range descParts {
				if strings.HasPrefix(p, "/") || p == "--" || i == len(descParts)-1 {
					cmdIdx = i
					break
				}
			}
			desc := strings.Join(descParts[:cmdIdx], " ")
			command := strings.Join(descParts[cmdIdx:], " ")
			if desc == "" {
				desc = name
			}
			if command == "" {
				m.messages = append(m.messages, errSty.Render("  usage: /add plugin <name> <description> <command>"))
				break
			}
			if err := m.agent.AddPluginFromCommand(name, desc, command); err != nil {
				m.messages = append(m.messages, errSty.Render("  error: "+err.Error()))
			} else {
				m.messages = append(m.messages, infSty.Render(fmt.Sprintf("  plugin %q registered", name)))
			}

		case "skill":
			if len(pts) < 3 {
				m.messages = append(m.messages, errSty.Render("  usage: /add skill <name> [description]"))
				break
			}
			name := pts[2]
			desc := ""
			if len(pts) > 3 {
				desc = strings.Join(pts[3:], " ")
			}
			if desc == "" {
				desc = name
			}
			if err := m.agent.AddSkill(name, desc); err != nil {
				m.messages = append(m.messages, errSty.Render("  error: "+err.Error()))
			} else {
				m.messages = append(m.messages, infSty.Render(fmt.Sprintf("  skill %q registered", name)))
			}

		case "mcp":
			if len(pts) < 4 {
				m.messages = append(m.messages, errSty.Render("  usage: /add mcp <name> <command> [args...]"))
				break
			}
			name := pts[2]
			cmdAndArgs := pts[3:]
			if len(cmdAndArgs) == 0 {
				m.messages = append(m.messages, errSty.Render("  usage: /add mcp <name> <command> [args...]"))
				break
			}
			command := cmdAndArgs[0]
			args := cmdAndArgs[1:]
			if err := m.agent.MCP.Connect(context.Background(), name, command, args, nil); err != nil {
				m.messages = append(m.messages, errSty.Render("  error: "+err.Error()))
			} else {
				m.messages = append(m.messages, infSty.Render(fmt.Sprintf("  MCP server %q connected", name)))
			}

		default:
			m.messages = append(m.messages, errSty.Render("  usage: /add tool|plugin|skill|mcp ..."))
		}

	// --- /mcp - list MCP servers ---
	case "/mcp":
		servers := m.agent.MCP.List()
		if len(servers) == 0 {
			m.messages = append(m.messages, dimSty.Render("  no MCP servers connected"))
		} else {
			m.messages = append(m.messages, infSty.Render("  MCP servers:"))
			for _, s := range servers {
				m.messages = append(m.messages, fmt.Sprintf("  - %s", s))
			}
		}

	// --- /mcp_connect <name> <cmd...> ---
	case "/mcp_connect":
		if len(pts) < 3 {
			m.messages = append(m.messages, errSty.Render("  usage: /mcp_connect <name> <command> [args...]"))
			break
		}
		name := pts[1]
		cmdAndArgs := pts[2:]
		if len(cmdAndArgs) == 0 {
			m.messages = append(m.messages, errSty.Render("  usage: /mcp_connect <name> <command> [args...]"))
			break
		}
		command := cmdAndArgs[0]
		args := cmdAndArgs[1:]
		if err := m.agent.MCP.Connect(context.Background(), name, command, args, nil); err != nil {
			m.messages = append(m.messages, errSty.Render("  error: "+err.Error()))
		} else {
			m.messages = append(m.messages, infSty.Render(fmt.Sprintf("  MCP server %q connected", name)))
		}

	// --- /skills - list registered skills ---
	case "/skills":
		skills := m.agent.ListSkills()
		if len(skills) == 0 {
			m.messages = append(m.messages, dimSty.Render("  no skills registered"))
		} else {
			m.messages = append(m.messages, infSty.Render("  Skills:"))
			for _, sk := range skills {
				desc := agent.TruncateStr(sk.Description, 60)
				m.messages = append(m.messages, infSty.Render(fmt.Sprintf("  - %s: %s", sk.Name, desc)))
			}
		}

	// --- /plan [on|off] - toggle plan mode ---
	case "/plan":
		on := strings.ToLower(strings.Join(pts[1:], " "))
		switch on {
		case "on", "1", "yes":
			m.agent.PlanEnabled.Store(true)
			m.messages = append(m.messages, infSty.Render("  plan mode: ON — drafts a plan for approval each turn ([y]/[n]/[Esc])"))
		case "off", "0", "no":
			m.agent.PlanEnabled.Store(false)
			m.messages = append(m.messages, infSty.Render("  plan mode: OFF"))
		default:
			m.agent.PlanEnabled.Store(!m.agent.PlanEnabled.Load())
			state := "OFF"
			if m.agent.PlanEnabled.Load() {
				state = "ON"
			}
			m.messages = append(m.messages, infSty.Render(fmt.Sprintf("  plan mode: %s", state)))
		}

	// --- /evolve - trigger evolution cycle ---
	case "/evolve":
		m.messages = append(m.messages, infSty.Render("  evolution triggered (auto-learning cycle)"))
		_ = m.agent.SaveState()

	// --- /providers - list providers ---
	case "/providers":
		providers := m.agent.ListProviders()
		m.messages = append(m.messages, infSty.Render("  Providers:"))
		for _, p := range providers {
			m.messages = append(m.messages, fmt.Sprintf("  - %s", p))
		}

	// --- /provider <name> - switch provider ---
	case "/provider":
		if len(pts) < 2 {
			m.messages = append(m.messages, errSty.Render("  usage: /provider <name>"))
			break
		}
		name := pts[1]
		if err := m.agent.SetProvider(name); err != nil {
			m.messages = append(m.messages, errSty.Render("  error: "+err.Error()))
		} else {
			m.messages = append(m.messages, infSty.Render(fmt.Sprintf("  switched to provider %q", name)))
		}

	default:
		m.messages = append(m.messages, errSty.Render("  unknown (try /help)"))
	}

	m.vp.SetContent(strings.Join(m.messages, "\n"))
	m.vp.GotoBottom()
	return m, nil
}

func (m Model) View() string {
	if !m.ready {
		return "starting..."
	}

	clock := m.now.Format("15:04:05 MST")
	s := m.agent.GetStats()
	modelStr := fmt.Sprintf("%v", s["model"])

	// Token info
	tokenBudget := 0
	totalTokensUsed := 0
	if tb, ok := s["token_budget"].(int); ok {
		tokenBudget = tb
	}
	if tt, ok := s["total_tokens_used"].(int); ok {
		totalTokensUsed = tt
	}
	tokenStr := ""
	if tokenBudget > 0 {
		if totalTokensUsed > 0 {
			usedK := float64(totalTokensUsed) / 1000.0
			budgetK := float64(tokenBudget) / 1000.0
			tokenStr = fmt.Sprintf("  T %.1fk/%.1fk", usedK, budgetK)
		} else {
			budgetK := float64(tokenBudget) / 1000.0
			tokenStr = fmt.Sprintf("  T 0/%.1fk", budgetK)
		}
	}

	// Compact header with clock, elapsed time (when loading), and stats
	elapsedStr := ""
	if m.loading {
		elapsed := m.now.Sub(m.startTime)
		if elapsed.Seconds() < 10 {
			elapsedStr = fmt.Sprintf(" ⏱ %.1fs", elapsed.Seconds())
		} else {
			elapsedStr = fmt.Sprintf(" ⏱ %ds", int(elapsed.Seconds()))
		}
	}
	sandboxStr := "snd off"
	if tools.SandboxEnabled() {
		sandboxStr = "🏝️ snd on"
	}
	header := hdrSty.Render(fmt.Sprintf(" %s%s  mem %d  mcp %d  tls %d  skl %d  %s",
		clock, elapsedStr, s["memory_items"], s["mcp_servers"], s["tools_available"], s["learned_skills"], sandboxStr))

	// Agent name shown above the input area, with spinner before and token info after
	statusIndicator := "●"
	if m.loading {
		statusIndicator = m.spinner.View()
	}
	agentLabel := dimSty.Render(fmt.Sprintf("  %s %s  \u2501%s", statusIndicator, modelStr, tokenStr))
	if modelStr == "" || modelStr == "<nil>" {
		agentLabel = dimSty.Render(fmt.Sprintf("  %s  \u2501%s", statusIndicator, tokenStr))
	}

	// Scrollable marquee banner at the top
	bannerRunes := []rune(bannerText)
	bannerLen := len(bannerRunes)
	dispW := m.width - 2
	if dispW < 10 {
		dispW = 10
	}
	// Repeat the banner text enough times to cover the display width
	repeats := (dispW / bannerLen) + 3
	fullBanner := strings.Repeat(bannerText, repeats)
	fullRunes := []rune(fullBanner)
	offset := m.bannerOffset % bannerLen
	var bannerLine string
	if offset+dispW <= len(fullRunes) {
		bannerLine = string(fullRunes[offset : offset+dispW])
	} else {
		bannerLine = string(fullRunes[offset:])
	}
	banner := banSty.Render(bannerLine)

	body := m.vp.View()
	input := m.input.View()
	// Paste / multiline hint line shown above the input box.
	hint := ""
	val := m.input.Value()
	if m.pasting {
		hint = dimSty.Render("  ⤵ pasting… newlines are held — Enter won't send yet")
	} else if strings.Contains(val, "\n") {
		hint = dimSty.Render("  multiline input — Enter to send")
	}
	hintLine := ""
	if hint != "" {
		hintLine = hint + "\n"
	}
	return fmt.Sprintf("%s\n%s\n%s\n%s\n%s%s", banner, header, body, agentLabel, hintLine, input)
}

// legendText builds the abbreviation legend (mem/mcp/tls/skl/snd) and wraps
// it to the given terminal width so the explanation never overflows the TUI.
func legendText(width int) string {
	text := "  mem=memory  ·  mcp=MCP servers  ·  tls=tools  ·  skl=skills  ·  snd=sandbox"
	return dimSty.Render(wrapText(text, width))
}

// welcomeMsg builds the first message shown in the viewport, wrapping the
// legend to the current terminal width so it adapts to any screen size.
func welcomeMsg(width int) string {
	return infSty.Render("ELING - AI Agent") + "\n" + legendText(width)
}

func wrapText(text string, width int) string {
	if width < 10 {
		width = 10
	}
	var out strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			out.WriteString("\n")
			continue
		}
		plain := stripANSI(line)
		if utf8.RuneCountInString(plain) <= width {
			if out.Len() > 0 {
				out.WriteString("\n")
			}
			out.WriteString(line)
			continue
		}
		words := strings.Fields(line)
		cur := ""
		for _, w := range words {
			test := cur
			if test != "" {
				test += " "
			}
			test += w
			if utf8.RuneCountInString(stripANSI(test)) > width && cur != "" {
				if out.Len() > 0 {
					out.WriteString("\n")
				}
				out.WriteString(cur)
				cur = w
			} else {
				cur = test
			}
		}
		if cur != "" {
			if out.Len() > 0 {
				out.WriteString("\n")
			}
			out.WriteString(cur)
		}
	}
	return strings.TrimRight(out.String(), "\n")
}

// wrapWithPrefix wraps text so the first line has prefix and subsequent
// lines are indented to align with the content after the prefix.
func wrapWithPrefix(prefix, text string, width int) string {
	prefixWidth := utf8.RuneCountInString(stripANSI(prefix))
	contentWidth := width - prefixWidth
	if contentWidth < 10 {
		contentWidth = 10
	}
	wrapped := wrapText(text, contentWidth)
	lines := strings.Split(wrapped, "\n")
	if len(lines) == 0 {
		return prefix
	}
	lines[0] = prefix + lines[0]
	indent := strings.Repeat(" ", prefixWidth)
	for i := 1; i < len(lines); i++ {
		lines[i] = indent + lines[i]
	}
	return strings.Join(lines, "\n")
}

func stripANSI(s string) string {
	var out strings.Builder
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		if r[i] == 0x1b {
			for i++; i < len(r); i++ {
				if (r[i] >= 'A' && r[i] <= 'Z') || (r[i] >= 'a' && r[i] <= 'z') {
					break
				}
			}
			continue
		}
		out.WriteRune(r[i])
	}
	return out.String()
}

// removeLine removes the line at index i from a string slice.
// Returns the original slice unchanged if i is out of range.
func removeLine(msgs []string, i int) []string {
	if i >= 0 && i < len(msgs) {
		result := make([]string, 0, len(msgs)-1)
		result = append(result, msgs[:i]...)
		result = append(result, msgs[i+1:]...)
		return result
	}
	return msgs
}

func extractDiff(jsonResult string) []string {
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(jsonResult), &data); err != nil {
		return nil
	}
	// The diff is nested inside result.data.diff
	var diffStr string
	if d, ok := data["data"].(map[string]interface{}); ok {
		if ds, ok := d["diff"].(string); ok {
			diffStr = ds
		}
	}
	// Also try top-level (some results may flatten)
	if diffStr == "" {
		if ds, ok := data["diff"].(string); ok {
			diffStr = ds
		}
	}
	if diffStr == "" {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(diffStr, "\n") {
		if line == "" {
			continue
		}
		first := byte(' ')
		if len(line) > 0 {
			first = line[0]
		}
		switch first {
		case '+':
			lines = append(lines, diffAddSty.Render("    "+line))
		case '-':
			lines = append(lines, diffDelSty.Render("    "+line))
		case '@':
			lines = append(lines, diffHdrSty.Render("    "+line))
		default:
			// Context lines (space-prefixed) or headers like ---/+++
			if strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") {
				lines = append(lines, diffHdrSty.Render("    "+line))
			} else {
				lines = append(lines, dimSty.Render("    "+line))
			}
		}
	}
	return lines
}
