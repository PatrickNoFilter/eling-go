#!/usr/bin/env bash
# =============================================================================
# ELING → Kimi WebBridge Browser Control Tool
# =============================================================================
# Wraps the Kimi WebBridge HTTP API as a bash-based ELING tool.
# Allows ELING to control your real Chrome browser using your login sessions.
#
# Usage: kimi-webbridge <action> [args...]
#
# Actions:
#   status              - Check if daemon is running
#   navigate <url>      - Open URL (use --new-tab for new tab, --group "name" for tab group)
#   snapshot            - Get page content (accessibility tree with @e refs)
#   click <selector>    - Click element by @e ref or CSS selector
#   fill <selector> <text> - Fill form field
#   get-attr <selector> <attr> - Get element attribute
#   evaluate <js-code>  - Run JavaScript in page
#   list-tabs           - List all open tabs
#   find-tab <url>      - Switch to tab by URL
#   close-session       - Close entire tab group
#   scroll <direction>  - Scroll page (up/down)
# =============================================================================

set -euo pipefail

KIMI_HOST="${KIMI_HOST:-http://127.0.0.1:10086}"
ACTION="${1:-}"
shift 2>/dev/null || true

# -------------------------------------------------------------------------
# Daemon management
# -------------------------------------------------------------------------
ensure_daemon() {
  if ! curl -sf "${KIMI_HOST}/status" >/dev/null 2>&1; then
    # Try to start it
    if command -v ~/.kimi-webbridge/bin/kimi-webbridge &>/dev/null; then
      ~/.kimi-webbridge/bin/kimi-webbridge start 2>/dev/null || true
      sleep 2
    fi
    if ! curl -sf "${KIMI_HOST}/status" >/dev/null 2>&1; then
      echo '{"error":"Kimi WebBridge daemon not running. Install: curl -fsSL https://cdn.kimi.com/webbridge/install.sh | bash"}'
      exit 1
    fi
  fi
}

# -------------------------------------------------------------------------
# API call helper
# -------------------------------------------------------------------------
kimi_api() {
  local method="${1:-GET}"
  local endpoint="${2:-/status}"
  local body="${3:-}"
  shift 3 2>/dev/null || true

  if [ "$method" = "GET" ]; then
    curl -sf "${KIMI_HOST}${endpoint}" 2>/dev/null || echo '{"error":"request failed"}'
  else
    curl -sf -X "${method}" "${KIMI_HOST}${endpoint}" \
      -H 'Content-Type: application/json' \
      -d "${body}" 2>/dev/null || echo '{"error":"request failed"}'
  fi
}

