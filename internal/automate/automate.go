// Package automate implements the D4 scheduled-automations subsystem (package
// heist feature note, Phase 4 milestone). A tiny 5-field crontab parser with
// no heavy dependency, a Scheduler that fires jobs on schedule while the daemon
// runs, and headless job execution.
//
// Two job kinds:
//   - Command jobs: a shell command run via /bin/sh -c (headless, no agent).
//   - Goal jobs: a natural-language goal run through the agent loop (agent.New
//     + Ask), session-less, same output handling as command jobs.
//
// Output is appended to ~/.eling/automations.log. Overlap is guarded: if a job's
// previous run is in-flight when a tick fires, that run is skipped + logged.
package automate

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"eling/internal/agent"
	"eling/internal/config"
)

// ── crontab parsing (5-field) ─────────────────────────────────────────────

// allowedSet is a set of allowed integer values for one cron field.
type allowedSet struct {
	m map[int]bool
	// if all=true, matches any value in [min,max] (fast path).
	all  bool
	min  int
	max  int
	step int // for all-mode */step
}

func (a *allowedSet) matches(v int) bool {
	if v < a.min || v > a.max {
		return false
	}
	if a.all {
		return (v-a.min)%a.step == 0
	}
	return a.m[v]
}

func (a *allowedSet) restricted() bool { return !a.all }

// Cron is a parsed 5-field crontab schedule.
type Cron struct {
	expr              string
	minute, hour      allowedSet
	dom, month, dow   allowedSet
}

// ParseCron parses a 5-field crontab (min hour dom mon dow).
// Accepts *, */n, N, N-M, comma lists, and range+step N-M/n.
func ParseCron(expr string) (*Cron, error) {
	fields := strings.FieldsFunc(strings.TrimSpace(expr), func(r rune) bool {
		return r == ' ' || r == '\t'
	})
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron: want 5 fields (min hour dom mon dow), got %d in %q", len(fields), expr)
	}
	parse := func(s string, lo, hi int, name string) (allowedSet, error) {
		return parseField(s, lo, hi, name)
	}
	minute, err := parse(fields[0], 0, 59, "minute")
	if err != nil {
		return nil, err
	}
	hour, err := parse(fields[1], 0, 23, "hour")
	if err != nil {
		return nil, err
	}
	dom, err := parse(fields[2], 1, 31, "day-of-month")
	if err != nil {
		return nil, err
	}
	month, err := parse(fields[3], 1, 12, "month")
	if err != nil {
		return nil, err
	}
	dow, err := parse(fields[4], 0, 6, "day-of-week")
	if err != nil {
		return nil, err
	}
	return &Cron{
		expr:   strings.Join(fields, " "),
		minute: minute, hour: hour, dom: dom, month: month, dow: dow,
	}, nil
}

// parseField parses one field into an allowedSet.
func parseField(expr string, lo, hi int, name string) (allowedSet, error) {
	out := allowedSet{all: false, m: make(map[int]bool), min: lo, max: hi, step: 1}
	if strings.TrimSpace(expr) == "*" {
		out.all = true
		return out, nil
	}
	step := 1
	// strip step suffix
	base := expr
	if idx := strings.Index(expr, "/"); idx >= 0 {
		s, err := strconv.Atoi(expr[idx+1:])
		if err != nil || s < 1 {
			return out, fmt.Errorf("cron: %s bad step in %q", name, expr)
		}
		step = s
		base = expr[:idx]
	}
	// whole-range step (e.g. */5 handled above, but also "0-59/5")
	if base == "*" || rangeFull(base, lo, hi) {
		out.all = true
		out.step = step
		return out, nil
	}
	// comma list, each item a value or range
	for _, part := range strings.Split(base, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if idx := strings.Index(part, "-"); idx >= 0 {
			loV, err1 := strconv.Atoi(strings.TrimSpace(part[:idx]))
			hiV, err2 := strconv.Atoi(strings.TrimSpace(part[idx+1:]))
			if err1 != nil || err2 != nil {
				return out, fmt.Errorf("cron: %s bad range %q", name, part)
			}
			if loV < lo || hiV > hi || loV > hiV {
				return out, fmt.Errorf("cron: %s range %s out of bounds %d-%d", name, part, lo, hi)
			}
			for v := loV; v <= hiV; v += step {
				out.m[v] = true
			}
		} else {
			v, err := strconv.Atoi(part)
			if err != nil || v < lo || v > hi {
				return out, fmt.Errorf("cron: %s bad value %q (want %d-%d)", name, part, lo, hi)
			}
			out.m[v] = true
		}
	}
	return out, nil
}

