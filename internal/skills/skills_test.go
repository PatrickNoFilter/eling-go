package skills

import (
	"testing"
)

func TestRegisterAndList(t *testing.T) {
	mgr := NewManager()

	// Built-in skills should be registered
	skills := mgr.List()
	if len(skills) < 3 {
		t.Errorf("expected at least 3 built-in skills, got %d", len(skills))
	}

	// Check echo skill
	echo, ok := mgr.Get("echo")
	if !ok {
		t.Fatal("expected 'echo' skill to be registered")
	}
	if echo.Description == "" {
		t.Error("echo skill should have a description")
	}

	// Execute echo
	result, err := echo.Execute(map[string]interface{}{"text": "hello"})
	if err != nil {
		t.Errorf("echo execution failed: %v", err)
	}
	if result != "hello" {
		t.Errorf("echo returned %q, expected %q", result, "hello")
	}
}

func TestRegisterDuplicate(t *testing.T) {
	mgr := NewManager()
	err := mgr.Register(Skill{
		Name:        "echo",
		Description: "Duplicate",
		Version:     "1.0.0",
	})
	if err == nil {
		t.Error("expected error for duplicate skill registration")
	}
}

func TestRemove(t *testing.T) {
	mgr := NewManager()
	mgr.Remove("echo")
	_, ok := mgr.Get("echo")
	if ok {
		t.Error("echo should have been removed")
	}
}

func TestCount(t *testing.T) {
	mgr := NewManager()
	count := mgr.Count()
	if count < 3 {
		t.Errorf("expected count >= 3, got %d", count)
	}
}
