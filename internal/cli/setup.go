package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"eling/internal/config"
)

const (
	setupGreen = "\033[0;32m"
	setupCyan  = "\033[0;36m"
	setupRed   = "\033[0;31m"
	setupYell  = "\033[0;33m"
	setupBold  = "\033[1m"
	setupNC    = "\033[0m"
)

func cmdSetup(cfg *config.Config, args []string) bool {
	// ── Same-as-eling-wizard delegation ─────────────────────────────────
	// `eling setup` should behave identically to `eling-wizard`.
	// Delegate to eling-wizard.sh whenever it can be found. Only the
	// extended flags the wizard doesn't support (--add-provider, --test)
	// keep using the built-in Go implementation below.
	if wizard, ok := findWizardScript(); ok {
		if wargs, ok2 := wizardArgs(args); ok2 {
			runWizard(wizard, wargs)
			return true // unreachable (runWizard exits)
		}
	}

	cfgPath := config.FindConfigPath()
	existing, err := config.Load(cfgPath)
	if err != nil {
		existing = config.DefaultConfig()
	}

	listMode := false
	addProvider := false
	testMode := false
	opts := map[string]string{}

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--list", "list", "-l":
			listMode = true
		case "--add-provider":
			addProvider = true
		case "--test":
			testMode = true
		case "--api-key", "--provider", "--model", "--base-url", "--system-prompt", "--max-context":
			if i+1 < len(args) {
				i++
				opts[args[i-1]] = args[i]
			}
		case "--help", "-h", "help":
			printSetupHelp()
			return true
		default:
			fmt.Fprintf(os.Stderr, "Unknown setup option: %s\n", args[i])
			printSetupHelp()
			return true
		}
	}

	if listMode {
		return setupList(cfgPath)
	}

	interactive := isTerminal(os.Stdin)

	if interactive && len(opts) == 0 && !addProvider {
		return setupInteractive(cfgPath, existing)
	}

	if len(opts) == 0 && !addProvider {
		fmt.Fprintf(os.Stderr, "%s[ERROR]%s No setup options provided.\n", setupRed, setupNC)
		fmt.Println("  Run 'eling setup' interactively, or pass flags:")
		fmt.Println("    eling setup --api-key sk-... --model <model> --base-url <url>")
		fmt.Println("    eling setup --list    # view current configuration")
		return true
	}

	return setupApply(cfgPath, existing, opts, addProvider, testMode)
}

