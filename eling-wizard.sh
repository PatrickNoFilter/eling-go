#!/usr/bin/env bash
# ═══════════════════════════════════════════════════════════════════════════
#  ELING Setup Wizard
#  ─────────────────
#  Interactive configuration wizard for ELING AI Agent.
#  Walks you through provider setup, API key, model selection,
#  base URL, system prompt, and agent configuration with style.
#
# Usage:
#   ./eling-wizard.sh          # Interactive wizard mode
#   ./eling-wizard.sh --help   # Show help
#   ./eling-wizard.sh --list   # Show current config
# ═══════════════════════════════════════════════════════════════════════════

set -euo pipefail

# ── Paths ──────────────────────────────────────────────────────────────────
CONFIG_FILE="${ELING_CONFIG:-$HOME/.eling/config.yaml}"
WIZARD_VERSION="2.0.0"

# ── Colors ─────────────────────────────────────────────────────────────────
declare -r RED='\033[0;31m'
declare -r GREEN='\033[0;32m'
declare -r YELLOW='\033[1;33m'
declare -r BLUE='\033[0;34m'
declare -r MAGENTA='\033[0;35m'
declare -r CYAN='\033[0;36m'
declare -r WHITE='\033[1;37m'
declare -r BOLD='\033[1m'
declare -r DIM='\033[2m'
declare -r UNDERLINE='\033[4m'
declare -r BLINK='\033[5m'
declare -r NC='\033[0m'
declare -r CLEAR_LINE='\033[2K'

# ── Icons ──────────────────────────────────────────────────────────────────
ICON_ROCKET="🚀"
ICON_SETUP="🔧"
ICON_PROVIDER="🌐"
ICON_KEY="🔑"
ICON_MODEL="🧠"
ICON_URL="📡"
ICON_PROMPT="💬"
ICON_CONTEXT="📦"
ICON_CHECK="✅"
ICON_CROSS="❌"
ICON_WARN="⚠️"
ICON_INFO="ℹ️"
ICON_STAR="⭐"
ICON_GEAR="⚙️"
ICON_SAVE="💾"
ICON_TEST="🧪"
ICON_DONE="🎉"
ICON_LIST="📋"

# ── Helper Functions ───────────────────────────────────────────────────────

info()    { echo -e " ${GREEN}${ICON_INFO}${NC} $*"; }
success() { echo -e " ${GREEN}${ICON_CHECK}${NC} $*"; }
warn()    { echo -e " ${YELLOW}${ICON_WARN}${NC} $*"; }
error()   { echo -e " ${RED}${ICON_CROSS}${NC} $*" >&2; }
header()  { echo -e "\n${BOLD}${BLUE}━━━ $* ━━━${NC}\n"; }
subheader() { echo -e "\n${BOLD}${CYAN}  ▸ $*${NC}\n"; }
divider() { echo -e "${DIM}────────────────────────────────────────────${NC}"; }

# Show a prompt with highlighted text
prompt() {
  echo -ne "\n ${CYAN}${ICON_GEAR}${NC} ${BOLD}$*${NC} "
}

# Read input with a default value
prompt_default() {
  local msg="$1"
  local default="$2"
  local result_var="$3"
  local input

  prompt "${msg} [${default}]:"
  read -r input
  if [ -z "$input" ]; then
    printf -v "$result_var" "%s" "$default"
  else
    printf -v "$result_var" "%s" "$input"
  fi
}

# Confirm with user
confirm() {
  local msg="$1"
  local default="${2:-Y}"
  local input

  if [ "$default" = "Y" ]; then
    prompt "${msg} [Y/n]:"
  else
    prompt "${msg} [y/N]:"
  fi
  read -r input
  case "$input" in
    [yY]|[yY][eE][sS]) return 0 ;;
    [nN]|[nN][oO]) return 1 ;;
    "") [ "$default" = "Y" ] && return 0 || return 1 ;;
    *) return 1 ;;
  esac
}

