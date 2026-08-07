package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestHelperMCPServer is a fake MCP server used as a helper subprocess.
// It answers the initialize handshake, then idles until stdin closes.
// Invoked via the re-exec pattern: env MCP_HELPER=1 triggers the server loop.
func TestHelperMCPServer(t *testing.T) {
	if os.Getenv("MCP_HELPER") != "1" {
		return
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal([]byte(sc.Text()), &req); err != nil || req.Method != "initialize" {
			continue
		}
		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]interface{}{},
				"serverInfo":      map[string]interface{}{"name": "fake", "version": "1.0.0"},
			},
		}
		b, _ := json.Marshal(resp)
		fmt.Println(string(b))
	}
	os.Exit(0)
}

// TestHelperMCPFull is a fake MCP server that answers initialize, tools/list,
// and tools/call — enough to exercise the post-connect ListTools/CallTool path.
// It idles until stdin closes, like a real long-lived server.
func TestHelperMCPFull(t *testing.T) {
	if os.Getenv("MCP_HELPER_FULL") != "1" {
		return
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal([]byte(sc.Text()), &req); err != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]interface{}{
					"protocolVersion": "2024-11-05",
					"capabilities":    map[string]interface{}{},
					"serverInfo":      map[string]interface{}{"name": "fake-full", "version": "1.0.0"},
				},
			}
			b, _ := json.Marshal(resp)
			fmt.Println(string(b))
		case "tools/list":
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]interface{}{
					"tools": []map[string]interface{}{
						{"name": "echo", "description": "echo back"},
						{"name": "add", "description": "add two ints"},
					},
				},
			}
			b, _ := json.Marshal(resp)
			fmt.Println(string(b))
		case "tools/call":
			result, _ := json.Marshal(map[string]interface{}{
				"content": []map[string]interface{}{{"type": "text", "text": "pong"}},
			})
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  json.RawMessage(result),
			}
			b, _ := json.Marshal(resp)
			fmt.Println(string(b))
		}
	}
	os.Exit(0)
}

// TestHelperFullAlive verifies the long-lived server process stays alive after
// Connect returns, i.e. that ListTools and CallTool work post-connect. This is
// a regression test for the bug where Connect()'s handshake timeout context was
// passed to exec.CommandContext, killing the child the moment Connect returned
// (making every later ListTools/CallTool deadlock or fail).
func TestHelperFullAlive(t *testing.T) {
	m := NewManager()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test binary: %v", err)
	}
	if err := m.Connect(context.Background(), "full", exe,
		[]string{"-test.run=TestHelperMCPFull"}, map[string]string{"MCP_HELPER_FULL": "1"}); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer m.Disconnect("full")

	// Give the child a moment to be fully up (not needed for correctness, but
	// proves the handshake-timeout cancel did not kill it).
	time.Sleep(100 * time.Millisecond)

	tools, err := m.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools after Connect failed: %v", err)
	}
	ts := tools["full"]
	if len(ts) != 2 {
		t.Fatalf("ListTools('full') = %d tools, want 2", len(ts))
	}

	res, err := m.CallTool(context.Background(), "full", "echo", map[string]interface{}{"text": "hi"})
	if err != nil {
		t.Fatalf("CallTool after Connect failed: %v", err)
	}
	if res.IsError || len(res.Content) == 0 || res.Content[0].Text != "pong" {
		t.Fatalf("CallTool result unexpected; want pong, got IsError=%v content=%+v", res.IsError, res.Content)
	}
}

// TestConnectSuccessHandshake verifies a server that answers initialize
// connects, appears in List(), and records no failure.
func TestConnectSuccessHandshake(t *testing.T) {
	m := NewManager()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test binary: %v", err)
	}
	if err := m.Connect(context.Background(), "fake", exe,
		[]string{"-test.run=TestHelperMCPServer"}, map[string]string{"MCP_HELPER": "1"}); err != nil {
		t.Fatalf("Connect with healthy fake server failed: %v", err)
	}
	defer m.Disconnect("fake")

	if got := m.List(); len(got) != 1 || got[0] != "fake" {
		t.Fatalf("List() = %v, want [fake]", got)
	}
	if got := m.Failures(); len(got) != 0 {
		t.Fatalf("Failures() = %v, want empty", got)
	}
}

// TestConnectTimeoutOnSilentServer verifies a server that starts but never
// answers initialize fails loudly within the timeout instead of hanging
// forever on context.Background().
func TestConnectTimeoutOnSilentServer(t *testing.T) {
	m := NewManager()
	m.SetConnectTimeout(300 * time.Millisecond)

	start := time.Now()
	err := m.Connect(context.Background(), "silent", "sh", []string{"-c", "sleep 30"}, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Connect with silent server succeeded; want timeout error")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("error = %v, want context deadline exceeded", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Connect took %v; handshake timeout did not apply", elapsed)
	}
	// The failed server must not appear as connected and must be recorded
	// so the TUI can surface it (banner / /mcp / /stats).
	if got := m.List(); len(got) != 0 {
		t.Fatalf("List() = %v, want empty (failed server must not be connected)", got)
	}
	if got := m.ConnectError("silent"); got == "" {
		t.Fatal("ConnectError(silent) = \"\", want recorded failure")
	}
	if got := m.Failures()["silent"]; got == "" {
		t.Fatal("Failures() missing silent server error")
	}
}

// TestConnectRecordsImmediateExitError verifies a server whose process exits
// before answering is surfaced as a recorded failure (not silently dropped).
func TestConnectRecordsImmediateExitError(t *testing.T) {
	m := NewManager()
	m.SetConnectTimeout(2 * time.Second)

	if err := m.Connect(context.Background(), "ghost", "sh", []string{"-c", "exit 1"}, nil); err == nil {
		t.Fatal("Connect with exiting server succeeded; want error")
	}
	if got := m.Failures()["ghost"]; got == "" {
		t.Fatal("Failures() missing ghost server error")
	}
	if got := m.List(); len(got) != 0 {
		t.Fatalf("List() = %v, want empty", got)
	}
}