// rangeFull reports whether s is "lo-hi" covering the whole [lo, hi].
func rangeFull(s string, lo, hi int) bool {
	idx := strings.Index(s, "-")
	if idx < 0 {
		return false
	}
	a, err1 := strconv.Atoi(s[:idx])
	b, err2 := strconv.Atoi(s[idx+1:])
	return err1 == nil && err2 == nil && a == lo && b == hi
}

// Next returns the first time strictly after `after` matching the schedule.
func (c *Cron) Next(after time.Time) (time.Time, bool) {
	t := after.Truncate(time.Minute).Add(time.Minute)
	limit := after.AddDate(0, 0, 1460) // ~4y lookahead
	for t.Before(limit) {
		if c.matches(t) {
			return t, true
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, false
}

// matches reports whether t satisfies the schedule.
func (c *Cron) matches(t time.Time) bool {
	if !c.month.matches(int(t.Month())) {
		return false
	}
	if !c.minute.matches(t.Minute()) || !c.hour.matches(t.Hour()) {
		return false
	}
	dom, dow := int(t.Day()), int(t.Weekday())
	dowSet := c.dow.restricted()
	domSet := c.dom.restricted()
	switch {
	case domSet && dowSet:
		return c.dom.matches(dom) || c.dow.matches(dow)
	case domSet:
		return c.dom.matches(dom)
	case dowSet:
		return c.dow.matches(dow)
	default:
		return true
	}
}

// String returns the canonical expression.
func (c *Cron) String() string { return c.expr }

// ── Runner abstraction ─────────────────────────────────────────────────────

// Runner executes one job. status is a short summary line for the log.
type Runner interface {
	Run(ctx context.Context, name, command, goal string) (string, error)
}

// HeadlessRunner runs jobs locally: command jobs via /bin/sh -c, goal jobs via
// a freshly-created agent (session-less).
type HeadlessRunner struct{}

// NewHeadlessRunner returns a headless job runner.
func NewHeadlessRunner() *HeadlessRunner { return &HeadlessRunner{} }

// Run implements Runner.
func (r *HeadlessRunner) Run(ctx context.Context, name, command, goal string) (string, error) {
	if command != "" {
		return runCMD(ctx, command)
	}
	if goal != "" {
		return runAgent(ctx, goal)
	}
	return "", fmt.Errorf("job %q has neither command nor goal", name)
}

func runCMD(ctx context.Context, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(truncate(string(out))), fmt.Errorf("command exited: %s", err)
	}
	return truncate(strings.TrimSpace(string(out))), nil
}

func runAgent(ctx context.Context, goal string) (string, error) {
	cfg := config.DefaultConfig()
	a, err := agent.New(cfg)
	if err != nil {
		return "", fmt.Errorf("create agent: %w", err)
	}
	// If a provider key is unset the CLI still reaches the default provider via
	// env; the agent.New setup is the same one the CLI `think` uses.
	res, err := a.Ask(ctx, goal)
	if err != nil {
		return "", err
	}
	return truncate(res), nil
}

// ── Scheduler ──────────────────────────────────────────────────────────────

// Scheduler monitors cron jobs and fires those that are due.
type Scheduler struct {
	cfg    *config.Config
	runner Runner
	logPath string

	mu       sync.Mutex
	inFlight map[string]context.CancelFunc
	stop     chan struct{}
	stopped  bool
}

// NewScheduler builds a scheduler bound to cfg. When r is nil it uses the
// headless runner.
func NewScheduler(cfg *config.Config, r Runner) *Scheduler {
	if r == nil {
		r = NewHeadlessRunner()
	}
	return &Scheduler{
		cfg:      cfg,
		runner:   r,
		logPath:  LogPath(),
		inFlight: make(map[string]context.CancelFunc),
		stop:     make(chan struct{}),
	}
}

// LogPath returns the automation log path.
func LogPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".eling", "automations.log")
}

// Start launches the scheduling loop. tick is the scan interval. Returns a
// stop func that cancels in-flight runs and stops the loop.
func (s *Scheduler) Start(tick time.Duration) func() {
	go s.loop(tick)
	return func() {
		s.mu.Lock()
		s.stopped = true
		close(s.stop)
		for _, c := range s.inFlight {
			c()
		}
		s.mu.Unlock()
	}
}

