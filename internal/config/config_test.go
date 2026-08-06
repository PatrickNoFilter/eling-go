package config

import (
	"strings"
	"testing"
)

// TestDefaultPromptContainsAtomicCommitDiscipline guards D7: the default
// system prompt must carry the atomic-commit discipline as first-class
// instruction text, so the habit applies on arbitrary projects, not just
// inside the heist workflow. If someone trims the prompt and drops the rule,
// this test fails — the discipline silently degrades otherwise.
func TestDefaultPromptContainsAtomicCommitDiscipline(t *testing.T) {
	prompt := DefaultConfig().Agent.SystemPrompt

	for _, want := range []string{
		"ATOMIC COMMIT DISCIPLINE",
		"plan",
		"ONE logical change",
		"go build",
		"go vet",
		"go test",
		"conventional message",
		"feat:",
		"Never batch unrelated changes",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("default system prompt missing atomic-commit discipline fragment %q", want)
		}
	}
}

// TestDefaultPromptKeepsSearchRule ensures the D7 edit appended to the prompt
// rather than replacing the SEARCH RULE — the two rules coexist.
func TestDefaultPromptKeepsSearchRule(t *testing.T) {
	prompt := DefaultConfig().Agent.SystemPrompt
	if !strings.Contains(prompt, "SEARCH RULE") {
		t.Error("SEARCH RULE was dropped from the default system prompt")
	}
	if !strings.Contains(prompt, "ugrep") {
		t.Error("ugrep mandate was dropped from the default system prompt")
	}
}
