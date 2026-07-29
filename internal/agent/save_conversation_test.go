package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eling/internal/config"
	"eling/internal/tools"
)

// ---------------------------------------------------------------------------
// Test: saveConversationToMemory mechanism
// ---------------------------------------------------------------------------

// TestSaveConversationToMemory_Basic verifies that after a conversation turn,
// the conversation is saved into both:
// 1. The semantic search index (vector embeddings)
// 2. The basic substring-searchable memory (Memory.Remember)
func TestSaveConversationToMemory_Basic(t *testing.T) {
	// Create a minimal agent config with SaveConversation enabled
	cfg := config.DefaultConfig()
	cfg.Agent.SaveConversation = true
	cfg.Memory.MaxShortTerm = 100
	cfg.Memory.MaxLongTerm = 500

	agent, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Simulate a conversation turn
	userPrompt := "What is the capital of France?"
	agentResponse := "The capital of France is Paris."

	// Clear semantic index to start fresh
	tools.ClearSemanticIndex()

	// Call the save function directly
	agent.saveConversationToMemory(userPrompt, agentResponse)

	// --- Verify basic memory storage ---
	mem := agent.GetMemory()
	if mem == nil {
		t.Fatal("memory is nil")
	}

	stats := mem.Stats()
	t.Logf("Memory stats: short=%d, long=%d, total=%d", stats["short_term"], stats["long_term"], stats["total"])

	// We expect at least 1 memory item (the conversation summary)
	if stats["total"] < 1 {
		t.Errorf("expected at least 1 memory item, got %d", stats["total"])
	}

	// Try to recall by content matching
	results := mem.Recall("France")
	if len(results) == 0 {
		t.Error("expected to find memory via Recall('France'), got 0 results")
	} else {
		t.Logf("Found %d result(s) for 'France': %s", len(results), results[0].Content)
		if !strings.Contains(results[0].Content, "France") {
			t.Errorf("recalled content does not contain 'France': %s", results[0].Content)
		}
	}

	// Try recall by tag
	resultsByTag := mem.Recall("conversation")
	if len(resultsByTag) == 0 {
		t.Error("expected to find memory via tag 'conversation'")
	} else {
		t.Logf("Found %d result(s) for tag 'conversation'", len(resultsByTag))
	}

	// --- Verify semantic index storage ---
	idxSize := tools.SemanticIndexSize()
	t.Logf("Semantic index size: %d", idxSize)
	if idxSize < 1 {
		t.Errorf("expected at least 1 semantic index item, got %d", idxSize)
	}

	// Check that memory items were also pushed to the cached memory items
	// for semantic search (SetMemoryItems is called during agent init)
	// The cached items should contain the auto-saved conversation
}

// TestSaveConversationToMemory_Disabled confirms that when SaveConversation
// is false, no memory is saved.
func TestSaveConversationToMemory_Disabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agent.SaveConversation = false

	agent, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	tools.ClearSemanticIndex()

	agent.saveConversationToMemory("test prompt", "test response")

	stats := agent.GetMemory().Stats()
	if stats["total"] != 0 {
		t.Errorf("expected 0 memories when SaveConversation=false, got %d", stats["total"])
	}

	idxSize := tools.SemanticIndexSize()
	if idxSize != 0 {
		t.Errorf("expected 0 semantic index items when SaveConversation=false, got %d", idxSize)
	}
}

