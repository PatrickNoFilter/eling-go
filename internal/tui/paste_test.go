package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestTrackKeySingleKeyNotPaste: a lone, slow keystroke must not enter paste mode.
func TestTrackKeySingleKeyNotPaste(t *testing.T) {
	m := Model{}
	_, pasting := m.trackKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if pasting {
		t.Fatal("single isolated key must not start paste mode")
	}
}

// TestTrackKeyBurstDetected: two keys arriving within pasteBurstWindow are a paste.
func TestTrackKeyBurstDetected(t *testing.T) {
	m := Model{}
	m, _ = m.trackKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m, pasting := m.trackKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	if !pasting {
		t.Fatal("rapid second key should trigger paste mode")
	}
}

// TestTrackKeyEnterInsideBurst: Enter arriving inside a paste burst stays in
// paste mode so it inserts a newline instead of submitting.
func TestTrackKeyEnterInsideBurst(t *testing.T) {
	m := Model{}
	m, _ = m.trackKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m, _ = m.trackKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	m, pasting := m.trackKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !pasting {
		t.Fatal("Enter inside a paste burst should remain in paste mode")
	}
}

// TestTrackKeyBracketedPaste: explicit paste events (bracketed paste mode)
// always enter paste mode regardless of timing.
func TestTrackKeyBracketedPaste(t *testing.T) {
	m := Model{}
	m, pasting := m.trackKey(tea.KeyMsg{Type: tea.KeyRunes, Paste: true, Runes: []rune("line1\nline2\n")})
	if !pasting {
		t.Fatal("bracketed paste event must enter paste mode")
	}
}

// TestTrackKeyGraceExpiry: paste mode expires once the grace window passes.
func TestTrackKeyGraceExpiry(t *testing.T) {
	m := Model{}
	m, _ = m.trackKey(tea.KeyMsg{Type: tea.KeyRunes, Paste: true, Runes: []rune("x")})
	m.pasteUntil = time.Now().Add(-time.Second) // force expiry
	_, pasting := m.trackKey(tea.KeyMsg{Type: tea.KeyEnter})
	if pasting {
		t.Fatal("paste mode must expire after the grace period")
	}
}
