// Package tools provides the ELING setup tool for configuring provider,
// API key, base URL, and agent settings via the LLM or CLI.
package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"eling/internal/config"

	"gopkg.in/yaml.v3"
)

func init() {
	DefaultRegistry.Register(Tool{
		Name: "eling_setup",
		Description: "Configure ELING agent settings: provider, API key, base URL, model, system prompt, max context. " +
			"Can also list current config or add a new provider. " +
			"Runs the eling-setup script. Use when user wants to change provider, update API key, or reconfigure the agent.",
		Version:  "1.1.0", // registry timeout budget
		Category: "system",
		Execute:  elingSetupExecute,
		Timeout:  60 * time.Second, // runs the eling-setup script
	})
}

// elingSetupExecute handles the eling_setup / eling-setup tool call.
// Supported arguments:
//
//	action        - "list" | "set" | "add-provider" | "remove-provider" | "set-default"
//	provider      - provider name (e.g. "opencode-zen", "openai", "groq", "custom")
//	model         - model name (e.g. "deepseek-v4-flash", "gpt-4o", "llama-3.3-70b")
//	api_key       - API key for the provider
//	base_url      - base URL for the provider API
//	system_prompt - system prompt text (for set action)
//	max_context   - max context tokens (for set action, integer)
//	name          - provider name (for add-provider / remove-provider)
func elingSetupExecute(args map[string]interface{}) (interface{}, error) {
	action, _ := args["action"].(string)
	if action == "" {
		action = "list" // default to listing
	}

	// Find and load config
	configPath := config.FindConfigPath()
	cfg, err := config.Load(configPath)
	if err != nil {
		return Err(fmt.Sprintf("failed to load config: %v", err)), nil
	}

	switch action {
	case "list":
		return listConfig(cfg, configPath), nil

	case "set", "set-default":
		return setConfig(cfg, configPath, args), nil

	case "add-provider":
		return addProvider(cfg, configPath, args), nil

	case "remove-provider":
		return removeProvider(cfg, configPath, args), nil

	case "set-api-key":
		return setAPIKey(cfg, configPath, args), nil

	default:
		return Err(fmt.Sprintf("unknown action %q; supported: list, set, add-provider, remove-provider, set-api-key", action)), nil
	}
}

// listConfig returns the current configuration as a formatted string.
func listConfig(cfg *config.Config, configPath string) Result {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("📁 Config file: %s\n\n", configPath))

	b.WriteString("🔧 Agent Settings:\n")
	b.WriteString(fmt.Sprintf("  Default Model:   %s\n", cfg.Agent.DefaultModel))
	b.WriteString(fmt.Sprintf("  Default Base URL: %s\n", cfg.Agent.DefaultBaseURL))
	sp := cfg.Agent.SystemPrompt
	if len(sp) > 80 {
		sp = sp[:80] + "..."
	}
	b.WriteString(fmt.Sprintf("  System Prompt:   %s\n", sp))
	b.WriteString(fmt.Sprintf("  Max Context:     %d\n", cfg.Agent.MaxContext))
	b.WriteString(fmt.Sprintf("  Max Turn Rounds: %d\n", cfg.Agent.MaxTurnRounds))
	b.WriteString(fmt.Sprintf("  Max Turn Dur:    %ds\n", cfg.Agent.MaxTurnDuration))
	b.WriteString(fmt.Sprintf("  Auto Test:       %v\n", cfg.Agent.AutoTest))
	b.WriteString(fmt.Sprintf("  Learn From Exch: %v\n", cfg.Agent.LearnFromExchange))
	b.WriteString("\n")

	b.WriteString("🌐 Providers:\n")
	if len(cfg.Agent.Providers) == 0 {
		b.WriteString("  (none configured)\n")
	} else {
		for i, p := range cfg.Agent.Providers {
			isDefault := ""
			if p.Name == cfg.Agent.DefaultModel || p.Model == cfg.Agent.DefaultModel {
				isDefault = " ⬅️ default"
			}
			b.WriteString(fmt.Sprintf("  %d. %s%s\n", i+1, p.Name, isDefault))
			b.WriteString(fmt.Sprintf("     Model:    %s\n", p.Model))
			b.WriteString(fmt.Sprintf("     Base URL: %s\n", p.BaseURL))
			key := p.APIKey
			if len(key) > 12 {
				key = key[:8] + "..." + key[len(key)-4:]
			} else if key != "" {
				key = "***"
			}
			b.WriteString(fmt.Sprintf("     API Key:  %s\n", key))
			if i < len(cfg.Agent.Providers)-1 {
				b.WriteString("\n")
			}
		}
	}

	b.WriteString("\n")
	b.WriteString("💻 UI Settings:\n")
	b.WriteString(fmt.Sprintf("  Theme:         %s\n", cfg.UI.Theme))
	b.WriteString(fmt.Sprintf("  Show Memory:   %v\n", cfg.UI.ShowMemory))
	b.WriteString(fmt.Sprintf("  Show Thinking: %v\n", cfg.UI.ShowThinking))
	b.WriteString(fmt.Sprintf("  Max Messages:  %d\n", cfg.UI.MaxMessages))
	b.WriteString(fmt.Sprintf("  Timezone:      %s\n", cfg.UI.Timezone))

	b.WriteString("\n")
	b.WriteString("🧠 Memory:\n")
	b.WriteString(fmt.Sprintf("  Max Short Term: %d\n", cfg.Memory.MaxShortTerm))
	b.WriteString(fmt.Sprintf("  Max Long Term:  %d\n", cfg.Memory.MaxLongTerm))
	b.WriteString(fmt.Sprintf("  Decay Rate:     %.2f\n", cfg.Memory.DecayRate))

	b.WriteString("\n")
	b.WriteString("🔌 MCP:\n")
	b.WriteString(fmt.Sprintf("  Enabled: %v\n", cfg.MCP.Enabled))
	b.WriteString(fmt.Sprintf("  Servers: %d configured\n", len(cfg.MCP.Servers)))

	b.WriteString("\n")
	b.WriteString("💾 Session:\n")
	b.WriteString(fmt.Sprintf("  Auto Save: %v\n", cfg.Session.AutoSave))
	b.WriteString(fmt.Sprintf("  Save Dir:  %s\n", cfg.Session.SaveDir))

	return OK(map[string]interface{}{
		"config":    b.String(),
		"providers": len(cfg.Agent.Providers),
		"models":    listModels(cfg),
		"file":      configPath,
	})
}

