package tools

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

// registerPermTest registers a trivial tool for permission tests.
func registerPermTest(t *testing.T, name string) (*Registry, *atomic.Int32) {
	t.Helper()
	r := NewRegistry()
	var calls atomic.Int32
	r.Register(Tool{
		Name:    name,
		Execute: func(map[string]interface{}) (interface{}, error) {
			calls.Add(1)
			return "ran", nil
		},
	})
	return r, &calls
}

func TestPermissions_InactivePreservesBehavior(t *testing.T) {
	r, calls := registerPermTest(t, "perm_probe")
	// Empty policy => inactive => everything allowed, no gate consulted.
	r.SetPermissions(PermPolicy{}, "/fake/proj")

	if _, err := r.ExecuteContext(context.Background(), "perm_probe", nil); err != nil {
		t.Fatalf("expected run, got err: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("tool should have run once, ran %d", calls.Load())
	}
}

func TestPermissions_DenyBlocksWithReason(t *testing.T) {
	r, calls := registerPermTest(t, "bash")
	p := NewPermPolicy("ask", map[string]string{"bash": "deny"}, nil)
	r.SetPermissions(p, "/proj")

	_, err := r.ExecuteContext(context.Background(), "bash", nil)
	if err == nil {
		t.Fatal("expected deny to block bash")
	}
	if !strings.Contains(err.Error(), "blocked by permission policy") {
		t.Fatalf("expected a policy-reason error, got: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("denied tool must not run, ran %d times", calls.Load())
	}

	// mode introspection reflects the rule
	mode, reason := r.PermissionModeFor("bash")
	if mode != "deny" || !strings.Contains(reason, "bash") {
		t.Fatalf("unexpected mode introspect: %q %q", mode, reason)
	}
}

func TestPermissions_AllowSkipsGate(t *testing.T) {
	r, calls := registerPermTest(t, "write")
	var gateCalls atomic.Int32
	r.SetPermissions(NewPermPolicy("ask", map[string]string{"write": "allow"}, nil), "/proj")
	r.SetPermissionGate(func(name string, args map[string]interface{}) (bool, error) {
		gateCalls.Add(1)
		return true, nil
	})

	if _, err := r.ExecuteContext(context.Background(), "write", nil); err != nil {
		t.Fatalf("allow tool should run: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("allow tool did not run")
	}
	if gateCalls.Load() != 0 {
		t.Fatalf("allow mode must skip the gate, gate called %d times", gateCalls.Load())
	}
}

func TestPermissions_AskPromptsOncePerCall(t *testing.T) {
	r, calls := registerPermTest(t, "bash")
	// default "ask" (unlisted) + a gate installed.
	r.SetPermissions(NewPermPolicy("", nil, nil), "/proj")
	var gateCalls atomic.Int32
	r.SetPermissionGate(func(name string, args map[string]interface{}) (bool, error) {
		gateCalls.Add(1)
		return true, nil
	})

	// Exactly one gate invocation per ExecuteContext call.
	for i := 0; i < 3; i++ {
		if _, err := r.ExecuteContext(context.Background(), "bash", nil); err != nil {
			t.Fatalf("allowed ask should run: %v", err)
		}
	}
	if gateCalls.Load() != 3 {
		t.Fatalf("ask must prompt exactly once per call; gate called %d times, want 3", gateCalls.Load())
	}
	if calls.Load() != 3 {
		t.Fatalf("tool should run 3 times, ran %d", calls.Load())
	}
}

func TestPermissions_AskDeniedByGate(t *testing.T) {
	r, calls := registerPermTest(t, "bash")
	r.SetPermissions(NewPermPolicy("", nil, nil), "/proj")
	r.SetPermissionGate(func(name string, args map[string]interface{}) (bool, error) {
		return false, nil
	})

	_, err := r.ExecuteContext(context.Background(), "bash", nil)
	if err == nil || !strings.Contains(err.Error(), "denied at ask prompt") {
		t.Fatalf("expected denied-at-ask error, got: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("denied tool must not run")
	}
}

func TestPermissions_NilGateAskIsAllowed(t *testing.T) {
	r, calls := registerPermTest(t, "bash")
	// active policy with default ask, but NO gate (headless) -> ask degrades
	// to allow rather than silently blocking automation.
	r.SetPermissions(NewPermPolicy("", nil, nil), "/proj")
	if p := r.PermissionPolicy(); !p.Active {
		t.Fatal("policy should be active")
	}
	if _, err := r.ExecuteContext(context.Background(), "bash", nil); err != nil {
		t.Fatalf("nil gate must not block ask in headless: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("tool should run")
	}
}

func TestPermissions_ProjectDeny(t *testing.T) {
	r, calls := registerPermTest(t, "write")
	r.SetPermissions(NewPermPolicy("allow", nil, map[string]string{"/root/eling": "deny"}), "/root/eling")
	_, err := r.ExecuteContext(context.Background(), "write", nil)
	if err == nil || !strings.Contains(err.Error(), "project trust") {
		t.Fatalf("expected project-deny block, got: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("denied tool must not run")
	}
}

func TestPermissions_ProjectAllowOutsideListed(t *testing.T) {
	r, calls := registerPermTest(t, "write")
	// denial only inside /root/eling; outside it the default ("allow") applies.
	r.SetPermissions(NewPermPolicy("allow", nil, map[string]string{"/root/eling": "deny"}), "/elsewhere")
	if _, err := r.ExecuteContext(context.Background(), "write", nil); err != nil {
		t.Fatalf("outside listed project should be allowed by default: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("tool should run")
	}
}