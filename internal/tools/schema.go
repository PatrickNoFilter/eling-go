package tools

import (
	"os"
	"strings"
)

// ToolDef mirrors the OpenAI/DeepSeek "tools" function-calling schema.
type ToolDef struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction describes one callable function.
type ToolFunction struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

// paramSchemas hand-describes the JSON-schema "parameters" object for each
// built-in tool, since the Tool struct itself has no schema field. Tools not
// listed here fall back to a permissive empty-object schema.
var paramSchemas = map[string]map[string]interface{}{
	"bash": {
		"type": "object",
		"properties": map[string]interface{}{
			"command":     map[string]interface{}{"type": "string", "description": "The shell command to run."},
			"timeout_sec": map[string]interface{}{"type": "number", "description": "Optional timeout in seconds (default 30)."},
			"working_dir": map[string]interface{}{"type": "string", "description": "Optional working directory (defaults to sandbox when enabled)."},
			"allow_host":  map[string]interface{}{"type": "boolean", "description": "Opt-in escape hatch: run against the real tree instead of the sandbox. Only use for commands that MUST touch the host (git add, rebuild.sh). Default false."},
		},
		"required": []string{"command"},
	},
	"worktree_create": {
		"type": "object",
		"properties": map[string]interface{}{
			"name":        map[string]interface{}{"type": "string", "description": "Worktree name (alnum, dash, dot, underscore; max 64)."},
			"base_branch": map[string]interface{}{"type": "string", "description": "Branch to branch from (default: current branch)."},
		},
		"required": []string{"name"},
	},
	"worktree_list": {
		"type":       "object",
		"properties": map[string]interface{}{},
	},
	"worktree_remove": {
		"type": "object",
		"properties": map[string]interface{}{
			"name": map[string]interface{}{"type": "string", "description": "Worktree name to remove."},
		},
		"required": []string{"name"},
	},
	"worktree_merge": {
		"type": "object",
		"properties": map[string]interface{}{
			"name":   map[string]interface{}{"type": "string", "description": "Worktree name to merge back."},
			"target": map[string]interface{}{"type": "string", "description": "Branch to merge into (default: current branch)."},
		},
		"required": []string{"name"},
	},
	"read": {
		"type": "object",
		"properties": map[string]interface{}{
			"file_path":  map[string]interface{}{"type": "string", "description": "Path to the file to read."},
			"max_lines":  map[string]interface{}{"type": "number", "description": "Optional max lines to read."},
			"start_line": map[string]interface{}{"type": "number", "description": "Optional line number to start reading from (0-indexed)."},
		},
		"required": []string{"file_path"},
	},
	"write": {
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{"type": "string", "description": "Path to the file to write."},
			"content":   map[string]interface{}{"type": "string", "description": "Content to write to the file."},
		},
		"required": []string{"file_path", "content"},
	},
	"edit": {
		"type": "object",
		"properties": map[string]interface{}{
			"file_path":  map[string]interface{}{"type": "string", "description": "Path to the file to edit."},
			"old_string": map[string]interface{}{"type": "string", "description": "Exact text to replace."},
			"new_string": map[string]interface{}{"type": "string", "description": "Replacement text."},
		},
		"required": []string{"file_path", "old_string", "new_string"},
	},
	"ls": {
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{"type": "string", "description": "Directory to list."},
		},
		"required": []string{"path"},
	},
	"grep": {
		"type": "object",
		"properties": map[string]interface{}{
			"query":       map[string]interface{}{"type": "string", "description": "Pattern to search for (uses GNU grep)."},
			"path":        map[string]interface{}{"type": "string", "description": "Directory or file to search in."},
			"type":        map[string]interface{}{"type": "string", "description": "Optional file extension filter, e.g. 'go'."},
			"regex":       map[string]interface{}{"type": "boolean", "description": "Treat query as a regular expression."},
			"max_results": map[string]interface{}{"type": "number", "description": "Maximum number of matches to return."},
		},
		"required": []string{"query"},
	},
	"web_search": {
		"type": "object",
		"properties": map[string]interface{}{
			"query":       map[string]interface{}{"type": "string", "description": "Search query."},
			"num_results": map[string]interface{}{"type": "number", "description": "Number of results to return."},
		},
		"required": []string{"query"},
	},
	"web_fetch": {
		"type": "object",
		"properties": map[string]interface{}{
			"url":    map[string]interface{}{"type": "string", "description": "URL to fetch."},
			"format": map[string]interface{}{"type": "string", "description": "Optional output format, e.g. 'text'."},
		},
		"required": []string{"url"},
	},
	"register_tool": {
		"type": "object",
		"properties": map[string]interface{}{
			"name":        map[string]interface{}{"type": "string", "description": "Tool name (used by the agent to invoke it)."},
			"description": map[string]interface{}{"type": "string", "description": "Description of what the tool does."},
			"command":     map[string]interface{}{"type": "string", "description": "Bash command to execute when the tool is called."},
			"script":      map[string]interface{}{"type": "string", "description": "Inline bash script body (alternative to command)."},
			"category":    map[string]interface{}{"type": "string", "description": "Optional category (default: dynamic)."},
		},
		"required": []string{"name", "description"},
	},
	"create_backup": {
		"type": "object",
		"properties": map[string]interface{}{
			"project_dir": map[string]interface{}{"type": "string", "description": "Optional project directory to backup (default: current directory)."},
			"backup_dir":  map[string]interface{}{"type": "string", "description": "Optional directory to store the backup (default: current directory)."},
		},
	},
	"codebase-intelligence": {
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{"type": "string", "description": "Query describing what you want to understand about the codebase."},
		},
	},
	"semantic_search": {
		"type": "object",
		"properties": map[string]interface{}{
			"query":  map[string]interface{}{"type": "string", "description": "The semantic query to search for."},
			"top_k":  map[string]interface{}{"type": "number", "description": "Number of results to return (default 5)."},
			"source": map[string]interface{}{"type": "string", "description": "Source to search: 'memory', 'index', or 'all' (default 'all')."},
		},
		"required": []string{"query"},
	},
	"semantic_index": {
		"type": "object",
		"properties": map[string]interface{}{
			"content":  map[string]interface{}{"type": "string", "description": "Text content to index for semantic search."},
			"category": map[string]interface{}{"type": "string", "description": "Optional category label (e.g. 'fact', 'concept', 'preference')."},
			"tags":     map[string]interface{}{"type": "string", "description": "Optional comma-separated tags."},
			"metadata": map[string]interface{}{"type": "object", "description": "Optional key-value metadata pairs.", "additionalProperties": map[string]interface{}{"type": "string"}},
		},
		"required": []string{"content"},
	},
	"eling_setup": {
		"type": "object",
		"properties": map[string]interface{}{
			"action":        map[string]interface{}{"type": "string", "description": "Action: 'list' (default), 'set', 'add-provider', 'remove-provider', 'set-api-key'"},
			"provider":      map[string]interface{}{"type": "string", "description": "Provider name to select/set as default (e.g. 'opencode-zen', 'openai', 'groq')"},
			"model":         map[string]interface{}{"type": "string", "description": "Model name (e.g. 'deepseek-v4-flash', 'gpt-4o', 'llama-3.3-70b')"},
			"api_key":       map[string]interface{}{"type": "string", "description": "API key for the provider"},
			"base_url":      map[string]interface{}{"type": "string", "description": "Base URL for the provider API"},
			"system_prompt": map[string]interface{}{"type": "string", "description": "System prompt text (for 'set' action)"},
			"max_context":   map[string]interface{}{"type": "string", "description": "Max context tokens (for 'set' action, e.g. '32768')"},
			"name":          map[string]interface{}{"type": "string", "description": "Provider name (for 'add-provider' / 'remove-provider')"},
			"set_default":   map[string]interface{}{"type": "boolean", "description": "Set this provider as default (for 'add-provider')"},
		},
	},
	"eling-setup": {
		"type": "object",
		"properties": map[string]interface{}{
			"action":        map[string]interface{}{"type": "string", "description": "Action: 'list' (default), 'set', 'add-provider', 'remove-provider', 'set-api-key'"},
			"provider":      map[string]interface{}{"type": "string", "description": "Provider name to select/set as default (e.g. 'opencode-zen', 'openai', 'groq')"},
			"model":         map[string]interface{}{"type": "string", "description": "Model name (e.g. 'deepseek-v4-flash', 'gpt-4o', 'llama-3.3-70b')"},
			"api_key":       map[string]interface{}{"type": "string", "description": "API key for the provider"},
			"base_url":      map[string]interface{}{"type": "string", "description": "Base URL for the provider API"},
			"system_prompt": map[string]interface{}{"type": "string", "description": "System prompt text (for 'set' action)"},
			"max_context":   map[string]interface{}{"type": "string", "description": "Max context tokens (for 'set' action, e.g. '32768')"},
			"name":          map[string]interface{}{"type": "string", "description": "Provider name (for 'add-provider' / 'remove-provider')"},
			"set_default":   map[string]interface{}{"type": "boolean", "description": "Set this provider as default (for 'add-provider')"},
		},
	},
	"ocr_review": {
		"type": "object",
		"properties": map[string]interface{}{
			"repo":            map[string]interface{}{"type": "string", "description": "Repository path to review."},
			"commit":          map[string]interface{}{"type": "string", "description": "Review one commit against its parent."},
			"from":            map[string]interface{}{"type": "string", "description": "Base ref for a branch/range comparison. Must be paired with 'to'."},
			"to":              map[string]interface{}{"type": "string", "description": "Target ref for a branch/range comparison. Must be paired with 'from'."},
			"resume":          map[string]interface{}{"type": "string", "description": "Resume a previous OCR review session by ID."},
			"background":      map[string]interface{}{"type": "string", "description": "Business or requirement context that the implementation should satisfy."},
			"exclude":         map[string]interface{}{"type": "string", "description": "Comma-separated gitignore-style exclusion patterns."},
			"model":           map[string]interface{}{"type": "string", "description": "Override the model configured in OpenCodeReview."},
			"concurrency":     map[string]interface{}{"type": "number", "description": "Maximum concurrent file reviews."},
			"timeout_minutes": map[string]interface{}{"type": "number", "description": "Per-file OCR timeout in minutes."},
			"max_tools":       map[string]interface{}{"type": "number", "description": "Maximum tool-call rounds per file."},
			"max_git_procs":   map[string]interface{}{"type": "number", "description": "Maximum concurrent Git subprocesses."},
			"preview":         map[string]interface{}{"type": "boolean", "description": "List the files that would be reviewed without calling an LLM."},
		},
	},
	"ocr_scan": {
		"type": "object",
		"properties": map[string]interface{}{
			"path":            map[string]interface{}{"type": "string", "description": "Directory or file path to scan."},
			"model":           map[string]interface{}{"type": "string", "description": "Override the model configured in OpenCodeReview."},
			"exclude":         map[string]interface{}{"type": "string", "description": "Comma-separated gitignore-style exclusion patterns."},
			"concurrency":     map[string]interface{}{"type": "number", "description": "Maximum concurrent file reviews."},
			"timeout_minutes": map[string]interface{}{"type": "number", "description": "Per-file OCR timeout in minutes."},
		},
	},
	"ocr_health": {
		"type":       "object",
		"properties": map[string]interface{}{},
	},
}

