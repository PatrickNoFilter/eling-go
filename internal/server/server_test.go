package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"eling/internal/config"
)

// mockUpstream returns an SSE-streaming OpenAI-compatible chat endpoint that
// replies "hello from mock" (two deltas) and counts chat/completions calls.
func mockUpstream(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	calls := &atomic.Int64{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		// two deltas
		fmt.Fprint(w, `data: {"id":"m1","object":"chat.completion.chunk","created":1,"model":"mock","choices":[{"index":0,"delta":{"role":"assistant","content":"hello "}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"m1","object":"chat.completion.chunk","created":1,"model":"mock","choices":[{"index":0,"delta":{"content":"from mock"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(ts.Close)
	return ts, calls
}

// serverTestConfig builds a config wired to the mock upstream with a
// throwaway session dir so tests never touch real ~/.eling state.
func serverTestConfig(t *testing.T, ts *httptest.Server) *config.Config {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agent.Providers = []config.ProviderConfig{
		{Name: "mock", Model: "mock-model", BaseURL: ts.URL, APIKey: "test-key"},
	}
	cfg.Session.SaveDir = t.TempDir()
	cfg.Agent.MaxTurnRounds = 2
	cfg.MCP.Enabled = false
	return cfg
}

// newTestServer builds a Server using agent.New wired to the mock upstream.
func newTestServer(t *testing.T, ts *httptest.Server, token string) *Server {
	t.Helper()
	cfg := serverTestConfig(t, ts)
	if token != "" {
		cfg.Server.Token = token
	}
	s := NewServer(cfg, "test-version", nil)
	return s
}

// sseFrame is a decoded SSE event from the response body.
type sseFrame struct {
	Event string
	Data  string
}

// parseSSE decodes "event: x\ndata: y\n\n" frames from a body.
func parseSSE(t *testing.T, body string) []sseFrame {
	t.Helper()
	var out []sseFrame
	var cur sseFrame
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(line, "event: "):
			cur.Event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.Data = strings.TrimPrefix(line, "data: ")
		case line == "":
			if cur.Event != "" || cur.Data != "" {
				out = append(out, cur)
				cur = sseFrame{}
			}
		}
	}
	return out
}

// doChat posts a chat request and returns the recorder.
func doChat(t *testing.T, h http.Handler, sessionID, prompt, token string) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"session_id":%q,"prompt":%q}`, sessionID, prompt)
	req := httptest.NewRequest("POST", "/v1/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// ── tests ────────────────────────────────────────────────────────────────────

func TestHealthEndpoint(t *testing.T) {
	ts, _ := mockUpstream(t)
	s := newTestServer(t, ts, "")

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", rec.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("health decode: %v", err)
	}
	if body["version"] != "test-version" {
		t.Errorf("version = %v, want test-version", body["version"])
	}
	tools, _ := body["tools"].([]interface{})
	if len(tools) == 0 {
		t.Error("expected non-empty tools list in health")
	}
	if body["addr"] != "127.0.0.1:8765" {
		t.Errorf("addr = %v, want 127.0.0.1:8765", body["addr"])
	}
}

func TestAuthWrongTokenRejected(t *testing.T) {
	ts, _ := mockUpstream(t)
	s := newTestServer(t, ts, "sekret")

	// missing token
	rec := doChat(t, s.Handler(), "", "hi", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d, want 401", rec.Code)
	}

	// wrong token
	rec = doChat(t, s.Handler(), "", "hi", "wrong-token")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d, want 401", rec.Code)
	}

	// correct token
	rec = doChat(t, s.Handler(), "", "hi", "sekret")
	if rec.Code != http.StatusOK {
		t.Fatalf("correct token status = %d, want 200", rec.Code)
	}
}

func TestChatStreamsSSE(t *testing.T) {
	ts, calls := mockUpstream(t)
	s := newTestServer(t, ts, "")

	rec := doChat(t, s.Handler(), "", "say hello", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("chat status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	events := parseSSE(t, rec.Body.String())
	eventsByType := map[string]string{}
	for _, e := range events {
		eventsByType[e.Event] = e.Data
	}

	if _, ok := eventsByType["session"]; !ok {
		t.Errorf("missing session event; got events: %v", events)
	}
	if eventsByType["message"] == "" {
		t.Errorf("missing message delta event; got: %v", events)
	}
	done := eventsByType["done"]
	if done == "" {
		t.Fatalf("missing done event; got: %v", events)
	}
	var donePayload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(done), &donePayload); err != nil {
		t.Fatalf("done payload decode: %v", err)
	}
	if !strings.Contains(donePayload.Text, "hello from mock") {
		t.Errorf("done text = %q, want it to contain the mock reply", donePayload.Text)
	}
	if calls.Load() != 1 {
		t.Errorf("provider calls = %d, want 1", calls.Load())
	}
}

func TestChatSameSessionContinues(t *testing.T) {
	ts, calls := mockUpstream(t)
	s := newTestServer(t, ts, "")

	// first turn
	rec1 := doChat(t, s.Handler(), "sess-1", "first message", "")
	if rec1.Code != http.StatusOK {
		t.Fatalf("turn1 status = %d", rec1.Code)
	}
	// second turn to the same session id
	rec2 := doChat(t, s.Handler(), "sess-1", "second message", "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("turn2 status = %d", rec2.Code)
	}
	if calls.Load() != 2 {
		t.Errorf("provider calls = %d, want 2 (session must continue in-memory)", calls.Load())
	}

	// exactly one live agent for the session
	if got := s.activeSessionIDs(); len(got) != 1 || got[0] != "sess-1" {
		t.Errorf("active sessions = %v, want [sess-1]", got)
	}
}

func TestChatBadRequest(t *testing.T) {
	ts, _ := mockUpstream(t)
	s := newTestServer(t, ts, "")

	// empty prompt
	rec := doChat(t, s.Handler(), "", "  ", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty prompt status = %d, want 400", rec.Code)
	}
	// garbage json
	req := httptest.NewRequest("POST", "/v1/chat", strings.NewReader("{not json"))
	rec2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec2, req)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("bad json status = %d, want 400", rec2.Code)
	}
}