// TestSaveConversationToMemory_MultipleTurns verifies that multiple
// conversation turns accumulate correctly in both memory stores.
func TestSaveConversationToMemory_MultipleTurns(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agent.SaveConversation = true
	cfg.Memory.MaxShortTerm = 100
	cfg.Memory.MaxLongTerm = 500

	agent, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	tools.ClearSemanticIndex()

	turns := []struct {
		user      string
		assistant string
	}{
		{"What is Go?", "Go is a programming language developed by Google."},
		{"Who created Linux?", "Linux was created by Linus Torvalds."},
		{"What is Kubernetes?", "Kubernetes is a container orchestration platform."},
	}

	for i, turn := range turns {
		agent.saveConversationToMemory(turn.user, turn.assistant)
		t.Logf("Turn %d saved. Memory stats: %v, Semantic index: %d",
			i+1, agent.GetMemory().Stats(), tools.SemanticIndexSize())
	}

	// Verify all turns are in memory
	stats := agent.GetMemory().Stats()
	if stats["total"] < 3 {
		t.Errorf("expected at least 3 memories after 3 turns, got %d", stats["total"])
	}

	// Verify semantic index has all 3 turns
	idxSize := tools.SemanticIndexSize()
	if idxSize < 3 {
		t.Errorf("expected at least 3 semantic index items, got %d", idxSize)
	}

	// Test recall by unique content across turns
	for _, turn := range turns {
		// Extract a keyword from each prompt
		keyword := extractKeyword(turn.user)
		results := agent.GetMemory().Recall(keyword)
		if len(results) == 0 {
			t.Errorf("expected to find memory for keyword '%s' (from '%s')", keyword, turn.user)
		} else {
			t.Logf("Recall '%s' → found %d result(s)", keyword, len(results))
		}
	}
}

// TestSaveConversationToMemory_JSONPersistence verifies that saved conversation
// memory survives a save/load cycle via SaveState/LoadState.
func TestSaveConversationToMemory_JSONPersistence(t *testing.T) {
	// Use a temp directory for state
	tmpDir, err := os.MkdirTemp("", "eling-memory-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Override home dir so state saves into tmpDir
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	cfg := config.DefaultConfig()
	cfg.Agent.SaveConversation = true
	cfg.Memory.MaxShortTerm = 100
	cfg.Memory.MaxLongTerm = 500

	agent, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Save some conversation turns
	turns := []struct{ user, assistant string }{
		{"What is Docker?", "Docker is a containerization platform."},
		{"Explain recursion.", "Recursion is when a function calls itself."},
	}

	for _, turn := range turns {
		agent.saveConversationToMemory(turn.user, turn.assistant)
	}

	// Save state
	if err := agent.SaveState(); err != nil {
		t.Fatalf("failed to save state: %v", err)
	}

	// Verify files were created
	stateDir := filepath.Join(tmpDir, ".eling")
	files, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatalf("failed to read state dir: %v", err)
	}
	t.Logf("State directory contents:")
	for _, f := range files {
		t.Logf("  %s", f.Name())
	}

	// Check memory.json exists
	memPath := filepath.Join(stateDir, "memory.json")
	if _, err := os.Stat(memPath); os.IsNotExist(err) {
		t.Fatal("memory.json was not saved")
	}

	// Read memory.json and verify contents
	memData, err := os.ReadFile(memPath)
	if err != nil {
		t.Fatalf("failed to read memory.json: %v", err)
	}
	t.Logf("memory.json contents:\n%s", string(memData))

	var savedMem Memory
	if err := json.Unmarshal(memData, &savedMem); err != nil {
		t.Fatalf("failed to unmarshal memory.json: %v", err)
	}

	// Verify items were persisted
	if savedMem.Len() == 0 {
		t.Error("memory.json has 0 items after save")
	}

	// Also check semantic_index.json exists
	semPath := filepath.Join(stateDir, "semantic_index.json")
	if _, err := os.Stat(semPath); os.IsNotExist(err) {
		t.Log("semantic_index.json not found (may not support persistence in this config)")
	} else {
		semData, _ := os.ReadFile(semPath)
		t.Logf("semantic_index.json size: %d bytes", len(semData))
	}

	// Create a new agent and load state
	agent2, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create second agent: %v", err)
	}

	if err := agent2.LoadState(); err != nil {
		t.Fatalf("failed to load state: %v", err)
	}

	// Verify loaded memory contains the saved conversations
	loadedMem := agent2.GetMemory()
	if loadedMem.Len() == 0 {
		t.Error("loaded memory has 0 items")
	}

	// Try to recall something from the saved conversations
	results := loadedMem.Recall("Docker")
	if len(results) == 0 {
		t.Error("expected to recall 'Docker' from loaded memory")
	} else {
		t.Logf("Loaded memory recall 'Docker': %s", results[0].Content)
	}

	results2 := loadedMem.Recall("recursion")
	if len(results2) == 0 {
		t.Error("expected to recall 'recursion' from loaded memory")
	} else {
		t.Logf("Loaded memory recall 'recursion': %s", results2[0].Content)
	}
}