// listModels returns a formatted list of available models from providers.
func listModels(cfg *config.Config) string {
	if len(cfg.Agent.Providers) == 0 {
		return "No providers configured."
	}
	var models []string
	for _, p := range cfg.Agent.Providers {
		models = append(models, fmt.Sprintf("  %s (%s) @ %s", p.Name, p.Model, p.BaseURL))
	}
	sort.Strings(models)
	return strings.Join(models, "\n")
}

// setConfig updates the default agent settings.
func setConfig(cfg *config.Config, configPath string, args map[string]interface{}) Result {
	changed := false

	if v, ok := args["provider"].(string); ok && v != "" {
		// Find provider by name and set as default
		for _, p := range cfg.Agent.Providers {
			if p.Name == v {
				cfg.Agent.DefaultModel = p.Model
				cfg.Agent.DefaultBaseURL = p.BaseURL
				changed = true
				break
			}
		}
		// If not found, set the name as model (user might be specifying model directly)
		if !changed {
			cfg.Agent.DefaultModel = v
			changed = true
		}
	}

	if v, ok := args["model"].(string); ok && v != "" {
		cfg.Agent.DefaultModel = v
		changed = true
	}

	if v, ok := args["base_url"].(string); ok && v != "" {
		cfg.Agent.DefaultBaseURL = v
		changed = true
	}

	if v, ok := args["api_key"].(string); ok && v != "" {
		// Set API key for all providers that match the current default model
		setCount := 0
		for i := range cfg.Agent.Providers {
			if cfg.Agent.Providers[i].Model == cfg.Agent.DefaultModel ||
				cfg.Agent.Providers[i].Name == cfg.Agent.DefaultModel {
				cfg.Agent.Providers[i].APIKey = v
				setCount++
			}
		}
		// Also set for the first provider if none matched
		if setCount == 0 && len(cfg.Agent.Providers) > 0 {
			cfg.Agent.Providers[0].APIKey = v
		}
		changed = true
	}

	if v, ok := args["system_prompt"].(string); ok && v != "" {
		cfg.Agent.SystemPrompt = v
		changed = true
	}

	if v, ok := args["max_context"].(string); ok && v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			cfg.Agent.MaxContext = n
			changed = true
		}
	}

	if !changed {
		return Err("no changes provided. Use: provider, model, base_url, api_key, system_prompt, max_context")
	}

	if err := saveConfig(cfg, configPath); err != nil {
		return Err(fmt.Sprintf("failed to save config: %v", err))
	}

	return OK(map[string]interface{}{
		"message":               "Configuration updated successfully",
		"config_file":           configPath,
		"model":                 cfg.Agent.DefaultModel,
		"base_url":              cfg.Agent.DefaultBaseURL,
		"max_context":           cfg.Agent.MaxContext,
		"system_prompt_preview": truncateString(cfg.Agent.SystemPrompt, 80),
	})
}