# Spin animation while running a command
spinner() {
  local pid=$!
  local delay=0.1
  local spinstr='⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏'
  local msg="$1"
  
  while [ "$(ps a | awk '{print $1}' | grep $pid)" ]; do
    local temp=${spinstr#?}
    printf "\r ${CYAN}[%c]${NC} %s" "$spinstr" "$msg"
    local spinstr=$temp${spinstr%"$temp"}
    sleep $delay
  done
  printf "\r${CLEAR_LINE}"
}

# Mask API key for display
mask_key() {
  local key="$1"
  local len=${#key}
  if [ "$len" -le 12 ]; then
    echo "${key:0:4}****${key: -4}"
  else
    echo "${key:0:8}...${key: -4}"
  fi
}

# ── YAML Read (simple) ────────────────────────────────────────────────────
yaml_read() {
  local key="$1"
  local file="$2"
  local parent=""
  local child="$key"

  if [[ "$key" == *"."* ]]; then
    parent="${key%.*}"
    child="${key##*.}"
  fi

  local in_section=false
  local found=""

  [ ! -f "$file" ] && { echo ""; return; }

  while IFS= read -r line; do
    local trimmed="${line#"${line%%[! ]*}"}"
    [[ -z "$trimmed" || "$trimmed" == \#* ]] && continue

    if [ -n "$parent" ]; then
      if [[ "$line" =~ ^[a-zA-Z_][a-zA-Z0-9_-]*: ]] && ! $in_section; then
        local section_name="${line%%:*}"
        [ "$section_name" = "$parent" ] && in_section=true
        continue
      fi
      if $in_section; then
        if [[ "$line" =~ ^[a-zA-Z_][a-zA-Z0-9_-]*: ]] && [[ "$line" != " "* ]]; then
          in_section=false
          continue
        fi
        local stripped="${line#"${line%%[! ]*}"}"
        if [[ "$stripped" =~ ^${child}: ]]; then
          found="${stripped#*: }"
          found="${found#\"}"
          found="${found%\"}"
          break
        fi
      fi
    else
      if [[ "$line" =~ ^${child}: ]]; then
        found="${line#*: }"
        found="${found#\"}"
        found="${found%\"}"
        break
      fi
    fi
  done < "$file"

  echo "$found"
}

# ── Print Current Config ───────────────────────────────────────────────────
print_config() {
  local file="$1"
  
  if [ ! -f "$file" ]; then
    warn "No config file found at ${file}"
    echo -e "  ${DIM}Run './eling-wizard.sh' to create one.${NC}"
    return
  fi

  local model=$(yaml_read "agent.default_model" "$file")
  local base_url=$(yaml_read "agent.default_base_url" "$file")
  local prompt=$(yaml_read "agent.system_prompt" "$file")
  local context=$(yaml_read "agent.max_context" "$file")

  echo
  echo -e " ${BOLD}${BLUE}${ICON_LIST}${NC} ${BOLD}Current ELING Configuration${NC}"
  divider
  echo -e " ${BOLD}Config File:${NC}  ${CYAN}$file${NC}"
  divider
  echo
  echo -e "   ${BOLD}${ICON_GEAR} Agent Settings:${NC}"
  echo -e "     ${DIM}Model:${NC}       ${GREEN}$model${NC}"
  echo -e "     ${DIM}Base URL:${NC}    ${GREEN}$base_url${NC}"
  echo -e "     ${DIM}System Prompt:${NC} ${GREEN}${prompt:0:80}${NC}"
  echo -e "     ${DIM}Max Context:${NC}  ${GREEN}$context${NC}"
  echo

  # Parse providers using awk (handles any indentation)
  echo -e "   ${BOLD}${ICON_PROVIDER} Providers:${NC}"
  awk '
    BEGIN { in_prov=0; pcount=0; }
    /^[[:space:]]*providers:/ { in_prov=1; next; }
    in_prov == 1 {
      # Check if we moved to a top-level key (start of line, no leading space)
      if ($0 ~ /^[a-zA-Z_][a-zA-Z0-9_-]*:/ && $0 !~ /^[[:space:]]/) {
        in_prov = 0;
        # Print last provider if pending
        if (pname != "") {
          printf "     \033[0;36m%d.\033[0m \033[1m%s\033[0m\n", pcount, pname;
          printf "        \033[2mModel:\033[0m    %s\n", pmodel;
          printf "        \033[2mBase URL:\033[0m %s\n", pbase;
          printf "        \033[2mAPI Key:\033[0m  %s\n", pkey_masked;
        }
        pname=""; pmodel=""; pbase=""; pkey=""; pkey_masked="";
        next;
      }
    }
    in_prov == 1 {
      stripped = $0;
      gsub(/^[[:space:]]*/, "", stripped);
      
      if (stripped ~ /^- name:/) {
        if (pname != "") {
          printf "     \033[0;36m%d.\033[0m \033[1m%s\033[0m\n", pcount, pname;
          printf "        \033[2mModel:\033[0m    %s\n", pmodel;
          printf "        \033[2mBase URL:\033[0m %s\n", pbase;
          printf "        \033[2mAPI Key:\033[0m  %s\n", pkey_masked;
        }
        pcount++;
        val = stripped;
        sub(/^- name:[[:space:]]*"?/, "", val);
        sub(/"?$/, "", val);
        pname = val;
        pmodel = ""; pbase = ""; pkey = ""; pkey_masked = "";
      }
      else if (stripped ~ /^model:/) {
        val = stripped;
        sub(/^model:[[:space:]]*"?/, "", val);
        sub(/"?$/, "", val);
        pmodel = val;
      }
      else if (stripped ~ /^base_url:/) {
        val = stripped;
        sub(/^base_url:[[:space:]]*"?/, "", val);
        sub(/"?$/, "", val);
        pbase = val;
      }
      else if (stripped ~ /^api_key:/) {
        val = stripped;
        sub(/^api_key:[[:space:]]*"?/, "", val);
        sub(/"?$/, "", val);
        pkey = val;
        if (length(val) > 8) {
          pkey_masked = substr(val, 1, 8) "..."
        } else if (length(val) > 0) {
          pkey_masked = "***";
        } else {
          pkey_masked = "(empty)";
        }
      }
    }
    END {
      if (pname != "") {
        printf "     \033[0;36m%d.\033[0m \033[1m%s\033[0m\n", pcount, pname;
        printf "        \033[2mModel:\033[0m    %s\n", pmodel;
        printf "        \033[2mBase URL:\033[0m %s\n", pbase;
        printf "        \033[2mAPI Key:\033[0m  %s\n", pkey_masked;
      }
    }
  ' "$file" 2>/dev/null || echo "     (could not parse providers)"

  echo
  divider
}

# ── Show Banner ────────────────────────────────────────────────────────────
show_banner() {
  clear
  echo -e "${CYAN}"
  cat << "EOF"
    ███████╗██╗     ██╗███╗   ██╗ ██████╗
    ██╔════╝██║     ██║████╗  ██║██╔════╝
    █████╗  ██║     ██║██╔██╗ ██║██║  ███╗
    ██╔══╝  ██║     ██║██║╚██╗██║██║   ██║
    ███████╗███████╗██║██║ ╚████║╚██████╔╝
    ╚══════╝╚══════╝╚═╝╚═╝  ╚═══╝ ╚═════╝
EOF
  echo -e "${NC}"
  echo -e "  ${BOLD}${WHITE}Setup Wizard${NC} ${DIM}v${WIZARD_VERSION}${NC}"
  echo -e "  ${DIM}Configure your AI agent — ${GREEN}fast${NC}${DIM}, ${CYAN}easy${NC}${DIM}, ${YELLOW}interactive${NC}${DIM}.${NC}"
  echo
  divider
  echo
}

# ── Step: Welcome & Introduction ──────────────────────────────────────────
step_welcome() {
  header "${ICON_ROCKET} Welcome to ELING Setup Wizard!"
  echo -e "  This wizard will help you configure your ELING AI Agent."
  echo
  echo -e "  ${BOLD}We'll walk through:${NC}"
  echo -e "   ${CYAN}1.${NC}  ${ICON_PROVIDER}  Choose an AI Provider"
  echo -e "   ${CYAN}2.${NC}  ${ICON_KEY}     Set your API Key"
  echo -e "   ${CYAN}3.${NC}  ${ICON_MODEL}   Select or Enter a Model"
  echo -e "   ${CYAN}4.${NC}  ${ICON_URL}     Configure Base URL"
  echo -e "   ${CYAN}5.${NC}  ${ICON_PROMPT}  Customize System Prompt"
  echo -e "   ${CYAN}6.${NC}  ${ICON_CONTEXT} Set Max Context"
  echo -e "   ${CYAN}7.${NC}  ${ICON_TEST}    Test the Connection"
  echo -e "   ${CYAN}8.${NC}  ${ICON_SAVE}    Save Configuration"
  echo
  divider
  echo
}

# ── Step 1: Provider Selection ────────────────────────────────────────────
step_provider() {
  local -n result_provider="$1"
  local -n result_model="$2"
  local -n result_base_url="$3"
  
  subheader "${ICON_PROVIDER} Choose Your AI Provider"
  
  echo
  echo -e "  ${BOLD}Popular Providers:${NC}"
  echo
  echo -e "  ${CYAN} 1)${NC}  ${BOLD}OpenAI${NC}         ${DIM}- GPT-4o, GPT-4o-mini, GPT-4-turbo${NC}"
  echo -e "             ${DIM}  https://api.openai.com/v1${NC}"
  echo
  echo -e "  ${CYAN} 2)${NC}  ${BOLD}Groq${NC}           ${DIM}- Llama 3.3 70B, Mixtral 8x7B, Gemma 2${NC}"
  echo -e "             ${DIM}  https://api.groq.com/openai/v1${NC}"
  echo
  echo -e "  ${CYAN} 3)${NC}  ${BOLD}Anthropic${NC}      ${DIM}- Claude 3 Opus, Claude 3 Sonnet, Claude 3 Haiku${NC}"
  echo -e "             ${DIM}  https://api.anthropic.com${NC}"
  echo
  echo -e "  ${CYAN} 4)${NC}  ${BOLD}DeepSeek${NC}       ${DIM}- deepseek-chat, deepseek-reasoner${NC}"
  echo -e "             ${DIM}  https://api.deepseek.com${NC}"
  echo
  echo -e "  ${CYAN} 5)${NC}  ${BOLD}OpenRouter${NC}     ${DIM}- Many models via one API${NC}"
  echo -e "             ${DIM}  https://openrouter.ai/api/v1${NC}"
  echo
  echo -e "  ${CYAN} 6)${NC}  ${BOLD}Together AI${NC}    ${DIM}- Open-source models${NC}"
  echo -e "             ${DIM}  https://api.together.xyz/v1${NC}"
  echo
  echo -e "  ${CYAN} 7)${NC}  ${BOLD}Google AI${NC}      ${DIM}- Gemini 1.5 Pro, Gemini 1.5 Flash${NC}"
  echo -e "             ${DIM}  https://generativelanguage.googleapis.com/v1beta${NC}"
  echo
  echo -e "  ${CYAN} 8)${NC}  ${BOLD}OpenCode Zen${NC}   ${DIM}- deepseek-v4-flash (free tier available)${NC}"
  echo -e "             ${DIM}  https://opencode.ai/zen/v1${NC}"
  echo
  echo -e "  ${CYAN} 9)${NC}  ${BOLD}Custom / Ollama${NC}${DIM}- Local models, any OpenAI-compatible API${NC}"
  echo
  echo -e "  ${CYAN} 0)${NC}  ${BOLD}Skip${NC}           ${DIM}- Keep current configuration${NC}"
  echo
  divider
  echo
  
  local choice
  prompt "Enter your choice [1-9, or 0 to skip]:"
  read -r choice

  case "$choice" in
    1) # OpenAI
      result_provider="openai"
      result_model="gpt-4o"
      result_base_url="https://api.openai.com/v1"
      ;;
    2) # Groq
      result_provider="groq"
      result_model="llama-3.3-70b"
      result_base_url="https://api.groq.com/openai/v1"
      ;;
    3) # Anthropic
      result_provider="anthropic"
      result_model="claude-3-sonnet-20240229"
      result_base_url="https://api.anthropic.com"
      ;;
    4) # DeepSeek
      result_provider="deepseek"
      result_model="deepseek-chat"
      result_base_url="https://api.deepseek.com"
      ;;
    5) # OpenRouter
      result_provider="openrouter"
      result_model="openai/gpt-4o"
      result_base_url="https://openrouter.ai/api/v1"
      ;;
    6) # Together AI
      result_provider="together"
      result_model="mistralai/Mixtral-8x7B-Instruct-v0.1"
      result_base_url="https://api.together.xyz/v1"
      ;;
    7) # Google AI
      result_provider="google"
      result_model="gemini-1.5-pro"
      result_base_url="https://generativelanguage.googleapis.com/v1beta"
      ;;
    8) # OpenCode Zen
      result_provider="opencode-zen"
      local has_free
      prompt "Use free tier? [Y/n]:"
      read -r has_free
      if [[ "$has_free" =~ ^[nN] ]]; then
        result_model="deepseek-v4-flash"
      else
        result_provider="opencode-zen-free"
        result_model="deepseek-v4-flash-free"
      fi
      result_base_url="https://opencode.ai/zen/v1"
      ;;
    9) # Custom
      step_custom_provider result_provider result_model result_base_url
      ;;
    0|"") # Skip
      info "Keeping current configuration."
      # Load existing values
      result_provider="$(yaml_read "agent.default_model" "$CONFIG_FILE")"
      result_model="$(yaml_read "agent.default_model" "$CONFIG_FILE")"
      result_base_url="$(yaml_read "agent.default_base_url" "$CONFIG_FILE")"
      if [ -z "$result_provider" ]; then
        result_provider="opencode-zen-free"
        result_model="deepseek-v4-flash-free"
        result_base_url="https://opencode.ai/zen/v1"
      fi
      return
      ;;
    *)
      warn "Invalid choice. Using default."
      result_provider="opencode-zen-free"
      result_model="deepseek-v4-flash-free"
      result_base_url="https://opencode.ai/zen/v1"
      ;;
  esac

  success "Selected: ${BOLD}$result_provider${NC} → ${CYAN}$result_model${NC}"
  echo
}

# ── Step 1b: Custom Provider ──────────────────────────────────────────────
step_custom_provider() {
  local -n c_provider="$1"
  local -n c_model="$2"
  local -n c_base_url="$3"
  
  echo
  subheader "Custom Provider Configuration"
  
  prompt "Provider name (e.g., 'ollama', 'my-llm'):"
  read -r c_provider
  if [ -z "$c_provider" ]; then
    c_provider="custom"
  fi
  
  prompt "Model name (e.g., 'llama3', 'mistral', 'gpt-4'):"
  read -r c_model
  if [ -z "$c_model" ]; then
    c_model="llama3"
  fi
  
  prompt "Base URL (e.g., 'http://localhost:11434/v1'):"
  read -r c_base_url
  if [ -z "$c_base_url" ]; then
    c_base_url="http://localhost:11434/v1"
  fi
  
  echo
  success "Custom provider: ${BOLD}$c_provider${NC} | ${CYAN}$c_model${NC} | ${GREEN}$c_base_url${NC}"
}

# ── Step 2: API Key ────────────────────────────────────────────────────────
step_api_key() {
  local provider="$1"
  local -n result_key="$2"
  local existing_key="$3"
  
  subheader "${ICON_KEY} API Key Setup"
  
  echo
  echo -e "  Provider: ${BOLD}${CYAN}$provider${NC}"
  echo
  echo -e "  ${DIM}Your API key will be stored securely in ${CONFIG_FILE}${NC}"
  echo -e "  ${DIM}It is used only to authenticate with the LLM provider.${NC}"
  echo
  
  if [ -n "$existing_key" ] && [ "$existing_key" != '""' ]; then
    echo -e "  ${GREEN}Existing key found:${NC} $(mask_key "$existing_key")"
    if confirm "Use existing API key?" "Y"; then
      result_key="$existing_key"
      return
    fi
    echo
  fi

  # Check environment variables
  local env_keys=("OPENAI_API_KEY" "GROQ_API_KEY" "ANTHROPIC_API_KEY" "DEEPSEEK_API_KEY" "OPENROUTER_API_KEY" "TOGETHER_API_KEY" "GOOGLE_API_KEY")
  for env_var in "${env_keys[@]}"; do
    local val="${!env_var:-}"
    if [ -n "$val" ]; then
      echo -e "  ${GREEN}Found ${BOLD}\$$env_var${NC}${GREEN} in environment:${NC} $(mask_key "$val")"
      if confirm "Use this API key?" "Y"; then
        result_key="$val"
        return
      fi
      echo
    fi
  done

  echo
  echo -e "  ${YELLOW}Enter your API key:${NC}"
  echo -e "  ${DIM}(It will be masked as you type... not really, but it's stored securely)${NC}"
  echo
  
  prompt "API Key:"
  read -r result_key
  
  while [ -z "$result_key" ]; do
    echo
    error "API key is required to use ELING with this provider."
    echo -e "  ${DIM}You can get one from your provider's dashboard.${NC}"
    echo
    prompt "API Key (or type 'skip' to continue without):"
    read -r result_key
    if [ "$result_key" = "skip" ]; then
      warn "Skipping API key setup. You'll need to set it later."
      result_key=""
      return
    fi
  done
  
  success "API key saved securely!"
}

# ── Step 3: Model Selection ────────────────────────────────────────────────
step_model() {
  local provider="$1"
  local default_model="$2"
  local -n result_model="$3"
  
  subheader "${ICON_MODEL} Model Configuration"
  
  echo
  echo -e "  Provider: ${BOLD}${CYAN}$provider${NC}"
  echo -e "  Current model: ${BOLD}${GREEN}$default_model${NC}"
  echo
  
  # Show recommended models based on provider
  case "$provider" in
    *openai*|*openrouter)
      echo -e "  ${BOLD}Recommended models for $provider:${NC}"
      echo -e "    ${CYAN}1)${NC} gpt-4o              ${DIM}(best overall)${NC}"
      echo -e "    ${CYAN}2)${NC} gpt-4o-mini         ${DIM}(fast & cheap)${NC}"
      echo -e "    ${CYAN}3)${NC} gpt-4-turbo         ${DIM}(powerful)${NC}"
      echo -e "    ${CYAN}4)${NC} gpt-3.5-turbo       ${DIM}(legacy)${NC}"
      echo -e "    ${CYAN}5)${NC} Enter custom model"
      ;;
    *groq*)
      echo -e "  ${BOLD}Recommended models for Groq:${NC}"
      echo -e "    ${CYAN}1)${NC} llama-3.3-70b-versatile     ${DIM}(best)${NC}"
      echo -e "    ${CYAN}2)${NC} llama-3.1-8b-instant        ${DIM}(fast)${NC}"
      echo -e "    ${CYAN}3)${NC} mixtral-8x7b-32768          ${DIM}(mixture)${NC}"
      echo -e "    ${CYAN}4)${NC} gemma2-9b-it                ${DIM}(Google)${NC}"
      echo -e "    ${CYAN}5)${NC} Enter custom model"
      ;;
    *anthropic*)
      echo -e "  ${BOLD}Recommended models for Anthropic:${NC}"
      echo -e "    ${CYAN}1)${NC} claude-3-opus-20240229      ${DIM}(best)${NC}"
      echo -e "    ${CYAN}2)${NC} claude-3-sonnet-20240229    ${DIM}(balanced)${NC}"
      echo -e "    ${CYAN}3)${NC} claude-3-haiku-20240307     ${DIM}(fast)${NC}"
      echo -e "    ${CYAN}4)${NC} Enter custom model"
      ;;
    *deepseek*)
      echo -e "  ${BOLD}Recommended models for DeepSeek:${NC}"
      echo -e "    ${CYAN}1)${NC} deepseek-chat               ${DIM}(default)${NC}"
      echo -e "    ${CYAN}2)${NC} deepseek-reasoner           ${DIM}(reasoning)${NC}"
      echo -e "    ${CYAN}3)${NC} Enter custom model"
      ;;
    *opencode*|*zen*)
      echo -e "  ${BOLD}Recommended models for OpenCode Zen:${NC}"
      echo -e "    ${CYAN}1)${NC} deepseek-v4-flash           ${DIM}(full)${NC}"
      echo -e "    ${CYAN}2)${NC} deepseek-v4-flash-free      ${DIM}(free tier)${NC}"
      echo -e "    ${CYAN}3)${NC} Enter custom model"
      ;;
    *)
      echo -e "  ${DIM}Enter the model name you want to use.${NC}"
      echo -e "  ${CYAN}1)${NC} $default_model (keep default)"
      echo -e "  ${CYAN}2)${NC} Enter custom model"
      ;;
  esac
  
  echo
  prompt "Your choice [1-5, or Enter to keep '$default_model']:"
  read -r model_choice
  
  if [ -z "$model_choice" ]; then
    result_model="$default_model"
    success "Keeping model: ${BOLD}$result_model${NC}"
    return
  fi
  
  case "$provider" in
    *openai*|*openrouter)
      case "$model_choice" in
        1) result_model="gpt-4o" ;;
        2) result_model="gpt-4o-mini" ;;
        3) result_model="gpt-4-turbo" ;;
        4) result_model="gpt-3.5-turbo" ;;
        5)
          prompt "Enter custom model name:"
          read -r result_model
          ;;
        *) result_model="$default_model" ;;
      esac
      ;;
    *groq*)
      case "$model_choice" in
        1) result_model="llama-3.3-70b-versatile" ;;
        2) result_model="llama-3.1-8b-instant" ;;
        3) result_model="mixtral-8x7b-32768" ;;
        4) result_model="gemma2-9b-it" ;;
        5)
          prompt "Enter custom model name:"
          read -r result_model
          ;;
        *) result_model="$default_model" ;;
      esac
      ;;
    *anthropic*)
      case "$model_choice" in
        1) result_model="claude-3-opus-20240229" ;;
        2) result_model="claude-3-sonnet-20240229" ;;
        3) result_model="claude-3-haiku-20240307" ;;
        4)
          prompt "Enter custom model name:"
          read -r result_model
          ;;
        *) result_model="$default_model" ;;
      esac
      ;;
    *deepseek*)
      case "$model_choice" in
        1) result_model="deepseek-chat" ;;
        2) result_model="deepseek-reasoner" ;;
        3)
          prompt "Enter custom model name:"
          read -r result_model
          ;;
        *) result_model="$default_model" ;;
      esac
      ;;
    *opencode*|*zen*)
      case "$model_choice" in
        1) result_model="deepseek-v4-flash" ;;
        2) result_model="deepseek-v4-flash-free" ;;
        3)
          prompt "Enter custom model name:"
          read -r result_model
          ;;
        *) result_model="$default_model" ;;
      esac
      ;;
    *)
      if [ "$model_choice" = "2" ]; then
        prompt "Enter model name:"
        read -r result_model
      else
        result_model="$default_model"
      fi
      ;;
  esac
  
  if [ -z "$result_model" ]; then
    result_model="$default_model"
  fi
  
  success "Model set to: ${BOLD}${CYAN}$result_model${NC}"
}

