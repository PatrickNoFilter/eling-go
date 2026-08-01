package agent

import (
	"testing"

	"eling/internal/provider"
)

// TestSanitizeToolMessagesKeepsPaired verifies that a normal tool-call
// sequence (assistant tool_calls → tool results) survives sanitization.
func TestSanitizeToolMessagesKeepsPaired(t *testing.T) {
	msgs := []provider.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "", ToolCalls: []provider.ToolCall{
			provider.NewToolCall("call_1", "bash", `{"cmd":"ls"}`),
			provider.NewToolCall("call_2", "read", `{"path":"a.go"}`),
		}},
		{Role: "tool", Content: "file list", ToolCallID: "call_1"},
		{Role: "tool", Content: "package main", ToolCallID: "call_2"},
	}

	out := sanitizeToolMessages(msgs)
	if len(out) != len(msgs) {
		t.Fatalf("expected %d messages, got %d: %+v", len(msgs), len(out), out)
	}
}

// TestSanitizeToolMessagesDropsOrphaned verifies that tool messages whose
// tool_call_id was never declared by a preceding assistant message are
// removed — this is exactly the DeepSeek error case.
func TestSanitizeToolMessagesDropsOrphaned(t *testing.T) {
	msgs := []provider.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "run tests"},
		{Role: "tool", Content: "FAIL: broken", ToolCallID: "_auto_test"}, // orphan!
		{Role: "assistant", Content: "final answer"},
	}

	out := sanitizeToolMessages(msgs)
	for _, m := range out {
		if m.Role == "tool" {
			t.Fatalf("orphaned tool message survived sanitization: %+v", m)
		}
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 messages after dropping orphan, got %d: %+v", len(out), out)
	}
}

// TestSanitizeToolMessagesEmptyID verifies tool messages with an empty
// tool_call_id are dropped (they can never be matched).
func TestSanitizeToolMessagesEmptyID(t *testing.T) {
	msgs := []provider.Message{
		{Role: "user", Content: "hi"},
		{Role: "tool", Content: "no id", ToolCallID: ""},
	}
	out := sanitizeToolMessages(msgs)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(out), out)
	}
}

// TestSanitizeToolMessagesSyntheticAutoTestPair verifies that the synthetic
// assistant tool_call + tool result pair injected by the auto-test path
// survives sanitization intact.
func TestSanitizeToolMessagesSyntheticAutoTestPair(t *testing.T) {
	msgs := []provider.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "fix it"},
		{Role: "assistant", Content: "", ToolCalls: []provider.ToolCall{
			provider.NewToolCall("_auto_test", "_auto_test", "{}"),
		}},
		{Role: "tool", Content: "test failures...", ToolCallID: "_auto_test"},
	}
	out := sanitizeToolMessages(msgs)
	if len(out) != 4 {
		t.Fatalf("expected 4 messages, got %d: %+v", len(out), out)
	}
	last := out[len(out)-1]
	if last.Role != "tool" || last.ToolCallID != "_auto_test" {
		t.Fatalf("expected paired tool message at end, got %+v", last)
	}
}

// TestSanitizeToolMessagesNoToolFastPath verifies the fast path returns the
// original slice when there are no tool messages.
func TestSanitizeToolMessagesNoToolFastPath(t *testing.T) {
	msgs := []provider.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello!"},
	}
	out := sanitizeToolMessages(msgs)
	if len(out) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(out))
	}
	// Fast path should return the same backing slice.
	if &out[0] != &msgs[0] {
		t.Fatalf("fast path should return the original slice")
	}
}

// TestSanitizeToolMessagesStripsUnsatisfiedTail verifies the exact error that
// occurred in production: an assistant tool_calls message at the END of the
// sequence with NO tool responses (e.g. after a connection abort mid-turn).
// DeepSeek rejects this with "insufficient tool messages following tool_calls
// message" — we must strip the tool_calls so the request succeeds.
func TestSanitizeToolMessagesStripsUnsatisfiedTail(t *testing.T) {
	msgs := []provider.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "do the thing"},
		{Role: "assistant", Content: "", ToolCalls: []provider.ToolCall{
			provider.NewToolCall("call_1", "bash", `{"cmd":"ls"}`),
		}},
		// no tool result follows — interrupted/aborted turn
	}
	out := sanitizeToolMessages(msgs)
	if len(out) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(out), out)
	}
	last := out[len(out)-1]
	if last.Role != "assistant" {
		t.Fatalf("expected assistant at end, got %+v", last)
	}
	if len(last.ToolCalls) != 0 {
		t.Fatalf("expected tool_calls to be stripped, got %+v", last.ToolCalls)
	}
}

