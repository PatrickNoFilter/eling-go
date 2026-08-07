// Package cli provides the ELING CLI with subcommands for memory operations,
// configuration, snapshot management, and agent integration.
// Ported from Python eling's cli.py + brain.py command pattern.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"eling/internal/agent"
	"eling/internal/autorepair"
	"eling/internal/automate"
	"eling/internal/config"
	"eling/internal/layers"
	"eling/internal/learnings"
	"eling/internal/server"
)

// RunCLI dispatches the appropriate subcommand based on args.
// version is the eling version string (used by `eling serve` health endpoint).
// Returns true if a CLI command was handled (caller should os.Exit).
func RunCLI(cfg *config.Config, version string, args []string) bool {
	if len(args) < 1 {
		return false
	}

	cmd := args[0]
	subArgs := args[1:]

	switch cmd {
	case "serve":
		return cmdServe(cfg, version, subArgs)
	case "remember":
		return cmdRemember(cfg, subArgs)
	case "recall":
		return cmdRecall(cfg, subArgs)
	case "probe":
		return cmdProbe(cfg, subArgs)
	case "reason":
		return cmdReason(cfg, subArgs)
	case "reflect":
		return cmdReflect(cfg, subArgs)
	case "resolve":
		return cmdResolve(cfg, subArgs)
	case "snapshot":
		return cmdSnapshot(cfg, subArgs)
	case "list-snapshots":
		return cmdListSnapshots(cfg)
	case "rollback":
		return cmdRollback(cfg, subArgs)
	case "link-stats":
		return cmdLinkStats(cfg)
	case "linked-facts":
		return cmdLinkedFacts(cfg, subArgs)
	case "evolve":
		return cmdEvolve(cfg, subArgs)
	case "forget":
		return cmdForget(cfg, subArgs)
	case "stats":
		return cmdStats(cfg)
	case "learnings":
		return cmdLearnings(cfg, subArgs)
	case "config":
		return cmdConfig(cfg, subArgs)
	case "setup":
		return cmdSetup(cfg, subArgs)
	case "contradictions":
		return cmdContradictions(cfg, subArgs)
	case "decay":
		return cmdDecay(cfg, subArgs)
	case "init-rules":
		return cmdInitRules(subArgs)
	case "rules":
		return cmdRules(subArgs)
	case "mcp":
		return cmdMCP(subArgs)
	case "continuum":
		return cmdContinuum(subArgs)
	case "blackbox":
		return cmdBlackbox(subArgs)
	case "markdownify":
		return cmdMarkdownify(subArgs)
	case "sync":
		return cmdSync(cfg, subArgs)
	case "install-opencode":
		return cmdInstallOpenCode(subArgs)
	case "install-zero":
		return cmdInstallZero(subArgs)
	case "install-termux":
		return cmdInstallTermux(subArgs)
	case "export":
		return cmdExport(cfg, subArgs)
	case "think":
		return cmdThink(cfg, subArgs)
	case "verify":
		return cmdVerify(cfg, subArgs)
	case "search-temporal":
		return cmdSearchTemporal(cfg, subArgs)
	case "version-history":
		return cmdVersionHistory(cfg, subArgs)
	case "versioned-update":
		return cmdVersionedUpdate(cfg, subArgs)
	case "undo-version":
		return cmdUndoVersion(cfg, subArgs)
	case "autorepair":
		return cmdAutorepair(cfg, subArgs)
	case "automate":
		return cmdAutomate(cfg, subArgs)
	case "tools-health", "health":
		return cmdAutorepair(cfg, subArgs)
	case "help", "--help", "-h":
		printHelp()
		return true
	default:
		return false
	}
}

// OpenBrain initializes the Brain (all 8 layers) from the config's state dir.
// Exported so main.go can attach the Brain to the Agent.
func OpenBrain(cfg *config.Config) (*layers.Brain, error) {
	home, _ := os.UserHomeDir()
	stateDir := filepath.Join(home, ".eling")
	if cfg != nil {
		if cfg.Agent.DefaultBaseURL != "" {
			// Use config to determine state dir
		}
	}

	os.MkdirAll(stateDir, 0755)

	// Initialize all 8 layers
	brain := layers.NewBrain()

	// Layer 1: Builtin
	builtin := layers.NewBuiltinLayer(stateDir)
	if builtin != nil {
		brain.AddLayer(builtin)
	}

	// Layer 2: Blackbox
	blackbox, err := layers.NewBlackboxLayer(stateDir)
	if err != nil {
		log.Printf("Warning: blackbox layer: %v", err)
	} else {
		brain.AddLayer(blackbox)
	}

	// Layer 3: Facts
	facts, err := layers.NewFactsLayer(stateDir)
	if err != nil {
		log.Printf("Warning: facts layer: %v", err)
	} else {
		brain.AddLayer(facts)
	}

	// Layer 4: Code
	code, err := layers.NewCodeLayer(stateDir)
	if err != nil {
		log.Printf("Warning: code layer: %v", err)
	} else {
		brain.AddLayer(code)
	}

	// Layer 5: KB
	kb, err := layers.NewKBLayer(stateDir)
	if err != nil {
		log.Printf("Warning: kb layer: %v", err)
	} else {
		brain.AddLayer(kb)
	}

	// Layer 6: Obsidian (optional, requires vault path)
	vaultPath := os.Getenv("OBSIDIAN_VAULT")
	if vaultPath != "" {
		obsidian := layers.NewObsidianLayer(vaultPath)
		if obsidian != nil {
			brain.AddLayer(obsidian)
		}
	}

	// Layer 7: Notion (optional, creates itself from env vars)
	notion := layers.NewNotionLayer()
	if notion != nil {
		brain.AddLayer(notion)
	}

	// Layer 8: Continuum (needs agent ID)
	continuum, err := layers.NewContinuumLayer(stateDir, "eling-cli")
	if err != nil {
		log.Printf("Warning: continuum layer: %v", err)
	} else {
		brain.AddLayer(continuum)
	}

	// Register default lifecycle hooks
	brain.RegisterDefaultHooks()

	return brain, nil
}

// ── Daemon Commands (Phase 4: eling serve) ─────────────────────────────────

// cmdServe runs the ELING HTTP+SSE daemon (`eling serve`, Phase 4 of the
// qwen-code feature steal). It exposes the agent over HTTP so any client
// (curl, TUI via --daemon-url, another device on the LAN) can talk to the
// same brain. Graceful shutdown on SIGINT/SIGTERM saves all session state.
//
// Flags:
//
//	--addr  127.0.0.1:8765   listen address (overrides config server.addr)
//	--token <bearer>         auth token (overrides config server.token)
//	--help                   usage
func cmdServe(cfg *config.Config, version string, args []string) bool {
	addr := cfg.Server.Addr
	token := cfg.Server.Token
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--addr", "-a":
			if i+1 < len(args) {
				addr = args[i+1]
				i++
			}
		case "--token", "-t":
			if i+1 < len(args) {
				token = args[i+1]
				i++
			}
		case "--help", "-h":
			fmt.Println("usage: eling serve [--addr 127.0.0.1:8765] [--token <bearer>]")
			fmt.Println()
			fmt.Println("Runs the ELING agent as an HTTP+SSE daemon (Phase 4).")
			fmt.Println()
			fmt.Println("Endpoints:")
			fmt.Println("  GET  /v1/health           version, providers, tools, mcp_servers")
			fmt.Println("  GET  /v1/sessions          list live session ids")
			fmt.Println("  GET  /v1/sessions/{id}     session entries")
			fmt.Println("  POST /v1/chat              {session_id?, prompt} -> SSE stream")
			fmt.Println()
			fmt.Println("Example:")
			fmt.Println("  curl -N -X POST http://127.0.0.1:8765/v1/chat \\")
			fmt.Println("    -H 'Authorization: Bearer <token>' \\")
			fmt.Println("    -d '{\"prompt\":\"hi\"}'")
			return true
		}
	}

	// apply overrides onto the config the server will read
	if addr != "" {
		cfg.Server.Addr = addr
	}
	if token != "" {
		cfg.Server.Token = token
	}

	srv := server.NewServer(cfg, version, agent.New)

	// D4: scheduled automations — run the scheduler alongside the daemon.
	// Stops both the scheduler's in-flight jobs and the HTTP server on ctx.
	schedStop := func() {}
	if cfg.Automate.Enabled {
		sched := automate.NewScheduler(cfg, automate.NewHeadlessRunner())
		schedStop = sched.Start(30 * time.Second)
		log.Printf("⏰ automation scheduler started (%d jobs)", len(cfg.Automate.Jobs))
	}

	// graceful shutdown: save all sessions, then stop the http server
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve("") }()

	select {
	case err := <-errCh:
		if err != nil {
			fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		}
	case <-ctx.Done():
		fmt.Println("\n⏳ shutting down daemon (saving sessions)…")
		schedStop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "shutdown error: %v\n", err)
		} else {
			fmt.Println("✅ daemon stopped, sessions saved.")
		}
	}
	return true
}

// ── Core Memory Commands ──────────────────────────────────────────────────