# ── Step 4: Base URL ──────────────────────────────────────────────────────
step_base_url() {
  local provider="$1"
  local default_url="$2"
  local -n result_url="$3"
  
  subheader "${ICON_URL} Base URL Configuration"
  
  echo
  echo -e "  Provider: ${BOLD}${CYAN}$provider${NC}"
  echo -e "  Current URL: ${BOLD}${GREEN}$default_url${NC}"
  echo
  
  if confirm "Keep this base URL?" "Y"; then
    result_url="$default_url"
    return
  fi
  
  echo
  prompt "Enter base URL (e.g., https://api.example.com/v1):"
  read -r result_url
  
  if [ -z "$result_url" ]; then
    result_url="$default_url"
    warn "Using default: $result_url"
  else
    success "Base URL set to: ${BOLD}${CYAN}$result_url${NC}"
  fi
}

# ── Step 5: System Prompt ─────────────────────────────────────────────────
step_system_prompt() {
  local existing_prompt="$1"
  local -n result_prompt="$2"
  
  subheader "${ICON_PROMPT} System Prompt"
  
  echo
  echo -e "  ${DIM}The system prompt defines the agent's behavior and personality.${NC}"
  echo
  
  if [ -n "$existing_prompt" ]; then
    echo -e "  ${BOLD}Current system prompt:${NC}"
    echo -e "  ${DIM}\"${existing_prompt:0:100}${NC}"
    if [ ${#existing_prompt} -gt 100 ]; then
      echo -e "  ${DIM}... (${#existing_prompt} chars total)${NC}"
    fi
    echo
    if confirm "Keep current system prompt?" "Y"; then
      result_prompt="$existing_prompt"
      return
    fi
  fi
  
  echo -e "  ${BOLD}Choose a preset or enter custom:${NC}"
  echo
  echo -e "  ${CYAN}1)${NC} ${BOLD}Default${NC}         ${DIM}- \"You are ELING, an auto-learning evolving AI agent.\"${NC}"
  echo -e "  ${CYAN}2)${NC} ${BOLD}Assistant${NC}       ${DIM}- Helpful, harmless, honest assistant${NC}"
  echo -e "  ${CYAN}3)${NC} ${BOLD}Programmer${NC}      ${DIM}- Expert software engineer${NC}"
  echo -e "  ${CYAN}4)${NC} ${BOLD}Minimal${NC}        ${DIM}- Short and direct${NC}"
  echo -e "  ${CYAN}5)${NC} ${BOLD}Custom${NC}         ${DIM}- Write your own${NC}"
  echo
  
  prompt "Choice [1-5, default 1]:"
  read -r sp_choice
  
  case "$sp_choice" in
    2)
      result_prompt="You are a helpful, harmless, and honest AI assistant created to assist users with their tasks."
      ;;
    3)
      result_prompt="You are an expert software engineer. You write clean, efficient, well-documented code. You follow best practices and design patterns. You're thorough in testing and debugging."
      ;;
    4)
      result_prompt="You are ELING, an AI agent. Be concise and helpful."
      ;;
    5)
      echo
      echo -e "  ${DIM}Enter your custom system prompt (end with a blank line or Ctrl+D):${NC}"
      echo
      result_prompt=""
      while IFS= read -r line; do
        [ -z "$line" ] && break
        [ -z "$result_prompt" ] && result_prompt="$line" || result_prompt="${result_prompt}\n${line}"
      done
      if [ -z "$result_prompt" ]; then
        result_prompt="You are ELING, an auto-learning evolving AI agent."
      fi
      ;;
    *)
      result_prompt="You are ELING, an auto-learning evolving AI agent."
      ;;
  esac
  
  echo
  success "System prompt set (${#result_prompt} chars)"
}