# -------------------------------------------------------------------------
# Main dispatch
# -------------------------------------------------------------------------
case "${ACTION}" in
  status)
    ensure_daemon
    kimi_api GET /status
    ;;

  navigate)
    URL="${1:-}"
    if [ -z "$URL" ]; then
      echo '{"error":"URL is required"}'
      exit 1
    fi
    NEW_TAB="false"
    GROUP_TITLE=""
    SESSION="${KIMI_SESSION:-eling-session}"

    # Parse flags
    for arg in "$@"; do
      case "$arg" in
        --new-tab) NEW_TAB="true" ;;
        --group=*) GROUP_TITLE="${arg#--group=}" ;;
      esac
    done

    JSON="{\"action\":\"navigate\",\"args\":{\"url\":\"${URL}\",\"newTab\":${NEW_TAB}}"
    [ -n "$GROUP_TITLE" ] && JSON="${JSON},\"group_title\":\"${GROUP_TITLE}\""
    JSON="${JSON}},\"session\":\"${SESSION}\"}"
    kimi_api POST /command "${JSON}"
    ;;

  snapshot)
    SESSION="${KIMI_SESSION:-eling-session}"
    kimi_api POST /command "{\"action\":\"snapshot\",\"args\":{},\"session\":\"${SESSION}\"}"
    ;;

  click)
    SELECTOR="${1:-}"
    if [ -z "$SELECTOR" ]; then
      echo '{"error":"selector is required (@e ref or CSS)"}'
      exit 1
    fi
    SESSION="${KIMI_SESSION:-eling-session}"
    kimi_api POST /command "{\"action\":\"click\",\"args\":{\"selector\":\"${SELECTOR}\"},\"session\":\"${SESSION}\"}"
    ;;

  fill)
    SELECTOR="${1:-}"
    TEXT="${2:-}"
    if [ -z "$SELECTOR" ] || [ -z "$TEXT" ]; then
      echo '{"error":"selector and text are required"}'
      exit 1
    fi
    SESSION="${KIMI_SESSION:-eling-session}"
    # Escape quotes in text
    TEXT_SAFE=$(echo "$TEXT" | sed 's/"/\\"/g')
    kimi_api POST /command "{\"action\":\"fill\",\"args\":{\"selector\":\"${SELECTOR}\",\"value\":\"${TEXT_SAFE}\"},\"session\":\"${SESSION}\"}"
    ;;

  get-attr)
    SELECTOR="${1:-}"
    ATTR="${2:-}"
    if [ -z "$SELECTOR" ] || [ -z "$ATTR" ]; then
      echo '{"error":"selector and attribute are required"}'
      exit 1
    fi
    SESSION="${KIMI_SESSION:-eling-session}"
    kimi_api POST /command "{\"action\":\"get_attribute\",\"args\":{\"selector\":\"${SELECTOR}\",\"attribute\":\"${ATTR}\"},\"session\":\"${SESSION}\"}"
    ;;

  evaluate)
    CODE="${1:-}"
    if [ -z "$CODE" ]; then
      echo '{"error":"JavaScript code is required"}'
      exit 1
    fi
    SESSION="${KIMI_SESSION:-eling-session}"
    CODE_SAFE=$(echo "$CODE" | sed 's/"/\\"/g')
    kimi_api POST /command "{\"action\":\"evaluate\",\"args\":{\"code\":\"${CODE_SAFE}\"},\"session\":\"${SESSION}\"}"
    ;;

  list-tabs)
    SESSION="${KIMI_SESSION:-eling-session}"
    kimi_api POST /command "{\"action\":\"list_tabs\",\"args\":{},\"session\":\"${SESSION}\"}"
    ;;

  find-tab)
    URL_PATTERN="${1:-}"
    if [ -z "$URL_PATTERN" ]; then
      echo '{"error":"URL pattern is required"}'
      exit 1
    fi
    SESSION="${KIMI_SESSION:-eling-session}"
    kimi_api POST /command "{\"action\":\"find_tab\",\"args\":{\"url\":\"${URL_PATTERN}\"},\"session\":\"${SESSION}\"}"
    ;;

  close-session)
    SESSION="${KIMI_SESSION:-eling-session}"
    kimi_api POST /command "{\"action\":\"close_session\",\"args\":{},\"session\":\"${SESSION}\"}"
    ;;

  scroll)
    DIR="${1:-down}"
    SESSION="${KIMI_SESSION:-eling-session}"
    kimi_api POST /command "{\"action\":\"evaluate\",\"args\":{\"code\":\"window.scrollBy(0, ${DIR}=="up" ? -500 : 500);\"},\"session\":\"${SESSION}\"}"
    ;;

  install)
    echo "Installing Kimi WebBridge daemon..."
    curl -fsSL https://cdn.kimi.com/webbridge/install.sh | bash
    echo "Starting daemon..."
    ~/.kimi-webbridge/bin/kimi-webbridge start 2>/dev/null || true
    sleep 2
    echo "Checking status..."
    curl -sf http://127.0.0.1:10086/status || echo "Daemon may need a moment to start."
    echo ""
    echo "⚠️  You also need to install the Chrome extension:"
    echo "   https://chromewebstore.google.com/detail/kimi-webbridge/fldmhceldgbpfpkbgopacenieobmligc"
    echo "   Then sign in and connect."
    ;;

  help|--help|-h)
    echo "ELING ⚡ Kimi WebBridge Browser Control"
    echo ""
    echo "Usage: kimi-webbridge <action> [args...]"
    echo ""
    echo "Actions:"
    echo "  status                    Check daemon status"
    echo "  navigate <url>            Open URL (--new-tab, --group=\"name\")"
    echo "  snapshot                  Get page accessibility tree"
    echo "  click <selector>          Click @e ref or CSS selector"
    echo "  fill <selector> <text>    Fill form field"
    echo "  get-attr <sel> <attr>     Get element attribute"
    echo "  evaluate <code>           Run JavaScript"
    echo "  list-tabs                 List open tabs"
    echo "  find-tab <url>            Switch to tab"
    echo "  close-session             Close tab group"
    echo "  scroll <up|down>          Scroll page"
    echo "  install                   Install daemon + extension"
    echo ""
    echo "Env: KIMI_HOST (default: http://127.0.0.1:10086)"
    echo "     KIMI_SESSION (default: eling-session)"
    ;;

  *)
    if [ -n "${ACTION}" ]; then
      echo "{\"error\":\"unknown action: ${ACTION}. Use: status, navigate, snapshot, click, fill, get-attr, evaluate, list-tabs, find-tab, close-session, scroll, install\"}"
    else
      # No action - show status
      ensure_daemon
      kimi_api GET /status
    fi
    ;;
esac