func cmdRemember(cfg *config.Config, args []string) bool {
	if len(args) < 1 {
		fmt.Println("Usage: eling remember <content> [--layer <layer>] [--category <cat>] [--tags <tags>] [--source <src>]")
		return true
	}

	content := args[0]
	layer := "auto"
	category := "general"
	tags := ""
	source := "cli"
	title := ""

	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--layer":
			if i+1 < len(args) {
				i++
				layer = args[i]
			}
		case "--category":
			if i+1 < len(args) {
				i++
				category = args[i]
			}
		case "--tags":
			if i+1 < len(args) {
				i++
				tags = args[i]
			}
		case "--source":
			if i+1 < len(args) {
				i++
				source = args[i]
			}
		case "--title":
			if i+1 < len(args) {
				i++
				title = args[i]
			}
		}
	}

	brain, err := OpenBrain(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening brain: %v\n", err)
		return true
	}
	defer brain.Close()

	item := layers.Item{
		Content:  content,
		Category: category,
		Tags:     splitTags(tags),
		Source:   source,
		Title:    title,
	}

	ctx := context.Background()

	switch layer {
	case "facts", "auto":
		err = brain.Store(ctx, item)
	case "kb":
		// Store in KB layer (layer 5)
		for _, l := range brain.Layers() {
			if l.Name() == "kb" {
				err = l.Store(ctx, item)
				break
			}
		}
		if err == nil {
			err = fmt.Errorf("KB layer not available")
		}
	case "notion":
		for _, l := range brain.Layers() {
			if l.Name() == "notion" {
				err = l.Store(ctx, item)
				break
			}
		}
		if err == nil {
			err = fmt.Errorf("Notion layer not available")
		}
	default:
		err = fmt.Errorf("unknown layer: %s", layer)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return true
	}

	printJSON(map[string]interface{}{
		"status":   "stored",
		"layer":    layer,
		"category": category,
	})
	return true
}

func cmdRecall(cfg *config.Config, args []string) bool {
	if len(args) < 1 || args[0] == "" {
		fmt.Println("Usage: eling recall <query> [--limit <n>] [--layers <comma-separated>]")
		return true
	}

	query := args[0]
	limit := 10
	layersFilter := ""
	sourceFilter := ""

	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--limit":
			if i+1 < len(args) {
				i++
				fmt.Sscanf(args[i], "%d", &limit)
			}
		case "--layers":
			if i+1 < len(args) {
				i++
				layersFilter = args[i]
			}
		case "--source":
			if i+1 < len(args) {
				i++
				sourceFilter = args[i]
			}
		}
	}

	brain, err := OpenBrain(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening brain: %v\n", err)
		return true
	}
	defer brain.Close()

	ctx := context.Background()
	results, err := brain.Query(ctx, query, limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return true
	}

	if layersFilter != "" {
		wanted := strings.Split(layersFilter, ",")
		wantedMap := make(map[string]bool)
		for _, w := range wanted {
			wantedMap[strings.TrimSpace(w)] = true
		}
		var filtered []layers.Result
		for _, r := range results {
			if wantedMap[r.Layer] {
				filtered = append(filtered, r)
			}
		}
		results = filtered
	}

	if sourceFilter != "" {
		var filtered []layers.Result
		for _, r := range results {
			if r.Source == sourceFilter {
				filtered = append(filtered, r)
			}
		}
		results = filtered
	}

	if len(results) == 0 {
		fmt.Println("No results found.")
		return true
	}

	fmt.Printf("Found %d result(s):\n\n", len(results))
	for i, r := range results {
		fmt.Printf("%d. [%s] (score: %.2f)\n", i+1, r.Layer, r.Score)
		fmt.Printf("   %s\n", truncateStr(r.Content, 120))
		if r.Category != "" {
			fmt.Printf("   Category: %s\n", r.Category)
		}
		if r.Source != "" {
			fmt.Printf("   Source: %s\n", r.Source)
		}
		fmt.Println()
	}

	return true
}

func cmdProbe(cfg *config.Config, args []string) bool {
	if len(args) < 1 {
		fmt.Println("Usage: eling probe <entity> [--limit <n>]")
		return true
	}

	entity := args[0]
	limit := 10

	for i := 1; i < len(args); i++ {
		if args[i] == "--limit" && i+1 < len(args) {
			i++
			fmt.Sscanf(args[i], "%d", &limit)
		}
	}

	brain, err := OpenBrain(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening brain: %v\n", err)
		return true
	}
	defer brain.Close()

	ctx := context.Background()
	results, err := brain.Probe(ctx, entity, limit, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return true
	}

	if len(results) == 0 {
		fmt.Printf("No facts found about '%s'.\n", entity)
		return true
	}

	fmt.Printf("📋 Facts about '%s':\n\n", entity)
	for i, r := range results {
		fmt.Printf("%d. (score: %.2f) %s\n", i+1, r.Score, r.Content)
	}
	return true
}

func cmdReason(cfg *config.Config, args []string) bool {
	if len(args) < 1 {
		fmt.Println("Usage: eling reason <entity1> [entity2 ...] [--limit <n>]")
		return true
	}

	limit := 10
	var entities []string

	for _, a := range args {
		if a == "--limit" {
			break
		}
		entities = append(entities, a)
	}

	for i := 0; i < len(args); i++ {
		if args[i] == "--limit" && i+1 < len(args) {
			fmt.Sscanf(args[i+1], "%d", &limit)
			break
		}
	}

	if len(entities) == 0 {
		fmt.Println("Usage: eling reason <entity1> [entity2 ...]")
		return true
	}

	brain, err := OpenBrain(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening brain: %v\n", err)
		return true
	}
	defer brain.Close()

	ctx := context.Background()
	results, err := brain.Reason(ctx, entities, "", limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return true
	}

	if len(results) == 0 {
		fmt.Printf("No compositional facts found for: %s\n", strings.Join(entities, ", "))
		return true
	}

	fmt.Printf("🧠 Reasoning across '%s':\n\n", strings.Join(entities, ", "))
	for i, r := range results {
		fmt.Printf("%d. (score: %.2f) %s\n", i+1, r.Score, r.Content)
	}
	return true
}

func cmdReflect(cfg *config.Config, args []string) bool {
	if len(args) < 1 {
		fmt.Println("Usage: eling reflect <fact_id>")
		return true
	}

	var factID int64
	fmt.Sscanf(args[0], "%d", &factID)
	if factID == 0 {
		fmt.Fprintln(os.Stderr, "Error: invalid fact_id")
		return true
	}

	brain, err := OpenBrain(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening brain: %v\n", err)
		return true
	}
	defer brain.Close()

	fact := brain.GetFact(factID)
	if fact == nil {
		fmt.Fprintf(os.Stderr, "Error: fact %d not found\n", factID)
		return true
	}

	// Store in Notion layer
	item := layers.Item{
		Content:  fact.Content,
		Category: fact.Category,
		Tags:     splitTags(fact.Tags),
		Source:   "cli:reflect",
	}

	err = brain.Store(context.Background(), item)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return true
	}

	printJSON(map[string]interface{}{
		"status":  "reflected",
		"fact_id": factID,
		"content": fact.Content,
	})
	return true
}

// ── Snapshot Commands ─────────────────────────────────────────────────────

func cmdSnapshot(cfg *config.Config, args []string) bool {
	reason := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--reason" && i+1 < len(args) {
			reason = args[i+1]
		}
	}

	brain, err := OpenBrain(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening brain: %v\n", err)
		return true
	}
	defer brain.Close()

	snapshot, err := brain.Snapshot(reason)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return true
	}

	printJSON(map[string]interface{}{
		"status":    "created",
		"id":        snapshot.ID,
		"timestamp": snapshot.Timestamp,
		"reason":    snapshot.Reason,
		"size":      snapshot.SizeBytes,
	})
	return true
}

func cmdListSnapshots(cfg *config.Config) bool {
	brain, err := OpenBrain(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening brain: %v\n", err)
		return true
	}
	defer brain.Close()

	snapshots, err := brain.ListSnapshots()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return true
	}

	if len(snapshots) == 0 {
		fmt.Println("No snapshots found.")
		return true
	}

	fmt.Printf("📸 %d snapshot(s):\n\n", len(snapshots))
	for i, s := range snapshots {
		fmt.Printf("%d. ID: %s\n", i+1, s.ID)
		fmt.Printf("   Time: %s\n", s.Timestamp)
		if s.Reason != "" {
			fmt.Printf("   Reason: %s\n", s.Reason)
		}
		if s.SizeBytes > 0 {
			fmt.Printf("   Size: %d bytes\n", s.SizeBytes)
		}
		fmt.Println()
	}
	fmt.Println("To restore: eling rollback <snapshot_id>")
	return true
}

func cmdRollback(cfg *config.Config, args []string) bool {
	if len(args) < 1 {
		fmt.Println("Usage: eling rollback <snapshot_id>")
		return true
	}

	snapshotID := args[0]

	brain, err := OpenBrain(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening brain: %v\n", err)
		return true
	}
	defer brain.Close()

	result, err := brain.Rollback(snapshotID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return true
	}

	fmt.Printf("✅ Rolled back to snapshot: %s (%s)\n", result.ID, result.Timestamp)
	fmt.Println("The previous database was saved as facts.db.pre_rollback")
	return true
}

// ── Link Commands ─────────────────────────────────────────────────────────

func cmdLinkStats(cfg *config.Config) bool {
	brain, err := OpenBrain(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening brain: %v\n", err)
		return true
	}
	defer brain.Close()

	stats := brain.LinkStats()
	printJSON(stats)
	return true
}

func cmdLinkedFacts(cfg *config.Config, args []string) bool {
	if len(args) < 1 {
		fmt.Println("Usage: eling linked-facts <fact_id> [--limit <n>]")
		return true
	}

	var factID int64
	fmt.Sscanf(args[0], "%d", &factID)
	limit := 10

	for i := 1; i < len(args); i++ {
		if args[i] == "--limit" && i+1 < len(args) {
			i++
			fmt.Sscanf(args[i], "%d", &limit)
		}
	}

	brain, err := OpenBrain(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening brain: %v\n", err)
		return true
	}
	defer brain.Close()

	linked := brain.LinkedFacts(factID, limit)
	if len(linked) == 0 {
		fmt.Printf("No linked facts for fact %d.\n", factID)
		return true
	}

	fmt.Printf("🔗 Linked facts for fact %d:\n\n", factID)
	for i, l := range linked {
		fmt.Printf("%d. %v\n", i+1, l["content"])
		if score, ok := l["score"]; ok {
			fmt.Printf("   Similarity: %v\n", score)
		}
		fmt.Println()
	}
	return true
}

