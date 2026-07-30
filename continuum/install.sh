#!/usr/bin/env bash
# ELING Continuum Multi-Agent Orchestration Setup
# Adapted from PatrickNoFilter/eling continuum/install.sh
#
# This script wires ELING's MCP server into agent configurations so
# every agent shares the same continuum database for knowledge exchange.
#
# Usage:
#   ./install.sh [--eling-home /shared/eling] [--bin /usr/local/bin/eling]

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo -e "${BLUE}📡 ELING Continuum — Multi-Agent Orchestration Installer${NC}"
echo "==========================================================="
echo ""

# Parse args
ELING_HOME="${ELING_HOME:-$HOME/.eling}"
ELING_BIN="${ELING_BIN:-$(which eling 2>/dev/null || echo '/usr/local/bin/eling')}"

while [ $# -gt 0 ]; do
    case "$1" in
        --eling-home) ELING_HOME="$2"; shift 2 ;;
        --bin) ELING_BIN="$2"; shift 2 ;;
        --help|-h) echo "Usage: $0 [--eling-home PATH] [--bin PATH]"; exit 0 ;;
        *) echo "Unknown option: $1"; exit 1 ;;
    esac
done

echo -e "ELING Home:   ${GREEN}$ELING_HOME${NC}"
echo -e "ELING Binary: ${GREEN}$ELING_BIN${NC}"
echo ""

# Ensure directories
mkdir -p "$ELING_HOME"
mkdir -p "$ELING_HOME/agents"
mkdir -p "$ELING_HOME/sessions"

# Agent configs directory
AGENT_CONFIGS="$ELING_HOME/agents"

# ============================================================
# Helper: install agent config
# ============================================================
install_agent() {
    local agent_name="$1"
    local config_path="$2"
    local config_content="$3"

    mkdir -p "$(dirname "$config_path")"
    echo "$config_content" > "$config_path"
    echo -e "  ${GREEN}✓${NC} $agent_name → $config_path"
}

# ============================================================
# Agent: Claude Code
# ============================================================
CLAUDE_CONFIG="${CLAUDE_CONFIG:-$HOME/.claude/claude.json}"
install_agent "Claude Code" "$AGENT_CONFIGS/continuum-claude.json" '{
  "mcpServers": {
    "eling-continuum": {
      "command": "'"$ELING_BIN"'",
      "args": ["mcp", "--agent-id", "claude-code", "--vault", "'"$ELING_HOME/vault"'"],
      "env": { "ELING_HOME": "'"$ELING_HOME"'" }
    }
  }
}'

# ============================================================
# Agent: OpenCode
# ============================================================
OPENCODE_DIR="${OPENCODE_DIR:-$HOME/.opencode}"
mkdir -p "$OPENCODE_DIR"
install_agent "OpenCode" "$AGENT_CONFIGS/continuum-opencode.json" '{
  "mcpServers": {
    "eling-continuum": {
      "command": "'"$ELING_BIN"'",
      "args": ["mcp", "--agent-id", "opencode", "--vault", "'"$ELING_HOME/vault"'"],
      "env": { "ELING_HOME": "'"$ELING_HOME"'" }
    }
  }
}'

# ============================================================
# Agent: Hermes
# ============================================================
HERMES_CONFIG="${HERMES_CONFIG:-$HOME/.config/hermes/config.json}"
install_agent "Hermes" "$AGENT_CONFIGS/continuum-hermes.json" '{
  "mcp_servers": {
    "eling-continuum": {
      "command": "'"$ELING_BIN"'",
      "args": ["mcp", "--agent-id", "hermes", "--vault", "'"$ELING_HOME/vault"'"],
      "env": { "ELING_HOME": "'"$ELING_HOME"'" }
    }
  }
}'