// TestSanitizeToolMessagesStripsPartiallySatisfied verifies that an assistant
// tool_calls message with only SOME results delivered (then a role boundary)
// gets its tool_calls stripped and the orphaned results dropped.
func TestSanitizeToolMessagesStripsPartiallySatisfied(t *testing.T) {
	msgs := []provider.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "run"},
		{Role: "assistant", Content: "", ToolCalls: []provider.ToolCall{
			provider.NewToolCall("call_1", "bash", `{"cmd":"a"}`),
			provider.NewToolCall("call_2", "read", `{"path":"b.go"}`),
		}},
		{Role: "tool", Content: "result a", ToolCallID: "call_1"},
		// call_2 result lost; user message follows → invalid sequence
		{Role: "user", Content: "[Auto-test result] FAIL"},
	}
	out := sanitizeToolMessages(msgs)
	// Expect: system, user, assistant(stripped), user = 4 messages.
	if len(out) != 4 {
		t.Fatalf("expected 4 messages, got %d: %+v", len(out), out)
	}
	asst := out[2]
	if asst.Role != "assistant" || len(asst.ToolCalls) != 0 {
		t.Fatalf("expected assistant with stripped tool_calls, got %+v", asst)
	}
	for _, m := range out {
		if m.Role == "tool" {
			t.Fatalf("orphaned tool message survived: %+v", m)
		}
	}
}

// TestSanitizeToolMessagesEmptyIDToolCalls verifies that an assistant
// message whose tool_calls have EMPTY ids is treated as unsatisfied and
// stripped — otherwise DeepSeek rejects the request with "insufficient tool
// messages following tool_calls message" (empty-id tool results are dropped
// in Pass 1, so the calls can never be answered).
func TestSanitizeToolMessagesEmptyIDToolCalls(t *testing.T) {
	msgs := []provider.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "run"},
		{Role: "assistant", Content: "", ToolCalls: []provider.ToolCall{
			{ID: "", Type: "function", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "bash", Arguments: `{"cmd":"ls"}`}},
		}},
		{Role: "tool", Content: "result", ToolCallID: ""}, // dropped in Pass 1
	}
	out := sanitizeToolMessages(msgs)
	if len(out) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(out), out)
	}
	last := out[len(out)-1]
	if last.Role != "assistant" || len(last.ToolCalls) != 0 {
		t.Fatalf("expected assistant with stripped tool_calls, got %+v", last)
	}
	for _, m := range out {
		if m.Role == "tool" {
			t.Fatalf("tool message survived: %+v", m)
		}
	}
}

// TestSanitizeToolMessagesMixedEmptyID verifies that an assistant message
// with a mix of valid and empty-id tool calls is treated as unsatisfied
// (the empty-id call can never receive a response), stripping all calls and
// dropping the partial results.
func TestSanitizeToolMessagesMixedEmptyID(t *testing.T) {
	msgs := []provider.Message{
		{Role: "user", Content: "go"},
		{Role: "assistant", Content: "", ToolCalls: []provider.ToolCall{
			provider.NewToolCall("call_1", "bash", `{"cmd":"a"}`),
			{ID: "", Type: "function", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "read", Arguments: `{"path":"b.go"}`}},
		}},
		{Role: "tool", Content: "ok", ToolCallID: "call_1"},
	}
	out := sanitizeToolMessages(msgs)
	// Expect: user, assistant(stripped) = 2 messages; tool result dropped.
	if len(out) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(out), out)
	}
	asst := out[1]
	if asst.Role != "assistant" || len(asst.ToolCalls) != 0 {
		t.Fatalf("expected assistant with stripped tool_calls, got %+v", asst)
	}
}

// TestNormalizeToolCallIDs verifies empty tool-call ids get unique
// synthesized ids so assistant tool_calls and tool result messages always
// pair up, while existing ids are preserved untouched.
func TestNormalizeToolCallIDs(t *testing.T) {
	calls := []provider.ToolCall{
		provider.NewToolCall("call_1", "bash", "{}"),
		{ID: "", Type: "function", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "read", Arguments: "{}"}},
	}
	out := normalizeToolCallIDs(calls, 5)
	if out[0].ID != "call_1" {
		t.Fatalf("existing id must be preserved, got %q", out[0].ID)
	}
	if out[1].ID == "" {
		t.Fatal("empty id was not synthesized")
	}
	if out[0].ID == out[1].ID {
		t.Fatal("synthesized id collides with existing id")
	}
}

// TestSanitizeToolMessagesDuplicateAutoTestIDs verifies that multiple
// auto-test pairs using the SAME hardcoded tool_call_id across rounds are
// each treated as satisfied (each assistant declaration is immediately
// followed by its own tool response) — the duplicate-ID scenario that
// previously produced the DeepSeek error.
func TestSanitizeToolMessagesDuplicateAutoTestIDs(t *testing.T) {
	msgs := []provider.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "fix"},
		{Role: "assistant", Content: "", ToolCalls: []provider.ToolCall{
			provider.NewToolCall("_auto_test", "_auto_test", "{}"),
		}},
		{Role: "tool", Content: "FAIL round 1", ToolCallID: "_auto_test"},
		{Role: "assistant", Content: "", ToolCalls: []provider.ToolCall{
			provider.NewToolCall("_auto_test", "_auto_test", "{}"),
		}},
		{Role: "tool", Content: "FAIL round 2", ToolCallID: "_auto_test"},
	}
	out := sanitizeToolMessages(msgs)
	if len(out) != len(msgs) {
		t.Fatalf("expected %d messages, got %d: %+v", len(msgs), len(out), out)
	}
	// Both assistant messages must retain their tool_calls.
	toolCallCount := 0
	for _, m := range out {
		if m.Role == "assistant" {
			toolCallCount += len(m.ToolCalls)
		}
	}
	if toolCallCount != 2 {
		t.Fatalf("expected 2 tool_calls preserved, got %d", toolCallCount)
	}
}