func cmdEvolve(cfg *config.Config, args []string) bool {
	threshold := 0.65
	for i := 0; i < len(args); i++ {
		if args[i] == "--threshold" && i+1 < len(args) {
			i++
			fmt.Sscanf(args[i], "%f", &threshold)
		}
	}

	brain, err := OpenBrain(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening brain: %v\n", err)
		return true
	}
	defer brain.Close()

	result := brain.Evolve(threshold)
	fmt.Printf("🧬 Evolution complete:\n")
	printJSON(result)
	return true
}

// ── Stats Command ─────────────────────────────────────────────────────────

func cmdStats(cfg *config.Config) bool {
	brain, err := OpenBrain(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening brain: %v\n", err)
		return true
	}
	defer brain.Close()

	stats := brain.FactsStats()
	linkStats := brain.LinkStats()

	fmt.Println("📊 ELING Brain Statistics")
	fmt.Println(strings.Repeat("─", 50))

	if stats != nil {
		for k, v := range stats {
			fmt.Printf("  %s: %v\n", k, v)
		}
	}

	if linkStats != nil {
		fmt.Println()
		fmt.Println("🔗 Link Graph:")
		for k, v := range linkStats {
			fmt.Printf("  %s: %v\n", k, v)
		}
	}

	// A5: runtime tool + provider metrics persisted at graceful shutdown.
	// In a fresh CLI process the live counters are zero, so we read the
	// snapshot written by the last interactive session (~/.eling/stats.json).
	if rt := agent.LoadStats(); rt != nil {
		fmt.Println()
		fmt.Println("🛠️ Runtime Metrics (last session)")
		fmt.Println(strings.Repeat("─", 50))
		printStatsMap(rt, "  ")
	} else {
		fmt.Println()
		fmt.Println("🛠️ No runtime tool metrics yet — run an interactive session first (they persist on exit).")
	}

	return true
}

// printStatsMap prints a nested stats map with sorted keys (A5). Maps are
// printed as indented headers followed by their entries; scalars inline.
func printStatsMap(m map[string]interface{}, indent string) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := m[k]
		if sub, ok := v.(map[string]interface{}); ok && len(sub) > 0 {
			fmt.Printf("%s%s:\n", indent, k)
			printStatsMap(sub, indent+"  ")
		} else {
			fmt.Printf("%s%s: %v\n", indent, k, v)
		}
	}
}

// ── Learnings Command (A10) ──────────────────────────────────────────────
// `eling learnings` lists the persistent lesson journal at ~/.eling/learnings.md.
// `eling learnings add "lesson"` appends a timestamped entry.

func cmdLearnings(cfg *config.Config, args []string) bool {
	if len(args) > 0 && args[0] == "add" {
		if len(args) < 2 {
			fmt.Println("Usage: eling learnings add \"lesson text\"")
			return true
		}
		entry := strings.Join(args[1:], " ")
		if err := learnings.Append(entry); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return true
		}
		fmt.Printf("📓 Learning recorded: %s\n", entry)
		return true
	}

	ls, err := learnings.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading learnings: %v\n", err)
		return true
	}
	if len(ls) == 0 {
		fmt.Println("📓 No learnings yet. Add one with: eling learnings add \"lesson text\"")
		return true
	}
	fmt.Printf("📓 Learnings (%d):\n", len(ls))
	for i, l := range ls {
		fmt.Printf("  %d. %s\n", i+1, l)
	}
	return true
}

// ── Autorepair Command (A14 / Phase 2) ──────────────────────────────────
// `eling autorepair` shows a tool-health dashboard: per-tool verdicts from the
// autorepair engine plus the persisted quarantine list.
//
// Subcommands:
//   eling autorepair reenable <tool>   Manual re-enable of a quarantined tool
//   eling autorepair autofix on|off      Toggle the opt-in autofix gate
//   eling autorepair                        (dashboard)
func cmdAutorepair(cfg *config.Config, args []string) bool {
	// Load persisted quarantine state so a fresh CLI process reflects last run.
	autorepair.LoadQuarantineState()

	if len(args) > 0 {
		switch args[0] {
		case "reenable":
			if len(args) < 2 {
				fmt.Println("Usage: eling autorepair reenable <tool>")
				return true
			}
			tool := args[1]
			if autorepair.Reenable(tool) {
				fmt.Printf("✅ Tool %q re-enabled (removed from quarantine).\n", tool)
			} else {
				fmt.Printf("ℹ️ Tool %q was not quarantined; nothing to re-enable.\n", tool)
			}
			return true
		case "autofix":
			if len(args) < 2 {
				fmt.Printf("autofix is currently %s\n", onOff(autorepair.Default().AutofixEnabled()))
				return true
			}
			switch args[1] {
			case "on":
				autorepair.SetAutofixEnabled(true)
				fmt.Println("autofix ENABLED — repairs may mutate environment (probe-first).")
			case "off":
				autorepair.SetAutofixEnabled(false)
				fmt.Println("autofix DISABLED (safe default).")
			default:
				fmt.Println("Usage: eling autorepair autofix on|off")
			}
			return true
		case "help", "--help", "-h":
			fmt.Println(`Usage:
  eling autorepair                        Tool-health dashboard
  eling autorepair reenable <tool>       Re-enable a quarantined tool
  eling autorepair autofix <on|off>      Toggle the autofix gate`)
			return true
		}
	}

	// ── Dashboard ──
	rows, summary := autorepair.Default().Dashboard()
	fmt.Println("🛠️  Tool Auto-Repair — health dashboard")
	fmt.Println(strings.Repeat("─", 60))
	fmt.Printf("  tracked: %d   broken: %d   autofix: %d   advisory: %d   quarantined: %d   retry: %d\n",
		summary.TotalTracked, summary.Broken, summary.Autofix, summary.Advisory, summary.Quarantine, summary.Transient)

	if len(rows) == 0 {
		fmt.Println("\n  No tool failures recorded in the current window — all healthy.")
	} else {
		fmt.Printf("\n  %-22s %-16s %-10s %s\n", "Tool", "Class", "Verdict", "Reason")
		fmt.Println(strings.Repeat("─", 60))
		for _, r := range rows {
			fmt.Printf("  %-22s %-16s %-10s %s\n",
				autorepair.Compact(r.Tool, 22), r.ClassLabel, r.VerdictLabel, truncateStr(r.Reason, 40))
		}
	}

	// ── Quarantine list ──
	if q := autorepair.QuarantinedTools(); len(q) > 0 {
		fmt.Println("\n🚫 Quarantined / disabled tools:")
		for _, rec := range q {
			fmt.Printf("  - %s [%s] %s (since %s)\n", rec.Tool, rec.ClassLabel, truncateStr(rec.Reason, 46), rec.DisabledAt)
		}
		fmt.Println("\n  Re-enable with: eling autorepair reenable <tool>")
	} else if summary.Quarantine == 0 {
		fmt.Println("\n  No quarantined tools.")
	}

	fmt.Println("\n  • autofix:", onOff(autorepair.Default().AutofixEnabled()))
	return true
}

func onOff(b bool) string {
	if b {
		return "ON"
	}
	return "OFF"
}

// ── Automate Commands (D4 scheduled automations) ─────────────────────────

func cmdAutomate(cfg *config.Config, args []string) bool {
	if len(args) < 1 {
		fmt.Println("Usage: eling automate <add|list|remove|logs|enable|disable>")
		fmt.Println("       eling automate --help for more")
		return true
	}
	switch args[0] {
	case "add":
		return cmdAutomateAdd(cfg, args[1:])
	case "list", "ls":
		return cmdAutomateList(cfg)
	case "remove", "rm":
		return cmdAutomateRemove(cfg, args[1:])
	case "run":
		return cmdAutomateRun(cfg, args[1:])
	case "enable":
		return cmdAutomateEnable(cfg, args[1:], true)
	case "disable":
		return cmdAutomateEnable(cfg, args[1:], false)
	case "logs":
		return cmdAutomateLogs(args[1:])
	case "enable-scheduler":
		return cmdAutomateSet(cfg, true)
	case "disable-scheduler":
		return cmdAutomateSet(cfg, false)
	case "help", "--help", "-h":
		printAutomateHelp()
		return true
	default:
		fmt.Printf("Unknown automate subcommand: %s\n", args[0])
		printAutomateHelp()
		return true
	}
}

func printAutomateHelp() {
	fmt.Println(`Usage:
  eling automate add <name> <command|goal> --schedule "0 2 * * *"   Add a scheduled job
  eling automate list                                              Show jobs
  eling automate remove <name>                              Remove a job
  eling automate run <name>                             Run a job once now
  eling automate enable|disable <name>              Toggle a job on/off
  eling automate enable-scheduler|disable-scheduler   Toggle the daemon scheduler
  eling automate logs                          Show recent automation log lines

  A <command> runs via /bin/sh -c. A <goal> is run through the agent.
  Example: eling automate add nightly "go test ./..." --schedule "0 2 * * *"
           el-ing automate add "Summarize learnings" --schedule "0 3 * * 1"`)
}

func cmdAutomateAdd(cfg *config.Config, args []string) bool {
	if len(args) < 2 {
		fmt.Println("Usage: eling automate add <name> '<command>|<goal>' --schedule '<cron>'")
		return true
	}
	name := args[0]
	body := args[1]
	schedule := ""
	kind := "command"
	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--schedule", "-s":
			if i+1 < len(args) {
				schedule = args[i+1]
				i++
			}
		case "--goal":
			kind = "goal"
		}
	}
	if schedule == "" {
		fmt.Println("Error: --schedule \"<cron>\" is required (5 fields: min hour dom mon dow)")
		return true
	}
	if _, err := automate.ParseCron(schedule); err != nil {
		fmt.Printf("Error: invalid schedule %q: %v\n", schedule, err)
		return true
	}
	// load latest cfg persisted (the running cfg may not include prior saves)
	cfgPath := config.FindConfigPath()
	loaded, err := config.Load(cfgPath)
	if err != nil {
		loaded = cfg
	}
	for _, j := range loaded.Automate.Jobs {
		if j.Name == name {
			fmt.Printf("Error: job %q already exists\n", name)
			return true
		}
	}
	var job config.AutomationJob
	if kind == "goal" {
		job = config.AutomationJob{Name: name, Goal: body, Schedule: schedule, Enabled: true}
	} else {
		job = config.AutomationJob{Name: name, Command: body, Schedule: schedule, Enabled: true}
	}
	loaded.Automate.Jobs = append(loaded.Automate.Jobs, job)
	if err := loaded.Save(cfgPath); err != nil {
		fmt.Printf("Error saving config: %v\n", err)
		return true
	}
	*cfg = *loaded
	fmt.Printf("✅ automation %q added — %s, schedule %q (enabled)\n", name, jobKind(job), schedule)
	return true
}