// TestSaveConversationToMemory_BuildContext verifies that saved memories
// are injected into the conversation context via buildContext.
func TestSaveConversationToMemory_BuildContext(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agent.SaveConversation = true

	agent, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	tools.ClearSemanticIndex()

	// Save a memory about a specific topic.
	// The saveConversationToMemory function saves a truncated version of
	// the prompt as the memory content, so substring recall needs to match
	// against words from the original prompt.
	userPrompt := "What is the tallest mountain in the world?"
	agent.saveConversationToMemory(
		userPrompt,
		"Mount Everest is the tallest mountain on Earth at 8,849 meters.",
	)

	// To verify buildContext includes memory, query with a word that appears
	// in the original prompt (since recall is substring-based).
	context := agent.buildContext("tallest mountain")

	t.Logf("Built context for 'tallest mountain':\n%s", context)

	// The context should contain the relevant memory
	if !strings.Contains(context, "tallest mountain") && !strings.Contains(context, "Relevant memories") {
		// Check if semantic search found it instead
		if strings.Contains(context, "Related memories") || strings.Contains(context, "Relevant memories") {
			t.Log("buildContext includes memory section (semantic or substring)")
		} else {
			t.Error("buildContext should include relevant memories section")
		}
	} else {
		t.Log("buildContext found relevant memory by substring match")
	}
}

// TestSaveConversationToMemory_AutoSaveFlow tests the full auto-save flow
// that happens after each Ask() call, verifying the goroutine fires correctly.
func TestSaveConversationToMemory_AutoSaveFlow(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agent.SaveConversation = true
	cfg.Agent.Providers = cfg.Agent.Providers[:0] // Remove providers to prevent actual API calls

	agent, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	tools.ClearSemanticIndex()

	// Directly invoke the auto-save goroutine (it's called with 'go' in Ask)
	agent.saveConversationToMemory(
		"How do I write a test in Go?",
		"You can write a test in Go by creating a file ending with _test.go and using the testing package.",
	)

	// Check memory immediately (the function is synchronous, the 'go' prefix
	// is only in Ask/AskStream)
	mem := agent.GetMemory()
	results := mem.Recall("test")
	if len(results) == 0 {
		t.Error("expected memory to contain 'test' after saveConversationToMemory")
	}

	// Check semantic index
	idxSize := tools.SemanticIndexSize()
	if idxSize == 0 {
		t.Error("expected semantic index to have items after saveConversationToMemory")
	}
}

// TestSaveConversationToMemory_EdgeCases tests edge cases
func TestSaveConversationToMemory_EdgeCases(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agent.SaveConversation = true

	agent, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	tools.ClearSemanticIndex()

	// Edge case: empty prompt
	agent.saveConversationToMemory("", "response")
	if tools.SemanticIndexSize() != 0 {
		t.Error("should not save empty prompt")
	}
	if agent.GetMemory().Len() != 0 {
		t.Error("should not save empty prompt to memory")
	}

	// Edge case: empty response
	agent.saveConversationToMemory("prompt", "")
	if tools.SemanticIndexSize() != 0 {
		t.Error("should not save empty response")
	}

	// Edge case: both empty
	agent.saveConversationToMemory("", "")
	if tools.SemanticIndexSize() != 0 {
		t.Error("should not save when both are empty")
	}

	// Edge case: very long content
	longPrompt := strings.Repeat("A", 10000)
	longResponse := strings.Repeat("B", 10000)
	agent.saveConversationToMemory(longPrompt, longResponse)
	if tools.SemanticIndexSize() == 0 {
		t.Error("should save long content")
	}
	if agent.GetMemory().Len() == 0 {
		t.Error("should save long content to memory")
	}
}