func printSetupHelp() {
	fmt.Print(`ELING Setup — configure providers, API key, base URL, and agent settings.
Saves to ~/.eling/config.yaml

Usage:
  eling setup                     Interactive wizard
  eling setup --list              View current configuration
  eling setup --api-key sk-...    Quick setup (prompts for the rest interactively)

Non-interactive (all flags):
  eling setup \
    --provider opencode-zen-free \
    --model deepseek-v4-flash-free \
    --api-key "sk-..." \
    --base-url "https://opencode.ai/zen/v1" \
    --system-prompt "You are ELING..." \
    --max-context 32768 \
    --test

  eling setup --add-provider --provider groq --model llama-3.3-70b \
    --api-key "gsk-..." --base-url "https://api.groq.com/openai/v1"
`)
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func readLine(reader *bufio.Reader) string {
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}

func setupHeader(cfgPath string) {
	fmt.Printf("%s━━━ ELING Setup %s━━━%s\n\n", setupCyan, setupNC, setupNC)
	fmt.Printf("Config file: %s\n\n", cfgPath)
}

func setupSummary(cfg *config.Config, provider *config.ProviderConfig) {
	fmt.Printf("%s━━━ Setup Complete! 🎉%s\n\n", setupBold, setupNC)
	if provider != nil {
		fmt.Printf("  Provider:    %s\n", provider.Name)
		fmt.Printf("  Model:       %s\n", provider.Model)
		fmt.Printf("  Base URL:    %s\n", provider.BaseURL)
		fmt.Printf("  API Key:     %s\n", maskKey(provider.APIKey))
	} else if len(cfg.Agent.Providers) > 0 {
		p := cfg.Agent.Providers[0]
		fmt.Printf("  Provider:    %s\n", p.Name)
		fmt.Printf("  Model:       %s\n", p.Model)
		fmt.Printf("  Base URL:    %s\n", p.BaseURL)
		fmt.Printf("  API Key:     %s\n", maskKey(p.APIKey))
	}
	fmt.Printf("  System Prompt: %s\n", truncateStr(cfg.Agent.SystemPrompt, 60))
	fmt.Printf("  Max Context:   %d\n", cfg.Agent.MaxContext)
	fmt.Printf("  Config File:   %s\n\n", config.FindConfigPath())
	fmt.Println("  Run 'eling' to start ELING with your new configuration.")
	fmt.Println("  Run 'eling setup --list' to view your config.")
	fmt.Println("  Run 'eling setup' again to reconfigure.")
}

func maskKey(key string) string {
	if len(key) <= 12 {
		return key
	}
	return key[:8] + "..." + key[len(key)-4:]
}

func setupList(cfgPath string) bool {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		return true
	}

	fmt.Printf("%s━━━ Current ELING Configuration %s━━━%s\n\n", setupCyan, setupNC, setupNC)
	fmt.Printf("Config file: %s\n\n", cfgPath)
	fmt.Printf("%sAgent Settings:%s\n", setupBold, setupNC)
	fmt.Printf("  Default Model:   %s\n", cfg.Agent.DefaultModel)
	fmt.Printf("  Default Base URL: %s\n", cfg.Agent.DefaultBaseURL)
	fmt.Printf("  System Prompt:   %s\n", cfg.Agent.SystemPrompt)
	fmt.Printf("  Max Context:     %d\n\n", cfg.Agent.MaxContext)

	if len(cfg.Agent.Providers) == 0 {
		fmt.Println("  (no providers configured)")
	} else {
		fmt.Printf("%sProviders:%s\n", setupBold, setupNC)
		for i, p := range cfg.Agent.Providers {
			fmt.Printf("  %s%d.%s %s\n", setupCyan, i+1, setupNC, p.Name)
			fmt.Printf("     Model:   %s\n", p.Model)
			fmt.Printf("     Base URL: %s\n", p.BaseURL)
			fmt.Printf("     API Key:  %s\n", maskKey(p.APIKey))
			if len(p.BackupKeys) > 0 {
				fmt.Printf("     Backup Keys: %d\n", len(p.BackupKeys))
			}
			fmt.Println()
		}
	}
	return true
}