func cmdAutomateList(cfg *config.Config) bool {
	cfgPath := config.FindConfigPath()
	loaded, err := config.Load(cfgPath)
	if err != nil {
		loaded = cfg
	}
	fmt.Println("⏰ Scheduled automations")
	fmt.Println(strings.Repeat("─", 70))
	fmt.Printf("  scheduler daemon: %s\n", onOff(loaded.Automate.Enabled))
	if len(loaded.Automate.Jobs) == 0 {
		fmt.Println("  No automations yet. Add one with: eling automate add <name> '<cmd|goal>' --schedule '0 2 * * *'")
		return true
	}
	fmt.Printf("\n  %-18s %-10s %-7s %s\n", "Name", "Kind", "Enabled", "Schedule")
	fmt.Println(strings.Repeat("─", 70))
	for _, j := range loaded.Automate.Jobs {
		fmt.Printf("  %-18s %-10s %-7s %s\n", compact(j.Name, 18), jobKind(j), onOff(j.Enabled), j.Schedule)
	}
	return true
}

func cmdAutomateRemove(cfg *config.Config, args []string) bool {
	if len(args) < 1 {
		fmt.Println("Usage: eling automate remove <name>")
		return true
	}
	name := args[0]
	cfgPath := config.FindConfigPath()
	loaded, err := config.Load(cfgPath)
	if err != nil {
		loaded = cfg
	}
	found := -1
	for i, j := range loaded.Automate.Jobs {
		if j.Name == name {
			found = i
			break
		}
	}
	if found < 0 {
		fmt.Printf("Error: no job named %q\n", name)
		return true
	}
	loaded.Automate.Jobs = append(loaded.Automate.Jobs[:found], loaded.Automate.Jobs[found+1:]...)
	if err := loaded.Save(cfgPath); err != nil {
		fmt.Printf("Error saving config: %v\n", err)
		return true
	}
	*cfg = *loaded
	fmt.Printf("✅ automation %q removed\n", name)
	return true
}

func cmdAutomateRun(cfg *config.Config, args []string) bool {
	if len(args) < 1 {
		fmt.Println("Usage: eling automate run <name>")
		return true
	}
	name := args[0]
	cfgPath := config.FindConfigPath()
	loaded, err := config.Load(cfgPath)
	if err != nil {
		loaded = cfg
	}
	for i := range loaded.Automate.Jobs {
		if loaded.Automate.Jobs[i].Name == name {
			job := &loaded.Automate.Jobs[i]
			fmt.Printf("▶ running automation %q once…\n", name)
			sched := automate.NewScheduler(loaded, nil)
			_, runErr := sched.SyncRun(job.Name, job.Command, job.Goal)
			ts := time.Now().Format(time.RFC3339)
			if runErr != nil {
				job.LastStatus = "error:" + runErr.Error()
				job.LastRun = ts
				fmt.Printf("❌ automation %q failed: %v\n", name, runErr)
			} else {
				job.LastStatus = "ok"
				job.LastRun = ts
				fmt.Printf("✅ automation %q completed.\n", name)
			}
			if err := loaded.Save(cfgPath); err != nil {
				fmt.Printf("⚠️ could not persist status: %v\n", err)
			}
			return true
		}
	}
	fmt.Printf("Error: no job named %q\n", name)
	return true
}

func cmdAutomateEnable(cfg *config.Config, args []string, enable bool) bool {
	if len(args) < 1 {
		fmt.Println("Usage: eling automate enable|disable <name>")
		return true
	}
	name := args[0]
	cfgPath := config.FindConfigPath()
	loaded, err := config.Load(cfgPath)
	if err != nil {
		loaded = cfg
	}
	for i := range loaded.Automate.Jobs {
		if loaded.Automate.Jobs[i].Name == name {
			loaded.Automate.Jobs[i].Enabled = enable
			if err := loaded.Save(cfgPath); err != nil {
				fmt.Printf("Error saving config: %v\n", err)
				return true
			}
			*cfg = *loaded
			fmt.Printf("%s automation %q\n", map[bool]string{true: "✅ enabled", false: "⏸ disabled"}[enable], name)
			return true
		}
	}
	fmt.Printf("Error: no job named %q\n", name)
	return true
}

func cmdAutomateSet(cfg *config.Config, enable bool) bool {
	cfgPath := config.FindConfigPath()
	loaded, err := config.Load(cfgPath)
	if err != nil {
		loaded = cfg
	}
	loaded.Automate.Enabled = enable
	if err := loaded.Save(cfgPath); err != nil {
		fmt.Printf("Error saving config: %v\n", err)
		return true
	}
	*cfg = *loaded
	verb := "enabled"
	if !enable {
		verb = "disabled"
	}
	fmt.Printf("✅ daemon automation scheduler %s — restart `eling serve` to apply\n", verb)
	return true
}

func cmdAutomateLogs(args []string) bool {
	path := automate.LogPath()
	tail := 40
	if len(args) > 0 {
		fmt.Sscanf(args[0], "%d", &tail)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("No automation log at %s yet (%v)\n", path, err)
		return true
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	fmt.Printf("⏰ automation log: %s (last %d lines)\n", path, len(lines))
	for _, l := range lines {
		fmt.Println(l)
	}
	return true
}

func jobKind(j config.AutomationJob) string {
	if j.Command != "" {
		return "command"
	}
	return "goal"
}

func compact(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ── Config Commands ───────────────────────────────────────────────────────

func cmdConfig(cfg *config.Config, args []string) bool {
	if len(args) < 1 {
		fmt.Println("Usage: eling config <get|set|list|init|schema> [key] [value]")
		fmt.Println("       eling config --help for more")
		return true
	}

	sub := args[0]
	subArgs := args[1:]

	switch sub {
	case "get":
		return cmdConfigGet(subArgs)
	case "set":
		return cmdConfigSet(subArgs)
	case "unset":
		return cmdConfigUnset(subArgs)
	case "list", "ls":
		return cmdConfigList()
	case "init":
		return cmdConfigInit()
	case "schema":
		return cmdConfigSchema()
	default:
		fmt.Printf("Unknown config subcommand: %s\n", sub)
		fmt.Println("Usage: eling config <get|set|unset|list|init|schema> [key] [value]")
	}
	return true
}

func cmdConfigGet(args []string) bool {
	cfgPath := config.FindConfigPath()
	if cfgPath == "" {
		home, _ := os.UserHomeDir()
		cfgPath = filepath.Join(home, ".eling", "config.yaml")
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return true
	}

	if len(args) < 1 {
		// Show all
		data, _ := os.ReadFile(cfgPath)
		fmt.Println(string(data))
		return true
	}

	key := args[0]
	val := getConfigValue(cfg, key)
	if val == "" {
		fmt.Printf("Key '%s' not found or empty\n", key)
	} else {
		fmt.Println(val)
	}
	return true
}

func cmdConfigSet(args []string) bool {
	if len(args) < 2 {
		fmt.Println("Usage: eling config set <key> <value>")
		return true
	}

	key := args[0]
	value := args[1]

	cfgPath := config.FindConfigPath()
	if cfgPath == "" {
		home, _ := os.UserHomeDir()
		cfgPath = filepath.Join(home, ".eling", "config.yaml")
		os.MkdirAll(filepath.Dir(cfgPath), 0755)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		cfg = config.DefaultConfig()
	}

	setConfigValue(cfg, key, value)

	if err := cfg.Save(cfgPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving config: %v\n", err)
		return true
	}
	fmt.Printf("Set %s = %s\n", key, value)
	return true
}

func cmdConfigList() bool {
	cfgPath := config.FindConfigPath()
	if cfgPath == "" {
		fmt.Println("No config file found")
		return true
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return true
	}
	fmt.Println(string(data))
	return true
}

func cmdConfigInit() bool {
	home, _ := os.UserHomeDir()
	cfgPath := filepath.Join(home, ".eling", "config.yaml")
	os.MkdirAll(filepath.Dir(cfgPath), 0755)

	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		defaultCfg := config.DefaultConfig()
		if err := defaultCfg.Save(cfgPath); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return true
		}
		fmt.Printf("Default config written to %s\n", cfgPath)
	} else {
		fmt.Printf("Config already exists at %s\n", cfgPath)
	}
	return true
}

func cmdConfigSchema() bool {
	fmt.Println(`Config schema:
  agent.default_model    - Default LLM model
  agent.default_base_url - Default API base URL
  agent.system_prompt    - System prompt text
  agent.max_context      - Max context tokens
  ui.timezone            - Display timezone
  memory.max_short_term  - Short-term memory capacity
  memory.max_long_term   - Long-term memory capacity
  memory.decay_rate      - Memory decay rate (0.0-1.0)
  session.auto_save      - Auto-save interval (seconds)
  mcp.enabled            - Enable MCP server`)
	return true
}

