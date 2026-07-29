#!/usr/bin/env bash
# =============================================================================
# ELING Deep Web Research Skill (adapted from Hermes Skills Bundle)
# =============================================================================
# Performs multi-tab deep research using Kimi WebBridge.
# Opens 10+ tabs across different source types, reads content, synthesizes.
#
# Usage: deep-web-research <topic> [--sources "github,twitter,news,..."]
# =============================================================================

TOPIC="${1:-}"
SOURCES="${2:-github,twitter,google,news,docs}"

if [ -z "$TOPIC" ]; then
  echo '{"error":"Topic is required. Usage: deep-web-research <topic> [--sources ...]"}'
  exit 1
fi

SESSION="deep-research-$(date +%s)"
echo "{\"session\":\"${SESSION}\",\"topic\":\"${TOPIC}\"}"

# Instructions for ELING agent
cat <<EOF

## Deep Web Research Protocol

### Session: ${SESSION}
### Topic: ${TOPIC}

### Methodology

1. **Create tab group**: Navigate to first source with \`--new-tab --group "Research: ${TOPIC}"\`
2. **Open sources in parallel** (same session, newTab:true):
   - Google: "https://google.com/search?q=$(echo ${TOPIC} | sed 's/ /+/g')"
   - GitHub: "https://github.com/search?q=$(echo ${TOPIC} | sed 's/ /+/g')&type=repositories"
   - News/Reddit: relevant sources
   - Documentation: official docs if applicable
3. **Read each tab**: Use \`snapshot\` to get accessibility tree
4. **Extract key info**: Use \`click @eXX\` to expand, \`get-attr\` for links
5. **Synthesize**: Summarize findings from all tabs

### Rules
- ✅ Use \`kimi-webbridge\` tool for all browser interactions
- ✅ One session per research task
- ✅ Use \`--new-tab\` for parallel pages
- ❌ Don't close session unless user asks
- ✅ Reference @e selectors from snapshots for clicking

### Output Format
After research, provide:
1. **Summary**: 3-5 paragraph synthesis
2. **Sources**: List of URLs consulted
3. **Key Findings**: Bullet points of important discoveries
4. **Further Questions**: What remains unclear

EOF

exit 0
