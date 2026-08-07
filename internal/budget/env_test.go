package budget

import (
	"context"
	"testing"
	"time"
)

func TestEnvDurationSeconds(t *testing.T) {
	t.Setenv("ELING_SESSION_MAX_DURATION_SEC", "90")
	if got := EnvDurationSeconds("ELING_SESSION_MAX_DURATION_SEC"); got != 90*time.Second {
		t.Errorf("EnvDurationSeconds = %v, want 90s", got)
	}

	t.Setenv("ELING_SESSION_MAX_DURATION_SEC", "")
	if got := EnvDurationSeconds("ELING_SESSION_MAX_DURATION_SEC"); got != 0 {
		t.Errorf("unset env = %v, want 0", got)
	}

	t.Setenv("ELING_SESSION_MAX_DURATION_SEC", "-5")
	if got := EnvDurationSeconds("ELING_SESSION_MAX_DURATION_SEC"); got != 0 {
		t.Errorf("negative env = %v, want 0", got)
	}

	t.Setenv("ELING_SESSION_MAX_DURATION_SEC", "abc")
	if got := EnvDurationSeconds("ELING_SESSION_MAX_DURATION_SEC"); got != 0 {
		t.Errorf("malformed env = %v, want 0", got)
	}
}

func TestWithEnvMaxDuration(t *testing.T) {
	// Not set -> no deadline, no-op cancel.
	t.Setenv("ELING_SESSION_MAX_DURATION_SEC", "")
	ctx, cancel, applied := WithEnvMaxDuration(context.Background(), "ELING_SESSION_MAX_DURATION_SEC")
	if applied {
		t.Error("unset env should not apply a deadline")
	}
	if _, has := ctx.Deadline(); has {
		t.Error("unset env ctx should not carry a deadline")
	}
	cancel()

	// Set -> deadline applied.
	t.Setenv("ELING_SESSION_MAX_DURATION_SEC", "30")
	ctx, cancel2, applied := WithEnvMaxDuration(context.Background(), "ELING_SESSION_MAX_DURATION_SEC")
	defer cancel2()
	if !applied {
		t.Fatal("set env should apply a deadline")
	}
	d, has := ctx.Deadline()
	if !has {
		t.Fatal("set env ctx should carry a deadline")
	}
	if time.Until(d) > 30*time.Second || time.Until(d) <= 0 {
		t.Errorf("deadline %v is not just ~30s out", d)
	}
}