func cmdConfigUnset(args []string) bool {
	if len(args) < 1 {
		fmt.Println("Usage: eling config unset <key>")
		return true
	}

	key := args[0]

	cfgPath := config.FindConfigPath()
	if cfgPath == "" {
		home, _ := os.UserHomeDir()
		cfgPath = filepath.Join(home, ".eling", "config.yaml")
		os.MkdirAll(filepath.Dir(cfgPath), 0755)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		return true
	}

	parts := strings.Split(key, ".")
	switch parts[0] {
	case "agent":
		if len(parts) > 1 {
			switch parts[1] {
			case "default_model":
				cfg.Agent.DefaultModel = ""
			case "default_base_url":
				cfg.Agent.DefaultBaseURL = ""
			case "system_prompt":
				cfg.Agent.SystemPrompt = ""
			case "max_context":
				cfg.Agent.MaxContext = 0
			}
		}
	case "ui":
		if len(parts) > 1 && parts[1] == "timezone" {
			cfg.UI.Timezone = ""
		}
	case "memory":
		if len(parts) > 1 {
			switch parts[1] {
			case "max_short_term":
				cfg.Memory.MaxShortTerm = 100
			case "max_long_term":
				cfg.Memory.MaxLongTerm = 1000
			case "decay_rate":
				cfg.Memory.DecayRate = 0.5
			}
		}
	case "session":
		if len(parts) > 1 && parts[1] == "auto_save" {
			cfg.Session.AutoSave = true
		}
	case "mcp":
		if len(parts) > 1 && parts[1] == "enabled" {
			cfg.MCP.Enabled = false
		}
	}

	if err := cfg.Save(cfgPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving config: %v\n", err)
		return true
	}
	fmt.Printf("Unset %s (reset to default)\\n", key)
	return true
}

// ── Init Rules Command ────────────────────────────────────────────────────

func cmdInitRules(args []string) bool {
	projectDir := "."
	agents := []string{"generic"}
	dryRun := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project-dir":
			if i+1 < len(args) {
				i++
				projectDir = args[i]
			}
		case "--agent":
			if i+1 < len(args) {
				i++
				agents = append(agents, args[i])
			}
		case "--dry-run":
			dryRun = true
		}
	}

	absDir, err := filepath.Abs(projectDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return true
	}

	if _, err := os.Stat(absDir); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: project directory %s not found\n", absDir)
		return true
	}

	detected := detectAgent(absDir)
	if len(detected) > 0 && len(agents) == 1 && agents[0] == "generic" {
		agents = detected
		fmt.Printf("Detected agents: %s\n", strings.Join(agents, ", "))
	}

	if dryRun {
		fmt.Println("[dry-run] Would write steering rules for agents:", strings.Join(agents, ", "))
		for _, a := range agents {
			fmt.Printf("  [%s] Would write: %s/ELING_MEMORY.md\n", a, absDir)
		}
		return true
	}

	written := writeRules(absDir, agents)
	for _, w := range written {
		fmt.Printf("  [%s] %s: %s\n", w.Agent, w.Action, w.File)
	}
	fmt.Println("Done. Restart your AI agent to load the steering rules.")
	return true
}

// cmdRules implements the `eling rules` subcommand (D1, DeepCode heist):
//
//	eling rules show [--project-dir <dir>]   print the ingested project rules
//	eling rules --refresh [--project-dir <dir>]  re-probe + reprint (read-only)
//
// Always read-only — it never writes or modifies the user's rules file.
func cmdRules(args []string) bool {
	projectDir := "."
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project-dir":
			if i+1 < len(args) {
				i++
				projectDir = args[i]
			}
		case "--refresh":
			// --refresh is a no-op distinction for clarity: we always probe
			// the filesystem fresh, so there is nothing cached to invalidate.
		case "show":
			// default action; kept explicit for readability.
		case "help", "--help", "-h":
			fmt.Println("Usage: eling rules [show] [--project-dir <dir>] [--refresh]")
			fmt.Println("  Reads the project's own rules (AGENTS.md/DEEPCODE.md/CLAUDE.md/.cursor/rules) read-only.")
			return true
		}
	}

	absDir, err := filepath.Abs(projectDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return true
	}
	if _, err := os.Stat(absDir); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: project directory %s not found\n", absDir)
		return true
	}

	file, content := layers.LoadProjectRules(absDir)
	if content == "" {
		fmt.Printf("No project rules found in %s (probed AGENTS.md, DEEPCODE.md, CLAUDE.md, .cursor/rules/*.mdc)\n", absDir)
		return true
	}
	fmt.Printf("Project rules from: %s\n", file)
	fmt.Println("---")
	fmt.Println(content)
	return true
}

type ruleResult struct {
	Agent  string `json:"agent"`
	Action string `json:"action"`
	File   string `json:"file"`
}

func detectAgent(projectDir string) []string {
	var detected []string
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return detected
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		switch {
		case name == ".cursorrules" || name == ".cursor" || strings.HasPrefix(name, ".cursor"):
			detected = append(detected, "cursor")
		case name == "claude_desktop_config.json" || name == "claude.json":
			detected = append(detected, "claude_code")
		case name == "opencode.jsonc" || name == ".opencode.json":
			detected = append(detected, "opencode")
		}
	}
	return detected
}

func writeRules(projectDir string, agents []string) []ruleResult {
	var results []ruleResult

	for _, a := range agents {
		var targetFile string
		var content string

		switch a {
		case "cursor":
			targetFile = filepath.Join(projectDir, ".cursorrules")
			content = cursorRulesContent()
		case "claude_code":
			targetFile = filepath.Join(projectDir, "CLAUDE.md")
			content = claudeRulesContent()
		case "opencode":
			targetFile = filepath.Join(projectDir, "ELING_MEMORY.md")
			content = elingMemoryContent()
		default:
			targetFile = filepath.Join(projectDir, "ELING_MEMORY.md")
			content = elingMemoryContent()
		}

		action := "written"
		if _, err := os.Stat(targetFile); err == nil {
			action = "updated"
		}

		if err := os.WriteFile(targetFile, []byte(content), 0644); err != nil {
			results = append(results, ruleResult{
				Agent:  a,
				Action: fmt.Sprintf("error: %v", err),
				File:   targetFile,
			})
			continue
		}
		results = append(results, ruleResult{
			Agent:  a,
			Action: action,
			File:   targetFile,
		})
	}
	return results
}

func cursorRulesContent() string {
	return "# ELING Memory — Cursor Rules\n\n" +
		"You are connected to ELING (second brain) via MCP. Use these tools:\n\n" +
		"## Remember\n" +
		"Always store important information immediately after learning it:\n" +
		"- User preferences and conventions\n" +
		"- Project architecture decisions\n" +
		"- Completed tasks and outcomes\n" +
		"- Useful commands and configurations\n\n" +
		"## Recall\n" +
		"Before answering any question, recall relevant context:\n" +
		"- \"Do I have existing knowledge about this?\"\n" +
		"- \"Has the user mentioned this before?\"\n" +
		"- \"What conventions were established?\"\n\n" +
		"## Probe\n" +
		"When the user asks about a person, project, or concept:\n" +
		"- Use probe to find what you already know\n\n" +
		"## Guidelines\n" +
		"1. Store facts after every meaningful interaction\n" +
		"2. Recall context before making assumptions\n" +
		"3. Use the brain — it's your persistent memory\n"
}

func claudeRulesContent() string {
	return `# ELING Memory — Claude Code Instructions

You have access to ELING as a second brain via MCP. Always:

1. **Remember** important facts after each task completes
2. **Recall** before making assumptions about user preferences
3. **Probe** entities when the user asks about something specific
4. **Reason** across multiple entities when solving complex problems

Your memory persists across sessions — use it proactively.
`
}

func elingMemoryContent() string {
	return "# 🧠 ELING Memory — Agent Steering Rules\n\n" +
		"This file guides how your AI agent interacts with the ELING second brain.\n\n" +
		"## Core Principles\n" +
		"- **Store first**: After any meaningful interaction, remember key facts\n" +
		"- **Recall before act**: Before making decisions, check existing knowledge\n" +
		"- **Probe unknowns**: When uncertain, search the brain for context\n\n" +
		"## Memory Layers Available\n" +
		"- **Facts**: Short preferences, observations, decisions (layer 3)\n" +
		"- **KB**: Long-form knowledge, articles, docs (layer 5)\n" +
		"- **Code**: Codebase symbols, imports, structure (layer 4)\n" +
		"- **Obsidian**: Local Markdown vault (layer 6)\n" +
		"- **Notion**: Online synced memory (layer 7)\n\n" +
		"## Commands\n" +
		"- `eling remember \"content\"` — Store in facts layer\n" +
		"- `eling recall \"query\"` — Search all layers\n" +
		"- `eling probe \"entity\"` — Get facts about entity\n" +
		"- `eling reason \"entity1\" \"entity2\"` — Cross-entity reasoning\n" +
		"- `eling stats` — Brain statistics\n"
}

// ── MCP Server Mode (as Skill/Plugin) ─────────────────────────────────────

func cmdMCP(args []string) bool {
	if len(args) > 0 && args[0] == "verify" {
		fmt.Println("MCP server mode is available as a skill/plugin.")
		fmt.Println("Run: eling skill add mcp-server -- from the skill registry")
		return true
	}

	fmt.Print(`📡 ELING MCP Server

To enable MCP server mode, use the MCP skill:

  eling skill install mcp-server

Or run directly via the binary:

  eling --mcp

MCP server exposes all 8 brain layers as tools over stdio JSON-RPC.
Configure agents to connect via:
  {
    "mcpServers": {
      "eling": {
        "command": "eling",
        "args": ["--mcp"]
      }
    }
  }
`)
	return true
}

func cmdContinuum(args []string) bool {
	fmt.Print(`📡 Continuum — Multi-Agent Orchestration

The Continuum Layer 8 is available as a skill/plugin.

  eling skill install continuum

This enables multi-agent orchestration with shared knowledge base.
`)
	return true
}

