package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalog(t *testing.T) {
	if len(Catalog) == 0 {
		t.Fatal("Catalog is empty")
	}
	// Every entry must be fully populated (no empty name/model/baseURL).
	seen := map[string]bool{}
	for _, s := range Catalog {
		if s.Name == "" || s.Model == "" || s.BaseURL == "" {
			t.Errorf("catalog entry incomplete: %+v", s)
		}
		if seen[s.Name] {
			t.Errorf("duplicate provider name in catalog: %s", s.Name)
		}
		seen[s.Name] = true
	}
	// Base URLs must be absolute http(s) URLs.
	for _, s := range Catalog {
		if len(s.BaseURL) < 8 || (s.BaseURL[:7] != "http://" && s.BaseURL[:8] != "https://") {
			t.Errorf("catalog entry %s has non-URL BaseURL: %q", s.Name, s.BaseURL)
		}
	}
}

func TestFind(t *testing.T) {
	spec, ok := Find("opencode-zen-free")
	if !ok {
		t.Fatal("expected to find opencode-zen-free")
	}
	if spec.Model != "deepseek-v4-flash-free" {
		t.Errorf("model mismatch: got %q want %q", spec.Model, "deepseek-v4-flash-free")
	}
	if spec.BaseURL != "https://opencode.ai/zen/v1" {
		t.Errorf("base URL mismatch: got %q", spec.BaseURL)
	}
	if _, ok := Find("no-such-provider"); ok {
		t.Error("expected no-such-provider to be missing from catalog")
	}
}

func TestDefaults(t *testing.T) {
	if DefaultProvider() != "opencode-zen-free" {
		t.Errorf("DefaultProvider = %q", DefaultProvider())
	}
	if DefaultModel() != "deepseek-v4-flash-free" {
		t.Errorf("DefaultModel = %q", DefaultModel())
	}
	if DefaultBaseURL() != "https://opencode.ai/zen/v1" {
		t.Errorf("DefaultBaseURL = %q", DefaultBaseURL())
	}
	// Defaults must be derivable from the catalog (single source of truth).
	if _, ok := Find(DefaultProvider()); !ok {
		t.Errorf("DefaultProvider %q not in catalog", DefaultProvider())
	}
}

// TestCatalogMatchesSetupPresets guards against drift: the catalog must
// contain the same presets the old setupPresets() table exposed. If a new
// provider is intentionally added, update this test to match.
func TestCatalogMatchesSetupPresets(t *testing.T) {
	legacy := map[string][2]string{
		"opencode-zen":      {"deepseek-v4-flash", "https://opencode.ai/zen/v1"},
		"opencode-zen-free": {"deepseek-v4-flash-free", "https://opencode.ai/zen/v1"},
		"deepseek-direct":   {"deepseek-v4-flash", "https://api.deepseek.com"},
		"openrouter":        {"moonshotai/kimi-k3-free", "https://openrouter.ai/api/v1"},
		"openai":            {"gpt-4o", "https://api.openai.com/v1"},
		"groq":              {"llama-3.3-70b", "https://api.groq.com/openai/v1"},
	}
	if len(Catalog) != len(legacy) {
		t.Fatalf("catalog has %d entries, legacy had %d", len(Catalog), len(legacy))
	}
	for name, want := range legacy {
		spec, ok := Find(name)
		if !ok {
			t.Errorf("catalog missing legacy provider %q", name)
			continue
		}
		if spec.Model != want[0] || spec.BaseURL != want[1] {
			t.Errorf("%s drifted: got (%s, %s) want (%s, %s)",
				name, spec.Model, spec.BaseURL, want[0], want[1])
		}
	}
}

// TestWizardBaseURLDrift pins eling-wizard.sh's base URLs to the catalog so
// the interactive wizard and the Go flag-setup path can never silently point
// at different endpoints. It only asserts base URLs (models intentionally
// differ: the wizard offers richer menus than the compact presets).
func TestWizardBaseURLDrift(t *testing.T) {
	wizardPath := filepath.Join("..", "..", "eling-wizard.sh")
	data, err := os.ReadFile(wizardPath)
	if err != nil {
		t.Skipf("eling-wizard.sh not found: %v", err)
	}
	script := string(data)
	for _, s := range Catalog {
		if !strings.Contains(script, s.BaseURL) {
			t.Errorf("eling-wizard.sh is missing base URL %q for provider %q (catalog drift)",
				s.BaseURL, s.Name)
		}
	}
}
