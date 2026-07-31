# ELING Documentation

Welcome to the ELING documentation. ELING is an auto-learning evolving AI agent built in Go.

## 📚 Documentation Index

### 🚀 Getting Started
- **[README.md](../README.md)** — Main project overview, quick start, features

### 🏗 Architecture
- **[ARCHITECTURE.md](ARCHITECTURE.md)** — Detailed system architecture, pipeline flow, thread safety model
- **[DESIGN.md](../DESIGN.md)** — Original architecture design overview

### 🛠 Reference
- **[TOOLS.md](TOOLS.md)** — Complete reference for all 20+ built-in tools
- **[API.md](API.md)** — Configuration schema, CLI flags, provider API compatibility, state storage

### 👩‍💻 Development
- **[DEVELOPMENT.md](DEVELOPMENT.md)** — Setup, workflow, adding tools, thread safety, debugging
- **[QUICK_WINS.md](QUICK_WINS.md)** — Recent improvements and changelog
- **[🎯 Recommended Consolidations.md](../🎯%20Recommended%20Consolidations.md)** — Duplicate reduction and optimization plan (executed)

### 📋 Sessions & State
- Sessions are saved as JSON in `~/.eling/sessions/`
- Auto-save every 5 minutes
- Graceful shutdown on SIGTERM
- Crash recovery on restart
- `write`/`edit` auto-backups (`*.bak.<timestamp>`, rotation keeps 5)

### 🔌 MCP Integration
- Full Model Context Protocol support
- JSON-RPC 2.0 over stdio
- Dynamic tool discovery from MCP servers

### 📦 Open Code Review
- Integrated with Alibaba Open Code Review
- `ocr_review` — Review git diffs
- `ocr_scan` — Full-file scan
- `ocr_health` — Check status

### 🛠 Tooling Highlights
- **ugrep 7.5.0** search engine (fuzzy, archives, JSON/CSV, `--bool`)
- **Paste-safe TUI** — multi-line pastes held until deliberate `Enter`
- **Scrolling marquee banner** in the TUI header
- **Web timeout prediction** — preflight probe + adaptive max-time per host
- **DeepSeek reasoning_content persistence** for thinking-mode models

---

## Quick Links

| What | Where |
|------|-------|
| Build & Run | `go build -o eling . && ./eling` (or `./rebuild.sh` for atomic replace) |
| Run Tests | `go test ./...` |
| Config File | `~/.eling/config.yaml` |
| Log File | `~/.eling/eling.log` |
| Crash Reports | `~/.eling/crash_report.log` |
| Sessions | `~/.eling/sessions/` |
| Setup Wizard | `./eling setup` (= `./eling-wizard.sh`) |
| Non-Interactive | `./eling --run "your query"` |

---

## Support

- Report issues on GitHub
- Check `~/.eling/eling.log` for diagnostics
- Run `ocr_health` to verify Open Code Review status
