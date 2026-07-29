#!/usr/bin/env bash
# ELING LinkedIn Automation (adapted from Hermes Skills Bundle)
# People search, connection filters, and pagination for LinkedIn networking
ACTION="${1:-search}"
QUERY="${2:-}"
SESSION="linkedin-$(date +%s)"
echo "{\"session\":\"${SESSION}\",\"action\":\"${ACTION}\",\"query\":\"${QUERY}\"}"
cat <<EOF
## LinkedIn Automation Protocol

### Session: ${SESSION}
### Action: ${ACTION}

### Available Actions:
- **search**: Search people by keyword/company/title
- **network**: Find 2nd degree connections in target company
- **profile**: Extract profile information from a LinkedIn URL
- **filter**: Apply filters (location, company, industry, past-24h)

### Workflow
1. Navigate to LinkedIn search URL
2. Use snapshot to find search results
3. Click @e references to expand profiles
4. Extract: name, headline, location, connections count
5. Paginate through results using find_tab

### Rules
- Use your REAL logged-in browser session (Kimi WebBridge)
- Don't automate actions that violate LinkedIn ToS (mass connection requests)
- Focus on information gathering (OSINT-style)
EOF