// setupInteractive runs the full wizard. Requires a terminal.
func setupInteractive(cfgPath string, existing *config.Config) bool {
	reader := bufio.NewReader(os.Stdin)
	setupHeader(cfgPath)

	currentKey := ""
	if len(existing.Agent.Providers) > 0 {
		currentKey = existing.Agent.Providers[0].APIKey
	}
	if currentKey == "" {
		currentKey = os.Getenv("DEEPSEEK_API_KEY")
	}
	if currentKey == "" {
		currentKey = os.Getenv("OPENCODE_API_KEY")
	}

	fmt.Printf("%sEnter your API key.%s\n", setupYell, setupNC)
	fmt.Println("  If you don't have one, get it from: https://opencode.ai")
	fmt.Printf("  %s(The key will be stored in %s)%s\n", setupYell, cfgPath, setupNC)
	fmt.Printf("%s[API Key]%s [%s]: ", setupBold, setupNC, maskKey(currentKey))
	apiKey := readLine(reader)
	if apiKey == "" {
		apiKey = currentKey
	}
	for apiKey == "" {
		fmt.Printf("%s[ERROR]%s API key cannot be empty.\n", setupRed, setupNC)
		fmt.Printf("%s[API Key]%s: ", setupBold, setupNC)
		apiKey = readLine(reader)
	}

	fmt.Println("\nAvailable providers:")
	presets := setupPresets()
	type catalogEntry struct {
		name   string
		model  string
		base   string
		exists bool
	}
	var catalog []catalogEntry
	for _, p := range existing.Agent.Providers {
		catalog = append(catalog, catalogEntry{p.Name, p.Model, p.BaseURL, true})
	}
	for _, p := range presets {
		if !hasProvider(existing, p[0]) {
			catalog = append(catalog, catalogEntry{p[0], p[1], p[2], false})
		}
	}
	catalog = append(catalog, catalogEntry{"custom", "", "", false})
	for i, e := range catalog {
		marker := ""
		if e.exists {
			marker = " (configured)"
		}
		fmt.Printf("  %d. %s%s (%s)\n", i+1, e.name, marker, e.model)
	}
	fmt.Printf("  %s[Provider]%s (1-%d, default %d): ", setupBold, setupNC, len(catalog), 1)

	choice := readLine(reader)
	idx := 1
	if choice != "" {
		if n, err := strconv.Atoi(choice); err == nil && n >= 1 && n <= len(catalog) {
			idx = n
		} else {
			fmt.Printf("%s[WARN]%s Invalid choice, using %s\n", setupYell, setupNC, catalog[0].name)
		}
	}
	entry := catalog[idx-1]

	providerName := entry.name
	model := entry.model
	baseURL := entry.base
	if entry.name == "custom" {
		fmt.Printf("%s[Provider name]%s: ", setupBold, setupNC)
		providerName = readLine(reader)
		for providerName == "" {
			fmt.Printf("%s[ERROR]%s Provider name cannot be empty.\n", setupRed, setupNC)
			fmt.Printf("%s[Provider name]%s: ", setupBold, setupNC)
			providerName = readLine(reader)
		}
		fmt.Printf("%s[Model]%s: ", setupBold, setupNC)
		model = readLine(reader)
		fmt.Printf("%s[Base URL]%s: ", setupBold, setupNC)
		baseURL = readLine(reader)
	} else {
		fmt.Printf("  Using %s — model: %s, base URL: %s\n", providerName, model, baseURL)
	}

	fmt.Printf("%s[Backup keys (comma-separated, optional)]%s: ", setupBold, setupNC)
	backupLine := readLine(reader)
	var backupKeys []string
	for _, k := range strings.Split(backupLine, ",") {
		k = strings.TrimSpace(k)
		if k != "" {
			backupKeys = append(backupKeys, k)
		}
	}

	fmt.Printf("%s[System prompt]%s [%s]: ", setupBold, setupNC, existing.Agent.SystemPrompt)
	systemPrompt := readLine(reader)
	if systemPrompt == "" {
		systemPrompt = existing.Agent.SystemPrompt
	}

	fmt.Printf("%s[Max context]%s [%d]: ", setupBold, setupNC, existing.Agent.MaxContext)
	maxContext := existing.Agent.MaxContext
	if mc := readLine(reader); mc != "" {
		if n, err := strconv.Atoi(mc); err == nil && n > 0 {
			maxContext = n
		}
	}

	provider := config.ProviderConfig{
		Name:       providerName,
		Model:      model,
		BaseURL:    baseURL,
		APIKey:     apiKey,
		BackupKeys: backupKeys,
	}
	cfg := upsertProvider(existing, provider, false)

	cfg.Agent.DefaultModel = model
	cfg.Agent.DefaultBaseURL = baseURL
	cfg.Agent.SystemPrompt = systemPrompt
	if maxContext > 0 {
		cfg.Agent.MaxContext = maxContext
	}

	if err := saveConfigWithBackup(cfgPath, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "%s[ERROR]%s %v\n", setupRed, setupNC, err)
		return true
	}
	setupSummary(cfg, &provider)

	fmt.Printf("%sTest the configuration by making a quick API call? [Y/n]:%s ", setupBold, setupNC)
	testChoice := readLine(reader)
	if !strings.HasPrefix(strings.ToLower(testChoice), "n") {
		setupTestAPI(provider)
	}
	return true
}

// setupPresets returns built-in provider presets: name, model, base URL.
func setupPresets() [][3]string {
	return [][3]string{
		{"opencode-zen", "deepseek-v4-flash", "https://opencode.ai/zen/v1"},
		{"opencode-zen-free", "deepseek-v4-flash-free", "https://opencode.ai/zen/v1"},
		{"deepseek-direct", "deepseek-v4-flash", "https://api.deepseek.com"},
		{"openrouter", "moonshotai/kimi-k3-free", "https://openrouter.ai/api/v1"},
		{"openai", "gpt-4o", "https://api.openai.com/v1"},
		{"groq", "llama-3.3-70b", "https://api.groq.com/openai/v1"},
	}
}

func hasProvider(cfg *config.Config, name string) bool {
	for _, p := range cfg.Agent.Providers {
		if p.Name == name {
			return true
		}
	}
	return false
}

