#!/usr/bin/env bash
# ELING Interactive Prompt Analyzer (adapted from Hermes Skills Bundle)
# Turns vague prompts into structured options with custom escape hatches
USER_REQUEST="${1:-}"
if [ -z "$USER_REQUEST" ]; then
  echo '{"error":"Request required. Usage: interactive-prompt-analyzer <vague request>"}'
  cat <<HELP
## Interactive Prompt Analyzer

This skill helps when the user gives a vague or open-ended request.
It structures the ambiguity into clear options.

### How to Use
When user says something unclear, use this workflow:

1. **Analyze**: Break down what the user likely wants
2. **Present Options**: Offer 3-5 concrete paths
3. **Let User Choose**: Ask which option to pursue
4. **Execute**: Run the chosen path

### Example
User: "I want to learn about AI"
You: Analyze into options:
- 🅰️ "Research AI frameworks (PyTorch, TensorFlow, LangChain)"
- 🅱️ "Study AI fundamentals (ML, NLP, Computer Vision)"
- 🅲 "Find AI tools I can use today"
- 🅳 "Build an AI project step by step"

### Escape Hatch
Always include: "Or tell me more about what you're looking for"
HELP
  exit 1
fi
echo "{\"request\":\"${USER_REQUEST}\"}"
EOF
