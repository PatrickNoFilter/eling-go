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