// setupApply applies flags non-interactively (or partially interactive).
func setupApply(cfgPath string, existing *config.Config, opts map[string]string, addProvider, testMode bool) bool {
	cfg := existing

	providerName := opts["--provider"]
	model := opts["--model"]
	baseURL := opts["--base-url"]
	apiKey := opts["--api-key"]
	systemPrompt := opts["--system-prompt"]
	maxContextStr := opts["--max-context"]

	if providerName == "" && len(cfg.Agent.Providers) > 0 {
		providerName = cfg.Agent.Providers[0].Name
	}
	if providerName == "" {
		providerName = "opencode-zen-free"
	}
	for _, p := range setupPresets() {
		if p[0] == providerName {
			if model == "" {
				model = p[1]
			}
			if baseURL == "" {
				baseURL = p[2]
			}
			break
		}
	}
	if model == "" && len(cfg.Agent.Providers) > 0 {
		model = cfg.Agent.Providers[0].Model
	}
	if model == "" {
		model = "deepseek-v4-flash-free"
	}
	if baseURL == "" && len(cfg.Agent.Providers) > 0 {
		baseURL = cfg.Agent.Providers[0].BaseURL
	}
	if baseURL == "" {
		baseURL = "https://opencode.ai/zen/v1"
	}
	if apiKey == "" {
		apiKey = os.Getenv("DEEPSEEK_API_KEY")
	}
	if apiKey == "" {
		apiKey = os.Getenv("OPENCODE_API_KEY")
	}

	if apiKey == "" {
		fmt.Fprintf(os.Stderr, "%s[ERROR]%s API key is required. Provide via --api-key flag or run interactively.\n", setupRed, setupNC)
		fmt.Println("  export DEEPSEEK_API_KEY='sk-...'")
		return true
	}

	provider := config.ProviderConfig{
		Name:    providerName,
		Model:   model,
		BaseURL: baseURL,
		APIKey:  apiKey,
	}
	cfg = upsertProvider(cfg, provider, addProvider)

	cfg.Agent.DefaultModel = model
	cfg.Agent.DefaultBaseURL = baseURL
	if systemPrompt != "" {
		cfg.Agent.SystemPrompt = systemPrompt
	}
	if maxContextStr != "" {
		if n, err := strconv.Atoi(maxContextStr); err == nil && n > 0 {
			cfg.Agent.MaxContext = n
		}
	}

	if err := saveConfigWithBackup(cfgPath, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "%s[ERROR]%s %v\n", setupRed, setupNC, err)
		return true
	}
	setupSummary(cfg, &provider)

	if testMode {
		setupTestAPI(provider)
	}
	return true
}

// upsertProvider updates an existing provider with the same name, or appends it.
func upsertProvider(cfg *config.Config, provider config.ProviderConfig, forceAppend bool) *config.Config {
	for i := range cfg.Agent.Providers {
		if cfg.Agent.Providers[i].Name == provider.Name && !forceAppend {
			cfg.Agent.Providers[i].Model = provider.Model
			cfg.Agent.Providers[i].BaseURL = provider.BaseURL
			cfg.Agent.Providers[i].APIKey = provider.APIKey
			if len(provider.BackupKeys) > 0 {
				cfg.Agent.Providers[i].BackupKeys = provider.BackupKeys
			}
			return cfg
		}
	}
	cfg.Agent.Providers = append(cfg.Agent.Providers, provider)
	return cfg
}

func saveConfigWithBackup(cfgPath string, cfg *config.Config) error {
	if data, err := os.ReadFile(cfgPath); err == nil {
		backup := fmt.Sprintf("%s.bak.%s", cfgPath, time.Now().Format("20060102150405"))
		if err := os.WriteFile(backup, data, 0600); err == nil {
			fmt.Printf("%s[INFO]%s Backed up existing config to %s\n", setupGreen, setupNC, backup)
		}
	}
	if err := cfg.Save(cfgPath); err != nil {
		return err
	}
	fmt.Printf("%s[INFO]%s Configuration written to %s\n\n", setupGreen, setupNC, cfgPath)
	return nil
}