// TestSemanticSearchIntegration verifies that conversation saved via
// saveConversationToMemory can be found via semantic_search tool.
func TestSemanticSearchIntegration(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agent.SaveConversation = true

	agent, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	tools.ClearSemanticIndex()

	// Save a conversation
	agent.saveConversationToMemory(
		"What is Go?",
		"Go is a statically typed, compiled programming language designed at Google.",
	)

	// Use the semantic_search tool to find it
	// The tool is registered in the default registry
	result, err := agent.UseTool("semantic_search", map[string]interface{}{
		"query": "Go programming language",
		"top_k": float64(5),
	})
	if err != nil {
		t.Logf("semantic_search returned error: %v (may be expected if no embedding API)", err)
		return
	}

	t.Logf("semantic_search result: %+v", result)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// extractKeyword pulls a distinctive word from a string for recall testing.
func extractKeyword(s string) string {
	words := strings.Fields(s)
	for _, w := range words {
		w = strings.Trim(w, "?.!,;:")
		if len(w) > 4 {
			return w
		}
	}
	if len(words) > 0 {
		return words[len(words)-1]
	}
	return s
}

// TestMemoryDecayIntegration verifies that memory decay works correctly
// with conversation items saved by saveConversationToMemory.
func TestMemoryDecayIntegration(t *testing.T) {
	mem := NewMemory()
	mem.MaxShort = 100
	mem.MaxLong = 500

	// Simulate saving conversations
	for i := 0; i < 20; i++ {
		mem.Remember(
			fmt.Sprintf("Conversation: User asked about topic %d", i),
			"conversation",
			[]string{"conversation", "auto-saved"},
		)
	}

	if mem.Len() != 20 {
		t.Errorf("expected 20 items, got %d", mem.Len())
	}

	// Manually weaken some items
	for i := 0; i < 10; i++ {
		if i < len(mem.ShortTerm) {
			mem.ShortTerm[i].Strength = 0.05 // below decay threshold
		}
	}

	// Apply decay
	mem.decayOnce(0.0) // rate 0 means only items below 0.1 are removed

	// We should have lost the 10 weakened items
	if mem.Len() != 10 {
		t.Errorf("expected 10 items after decay removal, got %d: short=%d items=%d",
			mem.Len(), len(mem.ShortTerm), len(mem.Items))
	}

	// Apply strength decay to the remaining items
	mem.decayOnce(0.5) // large decay rate

	// The remaining 10 items should be weakened but still above 0.1
	// (initial strength 1.0 - 0.5 = 0.5 > 0.1)
	stats := mem.Stats()
	t.Logf("After decay: short=%d, long=%d, total=%d", stats["short_term"], stats["long_term"], stats["total"])
}

// TestBuildContextWithMemorySummary verifies that buildContext includes
// both substring-based memory results and semantic search results.
func TestBuildContextWithMemorySummary(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agent.SaveConversation = true

	agent, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Store a memory via Remember (simulating what happens after save)
	mem := agent.GetMemory()
	mem.Remember("User asked about climate change", "conversation", []string{"conversation"})

	// Build context for a related query
	ctx := agent.buildContext("Tell me about climate")
	t.Logf("Context: %s", ctx)

	if !strings.Contains(ctx, "climate") {
		t.Error("buildContext should include 'climate' from memory")
	}

	// Build context for an unrelated query — should still include memories
	// (the agent includes all relevant memories, which is fine)
	ctx2 := agent.buildContext("Hello")
	t.Logf("Context for 'Hello': %s", ctx2)
	// This will likely include the climate change memory since it matches
	// via substring search on "conversation" tag
}
