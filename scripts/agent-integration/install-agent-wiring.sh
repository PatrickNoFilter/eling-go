#!/usr/bin/env bash
# ELING MCP Agent Integration Script
# Adapted from PatrickNoFilter/eling continuum/install.sh
#
# Usage:
#   ./install-agent-wiring.sh [--eling-home /path/to/eling-data]
#
# This script configures ELING Go MCP server for various AI agents:
#   - Claude Code (claude.json)
#   - OpenCode (.opencode)
#   - Hermes (.hermes)
#   - Zero (.zero)
#   - Codex (codx.json)
#
# Each agent gets its own isolated context but shares the continuum database
# for multi-agent orchestration.

set -euo pipefail

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}🧠 ELING MCP Agent Integration Installer${NC}"
echo "========================================"
echo ""

# Determine ELING binary path
ELING_BIN="${ELING_BIN:-$(which eling 2>/dev/null || echo '/usr/local/bin/eling')}"
if [ ! -x "$ELING_BIN" ]; then
    echo -e "${YELLOW}⚠️  eling binary not found at $ELING_BIN${NC}"
    echo "   Set ELING_BIN env var to the correct path."
    echo "   Continuing with placeholder..."
fi

# Determine ELING_HOME
ELING_HOME="${ELING_HOME:-$HOME/.eling}"
if [ $# -ge 2 ] && [ "$1" = "--eling-home" ]; then
    ELING_HOME="$2"
fi

echo -e "ELING Binary: ${GREEN}$ELING_BIN${NC}"
echo -e "ELING Home:   ${GREEN}$ELING_HOME${NC}"

# Create ELING_HOME if needed
mkdir -p "$ELING_HOME"

# Create agent configs directory
AGENT_DIR="$ELING_HOME/agents"
mkdir -p "$AGENT_DIR"

echo ""
echo -e "${BLUE}📝 Generating agent configurations...${NC}"

# Determine MCP server command
MCP_CMD="$ELING_BIN mcp"

# 1. Claude Code configuration
cat > "$AGENT_DIR/claude.json" << CLAUDEEOF
{
  "mcpServers": {
    "eling-brains": {
      "command": "$ELING_BIN",
      "args": ["mcp", "--agent-id", "claude-code"],
      "env": {
        "ELING_HOME": "$ELING_HOME"
      }
    }
  }
}
CLAUDEEOF
echo -e "  ${GREEN}✓${NC} Claude Code → $AGENT_DIR/claude.json"

# 2. OpenCode configuration
mkdir -p "$AGENT_DIR/opencode"
cat > "$AGENT_DIR/opencode/mcp.json" << OPENCODEOF
{
  "mcpServers": {
    "eling-brains": {
      "command": "$ELING_BIN",
      "args": ["mcp", "--agent-id", "opencode"],
      "env": {
        "ELING_HOME": "$ELING_HOME"
      }
    }
  }
}
OPENCODEOF
echo -e "  ${GREEN}✓${NC} OpenCode → $AGENT_DIR/opencode/mcp.json"

# 3. Hermes configuration
cat > "$AGENT_DIR/hermes.json" << HERMESEOF
{
  "mcp_servers": {
    "eling-brains": {
      "command": "$ELING_BIN",
      "args": ["mcp", "--agent-id", "hermes"],
      "env": {
        "ELING_HOME": "$ELING_HOME"
      }
    }
  }
}
HERMESEOF
echo -e "  ${GREEN}✓${NC} Hermes → $AGENT_DIR/hermes.json"

# 4. Zero configuration
cat > "$AGENT_DIR/zero.json" << ZEROEOF
{
  "mcpServers": {
    "eling-brains": {
      "command": "$ELING_BIN",
      "args": ["mcp", "--agent-id", "zero"],
      "env": {
        "ELING_HOME": "$ELING_HOME"
      }
    }
  }
}
ZEROEOF
echo -e "  ${GREEN}✓${NC} Zero → $AGENT_DIR/zero.json"

# 5. Codex configuration
cat > "$AGENT_DIR/codex.json" << CODEXEOF
{
  "mcp_servers": {
    "eling-brains": {
      "command": "$ELING_BIN",
      "args": ["mcp", "--agent-id", "codex"],
      "env": {
        "ELING_HOME": "$ELING_HOME"
      }
    }
  }
}
CODEXEOF
echo -e "  ${GREEN}✓${NC} Codex → $AGENT_DIR/codex.json"

# 6. Generic MCP configuration
cat > "$AGENT_DIR/README.md" << READMEEOF
# ELING MCP Agent Integration

## Connecting Agents

### Claude Code
\`\`\`json
// ~/.claude/claude.json — merge with existing mcpServers
$(cat "$AGENT_DIR/claude.json" | python3 -m json.tool 2>/dev/null || cat "$AGENT_DIR/claude.json")
\`\`\`

### OpenCode
\`\`\`json
// ~/.opencode/mcp.json
$(cat "$AGENT_DIR/opencode/mcp.json" | python3 -m json.tool 2>/dev/null || cat "$AGENT_DIR/opencode/mcp.json")
\`\`\`

### Hermes
\`\`\`json
// ~/.config/hermes/config.json — merge hermes.mcp_servers
$(cat "$AGENT_DIR/hermes.json" | python3 -m json.tool 2>/dev/null || cat "$AGENT_DIR/hermes.json")
\`\`\`

### Zero
\`\`\`json
// ~/.zero/config.json — merge with mcpServers
$(cat "$AGENT_DIR/zero.json" | python3 -m json.tool 2>/dev/null || cat "$AGENT_DIR/zero.json")
\`\`\`

### Codex
\`\`\`json
// ~/.codx/settings.json — merge with mcp_servers
$(cat "$AGENT_DIR/codex.json" | python3 -m json.tool 2>/dev/null || cat "$AGENT_DIR/codex.json")
\`\`\`

## Multi-Agent Continuum

All agents share the same continuum database at \`$ELING_HOME/continuum.db\`.
This enables knowledge sharing across agents.

To verify all agents are connected:
\`\`\`bash
$ELING_BIN mcp --verify
\`\`\`
READMEEOF
echo -e "  ${GREEN}✓${NC} Agent README → $AGENT_DIR/README.md"

# Create healthcheck script
cat > "$ELING_HOME/healthcheck.sh" << HEALTHCHK
#!/usr/bin/env bash
# Verify ELING MCP agent wiring
echo "🧠 ELING MCP Health Check"
echo "========================="
echo ""

# Check if MCP server responds
echo "🔌 Testing MCP server..."
echo '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' | timeout 5 $ELING_BIN mcp --once 2>/dev/null | head -c 200
echo ""
echo ""

# Check continuum
if [ -f "$ELING_HOME/continuum.db" ]; then
    echo "🌐 Continuum DB: ${GREEN}found${NC} ($(du -h "$ELING_HOME/continuum.db" | cut -f1))"
else
    echo "🌐 Continuum DB: ${YELLOW}not yet created (will be on first run)${NC}"
fi

echo ""
echo "📂 Agent configs:"
for f in "$AGENT_DIR"/*.json; do
    if [ -f "$f" ]; then
        echo "  ${GREEN}✓${NC} $(basename "$f")"
    fi
done

echo ""
echo -e "${GREEN}✅ Health check complete${NC}"
HEALTHCHK
chmod +x "$ELING_HOME/healthcheck.sh"
echo -e "  ${GREEN}✓${NC} Health check → $ELING_HOME/healthcheck.sh"

echo ""
echo -e "${GREEN}✅ Agent integration setup complete!${NC}"
echo ""
echo -e "  ${BLUE}Next steps:${NC}"
echo -e "  1. Copy the configs to their respective agent directories"
echo -e "     (see $AGENT_DIR/README.md for paths)"
echo -e "  2. Run the MCP server: ${GREEN}$ELING_BIN mcp${NC}"
echo -e "  3. Verify: ${GREEN}$ELING_HOME/healthcheck.sh${NC}"
echo ""
