package main

import (
	"testing"

	"eling/internal/config"
	"eling/internal/tools"
)

// TestPermPolicyFromConfig asserts the config.PermissionsConfig -> tools.PermPolicy
// bridge (P2.3) so a config error is caught before a session auto-rotates.
func TestPermPolicyFromConfig(t *testing.T) {
	t.Run("empty block is inactive", func(t *testing.T) {
		p := permPolicyFromConfig(config.PermissionsConfig{})
		if p.Active {
			t.Fatal("empty permissions block must be inactive (historical allow-everything)")
		}
		if mode, _ := p.ModeFor("bash", "/proj"); mode != "allow" {
			t.Fatalf("inactive policy must allow unlisted tools, got %q", mode)
		}
	})

	t.Run("default maps to DefaultMode", func(t *testing.T) {
		p := permPolicyFromConfig(config.PermissionsConfig{Default: "deny"})
		if !p.Active {
			t.Fatal("a configured default must activate the policy")
		}
		if p.DefaultMode != "deny" {
			t.Fatalf("DefaultMode = %q, want deny", p.DefaultMode)
		}
		if mode, _ := p.ModeFor("write", ""); mode != tools.PermDeny {
			t.Fatalf("unlisted tool resolved to %q, want deny", mode)
		}
	})

	t.Run("invalid rule modes are dropped", func(t *testing.T) {
		p := permPolicyFromConfig(config.PermissionsConfig{
			Default: "ask",
			Rules: []config.PermissionRule{
				{Tool: "bash", Mode: "allow"},
				{Tool: "write", Mode: "nonsense"},
			},
		})
		if _, ok := p.Rules["bash"]; !ok {
			t.Fatal("valid rule bash not carried")
		}
		if _, ok := p.Rules["write"]; ok {
			t.Fatal("invalid rule mode must be dropped from the policy")
		}
	})

	t.Run("project trust outranks default", func(t *testing.T) {
		p := permPolicyFromConfig(config.PermissionsConfig{
			Default:  "deny",
			Projects: map[string]string{"/root/eling": "full"},
		})
		if mode, _ := p.ModeFor("bash", "/root/eling"); mode != tools.PermAllow {
			t.Fatalf("project trust did not outrank default deny: got %q", mode)
		}
	})
}

// TestPermPolicyFromConfigExactRuleOutranksProject ensures a tool rule beats a
// broader project trust, matching the documented resolution order.
func TestPermPolicyFromConfigExactRuleOutranksProject(t *testing.T) {
	p := permPolicyFromConfig(config.PermissionsConfig{
		Default:  "ask",
		Projects: map[string]string{"/root/eling": "full"},
		Rules:    []config.PermissionRule{{Tool: "write", Mode: "deny"}},
	})
	if mode, _ := p.ModeFor("write", "/root/eling"); mode != tools.PermDeny {
		t.Fatalf("write under full-trust project resolved to %q, want deny", mode)
	}
	if mode, _ := p.ModeFor("bash", "/root/eling"); mode != tools.PermAllow {
		t.Fatalf("bash under full-trust project resolved to %q, want allow", mode)
	}
}