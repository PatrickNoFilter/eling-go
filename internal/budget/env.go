package budget

import (
	"context"
	"os"
	"strconv"
	"time"
)

// EnvSessionMaxDuration is the env var name that lets unattended surfaces
// (automate, benchmark) inherit a session wall-clock budget without config
// plumbing. A positive integer is interpreted as seconds.
const EnvSessionMaxDuration = "ELING_SESSION_MAX_DURATION_SEC"

// EnvDurationSeconds parses the named env var as a number of seconds. It
// returns 0 (off) when the var is unset, not a positive integer, or malformed.
func EnvDurationSeconds(name string) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

// WithEnvMaxDuration derives ctx with a wall-clock deadline from the named env
// var when it is set to a positive integer (seconds). If not set it returns ctx
// unchanged with a no-op cancel, so callers can always `defer cancel()`. The
// final bool reports whether a deadline was applied.
func WithEnvMaxDuration(ctx context.Context, name string) (context.Context, context.CancelFunc, bool) {
	d := EnvDurationSeconds(name)
	if d <= 0 {
		return ctx, func() {}, false
	}
	c, cancel := context.WithTimeout(ctx, d)
	return c, cancel, true
}
