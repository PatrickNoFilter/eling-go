#!/usr/bin/env bash
# ELING MCP Server Research (adapted from Hermes Skills Bundle)
# Finds free MCP servers for any topic
TOPIC="${1:-}"
if [ -z "$TOPIC" ]; then
  echo '{"error":"Topic required. Usage: mcp-server-research <topic>"}'
  exit 1
fi
SESSION="mcp-research-$(date +%s)"
echo "{\"session\":\"${SESSION}\",\"topic\":\"${TOPIC}\"}"
cat <<EOF
## MCP Server Research Protocol

### Topic: ${TOPIC}

### Sources to Check
1. **GitHub**: "https://github.com/search?q=${TOPIC}+mcp+server&type=repositories&s=stars"
2. **GitHub MCP Registry**: "https://github.com/mcp?q=${TOPIC}"
3. **npm/pip** for package-based MCP servers

### What to Extract
- Repository name, stars, last updated
- Description of what the MCP server does
- Installation command
- Configuration (env vars, args)

### Output
For each MCP server found, provide:
1. Name and URL
2. Stars and maintenance status
3. Setup instructions
4. Key capabilities/tools it exposes
EOF
