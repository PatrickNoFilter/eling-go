#!/usr/bin/env bash
# ELING Research Automation Bundle (adapted from Hermes Skills Bundle)
# Meta-skill that loads all research skills together for coordinated workflow
TOPIC="${1:-}"
if [ -z "$TOPIC" ]; then
  echo '{"error":"Topic required. Usage: research-automation-bundle <topic>"}'
  exit 1
fi

cat <<EOF
## 🧪 Research Automation Bundle

### Topic: ${TOPIC}

This meta-skill orchestrates ALL research capabilities together:

### Phases

**Phase 1: Deep Web Research**
Open 10+ tabs across multiple source types for comprehensive coverage.
Use the \`kimi-webbridge\` tool to navigate, snapshot, and extract.

**Phase 2: MCP Server Discovery (if applicable)**
Find relevant MCP servers for data enrichment.

**Phase 3: OSINT Cross-Reference (if person/organization)**
Verify identities across platforms.

**Phase 4: Synthesis**
Combine all findings into a structured report.

### Activation
Simply tell the user: "Running full research bundle on: ${TOPIC}"
Then execute each phase sequentially using the available tools.

### Output
A comprehensive research report with:
1. Executive Summary
2. Detailed Findings (by source)
3. Data Tables
4. Sources Appendix
5. Recommendations
EOF