// defaultSchema is used for any tool with no explicit schema above.
func defaultSchema() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}

// ToolAllowlist returns a set of tool names to advertise, or nil when
// ELING_TOOLS is unset (no filtering). Setting ELING_TOOLS to a comma-
// separated list (e.g. "read_file,write_file,edit_file,bash") shrinks the
// function-calling prompt dramatically — essential for small-context local
// models like llama-server where the full tool schema alone can exceed the
// context window.
func ToolAllowlist() map[string]bool {
	raw := os.Getenv("ELING_TOOLS")
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	set := make(map[string]bool)
	for _, n := range strings.Split(raw, ",") {
		n = strings.TrimSpace(n)
		if n != "" {
			set[n] = true
		}
	}
	return set
}

// ToProviderDefs converts registry tools into the function-calling format
// the provider (DeepSeek/OpenAI-compatible) API expects.
// When ELING_TOOLS is set, only the listed tools are advertised.
func (r *Registry) ToProviderDefs() []ToolDef {
	allow := ToolAllowlist()
	list := r.List()
	defs := make([]ToolDef, 0, len(list))
	for _, t := range list {
		if allow != nil && !allow[t.Name] {
			continue
		}
		schema, ok := paramSchemas[t.Name]
		if !ok {
			schema = defaultSchema()
		}
		defs = append(defs, ToolDef{
			Type: "function",
			Function: ToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  schema,
			},
		})
	}
	return defs
}