# ── Step 6: Max Context ──────────────────────────────────────────────────
step_max_context() {
  local existing_context="$1"
  local -n result_context="$2"
  
  if [ -z "$existing_context" ] || [ "$existing_context" = "0" ]; then
    existing_context="32768"
  fi
  
  subheader "${ICON_CONTEXT} Max Context Tokens"
  
  echo
  echo -e "  ${DIM}Maximum context window for the model in tokens.${NC}"
  echo -e "  ${DIM}Higher = more memory, but costs more.${NC}"
  echo
  echo -e "  ${BOLD}Common values:${NC}"
  echo -e "    ${CYAN}1)${NC} 4096    ${DIM}(small)${NC}"
  echo -e "    ${CYAN}2)${NC} 8192    ${DIM}(medium)${NC}"
  echo -e "    ${CYAN}3)${NC} 16384   ${DIM}(large)${NC}"
  echo -e "    ${CYAN}4)${NC} 32768   ${DIM}(very large)${NC}"
  echo -e "    ${CYAN}5)${NC} 65536   ${DIM}(extreme)${NC}"
  echo -e "    ${CYAN}6)${NC} 131072  ${DIM}(maximum)${NC}"
  echo -e "    ${CYAN}7)${NC} Custom"
  echo
  prompt "Choice [1-7, default 4 ($existing_context)]:"
  read -r ctx_choice
  
  case "$ctx_choice" in
    1) result_context="4096" ;;
    2) result_context="8192" ;;
    3) result_context="16384" ;;
    4) result_context="32768" ;;
    5) result_context="65536" ;;
    6) result_context="131072" ;;
    7)
      prompt "Enter max context tokens (number):"
      read -r result_context
      ;;
    *) result_context="$existing_context" ;;
  esac
  
  if ! [[ "$result_context" =~ ^[0-9]+$ ]] || [ "$result_context" -lt 1024 ]; then
    warn "Invalid value, using $existing_context"
    result_context="$existing_context"
  fi
  
  success "Max context: ${BOLD}${result_context}${NC} tokens"
}

