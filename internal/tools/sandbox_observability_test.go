package tools

import (
	"os/exec"
	"strings"
	"testing"
)

// TestSandboxMetricsShape verifies every documented sandbox counter key is
// present in the nested map, so a missing counter fails loudly rather than
// silently dropping from the dashboard.
func TestSandboxMetricsShape(t *testing.T) {
	m := sandboxMetrics()
	want := []string{
		"invocations",
		"cleanup_remove_failed",
		"net_unshare_missing",
		"dir_create_failed",
		"entropy_fallback",
		"home_fallback",
		"output_truncated",
	}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			t.Fatalf("sandboxMetrics missing key %q (got keys %v)", k, m)
		}
	}
}

// TestDefaultRegistryStatsIncludesSandbox verifies Stats() nests the sandbox
// counters under "sandbox", so the A5 dashboard exposes them.
func TestDefaultRegistryStatsIncludesSandbox(t *testing.T) {
	stats := DefaultRegistry.Stats()
	sb, ok := stats["sandbox"]
	if !ok {
		t.Fatal("Stats() should include a 'sandbox' sub-map for sandbox observability")
	}
	if _, ok := sb.(map[string]interface{}); !ok {
		t.Fatalf("stats['sandbox'] has wrong type %T", sb)
	}
}

// TestSandboxResultCarriesNetworkIsolation verifies a sandboxed run attaches
// network_isolation to the result (true when unshare engaged, false on hosts
// where it is absent — e.g. Termux/proot). Previously this was silent.
func TestSandboxResultCarriesNetworkIsolation(t *testing.T) {
	dir := t.TempDir()
	SetSandbox(SandboxSettings{Enabled: true, Root: dir, GuardMode: "block"})
	t.Cleanup(func() { SetSandbox(SandboxSettings{}) })

	res, err := bashExecute(map[string]interface{}{"command": "echo hi"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := resultMap(t, res)
	if m["sandbox"] != true {
		t.Fatalf("expected sandbox=true, got %v", m)
	}
	if _, ok := m["network_isolation"].(bool); !ok {
		t.Fatalf("expected network_isolation bool on sandboxed result, got %#v", m["network_isolation"])
	}
}

// TestWrapNetworkIsolationReportsIsolation checks the reported flag is
// coherent with whether unshare is actually on PATH.
func TestWrapNetworkIsolationReportsIsolation(t *testing.T) {
	_, isolated := wrapNetworkIsolation("echo hi")
	if _, err := exec.LookPath("unshare"); err != nil && isolated {
		t.Fatalf("reported isolated=true but unshare is not on PATH")
	}
}

// TestFinalizeOutputRecordsTruncation verifies hitting the 512 KiB cap bumps
// the output_truncated counter and returns the truncation marker.
func TestFinalizeOutputRecordsTruncation(t *testing.T) {
	before := sandboxMetrics()["output_truncated"].(int64)
	stdout := newLimitedBuffer(maxBashOutputBytes)
	stdout.Write([]byte(strings.Repeat("x", maxBashOutputBytes+100)))
	stderr := newLimitedBuffer(maxBashOutputBytes)
	out, errOut := finalizeOutput(stdout, stderr)
	if !strings.Contains(out, "truncated at 512 KiB") {
		t.Fatalf("expected truncation marker in stdout, got %q", out)
	}
	if errOut != "" {
		t.Fatalf("expected empty stderr, got %q", errOut)
	}
	if got := sandboxMetrics()["output_truncated"].(int64); got != before+1 {
		t.Fatalf("output_truncated = %d, want %d", got, before+1)
	}
}