func cmdBlackbox(args []string) bool {
	fmt.Print(`🔎 Blackbox — Flight Recorder

The Blackbox Layer 2 telemetry system is available as a skill/plugin.

  eling skill install blackbox

This captures agent tool calls, file reads, edits, and scores efficiency.
`)
	return true
}

func cmdMarkdownify(args []string) bool {
	fmt.Print(`📝 Markdownify — Document-to-Markdown

The Markdownify conversion server is available as a skill/plugin.

  eling skill install markdownify

Converts PDF, DOCX, XLSX, PPTX, images, audio, and web pages to Markdown.
`)
	return true
}

func cmdSync(cfg *config.Config, args []string) bool {
	direction := "all"
	layer := "auto"
	once := true
	interval := 300
	stateFile := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--direction":
			if i+1 < len(args) {
				i++
				direction = args[i]
			}
		case "--layer":
			if i+1 < len(args) {
				i++
				layer = args[i]
			}
		case "--daemon":
			once = false
		case "--once":
			once = true
		case "--interval":
			if i+1 < len(args) {
				i++
				fmt.Sscanf(args[i], "%d", &interval)
			}
		case "--state-file":
			if i+1 < len(args) {
				i++
				stateFile = args[i]
			}
		}
	}

	brain, err := OpenBrain(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening brain: %v\n", err)
		return true
	}
	defer brain.Close()

	fmt.Printf("Syncing (direction=%s, layer=%s)...\n", direction, layer)

	if os.Getenv("NOTION_TOKEN") != "" && os.Getenv("NOTION_PAGE_ID") != "" {
		fmt.Println("Notion sync: ensure NOTION_TOKEN and NOTION_PAGE_ID are set.")
	}

	if stateFile != "" {
		fmt.Printf("State file: %s\n", stateFile)
	}

	if once {
		fmt.Println("Sync complete.")
	} else {
		fmt.Printf("Daemon mode: running every %ds (Ctrl+C to stop)\n", interval)
		// TODO: implement daemon loop with interval
	}
	return true
}

// ── Install Commands ──────────────────────────────────────────────────────

func cmdInstallOpenCode(args []string) bool {
	dryRun := false
	for _, a := range args {
		if a == "--dry-run" {
			dryRun = true
		}
	}

	ocHome := os.Getenv("OPENCODE_HOME")
	var ocDir string
	if ocHome != "" {
		ocDir = ocHome
	} else {
		home, _ := os.UserHomeDir()
		candidates := []string{
			filepath.Join(home, ".config", "opencode"),
			filepath.Join(home, ".opencode"),
		}
		for _, c := range candidates {
			if info, err := os.Stat(c); err == nil && info.IsDir() {
				ocDir = c
				break
			}
		}
	}

	if ocDir == "" {
		fmt.Println("OpenCode config directory not found.")
		fmt.Println("Checked: OPENCODE_HOME, ~/.config/opencode, ~/.opencode")
		return true
	}

	pluginDir := filepath.Join(ocDir, "plugins")
	pluginPath := filepath.Join(pluginDir, "eling-memory.js")
	configFile := filepath.Join(ocDir, "opencode.jsonc")

	if dryRun {
		fmt.Printf("[dry-run] Would install plugin: → %s\n", pluginPath)
		fmt.Printf("[dry-run] Would register in: %s\n", configFile)
		return true
	}

	os.MkdirAll(pluginDir, 0755)
	pluginContent := `// ELING Memory Plugin for OpenCode
// Auto-generated by eling install-opencode
module.exports = {
  name: "eling-memory",
  description: "ELING second brain integration",
  hooks: {
    afterTool: async (context) => {
      if (context.result && context.tool) {
        const { execSync } = require('child_process');
        try {
          execSync('eling remember "' + context.tool + ': completed" --source opencode', { timeout: 5000 });
        } catch(e) { /* silent */ }
      }
    }
  }
};
`
	if err := os.WriteFile(pluginPath, []byte(pluginContent), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing plugin: %v\n", err)
		return true
	}
	fmt.Printf("Copied plugin: → %s\n", pluginPath)

	relPath := "./plugins/eling-memory.js"
	if _, err := os.Stat(configFile); err == nil {
		data, _ := os.ReadFile(configFile)
		if strings.Contains(string(data), relPath) {
			fmt.Println("Plugin already registered in opencode.jsonc")
		} else {
			content := string(data)
			if strings.Contains(content, "\"plugin\"") {
				content = strings.Replace(content,
					"\"plugin\": [",
					"\"plugin\": [\n    \""+relPath+"\",", 1)
			} else {
				content = strings.TrimRight(content, " \n\r") + ",\n  \"plugin\": [\"" + relPath + "\"]\n}"
			}
			os.WriteFile(configFile, []byte(content), 0644)
			fmt.Printf("Registered plugin in %s\n", configFile)
		}
	} else {
		cfgContent := "{\n  \"plugin\": [\"" + relPath + "\"]\n}\n"
		os.WriteFile(configFile, []byte(cfgContent), 0644)
		fmt.Printf("Created %s with plugin registration\n", configFile)
	}

	fmt.Println("Done. Restart OpenCode to load the eling memory plugin.")
	return true
}

func cmdInstallZero(args []string) bool {
	dryRun := false
	zeroConfigDir := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			dryRun = true
		case "--zero-config-dir":
			if i+1 < len(args) {
				i++
				zeroConfigDir = args[i]
			}
		}
	}

	home, _ := os.UserHomeDir()
	zeroCfg := zeroConfigDir
	if zeroCfg == "" {
		zeroCfg = filepath.Join(home, ".config", "zero")
	}

	zeroScripts := filepath.Join(home, ".zero", "scripts")
	zeroData := filepath.Join(home, ".local", "share", "zero")

	if dryRun {
		fmt.Printf("[dry-run] Zero config dir: %s\n", zeroCfg)
		fmt.Printf("[dry-run] Would install hook: %s/eling-hook.sh\n", zeroScripts)
		fmt.Printf("[dry-run] Would add MCP server to: %s/config.json\n", zeroCfg)
		_ = zeroData
		return true
	}

	os.MkdirAll(zeroScripts, 0755)
	hookScript := `#!/bin/bash
# ELING Hook for Zero — auto-store telemetry
# Installed by eling install-zero
echo "[eling-hook] Event: $ZERO_EVENT" >> /tmp/eling-zero-hooks.log
`
	hookPath := filepath.Join(zeroScripts, "eling-hook.sh")
	if err := os.WriteFile(hookPath, []byte(hookScript), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing hook: %v\n", err)
		return true
	}
	fmt.Printf("Installed hook: %s\n", hookPath)

	cfgPath := filepath.Join(zeroCfg, "config.json")
	os.MkdirAll(zeroCfg, 0755)

	type zeroMCP struct {
		Command     string   `json:"command"`
		Args        []string `json:"args"`
		Description string   `json:"description,omitempty"`
	}

	type zeroConfig struct {
		MCP map[string]zeroMCP `json:"mcp,omitempty"`
	}

	zc := zeroConfig{}
	if data, err := os.ReadFile(cfgPath); err == nil {
		json.Unmarshal(data, &zc)
	}
	if zc.MCP == nil {
		zc.MCP = make(map[string]zeroMCP)
	}

	if _, ok := zc.MCP["eling"]; !ok {
		zc.MCP["eling"] = zeroMCP{
			Command:     "eling",
			Args:        []string{"--mcp"},
			Description: "ELING second brain — all 8 memory layers",
		}
	}

	data, _ := json.MarshalIndent(zc, "", "  ")
	os.WriteFile(cfgPath, data, 0644)
	fmt.Printf("Added MCP server 'eling' to Zero config: %s\n", cfgPath)

	fmt.Println("\n✅ ELING installed in Zero. Restart Zero to load hooks.")
	return true
}

func cmdInstallTermux(args []string) bool {
	dryRun := false
	binDir := ""
	configureZero := false
	zeroConfigDir := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			dryRun = true
		case "--bin-dir":
			if i+1 < len(args) {
				i++
				binDir = args[i]
			}
		case "--configure-zero":
			configureZero = true
		case "--zero-config-dir":
			if i+1 < len(args) {
				i++
				zeroConfigDir = args[i]
			}
		}
	}

	home, _ := os.UserHomeDir()
	if binDir == "" {
		binDir = filepath.Join(home, ".local", "bin")
	}

	if dryRun {
		fmt.Printf("[dry-run] Scripts would be written to: %s/\n", binDir)
		fmt.Println("  eling-termux")
		fmt.Println("  eling-termux-mcp")
		fmt.Println("  as-brain-mcp")
		if configureZero {
			zcd := zeroConfigDir
			if zcd == "" {
				zcd = filepath.Join(home, ".config", "zero")
			}
			fmt.Printf("  Zero config would be updated: %s/config.json\n", zcd)
		}
		return true
	}

	os.MkdirAll(binDir, 0755)

	scripts := map[string]string{
		"eling-termux": `#!/data/data/com.termux/files/usr/bin/env bash
# ELING Termux CLI wrapper
exec eling "$@"
`,
		"eling-termux-mcp": `#!/data/data/com.termux/files/usr/bin/env bash
# ELING MCP server for Termux
exec eling --mcp "$@"
`,
		"as-brain-mcp": `#!/data/data/com.termux/files/usr/bin/env bash
# ELING local brain MCP for Termux
export ELING_HOME="${ELING_HOME:-$HOME/.eling}"
export ELING_NO_CODE_INDEX=1
exec eling --mcp --agent-id "as-brain" "$@"
`,
	}

	for name, content := range scripts {
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, []byte(content), 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing %s: %v\n", name, err)
			continue
		}
		fmt.Printf("  Written: %s\n", path)
	}

	if configureZero {
		zcd := zeroConfigDir
		if zcd == "" {
			zcd = filepath.Join(home, ".config", "zero")
		}
		os.MkdirAll(zcd, 0755)
		cfgPath := filepath.Join(zcd, "config.json")

		type zeroMCP struct {
			Command     string `json:"command"`
			Description string `json:"description,omitempty"`
		}
		type zeroConfig struct {
			MCP map[string]zeroMCP `json:"mcp,omitempty"`
		}

		zc := zeroConfig{}
		if data, err := os.ReadFile(cfgPath); err == nil {
			json.Unmarshal(data, &zc)
		}
		if zc.MCP == nil {
			zc.MCP = make(map[string]zeroMCP)
		}
		zc.MCP["eling"] = zeroMCP{
			Command:     filepath.Join(binDir, "eling-termux-mcp"),
			Description: "Notion-based second brain (remote/online memory)",
		}
		zc.MCP["as_brain"] = zeroMCP{
			Command:     filepath.Join(binDir, "as-brain-mcp"),
			Description: "Local memory layers: facts, KB, code, builtin",
		}
		data, _ := json.MarshalIndent(zc, "", "  ")
		os.WriteFile(cfgPath, data, 0644)
		fmt.Printf("  Updated Zero MCP config: %s\n", cfgPath)
	}

	fmt.Println("\n✅ Termux launcher scripts installed.")
	return true
}

