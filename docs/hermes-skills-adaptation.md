# 🧬 Hermes Skills Bundle → ELING Adaptation

## Overview

This document describes how the [Hermes Skills Bundle](https://github.com/sloemo01/hermes-skills-bundle) (9 automation skills) has been adapted to work with **ELING**, your auto-learning evolving AI agent.

## Architecture Differences

| Aspect | Hermes Agent | ELING |
|--------|-------------|-------|
| **Skills** | Markdown SKILL.md files with YAML frontmatter | Go-code skills + dynamic tool registration |
| **Browser** | Kimi WebBridge daemon (port 10086) | Can be wrapped as a tool |
| **Tools** | Built-in `clarify`, `memory`, `snapshot` etc. | Go-based tool registry with `register_tool` / `register_skill` |
| **Memory** | `~/.hermes/memories/memory.md` | Internal memory + semantic index |
| **Skill Location** | `~/.hermes/skills/hermes-skills-bundle/` | `/root/eling/skills/` |

## Adaptation Strategy

### 1. SKILL.md → ELING Skill Scripts

Each Hermes SKILL.md becomes an ELING-registered skill using `register_skill` or a bash-based tool. The instruction prompts are injected as tool descriptions and executed via bash wrappers.

### 2. Kimi WebBridge → ELING Browser Tool

The Kimi WebBridge daemon HTTP API is wrapped as an ELING tool called `kimi-webbridge`, allowing ELING to:
- Navigate to URLs with your real browser
- Click elements using `@e` references
- Fill forms, take snapshots, extract page content
- Manage tabs and sessions

### 3. Skill Categories Mapped

| Hermes Skill | ELING Adaptation | Status |
|-------------|-----------------|--------|
| `kimi-webbridge` | Tool: `kimi-webbridge` | ✅ |
| `deep-web-research` | Skill + instruction prompt | ✅ |
| `job-search-automation` | Skill + instruction prompt | ✅ |
| `linkedin-automation` | Skill + instruction prompt | ✅ |
| `mcp-server-research` | Skill + instruction prompt | ✅ |
| `osint-person-search` | Skill + instruction prompt | ✅ |
| `interactive-prompt-analyzer` | Skill + instruction prompt | ✅ |
| `research-automation-bundle` | Meta-skill (loads all research) | ✅ |
| `memory-setup` | Interactive onboarding skill | ✅ |

## Quick Start

```bash
# 1. Install Kimi WebBridge daemon (required for browser skills)
cd /root/eling
./eling --run "setup kimi-webbridge"

# 2. Register all Hermes skills
./eling --run "load hermes-skills"

# 3. Start a skill
./eling --run "deep research: AI agent frameworks"
```

## File Structure

```
/root/eling/
├── skills/
│   ├── hermes/                    # Adapted Hermes skills
│   │   ├── kimi-webbridge.sh      # Browser automation wrapper
│   │   ├── deep-web-research.sh   # Deep research skill
│   │   ├── job-search.sh          # Job search automation
│   │   ├── linkedin.sh            # LinkedIn automation
│   │   ├── mcp-research.sh        # MCP server research
│   │   ├── osint-search.sh        # OSINT person search
│   │   ├── prompt-analyzer.sh     # Prompt analysis
│   │   ├── research-bundle.sh     # Meta research bundle
│   │   └── memory-setup.sh        # Memory/onboarding setup
│   └── README.md
├── docs/hermes-skills-adaptation.md
└── scripts/
    └── install-hermes-skills.sh   # One-click installer
```
