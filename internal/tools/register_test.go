package tools

import (
	"testing"
)

func TestRegisterTool(t *testing.T) {
	// Clean up any leftover state
	RemoveDynamicTool("test_greet")
	DefaultRegistry.Unregister("test_greet")

	// Simulate the register_tool execution
	result, err := registerToolExecute(map[string]interface{}{
		"name":        "test_greet",
		"description": "A test tool that echoes a greeting",
		"command":     "echo hello",
	})
	if err != nil {
		t.Fatalf("registerToolExecute failed: %v", err)
	}
	res, ok := result.(Result)
	if !ok {
		t.Fatalf("expected Result, got %T", result)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}

	// Verify tool was registered in DefaultRegistry
	tool, exists := DefaultRegistry.Get("test_greet")
	if !exists {
		t.Fatal("test_greet tool was not registered")
	}
	if tool.Name != "test_greet" {
		t.Errorf("expected name test_greet, got %s", tool.Name)
	}
	if tool.Category != "dynamic" {
		t.Errorf("expected category dynamic, got %s", tool.Category)
	}

	// Execute the registered tool
	execResult, err := tool.Execute(map[string]interface{}{})
	if err != nil {
		t.Fatalf("tool execution failed: %v", err)
	}
	_ = execResult

	// Verify it was persisted
	dynTools := GetDynamicTools()
	found := false
	for _, dt := range dynTools {
		if dt.Name == "test_greet" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("test_greet was not persisted in dynamic tools")
	}

	// Clean up
	DefaultRegistry.Unregister("test_greet")
	RemoveDynamicTool("test_greet")
}

func TestRegisterToolDuplicate(t *testing.T) {
	// Clean up any leftover state
	RemoveDynamicTool("dup_tool")
	DefaultRegistry.Unregister("dup_tool")

	// Register a tool with the same name twice should fail
	result, err := registerToolExecute(map[string]interface{}{
		"name":    "dup_tool",
		"command": "echo dup",
	})
	if err != nil {
		t.Fatalf("first register failed: %v", err)
	}
	res := result.(Result)
	if !res.Success {
		t.Fatalf("expected success: %s", res.Error)
	}

	// Second register with same name
	result, err = registerToolExecute(map[string]interface{}{
		"name":    "dup_tool",
		"command": "echo dup",
	})
	if err != nil {
		t.Fatalf("second register should not error: %v", err)
	}
	res = result.(Result)
	if res.Success {
		t.Fatal("expected failure for duplicate tool")
	}

	// Clean up
	DefaultRegistry.Unregister("dup_tool")
	RemoveDynamicTool("dup_tool")
}

func TestRegisterToolMissingName(t *testing.T) {
	result, err := registerToolExecute(map[string]interface{}{
		"command": "echo test",
	})
	if err != nil {
		t.Fatalf("registerToolExecute should not error: %v", err)
	}
	res := result.(Result)
	if res.Success {
		t.Fatal("expected failure for missing name")
	}
}

func TestRegisterSkill(t *testing.T) {
	result, err := registerToolExecute(map[string]interface{}{
		"name":        "test_skill",
		"description": "A test skill",
		"type":        "skill",
	})
	if err != nil {
		t.Fatalf("registerToolExecute failed: %v", err)
	}
	res := result.(Result)
	if !res.Success {
		t.Fatalf("expected success: %s", res.Error)
	}

	// Verify it was registered in tool registry
	tool, exists := DefaultRegistry.Get("test_skill")
	if !exists {
		t.Fatal("test_skill was not registered as a tool")
	}
	if tool.Category != "skill" {
		t.Errorf("expected category skill, got %s", tool.Category)
	}

	// Execute the skill
	execResult, err := tool.Execute(map[string]interface{}{})
	if err != nil {
		t.Fatalf("skill execution failed: %v", err)
	}
	_ = execResult

	// Clean up
	DefaultRegistry.Unregister("test_skill")
	RemoveDynamicTool("test_skill")
}

func TestDynamicToolPersistence(t *testing.T) {
	// Clear any leftover state from other tests
	SetDynamicTools(nil)

	// Initially empty
	initial := GetDynamicTools()
	if len(initial) != 0 {
		t.Fatalf("expected empty, got %d", len(initial))
	}

	// Add a dynamic tool
	updated := AddDynamicTool(DynamicTool{
		Name:        "persist_test",
		Description: "Test persistence",
		Category:    "dynamic",
		Command:     "echo persist",
	})
	if len(updated) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(updated))
	}

	// Verify
	tools := GetDynamicTools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool after add, got %d", len(tools))
	}
	if tools[0].Name != "persist_test" {
		t.Errorf("expected persist_test, got %s", tools[0].Name)
	}

	// Set new list
	SetDynamicTools([]DynamicTool{
		{Name: "replaced", Description: "replaced", Category: "dynamic"},
	})
	tools = GetDynamicTools()
	if len(tools) != 1 || tools[0].Name != "replaced" {
		t.Fatalf("expected replaced tool, got %+v", tools)
	}

	// Remove
	RemoveDynamicTool("persist_test") // doesn't exist anymore, should be safe
	RemoveDynamicTool("replaced")
	tools = GetDynamicTools()
	if len(tools) != 0 {
		t.Fatalf("expected empty after removal, got %d", len(tools))
	}

	// Restore by clearing
	SetDynamicTools(nil)
}

func TestRunDynamicCommand(t *testing.T) {
	result, err := RunDynamicCommand("echo hello_world", map[string]interface{}{
		"extra": "value",
	})
	if err != nil {
		t.Fatalf("RunDynamicCommand failed: %v", err)
	}
	res, ok := result.(Result)
	if !ok {
		t.Fatalf("expected Result, got %T", result)
	}
	if !res.Success {
		t.Fatalf("expected success: %s", res.Error)
	}
	data, ok := res.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map data, got %T", res.Data)
	}
	stdout, ok := data["stdout"].(string)
	if !ok || stdout != "hello_world" {
		t.Fatalf("expected stdout 'hello_world', got %q", stdout)
	}
}
