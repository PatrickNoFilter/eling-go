#!/bin/bash
# ────────────────────────────────────────────────────────────
# ELING Force-Kill Helper
# Use this when Ctrl+C doesn't kill eling (due to PRoot ptrace
# or the two-stage TUI interrupt).
# ────────────────────────────────────────────────────────────

PID_FILE="$HOME/.eling/eling.pid"

if [ -f "$PID_FILE" ]; then
    PID=$(cat "$PID_FILE")
    if [ -n "$PID" ] && kill -0 "$PID" 2>/dev/null; then
        echo "📋 Found ELING PID: $PID"
        echo "   Sending SIGTERM..."
        kill -15 "$PID" 2>/dev/null
        sleep 1
        if kill -0 "$PID" 2>/dev/null; then
            echo "   Still alive, sending SIGKILL..."
            kill -9 "$PID" 2>/dev/null
            sleep 0.5
        fi
        if kill -0 "$PID" 2>/dev/null; then
            echo "⚠️  Process $PID still alive! Trying direct kill..."
            # Try direct kill via /proc
            echo 1 > /proc/"$PID"/oom_score_adj 2>/dev/null
            kill -9 "$PID" 2>/dev/null
            sleep 0.5
        fi
        if kill -0 "$PID" 2>/dev/null; then
            echo "❌ Could not kill PID $PID"
        else
            echo "✅ ELING process terminated"
            rm -f "$PID_FILE"
        fi
    else
        echo "📋 PID file says $PID but process not found — cleaning up"
        rm -f "$PID_FILE"
    fi
else
    echo "📋 No PID file found at $PID_FILE"
    echo "   Searching for eling processes..."
    PIDS=$(ps aux | grep '[e]ling' | awk '{print $2}')
    if [ -n "$PIDS" ]; then
        echo "   Found: $PIDS"
        for PID in $PIDS; do
            kill -15 "$PID" 2>/dev/null
        done
        sleep 1
        for PID in $PIDS; do
            kill -9 "$PID" 2>/dev/null
        done
        echo "✅ All eling processes terminated"
    else
        echo "✅ No eling processes running"
    fi
fi
