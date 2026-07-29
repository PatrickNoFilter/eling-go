# ELING Development Guide

## Getting Started as a Developer

### Prerequisites
- Go 1.21+ (1.25 recommended)
- Git
- Make (optional)
- An API key for testing (DeepSeek, OpenAI, etc.)

### Setup Development Environment

```bash
# Clone
git clone https://github.com/yourusername/eling.git
cd eling

# Install dependencies
go mod download

# Build
go build -o eling .

# Run tests
go test ./...

# Run linter
go vet ./...
```

### Project Layout

```
eling/
├── main.go                    # Entry point — CLI, flags, signals, crash handling
├── internal/
│   ├── agent/                 # Core AI agent
│   │   ├── agent.go          # Ask, AskStream, tool loop, auto-learn, sessions
│   │   ├── memory.go          # Memory system (short/long term, decay)
│   │   └── memory_test.go     # Memory unit tests
│   ├── config/                # Configuration management
│   │   └── config.go          # YAML loading, saving, defaults
│   ├── logger/                # Crash-safe logging system
│   │   ├── logger.go          # Logger, crash detection, signal handling
│   │   └── logger_test.go     # Logger tests
│   ├── mcp/                   # Model Context Protocol client
│   │   └── mcp.go             # JSON-RPC 2.0 over stdio, server management
│   ├── provider/              # LLM provider abstraction
│   │   ├── deepseek.go        # Multi-provider client, retry, key rotation
│   │   └── rotation_test.go   # Key rotation tests
│   ├── session/               # Session persistence
│   │   └── session.go         # Named sessions, save/resume, metadata
│   ├── skills/                # Legacy plugin/skill system
│   │   ├── skills.go          # Skill manager
│   │   └── skills_test.go     # Skill tests
│   ├── tools/                 # Dynamic tool system
│   │   ├── registry.go        # Thread-safe tool registry
│   │   ├── bash.go            # Shell execution tool
│   │   ├── files.go           # File read/write/edit/grep/ls
│   │   ├── web.go             # Web search/fetch tools
│   │   ├── register.go        # Dynamic tool/skill registration
│   │   ├── backup.go          # Backup & intelligence tools
│   │   ├── schema.go          # JSON parameter schemas
│   │   ├── semantic.go        # Vector embedding search engine
│   │   ├── setup.go           # Config management tool
│   │   ├── ocr.go             # Open Code Review tools
│   │   └── register_test.go   # Registration tests
│   └── tui/                   # Terminal UI
│       └── tui.go             # Bubbletea 3-panel TUI
├── docs/                      # Documentation
├── skills/                    # Community skill scripts
└── scripts/                   # Utility scripts
```

---

## Development Workflow

### 1. Code Changes

```bash
# Make your changes
vim internal/agent/agent.go

# Format
go fmt ./...

# Check for issues
go vet ./...
```

### 2. Testing

```bash
# Run all tests
go test ./... -v 2>&1 | head -50

# Run specific package tests
go test ./internal/agent/ -v -run TestMemory
go test ./internal/tools/ -v -run TestRegistry

# Run benchmarks
go test ./internal/benchmark/ -bench=. -benchtime=1x

# Test with race detector
go test -race ./...
```

### 3. Building

```bash
# Standard build
go build -o eling .

# Optimized build (smaller binary)
go build -ldflags="-s -w" -o eling .

# Cross-compile
GOOS=linux GOARCH=amd64 go build -o eling-linux-amd64 .
GOOS=darwin GOARCH=arm64 go build -o eling-darwin-arm64 .
```

### 4. Running

```bash
# Quick test
DEEPSEEK_API_KEY="sk-..." ./eling --run "hello"

# TUI mode
DEEPSEEK_API_KEY="sk-..." ./eling

# With specific config
DEEPSEEK_API_KEY="sk-..." ./eling --config test_config.yaml
```

---

## Adding a New Tool

1. **Create a new file** in `internal/tools/` (e.g., `my_tool.go`)

2. **Implement the tool function:**
```go
package tools

func init() {
    DefaultRegistry.Register(Tool{
        Name:        "my_tool",
        Description: "Description of what my_tool does",
        Version:     "1.0.0",
        Category:    "system", // or "skill", "dynamic", etc.
        Execute:     myToolExecute,
    })
}

func myToolExecute(args map[string]interface{}) (interface{}, error) {
    // Extract parameters
    param, _ := args["param_name"].(string)
    if param == "" {
        return Err("param_name is required"), nil
    }

    // Do work
    result := doSomething(param)

    // Return success
    return OK(map[string]interface{}{
        "result": result,
    }), nil
}
```