# ── Step 7: Review & Confirm ──────────────────────────────────────────────
step_review() {
  local provider="$1"
  local api_key="$2"
  local model="$3"
  local base_url="$4"
  local system_prompt="$5"
  local max_context="$6"
  
  clear
  header "${ICON_STAR} Review Your Configuration"
  
  echo
  echo -e "  ${BOLD}${BLUE}┌─────────────────────────────────────────────────┐${NC}"
  echo -e "  ${BOLD}${BLUE}│${NC}  ${ICON_PROVIDER} ${BOLD}Provider:${NC}     ${WHITE}$provider${NC}                  "
  echo -e "  ${BOLD}${BLUE}│${NC}  ${ICON_KEY}    ${BOLD}API Key:${NC}      ${WHITE}$(mask_key "$api_key")${NC}"
  echo -e "  ${BOLD}${BLUE}│${NC}  ${ICON_MODEL}   ${BOLD}Model:${NC}        ${WHITE}$model${NC}"
  echo -e "  ${BOLD}${BLUE}│${NC}  ${ICON_URL}     ${BOLD}Base URL:${NC}     ${WHITE}$base_url${NC}"
  echo -e "  ${BOLD}${BLUE}│${NC}  ${ICON_PROMPT}  ${BOLD}System Prompt:${NC} ${WHITE}${system_prompt:0:50}...${NC}"
  echo -e "  ${BOLD}${BLUE}│${NC}  ${ICON_CONTEXT} ${BOLD}Max Context:${NC}   ${WHITE}$max_context${NC}"
  echo -e "  ${BOLD}${BLUE}└─────────────────────────────────────────────────┘${NC}"
  echo
  
  if ! confirm "Save this configuration?" "Y"; then
    if confirm "Would you like to start over?" "N"; then
      return 2  # Restart signal
    fi
    return 1  # Cancel signal
  fi
  
  return 0
}

