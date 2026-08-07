// Package layers - Output shaping for end messages (P1, ECCADAption).
//
// The final assistant message of a turn is *state* to future turns
// (resume/context). This layer enforces a numeric budget and a format policy
// just-in-time, at the single choke point where the final string is produced,
// so the user-facing closing message is predictable and never blows the budget.
//
// All behaviors are OPT-IN and driven by config: the zero-value policy is a
// full passthrough (preserves today's exact behavior). The layer never
// hard-fails a turn.
package layers

import (
	"strings"
)

// EndMessagePolicy caps the agent's final assistant message. The zero value
// means "policy inactive" → full passthrough (no capping, no reformatting).
type EndMessagePolicy struct {
	MaxRunes       int  // hard ceiling on the final message length (runes); 0 = off
	MaxParas       int  // max paragraph count (blank-line split); 0 = off
	DisallowMarkdown bool // strip common markdown bullets/bolding; 0 (false) = off
}

// Active reports whether any shaping constraint is enabled. When all fields
// are zero, `NewEndMessage` is a no-op passthrough.
func (p EndMessagePolicy) Active() bool {
	return p.MaxRunes > 0 || p.MaxParas > 0 || p.DisallowMarkdown
}

// EndMessageWrap is a shaped message along with an audit trail.
type EndMessageWrap struct {
	content string
	used    int    // runes consumed after shaping (audit)
	note    string // short audit note describing what changed
	shaped  bool   // true if any policy rule fired (was not a pure passthrough)
}

// String returns the shaped final content.
func (w EndMessageWrap) String() string {
	return w.content
}

// Shaped reports whether any policy rule actually changed the content.
func (w EndMessageWrap) Shaped() bool {
	return w.shaped
}

// Used reports the rune count of the shaped content (audit/telemetry).
func (w EndMessageWrap) Used() int {
	return w.used
}

// Note returns the audit note describing what was applied ("" if none).
func (w EndMessageWrap) Note() string {
	return w.note
}

const truncationTrailer = "\n… (truncated to respect output budget)"
const paraTrailer = "\n… (paragraphs trimmed to budget)"

// NewEndMessage shapes msg under policy p.
//
// Behavior is a pure function of the inputs (stateless, warm-cache friendly):
//   - runes: measure `len([]rune(msg))` (runes, not bytes); if over the cap,
//     cut at the last space before the cap and append the truncation trailer.
//   - paras: split on "\n\n"; if more than the cap, keep the first cap and
//     append the paragraph trailer.
//   - markdown: strip "** … ", " * ", "- " bullet prefixes and bold markers.
//
// It always returns a valid wrap and never errs; the caller decides whether to
// adopt it. When the policy is inactive the wrap is a pure passthrough with
// `note` set to "noop".
func NewEndMessage(p EndMessagePolicy, msg string) EndMessageWrap {
	wrap := EndMessageWrap{content: msg, used: len([]rune(msg)), note: "noop"}

	if !p.Active() {
		return wrap
	}

	// 1) Rune cap.
	if p.MaxRunes > 0 {
		runes := []rune(msg)
		if len(runes) > p.MaxRunes {
			maxRunes := p.MaxRunes
			// Never split a multi-rune (wide) char: cut on a space boundary,
			// falling back to a plain hard cut at the cap.
			if maxRunes > len(runes) {
				maxRunes = len(runes)
			}
			cut := string(runes[:maxRunes])
			if idx := strings.LastIndex(cut, " "); idx >= 0 {
				cut = cut[:idx]
			}
			msg = cut + truncationTrailer
			wrap.shaped = true
			wrap.note = "runes_capped"
		}
	}

	// 2) Paragraph cap.
	if p.MaxParas > 0 {
		paras := strings.Split(msg, "\n\n")
		if len(paras) > p.MaxParas {
			msg = strings.Join(paras[:p.MaxParas], "\n\n") + paraTrailer
			wrap.shaped = true
			if wrap.note == "noop" {
				wrap.note = "paras_capped"
			} else {
				wrap.note += "+paras_capped"
			}
		}
	}

	// 3) Markdown stripping.
	if p.DisallowMarkdown {
		msg = stripMarkdown(msg)
		wrap.shaped = true
		if wrap.note == "noop" {
			wrap.note = "markdown_stripped"
		} else {
			wrap.note += "+markdown_stripped"
		}
	}

	wrap.content = msg
	wrap.used = len([]rune(msg))
	return wrap
}

func stripMarkdown(s string) string {
	var lines []string
	for _, ln := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(trimmed, "- "), strings.HasPrefix(trimmed, "* "),
			strings.HasPrefix(trimmed, "• "):
			// Drop bullet lines entirely (formatting, not content).
			continue
		case strings.HasPrefix(trimmed, "**"), strings.HasSuffix(trimmed, "**"):
			// Strip bold markers, keep the text.
			ln = strings.TrimPrefix(ln, "**")
			ln = strings.TrimSuffix(ln, "**")
		}
		lines = append(lines, ln)
	}
	return strings.Join(lines, "\n")
}