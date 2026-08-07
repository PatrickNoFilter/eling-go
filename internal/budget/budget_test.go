package budget

import (
	"context"
	"strings"
	"testing"
	"time"
)

// fakeClock returns a mutable clock for deterministic budget tests.
type fakeClock struct {
	t time.Time
}

func (f *fakeClock) now() time.Time          { return f.t }
func (f *fakeClock) advance(d time.Duration) { f.t = f.t.Add(d) }

func TestZeroConfigIsInert(t *testing.T) {
	b := New(Config{})
	if b.IsArmed() {
		t.Error("zero config must not be armed")
	}
	ctx, cancel, ok := b.Enforce(context.Background())
	defer cancel()
	if ok {
		t.Error("zero config must not apply a deadline")
	}
	if ctx == nil {
		t.Fatal("Enforce returned nil ctx")
	}
	if d := b.Deadline(); !d.IsZero() {
		t.Errorf("deadline = %v, want zero", d)
	}
	if b.BeginTurn() {
		t.Error("BeginTurn on zero config must never report exhausted")
	}
	if e := b.CheckTurns(); e != nil {
		t.Errorf("CheckTurns on zero config = %v, want nil", e)
	}
	if e := b.CheckIdle(); e != nil {
		t.Errorf("CheckIdle on zero config = %v, want nil", e)
	}
	if e := b.CheckDuration(); e != nil {
		t.Errorf("CheckDuration on zero config = %v, want nil", e)
	}
}

func TestMaxTurnsExceeded(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	b := NewWithClock(Config{MaxTurns: 3}, clk.now)

	if !b.IsArmed() {
		t.Fatal("armed config must report IsArmed")
	}
	if e := b.CheckTurns(); e != nil {
		t.Fatalf("CheckTurns before any turn = %v, want nil", e)
	}
	for i := 0; i < 3; i++ {
		if b.BeginTurn() {
			t.Fatalf("BeginTurn %d should not report exhausted", i+1)
		}
	}
	// After 3 turns the quota is fully consumed: CheckTurns fires.
	e := b.CheckTurns()
	if e == nil {
		t.Fatal("CheckTurns at cap = nil, want Exceeded")
	}
	if e.Kind != KindTurns || e.Turns != 3 {
		t.Errorf("Exceeded = %+v, want KindTurns/Turns=3", e)
	}
	// 4th turn is blocked.
	if !b.BeginTurn() {
		t.Error("BeginTurn past cap should report exhausted")
	}
}

func TestMaxDurationDeadline(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	b := NewWithClock(Config{MaxDuration: 10 * time.Second}, clk.now)

	ctx, cancel, ok := b.Enforce(context.Background())
	defer cancel()
	if !ok {
		t.Fatal("Enforce should apply a deadline when MaxDuration set")
	}
	if d, has := ctx.Deadline(); !has || d.IsZero() {
		t.Fatalf("ctx deadline = %v (has=%v), want set", d, has)
	}

	// Before the budget: CheckDuration nil.
	if e := b.CheckDuration(); e != nil {
		t.Errorf("CheckDuration before budget = %v, want nil", e)
	}
	clk.advance(11 * time.Second)
	e := b.CheckDuration()
	if e == nil {
		t.Fatal("CheckDuration after budget = nil, want Exceeded")
	}
	if e.Kind != KindDuration {
		t.Errorf("Exceeded.Kind = %v, want KindDuration", e.Kind)
	}
}

func TestIdleTimeoutFiresOnlyAfterQuietPeriod(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	b := NewWithClock(Config{IdleTimeout: 5 * time.Second}, clk.now)

	// No activity recorded yet: first check initializes the marker.
	if e := b.CheckIdle(); e != nil {
		t.Fatalf("first CheckIdle = %v, want nil (initializes)", e)
	}

	// Activity within the window keeps it alive.
	b.Activity()
	clk.advance(4 * time.Second)
	if e := b.CheckIdle(); e != nil {
		t.Fatalf("CheckIdle after 4s = %v, want nil", e)
	}

	// Crossing the window fires.
	clk.advance(2 * time.Second) // 6s since last activity
	e := b.CheckIdle()
	if e == nil {
		t.Fatal("CheckIdle past timeout = nil, want Exceeded")
	}
	if e.Kind != KindIdle {
		t.Errorf("Exceeded.Kind = %v, want KindIdle", e.Kind)
	}

	// A new activity resets and clears the fired state.
	b.Activity()
	if e := b.CheckIdle(); e != nil {
		t.Errorf("CheckIdle after reset = %v, want nil", e)
	}
}

func TestBeginTurnResetsIdle(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	b := NewWithClock(Config{IdleTimeout: 5 * time.Second}, clk.now)

	_ = b.BeginTurn() // marks activity
	clk.advance(4 * time.Second)
	if e := b.CheckIdle(); e != nil {
		t.Fatalf("CheckIdle mid-turn = %v, want nil", e)
	}
	// Even with no user input, a turn boundary (BeginTurn) refreshes the clock.
	_ = b.BeginTurn()
	clk.advance(4 * time.Second)
	if e := b.CheckIdle(); e != nil {
		t.Fatalf("CheckIdle after BeginTurn refresh = %v, want nil", e)
	}
}

func TestSnapshot(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	b := NewWithClock(Config{MaxTurns: 5, MaxDuration: time.Minute, IdleTimeout: 30 * time.Second}, clk.now)
	_ = b.BeginTurn()
	s := b.Snapshot()
	if !s.Armed || s.MaxTurns != 5 || s.MaxDuration != time.Minute || s.IdleTimeout != 30*time.Second {
		t.Errorf("snapshot = %+v", s)
	}
	if s.TurnsUsed != 1 {
		t.Errorf("TurnsUsed = %d, want 1", s.TurnsUsed)
	}
	if s.LastActivity.IsZero() {
		t.Error("LastActivity should be set after BeginTurn")
	}
}

func TestExceededErrorMessage(t *testing.T) {
	cases := []struct {
		e   *Exceeded
		sub string
	}{
		{&Exceeded{Kind: KindTurns, Turns: 3}, "max 3"},
		{&Exceeded{Kind: KindDuration, Duration: 10 * time.Second}, "10s"},
		{&Exceeded{Kind: KindIdle, Idle: 5 * time.Second}, "5s"},
		{&Exceeded{}, "session budget exceeded"},
	}
	for _, c := range cases {
		if got := c.e.Error(); got == "" || (c.sub != "" && !strings.Contains(got, c.sub)) {
			t.Errorf("Error() = %q, want containing %q", got, c.sub)
		}
	}
}