# ── Step 8: Save Configuration ────────────────────────────────────────────
step_save() {
  local provider="$1"
  local api_key="$2"
  local model="$3"
  local base_url="$4"
  local system_prompt="$5"
  local max_context="$6"
  
  subheader "${ICON_SAVE} Saving Configuration..."
  
  mkdir -p "$(dirname "$CONFIG_FILE")"
  
  # Backup existing config
  if [ -f "$CONFIG_FILE" ]; then
    local backup="${CONFIG_FILE}.bak.$(date +%Y%m%d%H%M%S)"
    cp "$CONFIG_FILE" "$backup"
    info "Backed up existing config to $backup"
  fi
  
  # Write the YAML config
  cat > "$CONFIG_FILE" <<YAMLEOF
agent:
  default_model: "${model}"
  default_base_url: "${base_url}"
  system_prompt: "${system_prompt}"
  max_context: ${max_context}
  max_turn_rounds: 250
  max_turn_duration: 300
  auto_test: true
  learn_from_exchange: true
  providers:
    - name: "${provider}"
      model: "${model}"
      base_url: "${base_url}"
      api_key: "${api_key}"

ui:
  theme: "default"
  show_memory: true
  show_thinking: true
  verbose_tool_output: true
  max_messages: 500
  timezone: "Local"

memory:
  max_short_term: 50
  max_long_term: 1000
  decay_rate: 0.01

mcp:
  enabled: false
  servers: []

session:
  auto_save: true
  save_dir: "${HOME}/.eling/sessions"
YAMLEOF
  
  echo
  success "Configuration saved to ${BOLD}${CONFIG_FILE}${NC}!"
  echo
}

