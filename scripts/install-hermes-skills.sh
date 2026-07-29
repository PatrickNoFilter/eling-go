#!/usr/bin/env bash
# =============================================================================
# ELING → Hermes Skills Bundle Installer
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ELING_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
ELING_BIN="${ELING_BIN:-${ELING_ROOT}/eling}"
SKILLS_DIR="${ELING_ROOT}/skills/hermes"
STATE_DIR="${HOME}/.eling"
TOOLS_FILE="${STATE_DIR}/tools.json"

BOLD='\033[1m'; GREEN='\033[0;32m'; BLUE='\033[0;34m'
YELLOW='\033[1;33m'; RED='\033[0;31m'; CYAN='\033[0;36m'; NC='\033[0m'
log()  { echo -e "${GREEN}[✓]${NC} $*"; }
info() { echo -e "${BLUE}[i]${NC} $*"; }
warn() { echo -e "${YELLOW}[!]${NC} $*"; }
err()  { echo -e "${RED}[✗]${NC} $*"; }
header() { echo -e "${CYAN}${BOLD}$*${NC}"; }

# Skill definitions: name|description|script|category
SKILLS=(
  "kimi-webbridge|Browser control your real Chrome via Kimi WebBridge daemon: navigate, click, fill, snapshot|kimi-webbridge.sh|tool"
  "deep-web-research|Deep multi-tab research across GitHub, Twitter, Google, news, docs for comprehensive investigation|deep-web-research.sh|skill"
  "job-search-automation|Scours LinkedIn, Indeed, Glassdoor for job postings matching criteria|job-search-automation.sh|skill"
  "linkedin-automation|LinkedIn people search, connection filtering, and profile extraction|linkedin-automation.sh|skill"
  "mcp-server-research|Finds free MCP servers for any topic from GitHub, MCP registry, npm/pip|mcp-server-research.sh|skill"
  "osint-person-search|Cross-platform person identification across 12+ social and public platforms|osint-person-search.sh|skill"
  "interactive-prompt-analyzer|Deconstructs vague requests into structured actionable options with escape hatches|interactive-prompt-analyzer.sh|skill"
  "research-automation-bundle|Meta-skill orchestrating deep-web-research, mcp-server-research, osint-person-search together|research-automation-bundle.sh|skill"
  "memory-setup|Interactive 5-question onboarding to configure user preferences and save to memory|memory-setup.sh|skill"
)

check_prereqs() {
  local m=0
  command -v curl &>/dev/null || { err "curl required"; m=1; }
  if [ ! -f "$ELING_BIN" ] && [ ! -x "$ELING_BIN" ]; then
    command -v eling &>/dev/null && ELING_BIN=$(command -v eling) || { err "eling binary not found"; m=1; }
  fi
  [ ! -d "$SKILLS_DIR" ] && { err "Skills dir not found: ${SKILLS_DIR}"; m=1; }
  [ $m -ne 0 ] && exit 1
  true
}

install_kimi() {
  header "━━━ Kimi WebBridge Setup ━━━"
  local kp=""
  for p in "${HOME}/.kimi-webbridge/bin/kimi-webbridge" "/usr/local/bin/kimi-webbridge"; do
    [ -x "$p" ] && kp="$p" && break
  done
  command -v kimi-webbridge &>/dev/null && kp=$(command -v kimi-webbridge)

  if [ -n "$kp" ]; then log "Kimi WebBridge found at ${kp}"
  else
    warn "Installing Kimi WebBridge..."
    curl -fsSL https://cdn.kimi.com/webbridge/install.sh | bash || true
    for p in "${HOME}/.kimi-webbridge/bin/kimi-webbridge" "/usr/local/bin/kimi-webbridge"; do
      [ -x "$p" ] && kp="$p" && break
    done
  fi

  if [ -n "$kp" ]; then
    info "Starting daemon..."
    "$kp" start 2>/dev/null || true
    sleep 2
    curl -sf http://127.0.0.1:10086/status >/dev/null 2>&1 && log "Daemon running" || warn "Daemon may need a moment"
  fi

  echo -e "${YELLOW}⚠ Install Chrome extension:${NC}"
  echo "  https://chromewebstore.google.com/detail/kimi-webbridge/fldmhceldgbpfpkbgopacenieobmligc"
  echo "  Then sign in and connect"
  if [ "${AUTO:-0}" != "1" ]; then
    read -p "Press Enter after installing extension... " -r </dev/tty 2>/dev/null || true
  fi
}