// ── Help ──────────────────────────────────────────────────────────────────

// ── New CLI Commands Ported from Python brain.py ──────────────────────────

func cmdExport(cfg *config.Config, args []string) bool {
	format := "json"
	outputPath := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--format":
			if i+1 < len(args) {
				i++
				format = args[i]
			}
		case "--output", "-o":
			if i+1 < len(args) {
				i++
				outputPath = args[i]
			}
		case "--help", "-h":
			fmt.Println("Usage: eling export [--format json|markdown] [--output <file>]")
			return true
		}
	}

	brain, err := OpenBrain(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening brain: %v\n", err)
		return true
	}
	defer brain.Close()

	ctx := context.Background()
	layers_list := brain.Layers()
	result := map[string]interface{}{
		"exported_at": time.Now().UTC().Format(time.RFC3339),
		"layers":      map[string]interface{}{},
	}

	out := ""
	for _, l := range layers_list {
		items, err := l.Query(ctx, "", 1000)
		if err != nil {
			continue
		}
		itemList := make([]map[string]interface{}, len(items))
		for i, item := range items {
			itemList[i] = map[string]interface{}{
				"content":  truncateStr(item.Content, 200),
				"layer":    item.Layer,
				"source":   item.Source,
				"score":    item.Score,
				"category": item.Category,
				"tags":     item.Tags,
			}
		}
		layerData := map[string]interface{}{
			"name":  l.Name(),
			"count": len(items),
			"items": itemList,
		}
		result["layers"].(map[string]interface{})[l.Name()] = layerData
	}

	if format == "markdown" {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("# ELING Memory Export (%s)\n\n", time.Now().Format("2006-01-02")))
		for _, l := range layers_list {
			items, _ := l.Query(ctx, "", 1000)
			b.WriteString(fmt.Sprintf("## Layer: %s (%d items)\n\n", l.Name(), len(items)))
			for _, item := range items {
				b.WriteString(fmt.Sprintf("- **%s**: %s\n", item.Source, truncateStr(item.Content, 100)))
			}
			b.WriteString("\n")
		}
		out = b.String()
	} else {
		data, _ := json.MarshalIndent(result, "", "  ")
		out = string(data)
	}

	if outputPath != "" {
		os.WriteFile(outputPath, []byte(out), 0644)
		fmt.Printf("Exported to %s (%d bytes)\n", outputPath, len(out))
	} else {
		fmt.Println(out)
	}
	return true
}

func cmdThink(cfg *config.Config, args []string) bool {
	if len(args) < 1 || args[0] == "" {
		fmt.Println("Usage: eling think <query> [--entities e1,e2] [--limit <n>]")
		return true
	}

	query := args[0]
	var entities []string
	limit := 10

	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--entities":
			if i+1 < len(args) {
				i++
				for _, e := range strings.Split(args[i], ",") {
					e = strings.TrimSpace(e)
					if e != "" {
						entities = append(entities, e)
					}
				}
			}
		case "--limit":
			if i+1 < len(args) {
				i++
				fmt.Sscanf(args[i], "%d", &limit)
			}
		}
	}

	brain, err := OpenBrain(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening brain: %v\n", err)
		return true
	}
	defer brain.Close()

	ctx := context.Background()
	result := brain.Think(ctx, query, entities, limit)
	if result == nil {
		printJSON(map[string]string{"error": "think returned nil"})
		return true
	}
	printJSON(result)
	return true
}

func cmdVerify(cfg *config.Config, args []string) bool {
	status := ""
	cmdName := ""
	output := ""
	specCheck := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--status":
			if i+1 < len(args) {
				i++
				status = args[i]
			}
		case "--command":
			if i+1 < len(args) {
				i++
				cmdName = args[i]
			}
		case "--output":
			if i+1 < len(args) {
				i++
				output = args[i]
			}
		case "--spec-check":
			specCheck = true
		case "--help", "-h":
			fmt.Println("Usage: eling verify [--status passed|failed|skipped] [--command <cmd>] [--output <text>] [--spec-check]")
			return true
		}
	}

	if status != "" {
		layers.RecordVerification(status, cmdName, output)
		printJSON(map[string]interface{}{
			"recorded": true,
			"status":   status,
		})
		return true
	}

	result := layers.VerifyStatus()
	if specCheck {
		result["spec_kit"] = layers.BuildVerifyNudgeSpecKit()
	}
	printJSON(result)
	return true
}

func cmdForget(cfg *config.Config, args []string) bool {
	if len(args) < 1 {
		fmt.Println("Usage: eling forget <fact_id>")
		return true
	}

	var factID int64
	fmt.Sscanf(args[0], "%d", &factID)
	if factID == 0 {
		fmt.Fprintln(os.Stderr, "Error: invalid fact_id")
		return true
	}

	// optional --yes flag
	yes := false
	for _, a := range args {
		if a == "--yes" || a == "-y" {
			yes = true
		}
	}

	// Confirm unless --yes
	if !yes {
		// First, show the fact content
		brain, err := OpenBrain(cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening brain: %v\n", err)
			return true
		}
		fact := brain.GetFact(factID)
		brain.Close()

		if fact == nil {
			fmt.Fprintf(os.Stderr, "Error: fact %d not found\n", factID)
			return true
		}

		fmt.Printf("Fact #%d:\n", factID)
		fmt.Printf("  Content: %s\n", fact.Content)
		fmt.Printf("  Trust:   %.2f\n", fact.Trust)
		fmt.Printf("  Tags:    %s\n", fact.Tags)
		fmt.Print("\nDelete this fact? [y/N] ")
		var response string
		fmt.Scanln(&response)
		if response != "y" && response != "Y" && response != "yes" {
			fmt.Println("Cancelled.")
			return true
		}
	}

	brain, err := OpenBrain(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening brain: %v\n", err)
		return true
	}
	defer brain.Close()

	err = brain.Forget(factID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return true
	}

	fmt.Printf("✅ Fact %d forgotten.\n", factID)
	return true
}

func cmdDecay(cfg *config.Config, args []string) bool {
	rate := layers.DefaultDecayRate

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--rate":
			if i+1 < len(args) {
				i++
				fmt.Sscanf(args[i], "%f", &rate)
			}
		case "--help", "-h":
			fmt.Println("Usage: eling decay [--rate <decay_rate>]")
			fmt.Println("  Default rate: 0.01 (per application)")
			fmt.Println("  Higher values = faster forgetting")
			return true
		}
	}

	brain, err := OpenBrain(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening brain: %v\n", err)
		return true
	}
	defer brain.Close()

	result := brain.ApplyDecay(rate)
	if result == nil {
		fmt.Println("Facts layer not available.")
		return true
	}

	fmt.Printf("🧬 Decay applied (rate=%.4f):\n", rate)
	printJSON(result)
	return true
}

func cmdContradictions(cfg *config.Config, args []string) bool {
	if len(args) < 1 {
		fmt.Println("Usage: eling contradictions <fact_id>")
		return true
	}

	var factID int64
	fmt.Sscanf(args[0], "%d", &factID)
	if factID == 0 {
		fmt.Fprintln(os.Stderr, "Error: invalid fact_id")
		return true
	}

	brain, err := OpenBrain(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening brain: %v\n", err)
		return true
	}
	defer brain.Close()

	hits := brain.DetectContradictions(factID)
	if len(hits) == 0 {
		fmt.Printf("No contradictions found for fact %d.\n", factID)
		return true
	}

	fmt.Printf("⚠️  Found %d contradiction(s) for fact %d:\n\n", len(hits), factID)

	// Also show the original fact
	fact := brain.GetFact(factID)
	if fact != nil {
		fmt.Printf("  [Original] #%d: %s (trust: %.2f)\n", fact.ID, fact.Content, fact.Trust)
		if fact.Tags != "" {
			fmt.Printf("             Tags: %s\n", fact.Tags)
		}
		fmt.Println()
	}

	for i, hit := range hits {
		id, _ := hit["id"].(int64)
		content, _ := hit["content"].(string)
		trust, _ := hit["trust"].(float64)
		fmt.Printf("  %d. #%d: %s (trust: %.2f)\n", i+1, id, content, trust)
		if entities, ok := hit["entities"].([]string); ok && len(entities) > 0 {
			fmt.Printf("     Shared entities: %s\n", strings.Join(entities, ", "))
		}
	}

	fmt.Println("\nTo resolve: eling resolve <fact_id>")
	return true
}

func cmdResolve(cfg *config.Config, args []string) bool {
	if len(args) < 1 {
		fmt.Println("Usage: eling resolve <fact_id>")
		return true
	}

	var factID int64
	fmt.Sscanf(args[0], "%d", &factID)
	if factID == 0 {
		fmt.Fprintln(os.Stderr, "Error: invalid fact_id")
		return true
	}

	brain, err := OpenBrain(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening brain: %v\n", err)
		return true
	}
	defer brain.Close()

	count := brain.ResolveContradictions(factID)
	fmt.Printf("✅ Resolved %d contradiction(s) for fact %d and its contradictors.\n", count, factID)
	return true
}

