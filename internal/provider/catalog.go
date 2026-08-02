// Package provider provides multi-provider LLM communication.
package provider

// ProviderSpec describes a built-in provider preset: display name, default
// model, and base URL for the OpenAI-compatible endpoint.
type ProviderSpec struct {
	Name    string
	Model   string
	BaseURL string
}

// Catalog is the single source of truth for built-in provider presets.
// The setup wizard, non-interactive --provider flag resolution, and any
// future tooling must read from this table instead of duplicating presets.
var Catalog = []ProviderSpec{
	{Name: "opencode-zen", Model: "deepseek-v4-flash", BaseURL: "https://opencode.ai/zen/v1"},
	{Name: "opencode-zen-free", Model: "deepseek-v4-flash-free", BaseURL: "https://opencode.ai/zen/v1"},
	{Name: "deepseek-direct", Model: "deepseek-v4-flash", BaseURL: "https://api.deepseek.com"},
	{Name: "openrouter", Model: "moonshotai/kimi-k3-free", BaseURL: "https://openrouter.ai/api/v1"},
	{Name: "openai", Model: "gpt-4o", BaseURL: "https://api.openai.com/v1"},
	{Name: "groq", Model: "llama-3.3-70b", BaseURL: "https://api.groq.com/openai/v1"},
}

// Find returns the spec for the named provider and whether it exists in the
// catalog. It is case-sensitive and matches on the canonical Name field.
func Find(name string) (ProviderSpec, bool) {
	for _, s := range Catalog {
		if s.Name == name {
			return s, true
		}
	}
	return ProviderSpec{}, false
}

// DefaultProvider returns the name of the fallback provider used when no
// provider is configured or requested.
func DefaultProvider() string { return "opencode-zen-free" }

// DefaultModel returns the default model for the fallback provider.
func DefaultModel() string {
	if s, ok := Find(DefaultProvider()); ok {
		return s.Model
	}
	return "deepseek-v4-flash-free"
}

// DefaultBaseURL returns the default base URL for the fallback provider.
func DefaultBaseURL() string {
	if s, ok := Find(DefaultProvider()); ok {
		return s.BaseURL
	}
	return "https://opencode.ai/zen/v1"
}
