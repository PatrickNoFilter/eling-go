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

// TestCBMProjectParamAdvertised guards against the "project not found" bug:
// cbm_* dynamic tools must advertise their required "project" parameter in the
// provider schema, otherwise the LLM never passes it and ELING_ARG_PROJECT
// stays empty. See schema.go paramSchemas where the cbm_* schemas live.
func TestCBMProjectParamAdvertised(t *testing.T) {
	os.Unsetenv("ELING_TOOLS")

	r := NewRegistry()
	r.Register(Tool{Name: "cbm_search_graph", Description: "search the graph"})
	r.Register(Tool{Name: "cbm_trace_path", Description: "trace a path"})
	r.Register(Tool{Name: "cbm_get_architecture", Description: "get architecture"})
	defs := r.ToProviderDefs()

	got := map[string]bool{}
	for _, d := range defs {
		fn, ok := d.Function.Parameters.(map[string]interface{})
		if !ok {
			t.Fatalf("tool %q: Parameters not a map: %T", d.Function.Name, d.Function.Parameters)
		}
		props, _ := fn["properties"].(map[string]interface{})
		_, hasProject := props["project"]
		got[d.Function.Name] = hasProject
	}

	for _, name := range []string{"cbm_search_graph", "cbm_trace_path", "cbm_get_architecture"} {
		if !got[name] {
			t.Errorf("tool %q does not advertise a 'project' parameter (%v)", name, got)
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