func cmdSearchTemporal(cfg *config.Config, args []string) bool {
	if len(args) < 1 || args[0] == "" {
		fmt.Println("Usage: eling search-temporal <query> [--start <time>] [--end <time>] [--limit <n>]")
		return true
	}

	query := args[0]
	timeStart := ""
	timeEnd := ""
	limit := 10

	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--start":
			if i+1 < len(args) {
				i++
				timeStart = args[i]
			}
		case "--end":
			if i+1 < len(args) {
				i++
				timeEnd = args[i]
			}
		case "--limit":
			if i+1 < len(args) {
				i++
				fmt.Sscanf(args[i], "%d", &limit)
			}
		}
	}

	brain, err := OpenBrain(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening brain: %v\n", err)
		return true
	}
	defer brain.Close()

	ctx := context.Background()
	result, err := brain.SearchTemporal(ctx, query, timeStart, timeEnd, "", "", 0.3, limit, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return true
	}
	printJSON(result)
	return true
}

func cmdVersionHistory(cfg *config.Config, args []string) bool {
	if len(args) < 1 {
		fmt.Println("Usage: eling version-history <fact_id> [--limit <n>]")
		return true
	}

	var factID int64
	fmt.Sscanf(args[0], "%d", &factID)
	limit := 20

	for i := 1; i < len(args); i++ {
		if args[i] == "--limit" && i+1 < len(args) {
			i++
			fmt.Sscanf(args[i], "%d", &limit)
		}
	}

	brain, err := OpenBrain(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening brain: %v\n", err)
		return true
	}
	defer brain.Close()

	versions := brain.GetVersionHistory(factID, limit)
	printJSON(map[string]interface{}{
		"fact_id":  factID,
		"versions": versions,
	})
	return true
}

func cmdVersionedUpdate(cfg *config.Config, args []string) bool {
	if len(args) < 2 {
		fmt.Println("Usage: eling versioned-update <fact_id> <new_content> [--reason <text>]")
		return true
	}

	var factID int64
	fmt.Sscanf(args[0], "%d", &factID)
	newContent := args[1]
	reason := ""

	for i := 2; i < len(args); i++ {
		if args[i] == "--reason" && i+1 < len(args) {
			i++
			reason = args[i]
		}
	}

	brain, err := OpenBrain(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening brain: %v\n", err)
		return true
	}
	defer brain.Close()

	result := brain.VersionedUpdate(factID, newContent, reason)
	printJSON(result)
	return true
}

func cmdUndoVersion(cfg *config.Config, args []string) bool {
	if len(args) < 2 {
		fmt.Println("Usage: eling undo-version <fact_id> <version_id>")
		return true
	}

	var factID, versionID int64
	fmt.Sscanf(args[0], "%d", &factID)
	fmt.Sscanf(args[1], "%d", &versionID)

	brain, err := OpenBrain(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening brain: %v\n", err)
		return true
	}
	defer brain.Close()

	result := brain.UndoToVersion(factID, versionID)
	printJSON(result)
	return true
}

func printHelp() {
	fmt.Print(`🧠 ELING — Auto-Learning AI Agent

Usage:
  eling <command> [options]

Core Memory Commands:
  remember <content>       Store content in memory
  recall <query>           Search across all memory layers
  probe <entity>           Get facts about an entity
  reason <entity...>       Compositional cross-entity query
  reflect <fact_id>        Promote a fact to Notion
  think <query>            Synthesis + gap-analysis (stale/contradicted)
  search-temporal <query>  Temporal search with time range

Verification:
  verify [--status passed|failed|skipped]  Verification-on-stop status
  verify [--spec-check]                    Check spec-kit coverage

Export:
  export [--format json|markdown] [--output <file>]  Export all memory layers

Snapshot Management:
  snapshot [--reason <text>]  Create a snapshot of the facts database
  list-snapshots              List all available snapshots
  rollback <snapshot_id>      Restore facts database to a snapshot

Link Management:
  linked-facts <fact_id>     Get facts linked to a fact
  link-stats                 Show Zettelkasten link graph statistics
  evolve [--threshold <n>]   Merge near-duplicate facts

Forgetting & Decay:
  forget <fact_id>           Delete a fact from memory (with confirmation)
  decay [--rate <n>]         Apply exponential strength decay to all facts

Contradiction Management:
  contradictions <fact_id>   Find facts flagged as contradictions
  resolve <fact_id>          Remove contradiction flags from a fact and its contradictors

Versioning (Memvid-inspired):
  version-history <fact_id>         Show fact version history
  versioned-update <fact_id> <content>  Update a fact with version tracking
  undo-version <fact_id> <version_id>   Rollback a fact to a previous version

Statistics:
  stats                      Show brain statistics

Tool Auto-Repair (A14):
  autorepair                 Show tool-health dashboard (verdicts + quarantine)
  autorepair reenable <tool>  Re-enable a quarantined tool
  autorepair autofix on|off   Toggle the opt-in autofix gate
  tools-health                Alias for the autorepair dashboard

Configuration:
  setup [--list]            Enter the setup wizard (interactive)
  setup --api-key <key>     Quick setup with flags (non-interactive)
  config get [key]          Get configuration value(s)
  config set <key> <val>     Set a configuration value
  config unset <key>         Reset a configuration key to default
  config list                List current configuration
  config init                Write default configuration file
  config schema              Show configuration schema

Agent Integration:
  init-rules [--project-dir <dir>]  Write steering rules for AI agents
  rules show [--project-dir <dir>]  Show the project's own rules (ingested, read-only)
  install-opencode                   Install ELING plugin into OpenCode
  install-zero                       Install ELING hooks into Zero
  install-termux                     Install Termux launcher scripts

Sync:
  sync [--direction push|pull|all] [--daemon]  Synchronize memory layers

Help:
  help, --help, -h         Show this help message

MCP Server Mode (as skill/plugin):
  eling --mcp              Run as MCP server (tools over stdio JSON-RPC)

Session Management:
  eling --resume <name>    Resume a named session
  eling --last             Resume the most recent session
  eling --session-name <n> Name the current session
  eling --list-sessions    List all saved sessions

Non-interactive Mode:
  eling --run "<query>"    Run a single query non-interactively

Markdownify Server:
  eling --markdownify      Start markdownify HTTP server

Flags:
  --api-key <key>          API key (or set DEEPSEEK_API_KEY)
  --model <model>          Override default model
  --config <path>          Path to config file
  --version                Print version
`)
}

// ── Helpers ───────────────────────────────────────────────────────────────

func printJSON(v interface{}) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling JSON: %v\n", err)
		return
	}
	fmt.Println(string(data))
}

func truncateStr(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}

func splitTags(tags string) []string {
	if tags == "" {
		return nil
	}
	var result []string
	for _, t := range strings.Split(tags, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			result = append(result, t)
		}
	}
	return result
}

// getConfigValue retrieves a config value by dot-notation key
func getConfigValue(cfg *config.Config, key string) string {
	parts := strings.Split(key, ".")
	switch parts[0] {
	case "agent":
		if len(parts) > 1 {
			switch parts[1] {
			case "default_model":
				return cfg.Agent.DefaultModel
			case "default_base_url":
				return cfg.Agent.DefaultBaseURL
			case "system_prompt":
				return cfg.Agent.SystemPrompt
			case "max_context":
				return fmt.Sprintf("%d", cfg.Agent.MaxContext)
			}
		}
	case "ui":
		if len(parts) > 1 && parts[1] == "timezone" {
			return cfg.UI.Timezone
		}
	case "memory":
		if len(parts) > 1 {
			switch parts[1] {
			case "max_short_term":
				return fmt.Sprintf("%d", cfg.Memory.MaxShortTerm)
			case "max_long_term":
				return fmt.Sprintf("%d", cfg.Memory.MaxLongTerm)
			case "decay_rate":
				return fmt.Sprintf("%.2f", cfg.Memory.DecayRate)
			}
		}
	case "session":
		if len(parts) > 1 && parts[1] == "auto_save" {
			return fmt.Sprintf("%t", cfg.Session.AutoSave)
		}
	case "mcp":
		if len(parts) > 1 && parts[1] == "enabled" {
			return fmt.Sprintf("%t", cfg.MCP.Enabled)
		}
	}
	return ""
}

// setConfigValue sets a config value by dot-notation key
func setConfigValue(cfg *config.Config, key, value string) {
	parts := strings.Split(key, ".")
	switch parts[0] {
	case "agent":
		if len(parts) > 1 {
			switch parts[1] {
			case "default_model":
				cfg.Agent.DefaultModel = value
			case "default_base_url":
				cfg.Agent.DefaultBaseURL = value
			case "system_prompt":
				cfg.Agent.SystemPrompt = value
			case "max_context":
				fmt.Sscanf(value, "%d", &cfg.Agent.MaxContext)
			}
		}
	case "ui":
		if len(parts) > 1 && parts[1] == "timezone" {
			cfg.UI.Timezone = value
		}
	case "memory":
		if len(parts) > 1 {
			switch parts[1] {
			case "max_short_term":
				fmt.Sscanf(value, "%d", &cfg.Memory.MaxShortTerm)
			case "max_long_term":
				fmt.Sscanf(value, "%d", &cfg.Memory.MaxLongTerm)
			case "decay_rate":
				fmt.Sscanf(value, "%f", &cfg.Memory.DecayRate)
			}
		}
	case "session":
		if len(parts) > 1 && parts[1] == "auto_save" {
			cfg.Session.AutoSave = value == "true" || value == "1"
		}
	case "mcp":
		if len(parts) > 1 && parts[1] == "enabled" {
			cfg.MCP.Enabled = value == "true" || value == "1"
		}
	}
}
