# 🧬 ELING Hermes Skills

This directory contains adapted skills from the [Hermes Skills Bundle](https://github.com/sloemo01/hermes-skills-bundle) — 9 powerful automation skills that give ELING the ability to control your real Chrome browser for autonomous web research, job searching, OSINT investigations, and more.

## 🚀 Quick Start

```bash
# Install everything (Kimi WebBridge + all skills)
./scripts/install-hermes-skills.sh

# Or just register skills (if Kimi already installed)
./scripts/install-hermes-skills.sh --auto
```

## 📋 Skills Overview

| Skill | Type | What it does |
|-------|------|-------------|
| `kimi-webbridge` | 🛠 Tool | Browser control: navigate, click, fill, snapshot your real Chrome |
| `deep-web-research` | 🧠 Skill | Opens 10+ tabs across GitHub, Twitter, Google, news for deep dives |
| `job-search-automation` | 💼 Skill | Scours LinkedIn, Indeed, Glassdoor for matching job postings |
| `linkedin-automation` | 🤝 Skill | People search, connection filters, profile extraction on LinkedIn |
| `mcp-server-research` | 🔌 Skill | Finds free MCP servers for any topic from GitHub & registries |
| `osint-person-search` | 🕵️ Skill | Cross-platform identity verification across 12+ social platforms |
| `interactive-prompt-analyzer` | 🧩 Skill | Turns vague requests into structured options |
| `research-automation-bundle` | 🏗️ Meta | Orchestrates all research skills in coordinated workflow |
| `memory-setup` | ⚙️ Skill | Interactive 5-question onboarding for user preferences |

## 🔧 Architecture

### How It Works

1. **ELING agent** reads skill definitions from `~/.eling/tools.json`
2. Each skill is a bash script that outputs structured **instruction prompts**
3. The agent uses these prompts as guidance plus the `kimi-webbridge` tool
4. `kimi-webbridge` wraps the Kimi WebBridge HTTP API at `http://127.0.0.1:10086`

### File Structure

```
skills/hermes/
├── kimi-webbridge.sh           # Browser control tool (wraps WebBridge API)
├── deep-web-research.sh        # Deep research with 10+ tabs
├── job-search-automation.sh    # Job search across platforms
├── linkedin-automation.sh      # LinkedIn networking automation
├── mcp-server-research.sh      # MCP server discovery
├── osint-person-search.sh      # Person search across platforms
├── interactive-prompt-analyzer.sh  # Vague request handler
├── research-automation-bundle.sh   # Meta-orchestrator
└── memory-setup.sh             # Interactive onboarding
```

## 📖 Usage Examples

Once registered, you can use these skills by asking ELING:

### Deep Web Research
> "Deep research **AI agent frameworks**"
> "Research **renewable energy policy EU** thoroughly"

### Job Search
> "Search jobs **ML Engineer** remote"
> "Find **React developer** jobs in Berlin"

### OSINT Investigation
> "OSINT search **@john_doe**"
> "Investigate **acme-corp** across platforms"

### MCP Servers
> "Find MCP servers for **crypto data**"
> "Research MCP tools for **web scraping**"

### LinkedIn
> "Find LinkedIn profiles for **AI engineers at Google**"
> "Search LinkedIn for **product managers fintech**"

### Prompt Analysis
> "Help me figure out: **I want to build something with AI**"

### Full Research Bundle
> "Full research on **LangChain vs LlamaIndex**"

## 📦 Prerequisites

- **Kimi WebBridge daemon** (browser control) — installed automatically
- **Kimi WebBridge Chrome Extension** — install manually from Chrome Web Store
- **curl** — for API calls to WebBridge daemon

## 🔄 Updating Skills

```bash
# Re-run installer (skips already-registered skills)
./scripts/install-hermes-skills.sh --auto

# Or remove and re-register a specific skill
sed -i '/"kimi-webbridge"/,+3d' ~/.eling/tools.json
./scripts/install-hermes-skills.sh --auto
```

## 📄 License

Adapted from [Hermes Skills Bundle](https://github.com/sloemo01/hermes-skills-bundle) (MIT).