# ── Step 9: Test Connection ───────────────────────────────────────────────
step_test() {
  local api_key="$1"
  local model="$2"
  local base_url="$3"
  
  if [ -z "$api_key" ] || [ "$api_key" = '""' ]; then
    warn "No API key set. Skipping connection test."
    echo -e "  ${DIM}You can test later by running './eling-wizard.sh' again.${NC}"
    return
  fi
  
  subheader "${ICON_TEST} Test API Connection"
  
  if ! confirm "Test the API connection now?" "Y"; then
    return
  fi
  
  echo
  info "Testing connection to ${BOLD}$base_url${NC}..."
  echo
  
  # Build the JSON payload
  local payload
  payload=$(cat <<JSONEOF
{
  "model": "${model}",
  "messages": [{"role": "user", "content": "Say 'Hello from ELING!' in one short sentence."}],
  "max_tokens": 50
}
JSONEOF
)

  # Make the API call with timeout
  local response
  local http_code
  
  # Determine the endpoint based on provider
  local endpoint="${base_url}/chat/completions"
  
  response=$(curl -s -w "\n%{http_code}" \
    --connect-timeout 10 \
    --max-time 30 \
    "$endpoint" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer ${api_key}" \
    -d "$payload" 2>/dev/null || echo "curl_error")
  
  http_code=$(echo "$response" | tail -1)
  local body=$(echo "$response" | sed '$d')
  
  echo
  
  if [ "$http_code" = "200" ]; then
    # Try to extract the response content
    local content
    if command -v python3 &>/dev/null; then
      content=$(echo "$body" | python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    content = data['choices'][0]['message']['content']
    print(content)
except Exception:
    print('Connected successfully!')
" 2>/dev/null)
    else
      content="Connected successfully!"
    fi
    
    echo -e "  ${GREEN}${ICON_CHECK}${NC} ${BOLD}Connection successful!${NC} ${GREEN}(HTTP ${http_code})${NC}"
    echo
    echo -e "  ${BOLD}Model response:${NC}"
    echo -e "  ${DIM}\"${content}\"${NC}"
    echo
    success "Your ELING agent is ready to go! 🚀"
  else
    warn "Connection returned HTTP ${http_code}"
    echo
    echo -e "  ${DIM}Response body (first 300 chars):${NC}"
    echo -e "  ${DIM}${body:0:300}${NC}"
    echo
    echo -e "  ${YELLOW}Possible issues:${NC}"
    echo -e "    • Invalid API key"
    echo -e "    • Wrong base URL or endpoint"
    echo -e "    • Model name not recognized"
    echo -e "    • Network/proxy issues"
    echo
    warn "Configuration was still saved. You can fix and test later."
  fi
}

# ── Step 10: Done! ─────────────────────────────────────────────────────────
step_done() {
  clear
  echo
  echo -e "  ${BOLD}${GREEN}${ICON_DONE}${NC}  ${BOLD}${WHITE}ELING Setup Complete!${NC}  ${GREEN}${ICON_DONE}${NC}"
  echo
  echo -e "  ${CYAN}┌──────────────────────────────────────────┐${NC}"
  echo -e "  ${CYAN}│${NC}                                          ${CYAN}│${NC}"
  echo -e "  ${CYAN}│${NC}  ${BOLD}${WHITE}What's next?${NC}                             ${CYAN}│${NC}"
  echo -e "  ${CYAN}│${NC}                                          ${CYAN}│${NC}"
  echo -e "  ${CYAN}│${NC}  ${ICON_ROCKET} ${GREEN}Run ${BOLD}./eling${NC}${GREEN} to start the agent${NC}          ${CYAN}│${NC}"
  echo -e "  ${CYAN}│${NC}  ${ICON_LIST} ${GREEN}Run ${BOLD}./eling-wizard.sh --list${NC}${GREEN} to view config${NC} ${CYAN}│${NC}"
  echo -e "  ${CYAN}│${NC}  ${ICON_SETUP} ${GREEN}Run ${BOLD}./eling-wizard.sh${NC}${GREEN} to reconfigure${NC}     ${CYAN}│${NC}"
  echo -e "  ${CYAN}│${NC}  ${ICON_GEAR} ${GREEN}Run ${BOLD}./eling --run \"hello\"${NC}${GREEN} to test${NC}       ${CYAN}│${NC}"
  echo -e "  ${CYAN}│${NC}                                          ${CYAN}│${NC}"
  echo -e "  ${CYAN}└──────────────────────────────────────────┘${NC}"
  echo
  echo -e "  ${DIM}Configuration saved to: ${CONFIG_FILE}${NC}"
  echo
}

# ── Quick Setup Mode (all params from CLI) ────────────────────────────────
quick_setup() {
  local provider="$1"
  local api_key="$2"
  local model="$3"
  local base_url="$4"
  local system_prompt="$5"
  local max_context="$6"
  
  echo
  info "Quick setup mode..."
  
  if [ -z "$provider" ]; then provider="opencode-zen-free"; fi
  if [ -z "$model" ]; then model="deepseek-v4-flash-free"; fi
  if [ -z "$base_url" ]; then base_url="https://opencode.ai/zen/v1"; fi
  if [ -z "$system_prompt" ]; then system_prompt="You are ELING, an auto-learning evolving AI agent."; fi
  if [ -z "$max_context" ]; then max_context="32768"; fi
  
  step_save "$provider" "$api_key" "$model" "$base_url" "$system_prompt" "$max_context"
  info "Quick setup complete!"
}

# ═══════════════════════════════════════════════════════════════════════════
#  MAIN
# ═══════════════════════════════════════════════════════════════════════════

# ── Parse arguments ────────────────────────────────────────────────────────
LIST_MODE=false
QUICK_MODE=false
QUICK_PROVIDER=""
QUICK_API_KEY=""
QUICK_MODEL=""
QUICK_BASE_URL=""
QUICK_PROMPT=""
QUICK_CONTEXT=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --help|-h)
      echo
      echo "  ${BOLD}ELING Setup Wizard${NC} ${DIM}v${WIZARD_VERSION}${NC}"
      echo
      echo "  ${BOLD}USAGE:${NC}"
      echo "    ./eling-wizard.sh              Interactive setup wizard"
      echo "    ./eling-wizard.sh --list       Show current configuration"
      echo "    ./eling-wizard.sh --quick      Quick non-interactive setup"
      echo "    ./eling-wizard.sh --help       Show this help"
      echo
      echo "  ${BOLD}QUICK SETUP OPTIONS:${NC}"
      echo "    --provider NAME     Provider name"
      echo "    --api-key KEY       API key"
      echo "    --model NAME        Model name"
      echo "    --base-url URL      Base URL"
      echo "    --system-prompt TXT System prompt"
      echo "    --max-context NUM   Max context tokens"
      echo
      echo "  ${BOLD}EXAMPLES:${NC}"
      echo "    ./eling-wizard.sh"
      echo "    ./eling-wizard.sh --list"
      echo "    ./eling-wizard.sh --quick --provider openai --api-key sk-... --model gpt-4o"
      echo
      exit 0
      ;;
    --list)
      LIST_MODE=true
      shift
      ;;
    --quick)
      QUICK_MODE=true
      shift
      ;;
    --provider)    QUICK_PROVIDER="$2"; shift 2 ;;
    --api-key)     QUICK_API_KEY="$2"; shift 2 ;;
    --model)       QUICK_MODEL="$2"; shift 2 ;;
    --base-url)    QUICK_BASE_URL="$2"; shift 2 ;;
    --system-prompt) QUICK_PROMPT="$2"; shift 2 ;;
    --max-context) QUICK_CONTEXT="$2"; shift 2 ;;
    *)
      error "Unknown argument: $1"
      echo "  Use --help for usage information."
      exit 1
      ;;
  esac
