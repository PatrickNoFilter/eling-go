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
