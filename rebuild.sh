#!/bin/bash
# ────────────────────────────────────────────────────────────
# ELING Rebuild Script
# Builds to a temp file then atomically replaces the binary.
# Uses mv (not cp) for atomic rename — safe on overlayfs/proot.
# ────────────────────────────────────────────────────────────
set -e

cd "$(dirname "$0")"

echo "🔨 Building ELING..."

# Build to a temp file in the same directory (atomic rename)
TMP_BIN=".eling.build.$$"
trap 'rm -f "$TMP_BIN"' EXIT

if go build -o "$TMP_BIN" . 2>&1; then
    # Atomic rename — instant on Linux, safe on overlayfs/proot
    # mv replaces the inode without affecting the running process
    mv -f "$TMP_BIN" ./eling
    echo "✅ Build successful! ($(ls -lh eling | awk '{print $5}'))"
else
    echo "❌ Build failed!"
    exit 1
fi
