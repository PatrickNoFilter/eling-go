package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"eling/internal/agent"
	"eling/internal/autorepair"
	"eling/internal/budget"
	"eling/internal/cli"
	"eling/internal/config"
	"eling/internal/learnings"
	"eling/internal/logger"
	"eling/internal/markdownify"
	"eling/internal/mcp/skill"
	"eling/internal/tools"
	"eling/internal/tui"

	"github.com/briandowns/spinner"
)

// killPreviousInstance signals any eling process started from the same
// PID file. Uses SIGTERM for graceful shutdown so state is saved.
// Falls back to SIGKILL after a short grace period if the process
// doesn't exit on its own.
func killPreviousInstance() {
	pidFile := pidFilePath()
	data, err := os.ReadFile(pidFile)
	if err == nil {
		var pid int
		if _, err := fmt.Sscanf(string(data), "%d", &pid); err == nil && pid > 0 && pid != os.Getpid() {
			// Verify the process is actually an eling binary before signalling
			comm, readErr := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
			if readErr == nil && strings.TrimSpace(string(comm)) == "eling" {
				logger.Global().Info("Killing previous ELING instance (PID %d)", pid)
				// Try SIGTERM first for graceful shutdown.
				// Use syscall.Kill directly (not exec.Command) so signal delivery
				// is not delayed by PRoot's ptrace of subprocesses.
				_ = syscall.Kill(pid, syscall.SIGTERM)
				// Wait for graceful shutdown (PRoot can add latency)
				for i := 0; i < 10; i++ {
					if syscall.Kill(pid, 0) != nil {
						// Process is gone
						return
					}
					time.Sleep(200 * time.Millisecond)
				}
				// Force kill if still alive
				logger.Global().Warn("Previous instance (PID %d) did not exit after SIGTERM, sending SIGKILL", pid)
				_ = syscall.Kill(pid, syscall.SIGKILL)
				time.Sleep(200 * time.Millisecond)
			}
		}
	}
}

func pidFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".eling", "eling.pid")
}

func writePIDFile() {
	pidFile := pidFilePath()
	if err := os.MkdirAll(filepath.Dir(pidFile), 0755); err != nil {
		return
	}
	_ = os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0644)
	logger.Global().Info("PID file written: %s -> %d", pidFile, os.Getpid())
}

// removePIDFile cleans up the PID file on graceful shutdown.
func removePIDFile() {
	pidFile := pidFilePath()
	// Only remove if it's our PID
	data, err := os.ReadFile(pidFile)
	if err == nil {
		var pid int
		if _, err := fmt.Sscanf(string(data), "%d", &pid); err == nil && pid == os.Getpid() {
			_ = os.Remove(pidFile)
			logger.Global().Info("PID file removed: %s", pidFile)
		}
	}
}

// checkCrashOnStartup detects if the previous instance crashed.
// Returns true and a message if a crash was detected.
// Checks both PID file staleness and bus error / fatal signal crash reports.
func checkCrashOnStartup() (bool, string) {
	pidFile := pidFilePath()

	// Check for PID-based crash (process died without cleaning up PID file)
	if crashed, reason := logger.Global().DetectCrash(pidFile); crashed {
		return true, reason
	}

	// Check for bus error / fatal signal crash (SIGBUS, SIGSEGV, etc.)
	if crashed, reason := logger.DetectBusErrorOnStartup(); crashed {
		return true, reason
	}

	return false, ""
}

// safeSaveState saves agent state with a timeout to prevent deadlock.
// If the save doesn't complete within 5 seconds, it's aborted.
// This is used during panic recovery to avoid deadlocking if the
// panic happened while holding the agent's mutex.
func safeSaveState(ag *agent.Agent) {
	if ag == nil {
		return
	}
	done := make(chan bool, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.WriteCrashReport(r, string(debug.Stack()))
			}
		}()
		if err := ag.SaveState(); err != nil {
			logger.Global().Error("safeSaveState: %v", err)
		}
		done <- true
	}()
	select {
	case <-done:
		logger.Global().Info("State saved successfully")
	case <-time.After(5 * time.Second):
		logger.Global().Warn("State save timed out (possible lock contention) - state may be incomplete")
	}
}

