package tools

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

// fetchPredictor is the timeout prediction mechanism for web_fetch / web_search.
//
// It prevents slow or dead hosts from hanging the agent and eliminates
// duplicate network work:
//  1. resolveForFetch - fast DNS + TCP reachability probe (fails in ~1s instead
//     of waiting for curl's full --max-time). It RETURNS the resolved IPs so the
//     caller can hand them to curl via --resolve and skip curl's own DNS lookup.
//  2. dnsCache        - per-hostname DNS cache (10 min TTL). Repeat fetches to
//     the same host never re-resolve DNS.
//  3. hostOK          - hosts proven reachable skip the TCP probe entirely;
//     only DNS (usually cached) is consulted on subsequent fetches.
//  4. adaptiveMaxTime - per-host curl --max-time derived from history: hosts
//     with prior failures/slow responses get a much shorter budget.
//  5. recordResult    - every fetch outcome feeds the model so predictions
//     improve over time.
//
// The global instance is initialized in init() below so the mechanism is
// always ready when the tool registry boots.
type fetchPredictor struct {
	mu          sync.Mutex
	hostLatency map[string]time.Duration // host -> last successful fetch latency
	hostFails   map[string]int           // host -> consecutive failures
	hostOK      map[string]bool          // host -> known reachable
	dnsCache    map[string]cachedIPs     // hostname -> resolved IPs (with fetch time)
	initialized bool
}

// cachedIPs holds a DNS resolution result and when it was obtained.
type cachedIPs struct {
	ips       []string
	fetchedAt time.Time
}

// predictor is the process-wide timeout prediction singleton.
// Initialized eagerly so the mechanism is never nil / "not initialized".
var predictor = newFetchPredictor()

// newFetchPredictor builds and initializes the timeout prediction mechanism.
func newFetchPredictor() *fetchPredictor {
	p := &fetchPredictor{
		hostLatency: make(map[string]time.Duration),
		hostFails:   make(map[string]int),
		hostOK:      make(map[string]bool),
		dnsCache:    make(map[string]cachedIPs),
		initialized: true,
	}
	return p
}

// hostOf extracts the host:port from a URL string (best effort).
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		// Fallback: strip scheme if parse failed.
		raw := strings.TrimPrefix(rawURL, "https://")
		raw = strings.TrimPrefix(raw, "http://")
		if i := strings.IndexAny(raw, "/?"); i >= 0 {
			raw = raw[:i]
		}
		return raw
	}
	return u.Host
}

// dnsLookup resolves a hostname to IPs, using the in-process cache to avoid
// repeat DNS round trips. The lookup has its own short timeout so a slow or
// dead DNS server cannot block the fetch.
func (p *fetchPredictor) dnsLookup(host string, timeout time.Duration) ([]string, error) {
	// Fast path: serve from cache if fresh.
	p.mu.Lock()
	if c, ok := p.dnsCache[host]; ok && time.Since(c.fetchedAt) < dnsCacheTTL {
		ips := c.ips
		p.mu.Unlock()
		return ips, nil
	}
	p.mu.Unlock()

	lookupDone := make(chan struct{})
	var ips []string
	var lookupErr error
	go func() {
		defer close(lookupDone)
		ips, lookupErr = net.LookupHost(host)
	}()
	select {
	case <-lookupDone:
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout prediction: DNS lookup for %q exceeded %v (host likely unreachable)", host, timeout)
	}
	if lookupErr != nil {
		return nil, fmt.Errorf("timeout prediction: DNS resolution failed for %q: %w", host, lookupErr)
	}

	p.mu.Lock()
	p.dnsCache[host] = cachedIPs{ips: ips, fetchedAt: time.Now()}
	p.mu.Unlock()
	return ips, nil
}

// preflightReachable performs a fast DNS + TCP reachability check with a short
// overall budget. On failure it returns an actionable error so the caller can
// abort before curl even starts, instead of hanging for --max-time seconds.
//
// It returns the resolved IPs so callers can pass --resolve to curl and skip
// curl's own (second) DNS lookup. Hosts already proven reachable skip the TCP
// probe entirely; the DNS result is served from cache whenever possible.
func (p *fetchPredictor) preflightReachable(rawURL string, timeout time.Duration) ([]string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("timeout prediction: invalid URL %q: %w", rawURL, err)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("timeout prediction: URL %q has no host", rawURL)
	}
	hostPort := hostOf(rawURL)

	p.mu.Lock()
	knownOK := p.hostOK[hostPort]
	fails := p.hostFails[hostPort]
	p.mu.Unlock()

	// Fast DNS lookup (cached when possible).
	ips, err := p.dnsLookup(host, timeout)
	if err != nil {
		return nil, err
	}

	// Host is known reachable and not currently failing: skip the TCP probe.
	// We already have IPs for curl's --resolve, so this saves a full round trip.
	if knownOK && fails == 0 {
		return ips, nil
	}

	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	addr := net.JoinHostPort(host, port)

	// Fast TCP connect probe (only for unknown or possibly-dead hosts).
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("timeout prediction: host %q unreachable (TCP %s): %w", host, addr, err)
	}
	conn.Close()

	p.mu.Lock()
	p.hostOK[hostPort] = true
	p.mu.Unlock()
	return ips, nil
}

// adaptiveMaxTime returns a per-host curl --max-time based on prediction state.
// Hosts that have failed recently or are historically slow get a much shorter
// budget so the agent fails fast instead of blocking for 10s.
func (p *fetchPredictor) adaptiveMaxTime(host string) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.adaptiveMaxTimeLocked(host)
}

func (p *fetchPredictor) adaptiveMaxTimeLocked(host string) time.Duration {
	fails := p.hostFails[host]
	switch {
	case fails >= 2:
		return 4 * time.Second // repeatedly failing host: fail fast
	case fails == 1:
		return 6 * time.Second
	}
	if lat, ok := p.hostLatency[host]; ok && lat > 6*time.Second {
		return 6 * time.Second // historically slow host
	}
	return 8 * time.Second // default budget (was hardcoded 10s)
}

// recordResult feeds a fetch outcome into the prediction model.
func (p *fetchPredictor) recordResult(host string, elapsed time.Duration, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		p.hostFails[host]++
		return
	}
	p.hostFails[host] = 0
	p.hostLatency[host] = elapsed
	p.hostOK[host] = true
}

// predictionInfo returns a readable summary of the current prediction state
// for a host. It is embedded in web_fetch / web_search results so the agent
// and the user can see the timeout decision.
func (p *fetchPredictor) predictionInfo(host string) map[string]interface{} {
	p.mu.Lock()
	defer p.mu.Unlock()

	info := map[string]interface{}{
		"host":             host,
		"predicted_max_ms": int(p.adaptiveMaxTimeLocked(host) / time.Millisecond),
	}
	if lat, ok := p.hostLatency[host]; ok {
		info["last_latency_ms"] = lat.Milliseconds()
	}
	if fails := p.hostFails[host]; fails > 0 {
		info["consecutive_failures"] = fails
	}
	if p.hostOK[host] {
		info["known_reachable"] = true
	}
	return info
}

// fetchTimeoutConstants are the tuning knobs for the prediction mechanism.
const (
	preflightTimeout    = 1000 * time.Millisecond // DNS+TCP probe budget (was 1500ms)
	defaultFetchMaxTime = 8 * time.Second         // default curl --max-time
	dnsCacheTTL         = 10 * time.Minute        // how long DNS results are reused
)
