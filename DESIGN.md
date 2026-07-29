# ELING - Auto-Learning Evolving AI Agent

## Architecture Overview

```
┌─────────────────────────────────────────────┐
│                  TUI (Bubbletea)             │
│  ┌─────────────┬──────────────────────────┐  │
│  │   Header    │    Body (Chat Log)        │  │
│  │  - Status   │    - Agent thoughts       │  │
│  │  - Model    │    - Learning logs        │  │
│  │  - Memory%  │    - Conversation         │  │
│  ├─────────────┴ │
│  │         User Input Area                 │  │
│  └────────────────────────────────────────┘  │
└──────────────────────┬──────────────────────┘
                       │
┌──────────────────────▼──────────────────────┐
│              Agent Core                      │
│  ┌────────────┬───────────┬──────────────┐  │
│  │  Provider  │  Memory   │   Skills     │  │
│  │ (DeepSeek) │ (Vector)  │  (Plugins)   │  │
│  └────────────┴───────────┴──────────────┘  │
│  ┌────────────────────────────────────────┐  │
│  │        MCP Client Layer                │  │
│  └────────────────────────────────────────┘  │
└──────────────────────────────────────────────┘
```

## Components

### 1. TUI Layer (Bubbletea v2)
- **Header**: Status bar, model info, memory usage, skills loaded
- **Body**: Scrollable viewport for conversation history and agent self-reflection
- **Input**: Bottom text input with command handling

### 2. Agent Core
- Auto-learning: Every interaction is analyzed, patterns extracted, stored in memory
- Evolving: Agent can modify its own behavior based on learned patterns
- Self-reflection: Periodic metacognition on its own responses

### 3. Provider (DeepSeek V4 Flash)
- OpenAI-compatible API
- Model: `deepseek-v4-flash`
- Streaming responses
- Tool/function calling support

### 4. Memory System
- Short-term: Conversation context window
- Long-term: Persistent memory store (skills learned, user preferences, patterns)
- Vector-like semantic search over past experiences

### 5. Skills & Plugins
- Dynamically loadable skills (code execution, web search, file ops)
- MCP protocol support for tool integration
- Plugin system for extensibility

### 6. MCP Client
- Connect to MCP servers for tools/resources
- Dynamic tool discovery
- Tool execution via LLM function calling

## Key Libraries
- `charm.land/bubbletea/v2` - TUI framework
- `charm.land/charm/bubbles` - UI components
- `charm.land/charm/lipgloss` - Styling
- `github.com/mark3labs/mcp-go` - MCP protocol
