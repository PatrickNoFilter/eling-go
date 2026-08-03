#!/bin/bash
# ELING launcher - works with or without a real terminal
# Wraps the ELING binary with OS-level signal trapping for SIGBUS/SIGSEGV.
# When the Go binary crashes with a fatal signal, the shell wrapper detects
# the exit code and writes an additional crash marker.
#
# IMPORTANT for overlayfs/proot environments (Termux, PRoot, Docker overlay):
#   - Use rebuild.sh (or mv) to update the binary — NEVER use cp.
#   - cp truncates the running file's inode, causing SIGBUS (exit code 135).
#   - mv atomically swaps the inode, keeping the running process unaffected.

cd "$(dirname "$0")"

# Path to crash report log
CRASH_LOG="$HOME/.eling/crash_report.log"

# Ensure log directory exists
mkdir -p "$HOME/.eling"

# P1.8: default tool allowlist — shrinks the function-calling prompt to a
# curated core so small-context local models fit. User can override with
# their own ELING_TOOLS before launching.
if [ -z "${ELING_TOOLS:-}" ]; then
    export ELING_TOOLS="bash,edit,grep,ls,read,ugrep,web_fetch,web_search,write,create_backup,register_tool,semantic_index,semantic_search,worktree_create,worktree_list,worktree_merge,worktree_remove"
fi

run_eling() {
    if [ -t 0 ]; then
        # Real terminal: launch TUI
        ./eling "$@"
    else
        # No terminal: run non-interactive
        if [ $# -eq 0 ]; then
            echo "Usage: ./start.sh \"your prompt\""
            echo "   or: ./start.sh (in a real terminal for TUI)"
            exit 1
        fi
        ./eling --run "$*"
    fi
}

# Run ELING and capture exit code
run_eling "$@"
EXIT_CODE=$?

# Detect fatal signals: 128 + signal_number
# SIGBUS  = 7  → exit code 135
# SIGSEGV = 11 → exit code 139
# SIGABRT = 6  → exit code 134
# SIGILL  = 4  → exit code 132
# SIGFPE  = 8  → exit code 136
if [ $EXIT_CODE -gt 128 ]; then
    SIGNAL_NUM=$((EXIT_CODE - 128))
    case $SIGNAL_NUM in
        4)  SIGNAL_NAME="SIGILL" ;;
        6)  SIGNAL_NAME="SIGABRT" ;;
        7)  SIGNAL_NAME="SIGBUS" ;;
        8)  SIGNAL_NAME="SIGFPE" ;;
        11) SIGNAL_NAME="SIGSEGV" ;;
        *)  SIGNAL_NAME="signal $SIGNAL_NUM" ;;
    esac
    TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
    echo "=== FATAL CRASH $TIMESTAMP ===" >> "$CRASH_LOG"
    echo "Signal: $SIGNAL_NAME (signal $SIGNAL_NUM)" >> "$CRASH_LOG"
    echo "PID: $$" >> "$CRASH_LOG"
    echo "This is a fatal OS signal caught by the shell wrapper." >> "$CRASH_LOG"
    # Add overlayfs-specific guidance for SIGBUS
    if [ "$SIGNAL_NUM" = "7" ]; then
        echo "" >> "$CRASH_LOG"
        echo "If you used 'cp' to replace the eling binary, this is the cause." >> "$CRASH_LOG"
        echo "On overlayfs/proot, cp truncates the running inode → SIGBUS." >> "$CRASH_LOG"
        echo "Use './rebuild.sh' or 'mv' instead of 'cp' to update eling." >> "$CRASH_LOG"
    fi
    echo "=== END CRASH REPORT ===" >> "$CRASH_LOG"
    echo "" >> "$CRASH_LOG"
    echo "🚨 ELING CRASHED: $SIGNAL_NAME (signal $SIGNAL_NUM) — caught by shell wrapper" >&2
    echo "   Crash report: $CRASH_LOG" >&2
    if [ "$SIGNAL_NUM" = "7" ]; then
        echo "   💡 SIGBUS on overlayfs: use rebuild.sh or mv, not cp, to update eling" >&2
    fi
fi

exit $EXIT_CODE