register_all() {
  header "━━━ Registering Hermes Skills ━━━"
  mkdir -p "$STATE_DIR"

  local reg=0 skip=0
  for entry in "${SKILLS[@]}"; do
    IFS='|' read -r name desc script category <<< "$entry"
    local spath="${SKILLS_DIR}/${script}"
    [ ! -f "$spath" ] && { warn "Missing ${script}, skip ${name}"; continue; }

    # Check if already in tools.json
    if [ -f "$TOOLS_FILE" ] && grep -q "\"name\": \"${name}\"" "$TOOLS_FILE" 2>/dev/null; then
      log "Already exists: ${name}"; skip=$((skip+1)); continue
    fi

    local cmd="bash ${spath} \$@"
    local jq_filter='. + [{"name":$n,"description":$d,"category":$c,"command":$cmd}]'

    if [ ! -f "$TOOLS_FILE" ] || [ ! -s "$TOOLS_FILE" ]; then
      echo '[]' > "$TOOLS_FILE"
    fi

    python3 -c "
import json
with open('${TOOLS_FILE}') as f: data = json.load(f)
data.append({'name':'${name}','description':'${desc}','category':'${category}','command':'bash ${spath} \"\$@\"'})
with open('${TOOLS_FILE}','w') as f: json.dump(data, f, indent=2)
" 2>/dev/null || {
      # Fallback: simple append
      local tmp
      tmp=$(python3 -c "
import json
with open('${TOOLS_FILE}') as f: data = json.load(f)
data.append({'name':'${name}','description':'${desc//\'/\\'}','category':'${category}','command':'bash ${spath} \"\$@\"'})
print(json.dumps(data, indent=2))
")
      echo "$tmp" > "$TOOLS_FILE"
    }

    log "Registered ${category}: ${name}"
    reg=$((reg+1))
  done

  echo ""
  header "━━━ Summary ━━━"
  echo -e "  ${GREEN}${reg} new${NC} / ${YELLOW}${skip} existing${NC}"
  echo ""
  [ $reg -gt 0 ] && {
    info "Skills registered! Restart ELING or just start using:"
    info "  ${ELING_BIN} --run \"deep research: AI agents\""
    info "  ${ELING_BIN} --run \"search jobs: ML Engineer\""
  }
}

list_skills() {
  header "━━━ Registered Hermes Skills ━━━"
  if [ ! -f "$TOOLS_FILE" ]; then info "No tools registered"; return; fi

  # Build grep pattern
  local pattern=""
  for entry in "${SKILLS[@]}"; do
    IFS='|' read -r name _ _ _ <<< "$entry"
    [ -n "$pattern" ] && pattern="${pattern}\|"
    pattern="${pattern}\"${name}\""
  done

  grep -E "$pattern" "$TOOLS_FILE" 2>/dev/null | while IFS= read -r line; do
    # Extract name and description
    local n=$(echo "$line" | grep -o '"name": *"[^"]*"' | head -1 | sed 's/"name": *"//;s/"$//')
    local d=$(echo "$line" | grep -o '"description": *"[^"]*"' | head -1 | sed 's/"description": *"//;s/"$//')
    local c=$(echo "$line" | grep -o '"category": *"[^"]*"' | head -1 | sed 's/"category": *"//;s/"$//')
    [ -n "$n" ] && printf "  %-30s [%6s] %s\n" "$n" "$c" "${d:0:60}"
  done
}

main() {
  echo ""
  header "╔══════════════════════════════════════════════════╗"
  header "║     ELING ⚡ Hermes Skills Bundle Installer     ║"
  header "╚══════════════════════════════════════════════════╝"
  echo ""

  case "${1:-}" in
    --auto) check_prereqs; AUTO=1 install_kimi || true; register_all ;;
    --kimi) check_prereqs; install_kimi ;;
    --list) list_skills ;;
    --help|-h)
      echo "Usage: $0 [--auto|--kimi|--list|--help]"
      echo "  (none)  Interactive — install Kimi + register skills"
      echo "  --auto  Non-interactive"; echo "  --kimi  Only Kimi WebBridge"
      echo "  --list  Show registered Hermes skills";;
    *)
      check_prereqs
      echo -e "${YELLOW}Will install Kimi WebBridge + 9 Hermes skills${NC}"
      read -p "Proceed? [Y/n] " -r reply; reply="${reply:-Y}"
      [[ "$reply" =~ ^[Yy] ]] || { info "Aborted."; exit 0; }
      install_kimi; register_all
      log "${BOLD}✅ Complete!${NC}"
      echo "  Docs: docs/hermes-skills-adaptation.md";;
  esac
}

main "$@"
