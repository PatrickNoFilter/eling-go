package tui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestLegendWrapsToWidth verifies the abbreviation legend (mem/mcp/tls/skl/snd)
// is wrapped to the given terminal width so it never overflows the TUI.
func TestLegendWrapsToWidth(t *testing.T) {
	cases := []struct {
		width      int
		maxLineLen int // the widest a rendered line may be (width, or content width when width is tiny)
	}{
		{30, 30},
		{40, 40},
		{50, 50},
		{80, 80},
		{120, 120},
	}
	for _, c := range cases {
		got := legendText(c.width)
		plain := stripANSI(got)
		lines := strings.Split(plain, "\n")
		if len(lines) < 1 {
			t.Fatalf("width=%d: legend rendered empty", c.width)
		}
		for _, ln := range lines {
			if n := utf8.RuneCountInString(ln); n > c.maxLineLen {
				t.Errorf("width=%d: line %q is %d runes, exceeds %d", c.width, ln, n, c.maxLineLen)
			}
		}
	}
}

// TestLegendWideWidthSingleLine verifies that on a wide terminal the whole
// legend fits on one line (no unnecessary wrapping).
func TestLegendWideWidthSingleLine(t *testing.T) {
	got := legendText(120)
	plain := stripANSI(got)
	if strings.Contains(plain, "\n") {
		t.Errorf("width=120: legend unexpectedly wrapped:\n%s", plain)
	}
}

// TestWelcomeMsgContainsAllAbbreviations ensures every abbreviation the user
// asked about (mem, mcp, tls, skl, snd) is present in the explanation.
func TestWelcomeMsgContainsAllAbbreviations(t *testing.T) {
	msg := welcomeMsg(60)
	plain := stripANSI(msg)
	for _, abbr := range []string{"mem", "mcp", "tls", "skl", "snd"} {
		if !strings.Contains(plain, abbr+"=") {
			t.Errorf("welcome message missing %q explanation:\n%s", abbr, plain)
		}
	}
}
