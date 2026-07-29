# ELING Documentation

Welcome to the ELING documentation. ELING is an auto-learning evolving AI agent built in Go.

## 📚 Documentation Index

### 🚀 Getting Started
- **[README.md](../README.md)** — Main project overview, quick start, features

### 🏗 Architecture
- **[ARCHITECTURE.md](ARCHITECTURE.md)** — Detailed system architecture, pipeline flow, thread safety model
- **[DESIGN.md](../DESIGN.md)** — Original architecture design overview

### 🛠 Reference
- **[TOOLS.md](TOOLS.md)** — Complete reference for all 22+ built-in tools
- **[API.md](API.md)** — Configuration schema, CLI flags, provider API compatibility, state storage

### 👩‍💻 Development
- **[DEVELOPMENT.md](DEVELOPMENT.md)** — Setup, workflow, adding tools, thread safety, debugging
- **[QUICK_WINS.md](QUICK_WINS.md)** — Recent improvements and changelog

### 📋 Sessions & State
- Sessions are saved as JSON in `~/.eling/sessions/`
- Auto-save every 5 minutes
- Graceful shutdown on SIGTERM
- Crash recovery on restart

### 🔌 MCP Integration
- Full Model Context Protocol support
- JSON-RPC 2.0 over stdio
- Dynamic tool discovery from MCP servers

### 📦 Open Code Review
- Integrated with Alibaba Open Code Review
- `ocr_review` — Review git diffs
- `ocr_scan` — Full-file scan
- `ocr_health` — Check status

---

## Quick Links

| What | Where |
|------|-------|
| Build & Run | `go build -o eling . && ./eling` |
| Run Tests | `go test ./...` |
| Config File | `~/.eling/config.yaml` |
| Log File | `~/.eling/eling.log` |
| Crash Reports | `~/.eling/crash_report.log` |
| Sessions | `~/.eling/sessions/` |
| Setup Wizard | `./eling-wizard.sh` |
| Non-Interactive | `./eling --run "your query"` |

---

## Support

- Report issues on GitHub
- Check `~/.eling/eling.log` for diagnostics
- Run `ocr_health` to verify Open Code Review status
