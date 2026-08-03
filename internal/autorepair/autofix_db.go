package autorepair

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"eling/internal/config"
)

// RegisterBuiltinFixers installs the Phase-1 grounded repair recipes. Each is
// probe-first + idempotent. They are registered but, per the plan, autofix is
// OFF by default so Repair() only reports advisory + runs nothing until
// SetAutofix(true) (Phase 3).
//
// The recipes are grounded in real incidents this session:
//   - ocr missing (npm i -g @alibaba-group/open-code-review)
//   - grep wrapper drift → ugrep-backed /usr/local/bin/grep
//   - provider base_url / api key drift → GET {base}/models probe
//   - env tokens (GITHUB_TOKEN) missing / empty
func buildBuiltinFixers() []Fixer {
	return []Fixer{
		// --- ClassMissingBinary: ocr code-review CLI ---
		{
			Tool:    "ocr",
			Class:   ClassMissingDep,
			Summary: "install ocr via npm -g @alibaba-group/open-code-review",
			Probe:   probeExecutable("ocr"),
			Fix: func() error {
				cmd := exec.Command("npm", "install", "-g", "@alibaba-group/open-code-review")
				cmd.Env = os.Environ()
				out, err := cmd.CombinedOutput()
				if err != nil {
					return fmt.Errorf("npm install failed: %w: %s", err, strings.TrimSpace(string(out)))
				}
				return nil
			},
		},
		// --- ClassMissingBinary: ugrep needed behind the grep wrapper ---
		{
			Tool:    "grep",
			Class:   ClassMissingDep,
			Summary: "ensure ugrep binary is available for the grep wrapper",
			Probe:   probeExecutable("ugrep"),
			Fix: func() error {
				// Idempotent: only attempt if not present.
				if _, err := exec.LookPath("ugrep"); err == nil {
					return nil
				}
				cmd := exec.Command("apt-get", "install", "-y", "ugrep")
				cmd.Env = os.Environ()
				if out, err := cmd.CombinedOutput(); err != nil {
					return fmt.Errorf("apt-get install ugrep failed: %w: %s", err, strings.TrimSpace(string(out)))
				}
				return nil
			},
		},
		// --- ClassConfigDrift: grep wrapper pointing at ugrep, not GNU grep ---
		{
			Tool:    "grep",
			Class:   ClassConfigDrift,
			Summary: "repair /usr/local/bin/grep wrapper to delegate to ugrep",
			Probe:   probeGrepWrapper(),
			Fix:     fixGrepWrapper(),
		},
		// --- ClassConfigDrift: provider base_url / api_key -> GET /models ---
		{
			Tool:    "",
			Class:   ClassConfigDrift,
			Summary: "re-validate provider via GET /models (advisory edit gate)",
			Probe:   probeProviderModels(),
			Fix: func() error {
				// Default provider endpoint may be transient; re-probe to
				// distinguish "fixable config" from "advisory". We do NOT
				// rewrite config destructively in Phase 1.
				return probeProviderModels()()
			},
			Destructive: true, // re-writing config is gated — advisory only
		},
		// --- ClassEnv: token presence ---
		{
			Tool:    "",
			Class:   ClassConfigDrift,
			Summary: "verify GITHUB_TOKEN / GH_TOKEN present in env",
			Probe: func() error {
				if os.Getenv("GITHUB_TOKEN") == "" && os.Getenv("GH_TOKEN") == "" {
					return fmt.Errorf("no GITHUB_TOKEN or GH_TOKEN set in environment")
				}
				return nil
			},
			Fix: func() error {
				// The actual token value lives in ~/.github-token; we only
				// surface the gap here — injecting secrets is manual (advisory)
				// so returning non-nil keeps this advisory.
				return fmt.Errorf("manual action required: re-export GITHUB_TOKEN/GH_TOKEN, or run the github-token setup tool")
			},
			Destructive: false,
		},
	}
}