done

# ── List mode ──────────────────────────────────────────────────────────────
if $LIST_MODE; then
  print_config "$CONFIG_FILE"
  exit 0
fi

# ── Quick mode ─────────────────────────────────────────────────────────────
if $QUICK_MODE; then
  quick_setup "$QUICK_PROVIDER" "$QUICK_API_KEY" "$QUICK_MODEL" "$QUICK_BASE_URL" "$QUICK_PROMPT" "$QUICK_CONTEXT"
  exit 0
fi

# ── Interactive Mode ───────────────────────────────────────────────────────

# Load existing values
EXISTING_MODEL="$(yaml_read "agent.default_model" "$CONFIG_FILE")"
EXISTING_BASE_URL="$(yaml_read "agent.default_base_url" "$CONFIG_FILE")"
EXISTING_PROMPT="$(yaml_read "agent.system_prompt" "$CONFIG_FILE")"
EXISTING_CONTEXT="$(yaml_read "agent.max_context" "$CONFIG_FILE")"

# Try to extract first provider's API key
EXISTING_API_KEY=""
if [ -f "$CONFIG_FILE" ]; then
  while IFS= read -r line; do
    stripped="${line#"${line%%[! ]*}"}"
    if [[ "$stripped" =~ ^-\ name: ]]; then
      for i in 1 2 3 4; do
        IFS= read -r nextline
        nextstripped="${nextline#"${nextline%%[! ]*}"}"
        if [[ "$nextstripped" =~ ^api_key: ]]; then
          EXISTING_API_KEY="${nextstripped#*api_key: }"
          EXISTING_API_KEY="${EXISTING_API_KEY#\"}"
          EXISTING_API_KEY="${EXISTING_API_KEY%\"}"
          break 2
        fi
      done
      break
    fi
  done < "$CONFIG_FILE"
fi

# Defaults
[ -z "$EXISTING_MODEL" ] && EXISTING_MODEL="deepseek-v4-flash-free"
[ -z "$EXISTING_BASE_URL" ] && EXISTING_BASE_URL="https://opencode.ai/zen/v1"
[ -z "$EXISTING_PROMPT" ] && EXISTING_PROMPT="You are ELING, an auto-learning evolving AI agent."
[ -z "$EXISTING_CONTEXT" ] && EXISTING_CONTEXT="32768"

# ── Run the wizard steps ──────────────────────────────────────────────────
show_banner
step_welcome

# Show current config if exists
if [ -f "$CONFIG_FILE" ]; then
  echo
  echo -e "  ${DIM}Found existing configuration.${NC}"
  if confirm "Show current config before proceeding?" "N"; then
    print_config "$CONFIG_FILE"
    echo
  fi
  echo
fi

# Step 1: Provider
PROVIDER=""
MODEL=""
BASE_URL=""
step_provider PROVIDER MODEL BASE_URL

# If provider wasn't set (skip), use existing
if [ -z "$PROVIDER" ]; then
  PROVIDER="$EXISTING_MODEL"
  MODEL="$EXISTING_MODEL"
  BASE_URL="$EXISTING_BASE_URL"
fi

# Step 2: API Key
API_KEY=""
step_api_key "$PROVIDER" API_KEY "$EXISTING_API_KEY"

# Step 3: Model
step_model "$PROVIDER" "$MODEL" MODEL

# Step 4: Base URL
step_base_url "$PROVIDER" "$BASE_URL" BASE_URL

# Step 5: System Prompt
SYSTEM_PROMPT="$EXISTING_PROMPT"
step_system_prompt "$EXISTING_PROMPT" SYSTEM_PROMPT

# Step 6: Max Context
MAX_CONTEXT="$EXISTING_CONTEXT"
step_max_context "$EXISTING_CONTEXT" MAX_CONTEXT

# Step 7: Review
while true; do
  step_review "$PROVIDER" "$API_KEY" "$MODEL" "$BASE_URL" "$SYSTEM_PROMPT" "$MAX_CONTEXT"
  REVIEW_RESULT=$?
  
  if [ $REVIEW_RESULT -eq 2 ]; then
    # Restart the wizard
    exec "$0" "$@"
  elif [ $REVIEW_RESULT -eq 1 ]; then
    echo
    info "Setup cancelled. Your configuration was not changed."
    exit 0
  else
    break
  fi
done

# Step 8: Save
step_save "$PROVIDER" "$API_KEY" "$MODEL" "$BASE_URL" "$SYSTEM_PROMPT" "$MAX_CONTEXT"

# Step 9: Test
step_test "$API_KEY" "$MODEL" "$BASE_URL"

# Step 10: Done!
step_done