// exitWithCleanup performs graceful cleanup before os.Exit, since os.Exit
// skips deferred functions (PID file removal, clean-shutdown marker, log close).
// Without this, an intentional exit after writePIDFile() leaves a stale PID
// file that the next startup's DetectCrash misreports as a crash.
func exitWithCleanup(ag *agent.Agent, code int) {
	safeSaveState(ag)
	removePIDFile()
	logger.WriteCleanShutdownMarker()
	_ = logger.Global().Close()
	os.Exit(code)
}

// recoverWithStack catches panics, logs them to the crash log,
// prints the stack trace to stderr, and optionally saves agent state
// before re-exiting. Uses safeSaveState to prevent deadlock.
func recoverWithStack(ag *agent.Agent) {
	if r := recover(); r != nil {
		// Log to crash log
		logger.Global().Panic(r)
		logger.WriteCrashReport(r, string(debug.Stack()))

		// Print to stderr
		fmt.Fprintf(os.Stderr, "\n=== ELING PANIC ===\n")
		fmt.Fprintf(os.Stderr, "Error: %v\n", r)
		fmt.Fprintf(os.Stderr, "Stack trace:\n%s\n", debug.Stack())
		fmt.Fprintf(os.Stderr, "====================\n")

		// Try to save state (with timeout to prevent deadlock)
		safeSaveState(ag)

		os.Exit(1)
	}
}

const Version = "0.4.4"

