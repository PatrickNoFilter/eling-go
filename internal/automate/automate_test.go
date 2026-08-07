package automate

import (
	"context"
	"sync"
	"testing"
	"time"

	"eling/internal/config"
)

// ── cron parsing & matching ───────────────────────────────────────────────

func TestParseCronEvery(t *testing.T) {
	c, err := ParseCron("0 2 * * *")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.Local)
	next, ok := c.Next(base)
	if !ok {
		t.Fatal("no next")
	}
	want := time.Date(2026, 8, 1, 2, 0, 0, 0, time.Local)
	if !next.Equal(want) {
		t.Errorf("next = %v, want %v", next, want)
	}
}

func TestParseCronStep(t *testing.T) {
	c, err := ParseCron("*/15 * * * *")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	base := time.Date(2026, 8, 1, 0, 10, 0, 0, time.Local)
	next, ok := c.Next(base)
	if !ok {
		t.Fatal("expected next")
	}
	if next.Minute() != 15 {
		t.Errorf("next minute = %d, want 15", next.Minute())
	}
}

func TestParseCronDow(t *testing.T) {
	// Runs at 3am Monday (dow=1).
	c, err := ParseCron("0 3 * * 1")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// 2026-08-07 is a Friday.
	base := time.Date(2026, 8, 7, 0, 0, 0, 0, time.Local)
	next, ok := c.Next(base)
	if !ok {
		t.Fatal("expected next")
	}
	if next.Weekday() != time.Monday {
		t.Errorf("next weekday = %d, want Monday", next.Weekday())
	}
	if next.Hour() != 3 {
		t.Errorf("next hour = %d, want 3", next.Hour())
	}
}

func TestParseCronInvalid(t *testing.T) {
	for _, bad := range []string{"", "0 2 * *", "99 * * * *", "0 0 32 * *"} {
		if _, err := ParseCron(bad); err == nil {
			t.Errorf("ParseCron(%q) expected error, got nil", bad)
		}
	}
}

// ── scheduler overlap guard & firing ──────────────────────────────────────

// blockingRunner blocks its Run until ctx is cancelled, so tests can simulate
// an in-flight job.
type blockingRunner struct {
	mu      sync.Mutex
	starts  int
	released chan struct{}
}

func (r *blockingRunner) Run(ctx context.Context, name, command, goal string) (string, error) {
	r.mu.Lock()
	r.starts++
	r.mu.Unlock()
	<-releasedOrDone(ctx, r.released)
	return "ok", nil
}

func releasedOrDone(ctx context.Context, ch chan struct{}) chan struct{} {
	out := make(chan struct{})
	go func() {
		select {
		case <-ch:
		case <-ctx.Done():
		}
		close(out)
	}()
	return out
}

func TestSchedulerOverlapGuard(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Automate.Enabled = true
	// Job scheduled to fire every minute; enabled.
	cfg.Automate.Jobs = []config.AutomationJob{
		{Name: "nightly", Command: "ls", Schedule: "*/1 * * * *", Enabled: true},
	}
	runner := &blockingRunner{released: make(chan struct{})}
	s := NewScheduler(cfg, runner)

	// Fire due at a time that matches, while the first run is in flight.
	s.scan(time.Now())

	// before releasing, scan again → the second call must be skipped (overlap).
	time.Sleep(10 * time.Millisecond)
	s.scan(time.Now())

	runner.mu.Lock()
	starts := runner.starts
	runner.mu.Unlock()
	if starts != 1 {
		t.Errorf("overlap guard failed: job started %d times (want 1)", starts)
	}

	close(runner.released)
	s.Shutdown()
}

func TestSchedulerFiresDue(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Automate.Enabled = true
	cfg.Automate.Jobs = []config.AutomationJob{
		{Name: "ok", Command: "echo hi", Schedule: "1 * * * *", Enabled: true},
	}
	runner := &fakeRunner{done: make(chan struct{}, 4)}
	s := NewScheduler(cfg, runner)
	// Scan at a point that's due.
	s.scan(time.Date(2026, 8, 1, 5, 1, 30, 0, time.Local))
	// Wait until the job's mark() has persisted its status.
	deadline := time.Now().Add(2 * time.Second)
	for cfg.Automate.Jobs[0].LastStatus == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	s.Shutdown()
	if runner.runs != 1 {
		t.Errorf("job ran %d times (want 1)", runner.runs)
	}
	if cfg.Automate.Jobs[0].LastStatus != "ok" {
		t.Errorf("LastStatus = %q, want ok", cfg.Automate.Jobs[0].LastStatus)
	}
	if cfg.Automate.Jobs[0].LastRun == "" {
		t.Error("LastRun not set")
	}
}

type fakeRunner struct {
	runs int
	done chan struct{}
}

func (f *fakeRunner) Run(ctx context.Context, name, command, goal string) (string, error) {
	f.runs++
	select {
	case f.done <- struct{}{}:
	default:
	}
	return "ran", nil
}

func TestDisabledJobNotFired(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Automate.Enabled = true
	cfg.Automate.Jobs = []config.AutomationJob{
		{Name: "off", Command: "ls", Schedule: "*/1 * * * *", Enabled: false},
	}
	runner := &fakeRunner{}
	s := NewScheduler(cfg, runner)
	s.scan(time.Now())
	s.Shutdown()
	if runner.runs != 0 {
		t.Errorf("disabled job ran %d times (want 0)", runner.runs)
	}
}

func TestSchedulerNotStartedWhenDisabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Automate.Enabled = false
	cfg.Automate.Jobs = []config.AutomationJob{
		{Name: "job", Command: "ls", Schedule: "*/1 * * * *", Enabled: true},
	}
	runner := &fakeRunner{}
	s := NewScheduler(cfg, runner)
	s.scan(time.Now())
	if runner.runs != 0 {
		t.Errorf("job ran with scheduler disabled (want 0)")
	}
}