func TestSessionNotFound(t *testing.T) {
	ts, _ := mockUpstream(t)
	s := newTestServer(t, ts, "")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/sessions/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestChatSameSessionSerialized verifies the per-session run lock: when
// multiple /v1/chat requests hit the same session_id concurrently, their
// agent turns must be serialized — the upstream provider must never see
// more than one in-flight chat/completions call for that session.
func TestChatSameSessionSerialized(t *testing.T) {
	var mu sync.Mutex
	inFlight, maxInFlight := 0, 0

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		mu.Unlock()
		defer func() {
			mu.Lock()
			inFlight--
			mu.Unlock()
		}()

		// Widen the race window so overlapping turns would be caught.
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"m1","object":"chat.completion.chunk","created":1,"model":"mock","choices":[{"index":0,"delta":{"role":"assistant","content":"pong"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(ts.Close)

	cfg := serverTestConfig(t, ts)
	s := NewServer(cfg, "test-version", nil)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := doChat(t, s.Handler(), "sess-serial", fmt.Sprintf("msg %d", i), "")
			if rec.Code != http.StatusOK {
				t.Errorf("turn %d status = %d, want 200", i, rec.Code)
			}
		}(i)
	}
	wg.Wait()

	if maxInFlight != 1 {
		t.Fatalf("max concurrent provider calls = %d, want 1 (turns must be serialized per session)", maxInFlight)
	}
	if got := s.activeSessionIDs(); len(got) != 1 || got[0] != "sess-serial" {
		t.Errorf("active sessions = %v, want [sess-serial]", got)
	}
}
