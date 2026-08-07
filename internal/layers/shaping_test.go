package layers

import (
	"strings"
	"testing"
)

func TestNewEndMessageZeroPolicyPassthrough(t *testing.T) {
	msg := "plain text without any policy"
	w := NewEndMessage(EndMessagePolicy{}, msg)
	if w.String() != msg {
		t.Fatalf("zero policy should passthrough; got %q", w.String())
	}
	if w.shaped {
		t.Fatalf("zero policy should not be shaped")
	}
	if w.note != "noop" {
		t.Fatalf("expected note=noop, got %q", w.note)
	}
	if w.used != len([]rune(msg)) {
		t.Fatalf("used runes mismatch: got %d want %d", w.used, len([]rune(msg)))
	}
}

func TestNewEndMessageRuneCap(t *testing.T) {
	msg := "alpha beta gamma delta epsilon"
	w := NewEndMessage(EndMessagePolicy{MaxRunes: 15}, msg)
	if len([]rune(w.String())) > 15+len([]rune(truncationTrailer)) {
		t.Fatalf("runes over cap+trailer: %q (%d)", w.String(), len([]rune(w.String())))
	}
	if !strings.Contains(w.String(), "truncated") {
		t.Fatalf("expected truncation trailer, got %q", w.String())
	}
	if w.note != "runes_capped" {
		t.Fatalf("note expected runes_capped, got %q", w.note)
	}
}

func TestNewEndMessageRuneCapNoSpace(t *testing.T) {
	// No space before the cap → plain hard cut.
	msg := "abcdefghijklmnopqrstuvwxyz"
	w := NewEndMessage(EndMessagePolicy{MaxRunes: 10}, msg)
	if len([]rune(w.String())) > 10+len([]rune(truncationTrailer)) {
		t.Fatalf("over hard-cap+trailer: %q", w.String())
	}
	if !strings.HasPrefix(w.String(), "abcdefg") {
		t.Fatalf("expected hard cut prefix, got %q", w.String())
	}
}

func TestNewEndMessageRuneCapMultiByte(t *testing.T) {
	// Wide characters: the rune cap must not split a rune.
	msg := "héllo wörld 日本語 of computers everywhere"
	_ = msg
	w := NewEndMessage(EndMessagePolicy{MaxRunes: 12}, msg)
	for _, r := range w.String() {
		if r == 0xFFFD {
			t.Fatalf("split a rune into replacement char: %q", w.String())
		}
	}
	if w.used <= 0 {
		t.Fatalf("expected positive used count")
	}
}

func TestNewEndMessageParaCap(t *testing.T) {
	msg := "one\n\ntwo\n\nthree\n\nfour"
	w := NewEndMessage(EndMessagePolicy{MaxParas: 2}, msg)
	if got := strings.Count(w.String(), "\n\n"); got+1 > 2 {
		t.Fatalf("paragraph count over cap: %q (%d paras)", w.String(), got+1)
	}
	if !strings.Contains(w.String(), "paragraphs trimmed") {
		t.Fatalf("expected para trailer, got %q", w.String())
	}
	if w.note != "paras_capped" {
		t.Fatalf("expected paras_capped, got %q", w.note)
	}
}

func TestNewEndMessageParaCapUnder(t *testing.T) {
	msg := "one\n\ntwo"
	w := NewEndMessage(EndMessagePolicy{MaxParas: 5}, msg)
	if w.String() != msg {
		t.Fatalf("under-cap should be untouched, got %q", w.String())
	}
	if w.shaped {
		t.Fatalf("should not be shaped when under cap")
	}
}

func TestNewEndMessageStripMarkdown(t *testing.T) {
	msg := "- first bullet\nplain line\n**bold text**\n- second bullet"
	w := NewEndMessage(EndMessagePolicy{DisallowMarkdown: true}, msg)
	out := w.String()
	if strings.Contains(out, "first bullet") {
		t.Fatalf("bullet line should be dropped, got %q", out)
	}
	if strings.Contains(out, "**") {
		t.Fatalf("bold markers should be stripped, got %q", out)
	}
	if !strings.Contains(out, "plain line") {
		t.Fatalf("plain line should survive, got %q", out)
	}
	if w.note != "markdown_stripped" {
		t.Fatalf("expected markdown_stripped, got %q", w.note)
	}
}

func TestNewEndMessageCombined(t *testing.T) {
	msg := strings.Repeat("word ", 40) + "\n\n- bad\n**b**"
	w := NewEndMessage(EndMessagePolicy{MaxRunes: 60, MaxParas: 1, DisallowMarkdown: true}, msg)
	if !w.shaped {
		t.Fatalf("combined policy should shape output")
	}
	if len([]rune(w.String())) > 60+len([]rune(truncationTrailer)) {
		t.Fatalf("combined over rune budget: %q", w.String())
	}
	if strings.Contains(w.String(), "**") {
		t.Fatalf("markdown should be stripped even when runes capped after, got %q", w.String())
	}
}

func TestNewEndMessageActive(t *testing.T) {
	if (EndMessagePolicy{}).Active() {
		t.Fatal("zero policy must be inactive")
	}
	if !(EndMessagePolicy{MaxRunes: 10}).Active() {
		t.Fatal("runes policy should be active")
	}
	if !(EndMessagePolicy{MaxParas: 1}).Active() {
		t.Fatal("paras policy should be active")
	}
	if !(EndMessagePolicy{DisallowMarkdown: true}).Active() {
		t.Fatal("markdown policy should be active")
	}
}