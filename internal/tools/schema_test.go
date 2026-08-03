package tools

import (
	"os"
	"strings"
	"testing"
)

func TestToolAllowlistUnset(t *testing.T) {
	os.Unsetenv("ELING_TOOLS")
	if got := ToolAllowlist(); got != nil {
		t.Fatalf("ToolAllowlist() = %v, want nil when ELING_TOOLS unset", got)
	}
}

func TestToolAllowlistParsesCSV(t *testing.T) {
	os.Setenv("ELING_TOOLS", "read_file, edit_file ,bash,,")
	defer os.Unsetenv("ELING_TOOLS")

	allow := ToolAllowlist()
	if allow == nil {
		t.Fatal("ToolAllowlist() = nil, want non-nil set")
	}
	for _, name := range []string{"read_file", "edit_file", "bash"} {
		if !allow[name] {
			t.Errorf("allowlist missing %q", name)
		}
	}
	if allow["write_file"] {
		t.Error("allowlist should not contain write_file")
	}
}

func TestToProviderDefsRespectsAllowlist(t *testing.T) {
	os.Setenv("ELING_TOOLS", "read,edit")
	defer os.Unsetenv("ELING_TOOLS")

	defs := DefaultRegistry.ToProviderDefs()
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Function.Name)
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "read") || !strings.Contains(joined, "edit") {
		t.Fatalf("filtered defs missing read/edit: %v", joined)
	}
	for _, n := range names {
		switch n {
		case "read", "edit":
		default:
			t.Errorf("unexpected tool advertised with allowlist: %q (all: %v)", n, joined)
		}
	}
}

// TestToProviderDefsSkipsNoop verifies the P1.6 regression guard: placeholder
// tools (learned-skill stubs, no-command dynamic tools) marked Noop are never
// advertised to the LLM, so the tool surface cannot be re-polluted by no-ops.
func TestToProviderDefsSkipsNoop(t *testing.T) {
	os.Unsetenv("ELING_TOOLS")

	r := NewRegistry()
	r.Register(Tool{Name: "real_tool", Description: "does real work"})
	r.Register(Tool{Name: "noop_stub", Description: "placeholder", Noop: true})

	defs := r.ToProviderDefs()
	if len(defs) != 1 {
		t.Fatalf("ToProviderDefs() = %d defs, want 1 (noop must be hidden); got %v", len(defs), defs)
	}
	if defs[0].Function.Name != "real_tool" {
		t.Errorf("advertised tool = %q, want real_tool", defs[0].Function.Name)
	}
}
