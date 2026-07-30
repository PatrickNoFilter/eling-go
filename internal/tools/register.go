package tools

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// DynamicTool tracks a tool that was registered at runtime (by the LLM or
// via /add tool) so it can be persisted and re-loaded across restarts.
type DynamicTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`            // "dynamic", "plugin", "skill"
	Command     string `json:"command,omitempty"`   // bash command or file path
	IsScript    bool   `json:"is_script,omitempty"` // true when command is an inline script
}

var (
	dynamicToolsMu sync.RWMutex
	dynamicTools   []DynamicTool // persisted to state/tools.json
)

// GetDynamicTools returns a copy of all persisted dynamic tools.
func GetDynamicTools() []DynamicTool {
	dynamicToolsMu.RLock()
	defer dynamicToolsMu.RUnlock()
	out := make([]DynamicTool, len(dynamicTools))
	copy(out, dynamicTools)
	return out
}

// SetDynamicTools replaces the persisted dynamic-tool list (used when loading state).
func SetDynamicTools(list []DynamicTool) {
	dynamicToolsMu.Lock()
	defer dynamicToolsMu.Unlock()
	if list == nil {
		list = []DynamicTool{}
	}
	dynamicTools = list
}

// AddDynamicTool appends one tool and returns a copy of the updated list.
func AddDynamicTool(dt DynamicTool) []DynamicTool {
	dynamicToolsMu.Lock()
	defer dynamicToolsMu.Unlock()
	dynamicTools = append(dynamicTools, dt)
	out := make([]DynamicTool, len(dynamicTools))
	copy(out, dynamicTools)
	return out
}

// RemoveDynamicTool removes a tool by name.
func RemoveDynamicTool(name string) {
	dynamicToolsMu.Lock()
	defer dynamicToolsMu.Unlock()
	for i, dt := range dynamicTools {
		if dt.Name == name {
			dynamicTools = append(dynamicTools[:i], dynamicTools[i+1:]...)
			return
		}
	}
}

func init() {
	// register_tool – lets the LLM dynamically create a new bash-wrapping tool
	DefaultRegistry.Register(Tool{
		Name: "register_tool",
		Description: "Dynamically register a new tool so the agent can call it. " +
			"The tool wraps a bash command. Provide name, description, and either command or inline script. " +
			"Use type='skill' to register as a skill (appears in skill list). " +
			"Use type='tool' (default) to register as a dynamic tool.",
		Version:  "1.0.0",
		Category: "system",
		Execute:  registerToolExecute,
	})
}

// registerToolExecute handles the register_tool tool call from the LLM.
// Expected args: name (string), description (string),
//
//	command (string, optional – bash command),
//	script (string, optional – inline script body).
func registerToolExecute(args map[string]interface{}) (interface{}, error) {
	name, _ := args["name"].(string)
	desc, _ := args["description"].(string)
	command, _ := args["command"].(string)
	script, _ := args["script"].(string)

	if name == "" {
		return Err("name is required"), nil
	}
	if desc == "" {
		desc = fmt.Sprintf("Dynamic tool: %s", name)
	}

	// Determine category upfront (needed for validation below)
	cat := "dynamic"
	if c, _ := args["category"].(string); c != "" {
		cat = c
	}
	// Support type="skill" to register as a skill
	if t, _ := args["type"].(string); t == "skill" {
		cat = "skill"
	}

	if command == "" && script == "" {
		// Skills don't require a command — they're just category markers
		if cat != "skill" {
			return Err("either command or script is required"), nil
		}
	}

	execCmd := command
	isScript := false
	if script != "" {
		if command != "" {
			// Both provided: log a warning and use script (script takes precedence)
			fmt.Printf("Warning: both 'command' and 'script' provided for tool %q; using 'script'\n", name)
		}
		execCmd = script
		isScript = true
	}
	if execCmd == "" {
		// For skills without a command, use a no-op placeholder
		if cat == "skill" {
			execCmd = "echo 'skill registered'"
		} else {
			return Err("resolved command is empty"), nil
		}
	}

	// Check if name already taken in the default registry.
	if _, exists := DefaultRegistry.Get(name); exists {
		return Err(fmt.Sprintf("tool %q already exists; unregister it first or use a different name", name)), nil
	}

	tool := Tool{
		Name:        name,
		Description: desc,
		Version:     "1.0.0",
		Category:    cat,
		Execute: func(a map[string]interface{}) (interface{}, error) {
			// Build the effective command line, merging any call-time args.
			fullCmd := execCmd
			// Append positional arguments if provided as "args" slice,
			// properly shell-escaped to prevent injection.
			if pos, ok := a["args"].([]interface{}); ok {
				for _, v := range pos {
					fullCmd += " " + shellEscape(fmt.Sprintf("%v", v))
				}
			}
			// Also accept individual named params appended as env or suffix.
			return RunDynamicCommand(fullCmd, a)
		},
	}

	DefaultRegistry.Register(tool)

	// Persist
	AddDynamicTool(DynamicTool{
		Name:        name,
		Description: desc,
		Category:    cat,
		Command:     execCmd,
		IsScript:    isScript,
	})

	return OK(map[string]interface{}{
		"registered": name,
		"category":   cat,
		"type":       "tool",
	}), nil
}

// RunDynamicCommand runs a bash command, optionally passing call arguments
// as environment variables (arg_KEY=VALUE).
func RunDynamicCommand(cmd string, args map[string]interface{}) (interface{}, error) {
	// Build environment with tool call args as ELING_ARG_* variables.
	env := os.Environ()
	for k, v := range args {
		if k == "args" {
			continue
		}
		env = append(env, fmt.Sprintf("ELING_ARG_%s=%v", strings.ToUpper(k), v))
	}

	execCmd := exec.Command("bash", "-c", cmd)
	execCmd.Env = env

	// Use limited buffer to prevent OOM from large command output
	stdout := newLimitedBuffer(maxBashOutputBytes)
	stderr := newLimitedBuffer(maxBashOutputBytes)
	execCmd.Stdout = stdout
	execCmd.Stderr = stderr

	err := execCmd.Run()
	output := strings.TrimSpace(stdout.String())
	errStr := strings.TrimSpace(stderr.String())
	if stdout.Len() >= maxBashOutputBytes {
		output += "\n... [stdout truncated at 512 KiB]"
	}
	if stderr.Len() >= maxBashOutputBytes {
		errStr += "\n... [stderr truncated at 512 KiB]"
	}

	if err != nil {
		return OK(map[string]interface{}{
			"stdout":   output,
			"stderr":   errStr,
			"exit_err": err.Error(),
		}), nil
	}
	return OK(map[string]interface{}{
		"stdout": output,
	}), nil
}

// shellEscape wraps a string in single quotes for safe shell passing.
// Handles single quotes inside the string by ending the quote, adding an escaped
// quote, and restarting the quote.
func shellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
