package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestFetchPredictorInitialized(t *testing.T) {
	// The mechanism must be initialized (never nil) at package load.
	if predictor == nil {
		t.Fatal("timeout prediction mechanism not initialized: predictor is nil")
	}
	if !predictor.initialized {
		t.Fatal("timeout prediction mechanism not initialized: initialized=false")
	}
	if predictor.hostLatency == nil || predictor.hostFails == nil || predictor.hostOK == nil {
		t.Fatal("timeout prediction maps not initialized")
	}
}

func TestHostOf(t *testing.T) {
	cases := map[string]string{
		"https://example.com/page":     "example.com",
		"http://sub.example.org:8080/x": "sub.example.org:8080",
		"https://api.github.com/repos": "api.github.com",
		"not-a-url":                    "not-a-url",
	}
	for in, want := range cases {
		if got := hostOf(in); got != want {
			t.Errorf("hostOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAdaptiveMaxTime(t *testing.T) {
	p := newFetchPredictor()
	host := "slow.example.com"

	// Fresh host -> default budget
	if got := p.adaptiveMaxTime(host); got != defaultFetchMaxTime {
		t.Errorf("fresh host max-time = %v, want %v", got, defaultFetchMaxTime)
	}

	// One failure -> shorter budget
	p.recordResult(host, 9*time.Second, fmt.Errorf("boom"))
	if got := p.adaptiveMaxTime(host); got != 6*time.Second {
		t.Errorf("after 1 failure max-time = %v, want 6s", got)
	}

	// Two+ failures -> fail-fast budget
	p.recordResult(host, 9*time.Second, fmt.Errorf("boom"))
	if got := p.adaptiveMaxTime(host); got != 4*time.Second {
		t.Errorf("after 2 failures max-time = %v, want 4s", got)
	}

	// Success resets failures and records latency
	p.recordResult(host, 700*time.Millisecond, nil)
	if got := p.adaptiveMaxTime(host); got != defaultFetchMaxTime {
		t.Errorf("after success max-time = %v, want default", got)
	}
	if p.hostFails[host] != 0 {
		t.Errorf("consecutive failures not reset: %d", p.hostFails[host])
	}
}

func TestPredictionInfo(t *testing.T) {
	p := newFetchPredictor()
	p.recordResult("ok.example.com", 300*time.Millisecond, nil)
	info := p.predictionInfo("ok.example.com")
	if info["host"] != "ok.example.com" {
		t.Errorf("prediction info missing host: %v", info)
	}
	if info["known_reachable"] != true {
		t.Errorf("known_reachable not set: %v", info)
	}
	if _, ok := info["predicted_max_ms"]; !ok {
		t.Errorf("predicted_max_ms missing: %v", info)
	}
}

func TestPreflightReachable(t *testing.T) {
	p := newFetchPredictor()
	// A dead host on a non-routable address should fail fast (< preflight budget).
	start := time.Now()
	err := p.preflightReachable("http://10.255.255.1:81/", preflightTimeout)
	elapsed := time.Since(start)
	if err == nil {
		t.Log("note: 10.255.255.1 unexpectedly reachable, skipping fast-fail assertion")
	} else if elapsed > 3*time.Second {
		t.Errorf("preflight took %v, expected fast fail (< 3s)", elapsed)
	}
}

func TestWebFetchCtxCancellation(t *testing.T) {
	// Cancelled context must abort the fetch immediately.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	start := time.Now()
	_, err := curlGetCtx(ctx, "https://example.com/")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from cancelled context fetch")
	}
	if !strings.Contains(err.Error(), "aborted") && !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("unexpected error: %v", err)
	}
	if elapsed > 3*time.Second {
		t.Errorf("cancelled fetch took %v, expected immediate abort", elapsed)
	}
}