func (s *Scheduler) loop(tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	s.scan(time.Now())
	for {
		select {
		case <-s.stop:
			return
		case now := <-t.C:
			s.scan(now)
		}
	}
}

// scan fires every enabled job whose cron time has arrived.
func (s *Scheduler) scan(now time.Time) {
	if s.cfg == nil || !s.cfg.Automate.Enabled {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.cfg.Automate.Jobs {
		job := &s.cfg.Automate.Jobs[i]
		if !job.Enabled {
			continue
		}
		c, err := ParseCron(job.Schedule)
		if err != nil {
			appendLog(s.logPath, fmt.Sprintf("[%s] job %q bad schedule %q: %v", now.Format(time.RFC3339), job.Name, job.Schedule, err))
			continue
		}
		// Fire when the current time falls on a scheduled minute.
		if !c.matches(now) {
			continue
		}
		// Don't re-fire a job that already ran within this minute.
		if last := lastRunParsed(job); !last.IsZero() && now.Truncate(time.Minute).Equal(last.Truncate(time.Minute)) {
			continue
		}
		// overlap guard: never run the same job twice concurrently.
		if _, busy := s.inFlight[job.Name]; busy {
			appendLog(s.logPath, fmt.Sprintf("[%s] job %q skipped: previous run still in-flight", now.Format(time.RFC3339), job.Name))
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		s.inFlight[job.Name] = cancel
		name, command, goal := job.Name, job.Command, job.Goal
		go func() {
			defer func() {
				s.mu.Lock()
				delete(s.inFlight, name)
				s.mu.Unlock()
				cancel()
			}()
			out, err := s.runner.Run(ctx, name, command, goal)
			ts := time.Now().Format(time.RFC3339)
			if err != nil {
				appendLog(s.logPath, fmt.Sprintf("[%s] job %q FAILED: %s", ts, name, err))
				if err := s.mark(job, ts, "error:"+err.Error()); err != nil {
					appendLog(s.logPath, fmt.Sprintf("[%s] update last_status for %q: %v", ts, name, err))
				}
				return
			}
			appendLog(s.logPath, fmt.Sprintf("[%s] job %q ran: %s", ts, name, short(out)))
			if err := s.mark(job, ts, "ok"); err != nil {
				appendLog(s.logPath, fmt.Sprintf("[%s] persist %q: %v", ts, name, err))
			}
		}()
	}
}

// mark writes LastRun/LastStatus to the underlying config and persists it.
// persisting every run keeps `eling automate list` current across restarts.
func (s *Scheduler) mark(job *config.AutomationJob, ts, status string) error {
	s.mu.Lock()
	job.LastRun = ts
	job.LastStatus = status
	s.mu.Unlock()
	if p := config.FindConfigPath(); p != "" {
		return s.cfg.Save(p)
	}
	return nil
}

// Shutdown cancels all in-flight jobs.
func (s *Scheduler) Shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.inFlight {
		c()
	}
	s.inFlight = make(map[string]context.CancelFunc)
}

// SyncRun runs a single job synchronously (used by `eling automate run`).
func (s *Scheduler) SyncRun(name, command, goal string) (string, error) {
	return s.runner.Run(context.Background(), name, command, goal)
}

// ── helpers ────────────────────────────────────────────────────────────────

func short(s string) string {
	if len(s) <= 80 {
		return s
	}
	return s[:80] + "…"
}

func truncate(s string) string {
	if len(s) <= 4096 {
		return s
	}
	b := []byte(s)
	cut := 4096
	for cut > 0 && b[cut]&0xC0 == 0x80 {
		cut--
	}
	return string(b[:cut]) + "\n…[truncated]"
}

func appendLog(path, line string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("[automate] log write failed: %v", err)
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintln(f, line)
}

// JobNames returns job names in sorted order (for listing).
func JobNames(jobs []config.AutomationJob) []string {
	out := make([]string, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, j.Name)
	}
	sort.Strings(out)
	return out
}

// lastRunParsed parses a job's LastRun RFC3339 timestamp, returning the zero
// time when unset or unparseable.
func lastRunParsed(j *config.AutomationJob) time.Time {
	if j == nil || j.LastRun == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, j.LastRun)
	if err != nil {
		return time.Time{}
	}
	return t
}