// probeExecutable returns a probe that verifies a binary exists on PATH.
func probeExecutable(name string) func() error {
	return func() error {
		if name == "" {
			return fmt.Errorf("empty binary name")
		}
		p, err := exec.LookPath(name)
		if err != nil {
			return err
		}
		fi, err := os.Stat(p)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return fmt.Errorf("%s resolves to a directory, not an executable", p)
		}
		return nil
	}
}

// probeGrepWrapper verifies the /usr/local/bin/grep wrapper delegates to ugrep
// and not to GNU grep (the classic drift this session hit).
func probeGrepWrapper() func() error {
	return func() error {
		p := "/usr/local/bin/grep"
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("grep wrapper missing at %s: %w", p, err)
		}
		out, err := exec.Command(p, "--version").Output()
		if err != nil {
			return fmt.Errorf("grep wrapper invocation failed: %w", err)
		}
		// ugrep identifies itself as "ugrep" in --version output.
		if !strings.Contains(strings.ToLower(string(out)), "ugrep") {
			return fmt.Errorf("grep wrapper is not ugrep-backed (output: %s)", strings.TrimSpace(string(out)))
		}
		return nil
	}
}

// fixGrepWrapper rewrites the wrapper script so grep → ugrep (with .bak backup).
// Idempotent: only overwrites when the probe is unhealthy.
func fixGrepWrapper() func() error {
	return func() error {
		p := "/usr/local/bin/grep"
		// Already healthy? no-op (idempotent guard).
		if err := probeGrepWrapper()(); err == nil {
			return nil
		}
		backup := p + ".bak"
		if b, err := os.ReadFile(p); err == nil {
			_ = os.WriteFile(backup, b, 0o755) // backup before overwrite
		}
		script := "#!/bin/sh\nexec ugrep \"$@\"\n"
		if _, err := exec.LookPath("ugrep"); err != nil {
			return fmt.Errorf("cannot repair grep wrapper: ugrep not on PATH")
		}
		if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
			return fmt.Errorf("writing grep wrapper: %w", err)
		}
		return nil
	}
}

// probeProviderModels validates the configured default provider by issuing a
// GET {base}/models with the configured api key (the way setup does). It
// returns nil when the endpoint responds 200/OK, which indicates the provider
// config is healthy (no drift).
func probeProviderModels() func() error {
	return func() error {
		cfgPath := config.FindConfigPath()
		if cfgPath == "" {
			return fmt.Errorf("no config file found")
		}
		cfg, err := config.Load(cfgPath)
		if err != nil {
			return fmt.Errorf("cannot load config %s: %w", cfgPath, err)
		}
		// Derive the active provider: the one whose base_url matches the agent
		// default, else the first configured provider.
		base := cfg.Agent.DefaultBaseURL
		key := ""
		for _, p := range cfg.Agent.Providers {
			if p.BaseURL != "" && p.BaseURL == cfg.Agent.DefaultBaseURL {
				key = p.APIKey
				break
			}
		}
		if key == "" && len(cfg.Agent.Providers) > 0 {
			key = cfg.Agent.Providers[0].APIKey
			if base == "" {
				base = cfg.Agent.Providers[0].BaseURL
			}
		}
		if key == "" && len(cfg.Agent.Providers) > 0 {
			// fall back to first non-empty key
			for _, p := range cfg.Agent.Providers {
				if p.APIKey != "" {
					key = p.APIKey
					break
				}
			}
		}
		if base == "" {
			return fmt.Errorf("provider base_url not configured (config drift)")
		}
		if key == "" {
			return fmt.Errorf("provider api_key not configured (config drift)")
		}
		url := strings.TrimRight(base, "/") + "/models"
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return fmt.Errorf("bad /models request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+key)
		client := &http.Client{Timeout: 8 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("provider /models unreachable: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("provider /models returned HTTP %d", resp.StatusCode)
		}
		return nil
	}
}

// grepWrapperPath returns the stable wrapper path so tests / TUI can reference it.
func grepWrapperPath() string { return filepath.Join("/", "usr", "local", "bin", "grep") }