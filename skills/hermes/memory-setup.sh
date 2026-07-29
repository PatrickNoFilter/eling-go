#!/usr/bin/env bash
# ELING Memory Setup (adapted from Hermes Skills Bundle)
# Interactive onboarding to configure user preferences
echo '{"skill":"memory-setup","action":"interactive"}'
cat <<EOF
## 🧠 Memory Setup — Interactive Onboarding

Run this when user says "Set up my memory" or first time setup.

### Workflow

**Step 1**: Announce setup
  "I'll ask 5 quick questions to learn your preferences."

**Step 2**: Ask questions sequentially:

  Q1: "What browser automation do you prefer?"
      - Kimi WebBridge (uses your real Chrome)
      - Headless browser
      - No preference

  Q2: "How do you like deep research done?"
      - Many tabs (10+) in named groups
      - One focused search at a time
      - Just give me the answer

  Q3: "What word means 'do it again the same way'?"
      - retry / rerun / again

  Q4: "Any privacy concerns?"
      - No public pastes
      - Local-only tools
      - Delete temp files
      - No strong preferences

  Q5: "Any UI patterns you use repeatedly?"
      - Preview pane for docs
      - Terminal split
      - Fixed dev port

**Step 3**: Save to ELING memory using semantic_index tool
  Store each answer as indexed memory with tags: "preference", "setup"

**Step 4**: Confirm and summarize
EOF
