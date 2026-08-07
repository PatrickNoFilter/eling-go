// Package budget provides a session-scoped resource budget: an aggregate
// safety net across a whole process/session. ELING already has per-turn and
// per-tool bounds; this package adds the session-level caps that don't exist
// elsewhere. All knobs default to 0 = off, so the budget is strictly opt-in.
//
// Three orthogonal knobs:
//   - MaxTurns       caps the number of user Ask turns in a session.
//   - MaxDuration    caps total wall-clock time for the process/session.
//   - IdleTimeout    auto-saves and exits after N of inactivity.
//
// Enforcement is layered on top of (not replacing) the per-turn logic. The
// session knobs never cut off an in-flight turn — that stays the per-turn
// timeout's job. They count/elapse only between turns.
package budget

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Exceeded is the structured error returned when any budget knob fires. At most
// one field is set in any single error instance.
type Exceeded struct {
	Kind     Kind
	Turns    int
	Duration time.Duration
	Idle     time.Duration
}

// Kind identifies which budget knob fired.
type Kind int

const (
	KindNone Kind = iota
	KindTurns
	KindDuration
	KindIdle
)

func (e *Exceeded) Error() string {
	switch e.Kind {
	case KindTurns:
		return fmt.Sprintf("session turn limit reached (max %d)", e.Turns)
	case KindDuration:
		return fmt.Sprintf("session duration limit reached (%s)", e.Duration)
	case KindIdle:
		return fmt.Sprintf("session idle limit reached (%s)", e.Idle)
	default:
		return "session budget exceeded"
	}
}

// Config holds the session budget knobs. Zero values are off.
type Config struct {
	MaxTurns    int
	MaxDuration time.Duration
	IdleTimeout time.Duration
}

// Budget tracks session-scoped budget state. It is safe for concurrent use.
// The optional now func enables deterministic tests with a fake clock; it
// defaults to time.Now.
type Budget struct {
	cfg Config
	now func() time.Time

	mu         sync.Mutex
	turns      int
	lastActive time.Time
	started    time.Time
	deadline   time.Time // zero if not armed
	armed      bool
}

// New returns a Budget from the given config. A zero Config yields an inert
// budget (IsArmed() == false, no deadline, no counting).
func New(cfg Config) *Budget {
	return &Budget{
		cfg:   cfg,
		now:   time.Now,
		armed: cfg.MaxTurns > 0 || cfg.MaxDuration > 0 || cfg.IdleTimeout > 0,
	}
}

// NewWithClock is New with an injectable clock for tests.
func NewWithClock(cfg Config, now func() time.Time) *Budget {
	b := New(cfg)
	b.now = now
	return b
}

// IsArmed reports whether any budget knob is set.
func (b *Budget) IsArmed() bool {
	return b.armed
}

// Enforce prepares the session root context. If MaxDuration is set, it returns
// a derived context with that deadline and a cancel func. The second return is
// whether a deadline was applied. Callers should defer cancel().
func (b *Budget) Enforce(ctx context.Context) (context.Context, context.CancelFunc, bool) {
	b.mu.Lock()
	if b.cfg.MaxDuration <= 0 {
		b.mu.Unlock()
		return ctx, func() {}, false
	}
	start := b.now()
	if b.started.IsZero() {
		b.started = start
	}
	b.deadline = start.Add(b.cfg.MaxDuration)
	b.mu.Unlock()

	deadlined, cancel := context.WithDeadline(ctx, start.Add(b.cfg.MaxDuration))
	return deadlined, cancel, true
}

// Deadline returns the session wall-clock deadline, or the zero time if not
// armed with a MaxDuration.
func (b *Budget) Deadline() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.deadline
}

// SetStarted when b is armed with a duration but the caller manages the parent
// ctx directly, records the session start. Called by Enforce automatically;
// exposed for surfaces that can't call Enforce (e.g. automate/benchmark).
func (b *Budget) SetStarted() {
	b.mu.Lock()
	b.started = b.now()
	b.deadline = b.started.Add(b.cfg.MaxDuration)
	b.mu.Unlock()
}

// BeginTurn records that a user turn is starting. It resets the idle clock so a
// busy session is never flagged idle. Returns whether the turn budget is
// already exhausted (BeginTurn may be called repeatedly after hitting the cap).
func (b *Budget) BeginTurn() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cfg.MaxTurns > 0 && b.turns >= b.cfg.MaxTurns {
		return true
	}
	b.turns++
	b.lastActive = b.now()
	return false
}

// EndTurn marks the end of a turn. It does nothing for the counters (turns
// already counted in BeginTurn) but refreshes the idle marker.
func (b *Budget) EndTurn() {
	b.mu.Lock()
	b.lastActive = b.now()
	b.mu.Unlock()
}

// Activity records user activity (e.g. a new REPL/TUI input) outside of
// BeginTurn, and resets the idle stopwatch.
func (b *Budget) Activity() {
	b.mu.Lock()
	b.lastActive = b.now()
	b.mu.Unlock()
}

// CheckTurns returns an *Exceeded if the turn budget is met. Only meaningful at
// turn boundaries (before the next BeginTurn).
func (b *Budget) CheckTurns() *Exceeded {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cfg.MaxTurns > 0 && b.turns >= b.cfg.MaxTurns {
		return &Exceeded{Kind: KindTurns, Turns: b.cfg.MaxTurns}
	}
	return nil
}

// CheckIdle returns an *Exceeded if the idle budget has elapsed since the last
// activity. A zero IdleTimeout never fires.
func (b *Budget) CheckIdle() *Exceeded {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cfg.IdleTimeout <= 0 {
		return nil
	}
	if b.lastActive.IsZero() {
		b.lastActive = b.now()
		return nil
	}
	elapsed := b.now().Sub(b.lastActive)
	if elapsed >= b.cfg.IdleTimeout {
		return &Exceeded{Kind: KindIdle, Idle: elapsed}
	}
	return nil
}

// CheckDuration returns an *Exceeded if the wall-clock budget has elapsed. Only
// useful when the session is not driven through the Enforce-derived context.
func (b *Budget) CheckDuration() *Exceeded {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cfg.MaxDuration <= 0 {
		return nil
	}
	start := b.started
	if start.IsZero() {
		return nil
	}
	if b.now().Sub(start) >= b.cfg.MaxDuration {
		return &Exceeded{Kind: KindDuration, Duration: b.cfg.MaxDuration}
	}
	return nil
}

// Snapshot returns a copy of the current budget state (for /sessionbudget).
type Snapshot struct {
	Armed        bool          `json:"armed"`
	MaxTurns     int           `json:"max_turns"`
	MaxDuration  time.Duration `json:"max_duration"`
	IdleTimeout  time.Duration `json:"idle_timeout"`
	TurnsUsed    int           `json:"turns_used"`
	LastActivity time.Time     `json:"last_activity"`
}

func (b *Budget) Snapshot() Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return Snapshot{
		Armed:        b.armed,
		MaxTurns:     b.cfg.MaxTurns,
		MaxDuration:  b.cfg.MaxDuration,
		IdleTimeout:  b.cfg.IdleTimeout,
		TurnsUsed:    b.turns,
		LastActivity: b.lastActive,
	}
}