func setupTestAPI(p config.ProviderConfig) {
	fmt.Printf("%s[INFO]%s Testing API connection...\n", setupGreen, setupNC)

	payload, _ := json.Marshal(map[string]interface{}{
		"model": p.Model,
		"messages": []map[string]string{
			{"role": "user", "content": "Say 'Hello from ELING!' in one sentence."},
		},
		"max_tokens": 50,
	})

	client := &http.Client{Timeout: 20 * time.Second}
	url := strings.TrimSuffix(p.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequest("POST", url, strings.NewReader(string(payload)))
	if err != nil {
		fmt.Printf("%s[WARN]%s Could not build request: %v\n", setupYell, setupNC, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("%s[WARN]%s API connection test failed: %v\n", setupYell, setupNC, err)
		fmt.Println("  You may still use ELING. The config file has been saved.")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var data struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		content := "Connected successfully!"
		if err := json.NewDecoder(resp.Body).Decode(&data); err == nil && len(data.Choices) > 0 {
			content = data.Choices[0].Message.Content
		}
		fmt.Printf("%s✓ API connection successful!%s\n", setupGreen, setupNC)
		fmt.Printf("  Response: %s\n", content)
	} else {
		body := make([]byte, 512)
		n, _ := resp.Body.Read(body)
		fmt.Printf("%s[WARN]%s API connection test returned HTTP %d\n", setupYell, setupNC, resp.StatusCode)
		fmt.Printf("  Response: %s\n", strings.TrimSpace(string(body[:n])))
		fmt.Println("  You may still use ELING. The config file has been saved.")
	}
}

// ── eling-wizard.sh delegation ─────────────────────────────────────────────
// These helpers make `eling setup` behave identically to `eling-wizard` by
// locating the wizard script (eling-wizard.sh) and exec'ing it with the same
// arguments, preserving stdin/stdout/stderr and the exit code.

// findWizardScript locates eling-wizard.sh next to the binary, in standard
// install locations, or via the ELING_WIZARD env var.
func findWizardScript() (string, bool) {
	candidates := []string{}

	if env := os.Getenv("ELING_WIZARD"); env != "" {
		candidates = append(candidates, env)
	}

	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates,
			filepath.Join(filepath.Dir(exe), "eling-wizard.sh"),
			filepath.Join(filepath.Dir(exe), "eling-wizard"),
		)
	}

	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".local", "bin", "eling-wizard"),
			filepath.Join(home, ".local", "bin", "eling-wizard.sh"),
			filepath.Join(home, ".eling", "eling-wizard.sh"),
		)
	}

	candidates = append(candidates,
		"/usr/local/bin/eling-wizard",
		"/usr/local/bin/eling-wizard.sh",
	)

	// Also allow the repo layout when running from the source tree.
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "eling-wizard.sh"))
	}

	for _, c := range candidates {
		if c == "" {
			continue
		}
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() && fi.Mode().IsRegular() {
			return c, true
		}
	}
	return "", false
}

// wizardArgs translates `eling setup` arguments into eling-wizard.sh
// arguments. Returns ok=false when the built-in Go setup should be used
// instead (e.g. --add-provider / --test which the wizard doesn't support).
func wizardArgs(args []string) ([]string, bool) {
	if len(args) == 0 {
		return nil, true // interactive wizard mode
	}

	var out []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--list", "-l", "list":
			out = append(out, "--list")
		case "--help", "-h", "help":
			out = append(out, "--help")
		case "--quick":
			out = append(out, "--quick")
		case "--provider", "--api-key", "--model", "--base-url", "--system-prompt", "--max-context":
			if i+1 < len(args) {
				out = append(out, args[i], args[i+1])
				i++
			}
		case "--add-provider", "--test":
			// Extended flags not supported by the wizard — use built-in.
			return nil, false
		default:
			// Unknown argument — let the built-in setup report the error.
			return nil, false
		}
	}

	// Value-only flags (e.g. --api-key sk-...) imply quick mode so the
	// wizard doesn't drop into the interactive prompts.
	hasMode := false
	for _, a := range out {
		if a == "--list" || a == "--help" || a == "--quick" {
			hasMode = true
			break
		}
	}
	if !hasMode && len(out) > 0 {
		out = append([]string{"--quick"}, out...)
	}
	return out, true
}

// runWizard execs the wizard script with the given args, wiring up stdio and
// preserving the wizard's exit code. It never returns on success.
func runWizard(script string, args []string) {
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	if err := cmd.Run(); err != nil {
		code := 1
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		os.Exit(code)
	}
	os.Exit(0)
}