func main() {
	apiKey := flag.String("api-key", "", "DeepSeek API key (or set DEEPSEEK_API_KEY env var)")
	model := flag.String("model", "", "Model to use (default from config)")
	configPath := flag.String("config", "", "Path to config file")
	sessionName := flag.String("resume", "", "Resume a named session")
	listSessions := flag.Bool("list-sessions", false, "List all saved sessions")
	lastSession := flag.Bool("last", false, "Resume the most recent session")
	showSessions := flag.Bool("sessions", false, "List sessions (alias for --list-sessions)")
	sessionLabel := flag.String("session-name", "", "Name this session (for easy recall later)")
	nonInteractive := flag.Bool("run", false, "Run a single command non-interactively")
	mcpMode := flag.Bool("mcp", false, "Run as MCP server (exposes brain layers as MCP tools)")
	mcpVerify := flag.Bool("mcp-verify", false, "Verify MCP server is running (check tools)")
	agentID := flag.String("agent-id", "eling-mcp", "Agent identifier for continuum (used with --mcp)")
	vaultPath := flag.String("vault", "", "Path to Obsidian vault (used with --mcp)")
	markdownifyMode := flag.Bool("markdownify", false, "Start markdownify HTTP server for document-to-Markdown conversion")
	markdownifyAddr := flag.String("markdownify-addr", ":8080", "Address for markdownify HTTP server")
	showVersion := flag.Bool("version", false, "Print version and exit")
	planMode := flag.Bool("plan", false, "Plan mode: draft a plan and require approval before executing tools")
	noVerify := flag.Bool("no-verify", false, "Disable the verify→repair auto-verification loop (same as `verify.enabled: false`)")
	flag.Parse()

	if *showVersion {
		fmt.Println("eling version", Version)
		os.Exit(0)
	}

	// ── CLI Subcommand Mode ─────────────────────────────────────────────────
	// If the first argument is a CLI subcommand, handle it directly
	// without requiring an API key or full agent initialization.
	if flag.NArg() > 0 {
		cliCmd := flag.Arg(0)
		// Check if this looks like a CLI subcommand (not a prompt)
		cliSubcommands := map[string]bool{
			"remember": true, "recall": true, "probe": true, "reason": true,
			"reflect": true, "snapshot": true, "list-snapshots": true,
			"rollback": true, "link-stats": true, "linked-facts": true,
			"evolve": true, "stats": true, "learnings": true, "config": true, "init-rules": true,
			"rules": true,
			"mcp":   true, "continuum": true, "blackbox": true, "markdownify": true,
			"sync": true, "setup": true, "install-opencode": true, "install-zero": true,
			"install-termux": true, "help": true,
			"export": true, "think": true, "verify": true,
			"search-temporal": true, "version-history": true,
			"versioned-update": true, "undo-version": true,
			"autorepair": true, "tools-health": true, "health": true,
			"permission": true, "permissions": true,
		}
		if cliSubcommands[cliCmd] {
			// Handle CLI commands with minimal setup
			cfgPath := *configPath
			if cfgPath == "" {
				cfgPath = config.FindConfigPath()
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				// Create a default config if none exists
				cfg = config.DefaultConfig()
			}
			// Wire the autorepair opt-in gate from config (Phase 3): autofix
			// stays OFF unless `autorepair.autofix: true`; detection +
			// classification always run regardless.
			autorepair.SetAutofixEnabled(cfg.Autorepair.Autofix)
			autorepair.SetMaxRetries(cfg.Autorepair.MaxRetries)
			autorepair.LoadQuarantineState()
			if cli.RunCLI(cfg, Version, flag.Args()) {
				os.Exit(0)
			}
			// If RunCLI returns false, fall through to normal agent mode
		}
	}

	// ── MCP Server Mode (delegated to mcp-server skill) ────────────────────
	if *mcpMode || *mcpVerify {
		logger.Global().Info("Starting MCP server mode (agent-id: %s)", *agentID)

		// Determine state directory
		stateDir := os.Getenv("ELING_HOME")
		if stateDir == "" {
			home, _ := os.UserHomeDir()
			stateDir = filepath.Join(home, ".eling")
		}
		os.MkdirAll(stateDir, 0755)

		// Register the MCP server as a skill first
		skillCfg := mcpskill.DefaultMCPSkillConfig()
		skillCfg.Name = "eling-brains"
		skillCfg.Version = Version
		skillCfg.StateDir = stateDir
		skillCfg.VaultPath = *vaultPath
		skillCfg.AgentID = *agentID

		mcpskill.RegisterMCPSkill(skillCfg)

		if *mcpVerify {
			// Verification mode: start, check status, then stop
			if err := mcpskill.MCPSkillStart(); err != nil {
				log.Fatalf("Failed to start MCP server: %v", err)
			}
			fmt.Fprintf(os.Stderr, "🧠 ELING MCP Server v%s\n", Version)
			fmt.Fprintf(os.Stderr, "   Agent ID: %s\n", *agentID)
			fmt.Fprintf(os.Stderr, "   State:    %s\n", stateDir)
			fmt.Fprintf(os.Stderr, "   Verifying...\n\n")
			status, _ := mcpskill.MCPSkillStatus()
			fmt.Fprintf(os.Stderr, "   ✅ MCP server is ready\n")
			fmt.Fprintf(os.Stderr, "   %s\n", status)
			mcpskill.MCPSkillStop()
			logger.WriteCleanShutdownMarker()
			return
		}

		// Start the MCP server via the skill
		if err := mcpskill.MCPSkillStart(); err != nil {
			log.Fatalf("MCP server start error: %v", err)
		}
		logger.Global().Info("MCP server skill started successfully")

		// Block until interrupted (the skill runs in background goroutine)
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		logger.Global().Info("Shutting down MCP server...")
		mcpskill.MCPSkillStop()
		logger.WriteCleanShutdownMarker()
		return
	}

	// ── Markdownify Server Mode ──────────────────────────────────────────
	if *markdownifyMode {
		logger.Global().Info("Starting markdownify HTTP server on %s", *markdownifyAddr)
		conv := markdownify.NewConverter()
		fmt.Fprintf(os.Stderr, "📝 ELING Markdownify Server\n")
		fmt.Fprintf(os.Stderr, "   Listening on %s\n", *markdownifyAddr)
		fmt.Fprintf(os.Stderr, "   GET  /convert?url=<url>  — Convert URL to Markdown\n")
		fmt.Fprintf(os.Stderr, "   POST /convert (file=...) — Upload file to convert\n\n")
		if err := conv.ServeHTTP(*markdownifyAddr); err != nil {
			logger.Global().Error("Markdownify server error: %v", err)
			log.Fatalf("Markdownify server error: %v", err)
		}
		logger.WriteCleanShutdownMarker()
		return
	}

	// Initialize crash-safe logger
	_ = logger.Global()
	defer func() {
		if err := logger.Global().Close(); err != nil {
			log.Printf("Warning: failed to close log: %v", err)
		}
	}()

	logger.Global().Info("=== ELING STARTUP ===")
	logger.Global().Info("PID: %d, Args: %v", os.Getpid(), os.Args)

	// Detect previous crash
	if crashed, reason := checkCrashOnStartup(); crashed {
		logger.Global().Warn("Detected previous unclean shutdown: %s", reason)
		fmt.Fprintf(os.Stderr, "\n⚠️  Detected possible crash from previous ELING session!\n")
		fmt.Fprintf(os.Stderr, "   Reason: %s\n", reason)
		fmt.Fprintf(os.Stderr, "   📄 Check ~/.eling/eling.log for details.\n")
		fmt.Fprintf(os.Stderr, "   📄 Check ~/.eling/crash_report.log for crash report.\n")
		if strings.Contains(reason, "BUS") || strings.Contains(reason, "SEGV") || strings.Contains(reason, "signal") {
			fmt.Fprintf(os.Stderr, "   💡 This was a fatal OS signal (bus error / segfault).\n")
			fmt.Fprintf(os.Stderr, "   💡 Check 'dmesg | grep eling' for kernel-level fault info.\n")
		}
		fmt.Fprintf(os.Stderr, "\n")
	}

	// Kill any previous eling from the same terminal
	killPreviousInstance()

	// Write PID file
	writePIDFile()
	// Clean up PID file on exit
	defer removePIDFile()

	cfgPath := *configPath
	if cfgPath == "" {
		cfgPath = config.FindConfigPath()
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Wire the autorepair opt-in gate from config (Phase 3): detection +
	// classification always run; autofix only if the user opted in.
	autorepair.SetAutofixEnabled(cfg.Autorepair.Autofix)
	autorepair.SetMaxRetries(cfg.Autorepair.MaxRetries)
	autorepair.LoadQuarantineState()

	// Wire the session resource budget (opt-in; all knobs default 0 = off).
	// This is the aggregate safety net across the whole process/session.
	sess := budget.New(budget.Config{
		MaxTurns:    cfg.Session.MaxTurns,
		MaxDuration: time.Duration(cfg.Session.MaxDurationSec) * time.Second,
		IdleTimeout: time.Duration(cfg.Session.IdleTimeoutSec) * time.Second,
	})

	if *model != "" {
		cfg.Agent.DefaultModel = *model
	}

	// Save config if new (before injecting API key)
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		saveCfg := *cfg
		providers := make([]config.ProviderConfig, len(cfg.Agent.Providers))
		copy(providers, cfg.Agent.Providers)
		saveCfg.Agent.Providers = providers
		for i := range saveCfg.Agent.Providers {
			saveCfg.Agent.Providers[i].APIKey = ""
		}
		if err := saveCfg.Save(cfgPath); err != nil {
			log.Printf("Warning: could not save default config: %v", err)
		}
	}

	key := *apiKey
	if key == "" {
		key = os.Getenv("DEEPSEEK_API_KEY")
	}
	if key == "" {
		fmt.Println("Error: DeepSeek API key required. Set DEEPSEEK_API_KEY env var or use --api-key")
		exitWithCleanup(nil, 1)
	}
	for i := range cfg.Agent.Providers {
		cfg.Agent.Providers[i].APIKey = key
	}

	// D2 (DeepCode heist): --no-verify opts out of the evidence-driven
	// verify→repair loop (equivalent to `verify.enabled: false` in config).
	// Applied BEFORE agent.New so the verifier is commissioned disabled from
	// birth — the per-turn plan-mode logic can never resurrect it.
	if *noVerify {
		cfg.Verify.Enabled = false
	}

	ag, err := agent.New(cfg)
	if err != nil {
		log.Fatalf("Failed to create agent: %v", err)
	}

	// A10: log the persistent learnings journal at boot so durable lessons
	// from previous sessions are visible (and findable) in every session.
	if n := learnings.Count(); n > 0 {
		log.Printf("📓 %d learning(s) loaded from %s", n, learnings.Path())
	}

	// A5: persist live tool + provider metrics on graceful shutdown so the
	// standalone `eling stats` CLI can display them across processes.
	defer func() {
		if err := ag.SaveStats(); err != nil {
			log.Printf("⚠️  could not persist stats: %v", err)
		}
	}()

	// D6 (DeepCode heist): install the per-tool permission policy from config.
	// A fully-empty `permissions` block is inactive and preserves the
	// historical allow-everything behaviour; once any rule/default/project is
	// configured, unlisted tools resolve to the default mode. The interactive
	// TUI attaches an "ask" gate per submit; in headless / serve / automate
	// runs the nil gate degrades "ask" to "allow" so nothing silently blocks.
	cwd, _ := os.Getwd()
	tools.DefaultRegistry.SetPermissions(permPolicyFromConfig(cfg.Permissions), cwd)

	// Phase 1: configure the bash sandbox from config (default on).
	tools.SetSandbox(tools.SandboxSettings{
		Enabled:    cfg.Sandbox.Enabled,
		Root:       cfg.Sandbox.Root,
		MaxOutput:  cfg.Sandbox.MaxOutput,
		TimeoutSec: cfg.Sandbox.TimeoutSec,
		GuardMode:  cfg.Sandbox.GuardMode,
	})

	// Plan mode: CLI flag overrides config; enables the draft-then-approve
	// gate. The TUI attaches an interactive PlanApprover per submit; the
	// non-interactive CLI auto-approves (nil approver).
	ag.PlanEnabled.Store(*planMode || cfg.Agent.PlanMode)

	// Initialize the 8-layer memory Brain and attach it to the agent.
	// This enables all 15 lifecycle hooks during conversation.
	{
		brain, brainErr := cli.OpenBrain(cfg)
		if brainErr != nil {
			log.Printf("Warning: could not initialize memory layers: %v", brainErr)
		} else {
			ag.SetBrain(brain)
			log.Printf("🧠 8-layer Brain initialized with %d hooks", brain.Hooks.TotalHandlers())
		}
	}

	if err := ag.LoadState(); err != nil {
		log.Printf("No prior state found, starting fresh: %v", err)
	}

	// ── List all saved sessions ──────────────────────────────────────────
	if *listSessions || *showSessions {
		sessions, err := ag.ListSessions()
		if err != nil {
			log.Fatalf("Failed to list sessions: %v", err)
		}
		if len(sessions) == 0 {
			fmt.Println("📂 No saved sessions found.")
			fmt.Println("   Start a conversation and it will be automatically saved.")
			logger.Global().Info("Session list: empty")
			exitWithCleanup(ag, 0)
		}
		fmt.Printf("📂 %d saved session(s):\n\n", len(sessions))
		// Load each session to show details
		for i, name := range sessions {
			s, loadErr := ag.Sessions.Load(name)
			if loadErr != nil {
				fmt.Printf("  %d. %s (unable to load)\n", i+1, name)
				continue
			}
			summary := ""
			if s.Metadata != nil {
				summary = s.Metadata["summary"]
			}
			if summary == "" {
				summary = fmt.Sprintf("%d messages", len(s.Entries))
			}
			timeStr := s.UpdatedAt.Format("Jan 2 15:04")
			fmt.Printf("  %d. %s\n", i+1, name)
			fmt.Printf("      📅 %s | %s\n", timeStr, summary)
		}
		fmt.Println()
		fmt.Println("To resume a session:  eling --resume <name>")
		fmt.Println("To resume last:       eling --last")
		logger.Global().Info("Session list: %d sessions displayed", len(sessions))
		exitWithCleanup(ag, 0)
	}

	// ── Resume most recent session ───────────────────────────────────────
	if *lastSession {
		last, err := ag.GetLastSession()
		if err != nil {
			log.Printf("No previous session to resume: %v", err)
		} else if last == nil {
			log.Print("No previous session to resume: last session is nil")
		} else {
			context, err := ag.ResumeSession(last.Name)
			if err != nil {
				log.Printf("Could not resume last session %q: %v", last.Name, err)
			} else {
				logger.Global().Info("Resumed last session: %s", last.Name)
				log.Printf("✅ Resumed last session %q: %s", last.Name, truncateStr(context, 100))
			}
		}
	}

	// Graceful shutdown: save state on SIGTERM only.
	// SIGINT is handled by the TUI (Bubbletea) for interrupt support.
	// We only handle SIGTERM here for external kill signals.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)
	go func() {
		defer recoverWithStack(ag)
		sig := <-sigCh
		logger.Global().Info("Received signal %v, shutting down gracefully...", sig)
		log.Println("Shutting down, saving state...")
		safeSaveState(ag)
		removePIDFile()
		logger.WriteCleanShutdownMarker()
		logger.Global().Info("=== ELING SHUTDOWN (SIGTERM) ===")
		_ = logger.Global().Close()
		os.Exit(0)
	}()

	// ── Fatal signal handler (SIGBUS only) ───────────────────────────────────
	// NOTE: We do NOT catch SIGSEGV here because the Go runtime uses SIGSEGV
	// internally for memory management (stack growth, etc.). Intercepting it
	// via signal.Notify would prevent the runtime from handling it correctly,
	// causing the program to crash on normal memory operations. Go already
	// converts runtime-detected SIGSEGV (nil pointer dereference, etc.) into
	// panics that are caught by recoverWithStack above.
	// SIGBUS (bus error / memory-mapped I/O failure) is safe to catch — it is
	// not used internally by the Go runtime and indicates genuine hardware or
	// OS-level memory faults that warrant a crash report.
	fatalSigCh := make(chan os.Signal, 1)
	signal.Notify(fatalSigCh, syscall.SIGBUS)
	go func() {
		sig := <-fatalSigCh
		sigName := sig.String()
		sigNum := 0
		sigVal, ok := sig.(syscall.Signal)
		if ok {
			sigNum = int(sigVal)
		} else {
			_, _ = fmt.Sscanf(sigName, "signal %d", &sigNum)
		}
		logger.BusErrorCrashHandler(sigName, sigNum)

		// Reset signal handler to default and re-raise so the OS
		// can produce a core dump and terminate the process.
		signal.Reset(sig)
		if ok {
			_ = syscall.Kill(os.Getpid(), sigVal)
		}
	}()

	// Auto-save timer: persist state every 5 minutes
	if cfg.Session.AutoSave {
		go func() {
			defer recoverWithStack(ag)
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				if err := ag.SaveState(); err != nil {
					log.Printf("Auto-save warning: %v", err)
					logger.Global().Warn("Auto-save failed: %v", err)
				} else {
					logger.Global().Info("Auto-save completed")
				}
			}
		}()
	}

	if *sessionName != "" {
		context, err := ag.ResumeSession(*sessionName)
		if err != nil {
			log.Printf("Could not resume session %s: %v", *sessionName, err)
		} else {
			log.Printf("Resumed session %s: %s", *sessionName, truncateStr(context, 100))
		}
	}

	// ── Set session name ─────────────────────────────────────────────────
	if *sessionLabel != "" {
		if err := ag.SetSessionName(*sessionLabel); err != nil {
			log.Printf("Could not rename session: %v", err)
		} else {
			log.Printf("Session named: %s", *sessionLabel)
		}
	}

	// Non-interactive mode with spinner
	if *nonInteractive {
		if flag.NArg() == 0 {
			fmt.Println("Usage with --run: eling --run \"your prompt\"")
			exitWithCleanup(ag, 1)
		}
		prompt := flag.Arg(0)
		logger.Global().Info("Non-interactive mode: query length=%d", len(prompt))
		s := spinner.New(spinner.CharSets[14], 100*time.Millisecond)
		s.Suffix = " thinking..."
		s.Start()
		defer recoverWithStack(ag)
		askCtx, askCancel, _ := sess.Enforce(context.Background())
		defer askCancel()
		response, err := ag.Ask(askCtx, prompt)
		s.Stop()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			logger.Global().Error("Non-interactive query failed: %v", err)
			exitWithCleanup(ag, 1)
		}
		fmt.Println(response)
		_ = ag.SaveState()
		logger.WriteCleanShutdownMarker()
		logger.Global().Info("Non-interactive query completed")
		return
	}

	// Check for real terminal
	if isTerminal() {
		logger.Global().Info("Starting TUI mode")
		// Load timezone from config
		tzName := cfg.UI.Timezone
		if tzName == "" {
			tzName = "Local"
		}
		loc, err := time.LoadLocation(tzName)
		if err != nil {
			log.Printf("Warning: invalid timezone %q, using Local: %v", tzName, err)
			logger.Global().Warn("Invalid timezone %q, using Local: %v", tzName, err)
			loc = time.Local
		}
		program := tui.NewProgram(ag, loc)
		func() {
			defer recoverWithStack(ag)
			if _, err := program.Run(); err != nil {
				logger.Global().Error("TUI error: %v", err)
				log.Fatalf("TUI error: %v", err)
			}
		}()
	} else {
		// REPL mode for non-TTY environments
		fmt.Println("\nELING - Auto-Learning AI Agent  (type /help for commands)")
		fmt.Println()
		scanner := bufio.NewScanner(os.Stdin)
		// Create a cancellable context for graceful shutdown, layered on the
		// optional session budget deadline (max_duration_sec, 0 = off).
		baseCtx, baseCancel, _ := sess.Enforce(context.Background())
		defer baseCancel()
		replCtx, replCancel := context.WithCancel(baseCtx)
		defer replCancel()
		// Handle signals for REPL mode too
		go func() {
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			<-sigCh
			replCancel()
		}()
		// Read stdin in a background goroutine so SIGINT (Ctrl+C) can exit
		// immediately — scanner.Scan() blocks and can't be interrupted from
		// the signal handler.
		lines := make(chan string, 1)
		go func() {
			for scanner.Scan() {
				lines <- scanner.Text()
			}
			close(lines)
		}()
		// Session idle stopwatch (idle_timeout_sec, 0 = off). A nil channel
		// never fires, so the ticker is inert unless the knob is configured.
		var idleTick <-chan time.Time
		if cfg.Session.IdleTimeoutSec > 0 {
			it := time.NewTicker(5 * time.Second)
			defer it.Stop()
			idleTick = it.C
		}
		for {
			fmt.Print(">>> ")
			select {
			case <-replCtx.Done():
				fmt.Println("\nBye!")
				goto replDone
			case <-idleTick:
				if sess.CheckIdle() != nil {
					if err := ag.SaveState(); err != nil {
						log.Printf("Warning: failed to save state: %v", err)
					}
					fmt.Println("\nsession idle limit reached — state saved")
					goto replDone
				}
			case line, ok := <-lines:
				if !ok {
					goto replDone
				}
				input := strings.TrimSpace(line)
				if input == "" {
					continue
				}
				sess.Activity()

				if strings.HasPrefix(input, "/") {
					parts := strings.Fields(input)
					cmd := strings.ToLower(parts[0])
					switch cmd {
					case "/quit", "/exit":
						fmt.Println("Bye!")
						goto replDone
					case "/help":
						fmt.Println("  /help      - Show this help")
						fmt.Println("  /stats     - Show agent stats")
						fmt.Println("  /tools     - List available tools")
						fmt.Println("  /skills    - List learned skills")
						fmt.Println("  /memory    - Show recent memories")
						fmt.Println("  /recall <q>- Search memories")
						fmt.Println("  /plan      - Toggle plan mode (draft + approve before tools)")
						fmt.Println("  /save      - Save state")
						fmt.Println("  /session   - Show session info")
						fmt.Println("  /providers - List providers")
						fmt.Println("  /quit      - Exit")
						fmt.Println("  /clear     - Clear screen")
					case "/stats":
						for k, v := range ag.GetStats() {
							fmt.Printf("  %s: %v\n", k, v)
						}
					case "/tools":
						for _, t := range ag.ListTools() {
							fmt.Printf("  %s - %s\n", t.Name, agent.TruncateStr(t.Description, 60))
						}
					case "/skills":
						skills := ag.ListSkills()
						if len(skills) == 0 {
							fmt.Println("  no skills registered")
						} else {
							fmt.Println("  Skills:")
							for _, sk := range skills {
								fmt.Printf("  - %s: %s\n", sk.Name, agent.TruncateStr(sk.Description, 60))
							}
						}
					case "/memory":
						items := ag.GetMemory().Recent(5)
						if len(items) == 0 {
							fmt.Println("  no memories")
						} else {
							for _, it := range items {
								fmt.Printf("  [%s] %s\n", it.Category, agent.TruncateStr(it.Content, 60))
							}
						}
					case "/recall":
						q := strings.Join(parts[1:], " ")
						if q == "" {
							fmt.Println("  usage: /recall <query>")
						} else {
							for _, it := range ag.GetMemory().Recall(q) {
								fmt.Printf("  [%s] %s\n", it.Category, agent.TruncateStr(it.Content, 80))
							}
						}
					case "/plan":
						on := strings.ToLower(strings.Join(parts[1:], " "))
						switch on {
						case "on", "1", "yes":
							ag.PlanEnabled.Store(true)
							fmt.Println("  plan mode: ON (drafts a plan for approval each turn; auto-approves in REPL)")
						case "off", "0", "no":
							ag.PlanEnabled.Store(false)
							fmt.Println("  plan mode: OFF")
						default:
							ag.PlanEnabled.Store(!ag.PlanEnabled.Load())
							fmt.Printf("  plan mode: %v\n", map[bool]string{true: "ON", false: "OFF"}[ag.PlanEnabled.Load()])
						}
					case "/save":
						if err := ag.SaveState(); err != nil {
							fmt.Printf("  Error saving: %v\n", err)
						} else {
							fmt.Println("  saved")
						}
					case "/session":
						s := ag.GetSession()
						if s != nil {
							fmt.Printf("  %s | %d messages\n", s.Name, len(s.Entries))
						} else {
							fmt.Println("  no active session")
						}
						if snap := sess.Snapshot(); snap.Armed {
							fmt.Printf("  budget: %d/%d turns, max_dur=%v, idle=%v\n",
								snap.TurnsUsed, snap.MaxTurns, snap.MaxDuration, snap.IdleTimeout)
						} else {
							fmt.Println("  budget: off (max_turns/max_duration_sec/idle_timeout_sec = 0)")
						}
					case "/providers":
						for _, p := range ag.ListProviders() {
							fmt.Printf("  - %s\n", p)
						}
					case "/clear":
						fmt.Print("\033[H\033[2J")
					default:
						fmt.Printf("  Unknown command %s (try /help)\n", cmd)
					}
					continue
				}

				// Session turn budget (max_turns, 0 = off): count this turn
				// and stop gracefully once the cap is reached.
				if sess.BeginTurn() {
					fmt.Printf("\nsession turn limit reached (max %d turns)\n", cfg.Session.MaxTurns)
					goto replDone
				}

				// Show spinner while waiting
				s := spinner.New(spinner.CharSets[14], 100*time.Millisecond)
				s.Suffix = " thinking..."
				s.Start()
				response, askErr := func() (string, error) {
					defer recoverWithStack(ag)
					return ag.Ask(replCtx, input)
				}()
				s.Stop()
				if askErr != nil {
					if replCtx.Err() != nil {
						// Interrupted (SIGINT) mid-turn: exit quietly.
						fmt.Println("\nBye!")
						goto replDone
					}
					fmt.Fprintf(os.Stderr, "Error: %v\n", askErr)
					continue
				}
				fmt.Println()
				fmt.Println(response)
				fmt.Println()
			}
		}
	replDone:
	}

	if err := ag.SaveState(); err != nil {
		log.Printf("Warning: failed to save state: %v", err)
	}
	logger.WriteCleanShutdownMarker()
	logger.Global().Info("=== ELING SHUTDOWN (normal) ===")
}

func isTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func truncateStr(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}

// permPolicyFromConfig translates the YAML `permissions` block into the
// tools.PermPolicy used by the registry at runtime. A fully-empty block is
// inactive (historical allow-everything behaviour, no surprise gates); once
// active, only valid modes are kept in the tool rules.
func permPolicyFromConfig(p config.PermissionsConfig) tools.PermPolicy {
	if !p.Active() {
		return tools.PermPolicy{}
	}
	rules := make(map[string]string, len(p.Rules))
	for _, r := range p.Rules {
		if tools.ValidPermMode(r.Mode) {
			rules[r.Tool] = r.Mode
		}
	}
	return tools.NewPermPolicy(p.Default, rules, p.Projects)
}