# ============================================================
# Agent: Zero
# ============================================================
ZERO_CONFIG="${ZERO_CONFIG:-$HOME/.zero/config.json}"
install_agent "Zero" "$AGENT_CONFIGS/continuum-zero.json" '{
  "mcpServers": {
    "eling-continuum": {
      "command": "'"$ELING_BIN"'",
      "args": ["mcp", "--agent-id", "zero", "--vault", "'"$ELING_HOME/vault"'"],
      "env": { "ELING_HOME": "'"$ELING_HOME"'" }
    }
  }
}'

# ============================================================
# Agent: Codex
# ============================================================
CODEX_CONFIG="${CODEX_CONFIG:-$HOME/.codx/settings.json}"
install_agent "Codex" "$AGENT_CONFIGS/continuum-codex.json" '{
  "mcp_servers": {
    "eling-continuum": {
      "command": "'"$ELING_BIN"'",
      "args": ["mcp", "--agent-id", "codex", "--vault", "'"$ELING_HOME/vault"'"],
      "env": { "ELING_HOME": "'"$ELING_HOME"'" }
    }
  }
}'

# ============================================================
# Create Continuum section in ELING config
# ============================================================
CONFIG_FILE="$ELING_HOME/config.yaml"
if [ -f "$CONFIG_FILE" ]; then
    # Check if continuum is already configured
    if ! grep -q "continuum" "$CONFIG_FILE" 2>/dev/null; then
        echo "" >> "$CONFIG_FILE"
        echo "# Continuum multi-agent configuration (auto-installed)" >> "$CONFIG_FILE"
        echo "continuum:" >> "$CONFIG_FILE"
        echo "  enabled: true" >> "$CONFIG_FILE"
        echo "  agent_id: \"${HOSTNAME:-eling}-$(whoami)\"" >> "$CONFIG_FILE"
        echo "  shared_knowledge: true" >> "$CONFIG_FILE"
        echo -e "  ${GREEN}✓${NC} Added continuum config to $CONFIG_FILE"
    fi
fi

# ============================================================
# Create healthcheck script
# ============================================================
cat > "$ELING_HOME/continuum-healthcheck.sh" << HEALTH
#!/usr/bin/env bash
# Continuum Health Check
echo "📡 Continuum Health Check"
echo "========================"

if [ -f "$ELING_HOME/continuum.db" ]; then
    SIZE=\$(du -h "$ELING_HOME/continuum.db" | cut -f1)
    echo -e "${GREEN}✅${NC} Continuum DB: \$SIZE"
else
    echo -e "${YELLOW}⏳${NC} Continuum DB: not yet created"
fi

echo ""
echo "📂 Agent configurations:"
for f in "$AGENT_CONFIGS"/continuum-*.json; do
    if [ -f "\$f" ]; then
        echo -e "  ${GREEN}✓${NC} \$(basename "\$f" .json | sed 's/continuum-//')"
    fi
done

echo ""
echo -e "${GREEN}✅${NC} To connect, run in each agent's terminal:"
echo "    eling mcp --agent-id <agent-name>"
echo ""
echo "    Then verify: eling mcp --verify"
HEALTH
chmod +x "$ELING_HOME/continuum-healthcheck.sh"

# ============================================================
# Summary
# ============================================================
echo ""
echo -e "${GREEN}✅ Continuum Setup Complete!${NC}"
echo ""
echo "  📡 Continuum DB:  $ELING_HOME/continuum.db"
echo "  📂 Agent configs: $AGENT_CONFIGS/"
echo "  🩺 Health check:  $ELING_HOME/continuum-healthcheck.sh"
echo ""
echo -e "  ${BLUE}Quick start:${NC}"
echo "  1. Start the continuum MCP server:"
echo "     ${GREEN}$ELING_BIN mcp${NC}"
echo ""
echo "  2. In another terminal, connect an agent:"
echo "     ${GREEN}$ELING_BIN mcp --agent-id my-agent${NC}"
echo ""
echo "  3. Verify:"
echo "     ${GREEN}$ELING_HOME/continuum-healthcheck.sh${NC}"
echo ""