3. **Add parameter schema** in `internal/tools/schema.go`:
```go
"my_tool": {
    "type": "object",
    "properties": map[string]interface{}{
        "param_name": map[string]interface{}{
            "type": "string",
            "description": "Description of param",
        },
    },
    "required": []string{"param_name"},
},
```

4. **Build and test:**
```bash
go build -o eling .
./eling --run "use my_tool"
```

---

## Adding a New Provider

1. **Add provider config** in your `config.yaml`:
```yaml
agent:
  providers:
    - name: "my-provider"
      model: "my-model"
      base_url: "https://api.myprovider.com/v1"
```

2. **Ensure compatibility**: The provider must support the OpenAI-compatible chat completions API format.

3. **Test:**
```bash
DEEPSEEK_API_KEY="sk-..." ./eling --run "hello" --model "my-model"
```

If you need custom provider behavior (non-OpenAI API), extend `internal/provider/deepseek.go`.

---

## Thread Safety Guidelines

When modifying ELING, always follow these locking rules:

```go
// ✅ CORRECT: Acquire Agent lock, then call subsystems
a.mu.RLock()
defer a.mu.RUnlock()
a.memory.Recall(query)  // Memory has its own lock

// ❌ WRONG: Never call Agent methods while holding another lock
a.mu.Lock()
defer a.mu.Unlock()
a.SomeMethod()  // SomeMethod may try to acquire Agent.mu → DEADLOCK

// ✅ CORRECT: Use read lock for read-only Agent state
a.mu.RLock()
providers := a.providers.List()
a.mu.RUnlock()

// ✅ CORRECT: Isolate subsystem locks
func (a *Agent) SomeOperation() {
    a.mu.RLock()
    sessionName := a.sessionName
    a.mu.RUnlock()
    
    // Do work without Agent lock held
    a.Sessions.Save(sessionName)  // Sessions has its own lock
}
```

### Mutex Hierarchy
```
1. Agent.mu (sync.RWMutex)
2. Memory.mu (sync.Mutex)          — independent
3. Session.Manager.mu (sync.RWMutex) — independent
4. Provider.Manager.mu (sync.RWMutex) — independent
5. Registry.mu (sync.RWMutex)       — independent
6. Agent.turnTimeoutMu (sync.RWMutex) — independent
```

**No nesting allowed** — each subsystem has its own lock and they are never acquired simultaneously. This prevents deadlocks.

---

## Code Review Checklist

Before submitting changes:

- [ ] `go build` succeeds with no errors
- [ ] `go test ./...` passes all tests
- [ ] `go vet ./...` reports no issues
- [ ] `go fmt ./...` has been run
- [ ] New code is panic-safe (uses `recover()` where appropriate)
- [ ] Thread safety is maintained (correct locking)
- [ ] Tool output is bounded (256 KiB cap for tool results)
- [ ] UTF-8 safety (no byte-splitting of multi-byte characters)
- [ ] Error messages are user-friendly
- [ ] Configuration changes are backward-compatible
- [ ] Session data is preserved across restarts

---

## Debugging

### Logging
```go
import "eling/internal/logger"

// Levels
logger.Global().Info("message %s", arg)
logger.Global().Warn("warning %v", err)
logger.Global().Error("error %v", err)
logger.Global().Panic(r)  // Record panic
```

Log file: `~/.eling/eling.log`

### Crash Reports
When a panic occurs, it's logged to `~/.eling/crash_report.log` with:
- Panic value
- Stack trace
- Timestamp
- Signal information (for bus errors/segfaults)

### Race Detection
```bash
go test -race ./...           # Test with race detector
go run -race .                # Run with race detector
```

---

## Release Process

```bash
# 1. Update version in main.go
sed -i 's/const Version = ".*"/const Version = "1.0.0"/' main.go

# 2. Run full test suite
go test -race ./...

# 3. Build binaries
go build -ldflags="-s -w" -o eling-linux-amd64 .

# 4. Tag the release
git tag v1.0.0
git push origin v1.0.0

# 5. Create GitHub release with binaries
```

---

## Performance Considerations

| Area | Best Practice |
|------|--------------|
| **Tool output** | Always use `limitedBuffer` (max 512 KiB) |
| **Tool results** | Cap at 256 KiB with rune-aware truncation |
| **Messages** | Trim tool loop to max 100 messages |
| **Memory** | Cap at `MaxLongTerm` (default 1000) |
| **Skills** | Cap at 100 learned skills |
| **Evolutions** | Cap at 1000 entries |
| **Timeout history** | Cap at 100 records |
| **Embedding cache** | Cap at 1000 entries |
| **Session history** | Trimmed by token budget |
| **Context window** | Configurable `MaxContext` (default 32768) |