// addProvider adds a new provider or updates an existing one.
func addProvider(cfg *config.Config, configPath string, args map[string]interface{}) Result {
	name, _ := args["name"].(string)
	if name == "" {
		name, _ = args["provider"].(string)
	}
	if name == "" {
		return Err("provider name is required (use 'name' or 'provider' argument)")
	}

	model, _ := args["model"].(string)
	baseURL, _ := args["base_url"].(string)
	apiKey, _ := args["api_key"].(string)

	if model == "" {
		// Look up from existing providers with same name
		for _, p := range cfg.Agent.Providers {
			if p.Name == name {
				model = p.Model
				baseURL = p.BaseURL
				if apiKey == "" {
					apiKey = p.APIKey
				}
				break
			}
		}
	}
	if model == "" {
		model = "deepseek-v4-flash"
	}
	if baseURL == "" {
		baseURL = "https://opencode.ai/zen/v1"
	}

	// Check if provider already exists
	found := false
	for i := range cfg.Agent.Providers {
		if cfg.Agent.Providers[i].Name == name {
			cfg.Agent.Providers[i].Model = model
			cfg.Agent.Providers[i].BaseURL = baseURL
			if apiKey != "" {
				cfg.Agent.Providers[i].APIKey = apiKey
			}
			found = true
			break
		}
	}

	if !found {
		newProvider := config.ProviderConfig{
			Name:    name,
			Model:   model,
			BaseURL: baseURL,
			APIKey:  apiKey,
		}
		cfg.Agent.Providers = append(cfg.Agent.Providers, newProvider)
	}

	// If this is the only provider, set as default
	if len(cfg.Agent.Providers) == 1 {
		cfg.Agent.DefaultModel = model
		cfg.Agent.DefaultBaseURL = baseURL
	}

	// Set as default if requested
	if setDefault, _ := args["set_default"].(bool); setDefault {
		cfg.Agent.DefaultModel = model
		cfg.Agent.DefaultBaseURL = baseURL
	}

	if err := saveConfig(cfg, configPath); err != nil {
		return Err(fmt.Sprintf("failed to save config: %v", err))
	}

	return OK(map[string]interface{}{
		"message":     fmt.Sprintf("Provider %q added/updated", name),
		"provider":    name,
		"model":       model,
		"base_url":    baseURL,
		"api_key_set": apiKey != "",
	})
}

// removeProvider removes a provider by name.
func removeProvider(cfg *config.Config, configPath string, args map[string]interface{}) Result {
	name, _ := args["name"].(string)
	if name == "" {
		name, _ = args["provider"].(string)
	}
	if name == "" {
		return Err("provider name is required (use 'name' argument)")
	}

	removed := false
	for i := range cfg.Agent.Providers {
		if cfg.Agent.Providers[i].Name == name {
			cfg.Agent.Providers = append(cfg.Agent.Providers[:i], cfg.Agent.Providers[i+1:]...)
			removed = true
			break
		}
	}

	if !removed {
		return Err(fmt.Sprintf("provider %q not found", name))
	}

	if err := saveConfig(cfg, configPath); err != nil {
		return Err(fmt.Sprintf("failed to save config: %v", err))
	}

	return OK(map[string]interface{}{
		"message":  fmt.Sprintf("Provider %q removed", name),
		"provider": name,
	})
}

// setAPIKey sets the API key for a specific provider or all providers.
func setAPIKey(cfg *config.Config, configPath string, args map[string]interface{}) Result {
	apiKey, _ := args["api_key"].(string)
	if apiKey == "" {
		return Err("api_key is required")
	}

	providerName, _ := args["provider"].(string)
	providerName2, _ := args["name"].(string)
	if providerName2 != "" {
		providerName = providerName2
	}

	setCount := 0
	if providerName != "" {
		// Set for specific provider
		for i := range cfg.Agent.Providers {
			if cfg.Agent.Providers[i].Name == providerName {
				cfg.Agent.Providers[i].APIKey = apiKey
				setCount++
				break
			}
		}
		if setCount == 0 {
			return Err(fmt.Sprintf("provider %q not found", providerName))
		}
	} else {
		// Set for all providers
		for i := range cfg.Agent.Providers {
			cfg.Agent.Providers[i].APIKey = apiKey
			setCount++
		}
	}

	if err := saveConfig(cfg, configPath); err != nil {
		return Err(fmt.Sprintf("failed to save config: %v", err))
	}

	return OK(map[string]interface{}{
		"message":     fmt.Sprintf("API key set for %d provider(s)", setCount),
		"providers":   setCount,
		"api_key_set": apiKey != "",
	})
}

// saveConfig saves the config to the YAML file.
func saveConfig(cfg *config.Config, configPath string) error {
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	// Backup existing config
	if _, err := os.Stat(configPath); err == nil {
		backupPath := configPath + ".bak"
		data, _ := os.ReadFile(configPath)
		_ = os.WriteFile(backupPath, data, 0600)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	return nil
}

func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
