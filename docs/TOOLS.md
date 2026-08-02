# ELING Tool Reference

Complete documentation for all 22+ built-in tools available in ELING's dynamic tool registry.

---

## ⏱ Timeout Strategy (v0.4.0)

Every tool has a **hard wall-clock budget** — no tool can hang the agent forever:

| Tool | Budget | Notes |
|------|--------|-------|
| `read` | 20 s | aborts mid-read on turn deadline / Ctrl+C; **64 MiB size cap** |
| `write` / `edit` / `ls` | 15 s | local disk ops |
| `grep` | 20 s | internal 10 s command timeout already |
| `web_search` / `web_fetch` | 30 s | adaptive per-host curl `--max-time` (4–8 s) + fallback chain |
| `bash` | 10 min | absolute cap; own default is 30 s (`timeout_sec`) |
| `ocr_review` / `ocr_scan` | 5 min | override via `tool_timeout_sec` arg |
| `ocr_health` | 60 s | version + LLM ping only |
| `worktree_*` | 60 s | git operations |
| `create_backup` / `codebase-intelligence` | 2 min | zip / multi-step graph queries |
| `semantic_search` / `semantic_index` | 30 s | embeddings may hit a remote API |
| `eling_setup` | 60 s | runs the setup script |
| `register_tool` | 30 s | dynamic registration |
| *any other tool* | 5 min | `DefaultToolTimeout` fallback |

**How it works:**
- The caller's turn deadline (e.g. `max_duration`) **always wins** — if it fires
  earlier, tools are aborted at that point.
- Context-aware tools (web, bash, ocr, read) receive a context carrying the
  budget and **cancel mid-flight** (curl/ocr killed via `CommandContext`).
- Plain tools run under a goroutine + timer guard; on expiry any tracked
  subprocesses are SIGKILLed and a timeout error is returned.
- If a tool exceeds its budget, the agent gets a clear error
  (`tool X timed out after …`) instead of blocking the turn indefinitely.

---

## 🔧 System Tools

### `bash`
Execute shell commands with timeout protection.

**Parameters:**
| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `command` | string | ✅ | — | Shell command to execute |
| `timeout_sec` | number | ❌ | `30` | Timeout in seconds |
| `working_dir` | string | ❌ | current dir | Working directory |

**Output:** `{ "exit_code": int, "stdout": string, "stderr": string, "command": string }`

**Limits:**
- Max output: 512 KiB (stdout + stderr)
- Max execution time: configurable timeout (default 30s)
- Running processes tracked for Ctrl+C interrupt

**Example:**
```
bash(cmd=ls -la, timeout_sec=10)
bash(cmd=git log --oneline -5, working_dir=/home/user/project)
```

---

### `read`
Read file contents with line number limits.

**Parameters:**
| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `file_path` | string | ✅ | — | Path to file |
| `max_lines` | number | ❌ | all | Max lines to return |
| `start_line` | number | ❌ | 0 | Starting line (0-indexed) |

**Output:** File contents as plain text.

**Limits:**
- **64 MiB size cap** — files larger than this are refused up front with an
  actionable error (use `grep` / `ls` / `max_lines` instead), so a multi-GB log
  can never stall the agent.
- 20 s hard budget; the read aborts immediately on turn deadline / Ctrl+C.

**Example:**
```
read(path=main.go, max_lines=50)
read(file_path=/etc/hosts, start_line=5, max_lines=10)
```

---

### `write`
Write content to a file (creates directories if needed, overwrites existing).

**Parameters:**
| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `file_path` | string | ✅ | Path to write |
| `content` | string | ✅ | Content to write |

**Auto-backup:** Before overwriting an existing file, ELING snapshots it to `*.bak.<timestamp>` (e.g. `main.go.bak.20260801_120000`). Rotation keeps the last **5** backups per file — old ones are pruned automatically. If `ELING_BACKUP_DIR` is set, backups are mirrored under that central directory (preserving the source path); `ELING_BACKUP_KEEP` overrides the rotation count. Writing identical content is a no-op (no backup created).

**Output:** `{ "path": string, "written": int, "backup": string }` — `written: 0` + `unchanged: true` when the file already had the exact content.

**Example:**
```
write(path=hello.txt, content=Hello World!)
write(file_path=src/main.go, content=package main\n\nfunc main() {\n\tprintln("hello")\n})
```

---

### `edit`
Replace exact text in a file (string match, not regex).

**Parameters:**
| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `file_path` | string | ✅ | File to edit |
| `old_string` | string | ✅ | Exact text to find |
| `new_string` | string | ✅ | Replacement text |

**Auto-backup:** Same as `write` — the original file is snapshotted to `*.bak.<timestamp>` before the edit (rotation keeps the last 5, configurable via `ELING_BACKUP_DIR` / `ELING_BACKUP_KEEP`).

**Output:** `{ "edited": bool, "changes": int, "diff": string, "backup": string }`

**Example:**
```
edit(path=main.go, old_string=println("hello"), new_string=println("hi"))
```

---

### `ls`
List directory contents with file sizes and metadata.

**Parameters:**
| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `path` | string | ✅ | — | Directory to list |

**Output:** Formatted directory listing with sizes, permissions, and modification times.

**Example:**
```
ls(path=.)
ls(path=/home/user/project)
```

---

### `grep`
Search for text patterns in files with regex support — uses **ugrep 7.5.0**.

**Parameters:**
| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `query` | string | ✅ | — | Pattern to search |
| `path` | string | ❌ | current dir | Directory/file to search |
| `type` | string | ❌ | all | File extension filter (e.g. `go`) |
| `regex` | boolean | ❌ | `false` | Treat query as regex |
| `max_results` | number | ❌ | all | Max matches to return |

**Output:** Matching lines with file paths and line numbers.

**Example:**
```
grep(query=function, type=go)
grep(query=func.*main, path=./src, regex=true)
```

**Notes:**
- Uses **ugrep** for all searches (`/usr/local/bin/grep` wrapper → ugrep 7.5.0).
- ugrep powers fuzzy search (`-Z`), compressed archives (`-z`), JSON/CSV output, file-type filters (`-t`), boolean operators (`--bool`), smart case (`-S`), and multi-line matching (`-U`).
- The tool internally invokes `grep -rn -I -F` (or `-E` for regex) with `-m 5000` per-file cap, excludes `.git`/`node_modules`/`vendor`, and caps total output at 1 MB.
- 10-second search timeout; exit code 1 (no match) is not an error.

---

## 🌐 Web Tools

### `web_search`
Search the web using DuckDuckGo with automatic fallback.

**Parameters:**
| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `query` | string | ✅ | — | Search query |
| `num_results` | number | ❌ | 5 | Number of results |

**Output:** `{ "query": string, "results": [{ "title": string, "url": string, "snippet": string }], "timeout_prediction": {...} }`

**Implementation:**
- Primary: DuckDuckGo HTML endpoint
- Fallback: DuckDuckGo Lite endpoint
- Uses curl for reliable DNS resolution
- **Timeout prediction:** fast DNS+TCP preflight probe (dead hosts fail in ~1.5s instead of hanging), plus adaptive `--max-time` per host derived from recorded latency/failure history (v2.1.0)

**Example:**
```
web_search(query=Go programming best practices, num_results=10)
```

---

### `web_fetch`
Fetch URL content as text.

**Parameters:**
| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `url` | string | ✅ | — | URL to fetch |
| `format` | string | ❌ | `text` | Output format (`text` or `json`) |

**Output:** `{ "url": string, "content": string|object, "timeout_prediction": {...} }`

**Limits:**
- Max response size: 1 MB
- Connection timeout: 5s (after preflight)
- Total timeout: 10s (adaptive per host)
- Dead/unreachable hosts are rejected by the **preflight probe** in ~1.5s

**Example:**
```
web_fetch(url=https://example.com)
web_fetch(url=https://api.github.com/repos/owner/repo, format=json)
```

---

## 🧠 Memory & Search Tools

### `semantic_search`
Meaning-based vector search over indexed content.

**Parameters:**
| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `query` | string | ✅ | — | Semantic query |
| `top_k` | number | ❌ | 5 | Number of results |
| `source` | string | ❌ | `all` | Source: `memory`, `index`, or `all` |

**Output:** `[{ "content": string, "category": string, "score": float, "tags": string[] }]`

**Example:**
```
semantic_search(query=discussions about code review)
```

---

### `semantic_index`
Add content to the semantic search index.

**Parameters:**
| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `content` | string | ✅ | — | Text to index |
| `category` | string | ❌ | `general` | Category label |
| `tags` | string | ❌ | — | Comma-separated tags |
| `metadata` | object | ❌ | — | Key-value pairs |

**Example:**
```
semantic_index(content=Go is a statically typed language, category=fact, tags=go,programming)
```

---

## 🛠 Dynamic Registration Tools

### `register_tool`
Dynamically create a new bash-wrapping tool the agent can call.

**Parameters:**
| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `name` | string | ✅ | — | Tool name |
| `description` | string | ✅ | — | Tool description |
| `command` | string | ❌* | — | Bash command to execute |
| `script` | string | ❌* | — | Inline bash script |
| `type` | string | ❌ | `tool` | Registration type: `tool` (dynamic tool) or `skill` (appears in skill list) |
| `category` | string | ❌ | `dynamic` | Tool category (overridden to `skill` when `type=skill`) |

*\* Either `command` or `script` is required (not needed for `type=skill`).*

**Environment variables passed to command:** `ELING_ARG_NAME=VALUE` for each argument key-value pair.

**Examples:**
```
register_tool(name=weather, description=Get weather for a city, command=curl -s "wttr.in/$1?format=%C+%t")
register_tool(name=system-health, description=Check system health, type=skill, command=top -bn1 | head -5)
```

---

## 📦 Backup & Analysis Tools

### `create_backup`
Create a timestamped ZIP backup of a project.

**Parameters:**
| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `project_dir` | string | ❌ | current dir | Project to backup |
| `backup_dir` | string | ❌ | current dir | Where to save backup |

**Auto-excluded patterns:** `eling`, `*.zip`, `.git/*`, `node_modules/*`, `vendor/*`, `.cache/*`, `__pycache__/*`, `*.pyc`, `.DS_Store`

**Output:** `{ "backup_path": string, "size_bytes": int, "size_human": string, "timestamp": string }`

**Example:**
```
create_backup()
create_backup(project_dir=/home/user/project, backup_dir=/tmp)
```

---

### `codebase-intelligence`
Meta-skill for codebase analysis (orchestrates grep, read, bash, semantic_search).

**Parameters:**
| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `query` | string | ✅ | Analysis query |

**Example:**
```
codebase-intelligence(query=Show me the architecture of this project)
```

---

## ⚙️ Configuration Tools

### `eling_setup` / `eling-setup`
Configure provider, API key, model, and agent settings.

**Parameters:**
| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `action` | string | ❌ | `list` | Action: `list`, `set`, `add-provider`, `remove-provider`, `set-api-key` |
| `provider` | string | varies | — | Provider name |
| `model` | string | varies | — | Model name |
| `api_key` | string | varies | — | API key |
| `base_url` | string | varies | — | Base URL |
| `system_prompt` | string | ❌ | — | Custom system prompt |
| `max_context` | string | ❌ | — | Max context tokens |
| `name` | string | varies | — | Provider name for add/remove |

**Examples:**
```
eling_setup(action=list)
eling_setup(action=set, provider=openai, model=gpt-4o, api_key=sk-..., base_url=https://api.openai.com/v1)
eling_setup(action=add-provider, name=groq, model=llama-3.3-70b, base_url=https://api.groq.com/openai/v1, set_default=true)
```

---

## 📋 Session Commands (TUI built-in)

| Command | Description |
|---------|-------------|
| `/help` | Show all commands |
| `/stats` | Agent statistics |
| `/tools` | List all tools |
| `/skills` | Show learned skills |
| `/memory` | Show recent memories |
| `/recall <query>` | Search memories |
| `/session` | Current session info |
| `/save` | Save state immediately |
| `/sessions` | List saved sessions |
| `/resume <name>` | Resume a session |
| `/providers` | List providers |
| `/provider <name>` | Switch provider |
| `/mcp` | MCP server status |
| `/mcp_connect <name> <cmd>` | Connect MCP server |
| `/evolve` | Trigger evolution |
| `/config` | Show configuration |
| `/clear` | Clear screen |
| `/quit` | Exit |

---

## 📦 Open Code Review Tools

### `ocr_review`
Run OpenCodeReview on git changes.

**Parameters:**
| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `repo` | string | ❌ | Repository path |
| `commit` | string | ❌ | Review one commit |
| `from` | string | ❌ | Base ref for range |
| `to` | string | ❌ | Target ref for range |
| `resume` | string | ❌ | Resume session ID |
| `background` | string | ❌ | Business context |
| `exclude` | string | ❌ | Exclusion patterns |
| `model` | string | ❌ | Override model |
| `concurrency` | number | ❌ | Max concurrent reviews |
| `timeout_minutes` | number | ❌ | Per-file timeout |
| `max_tools` | number | ❌ | Max tool rounds |
| `max_git_procs` | number | ❌ | Max git processes |
| `preview` | boolean | ❌ | Preview only |
| `tool_timeout_sec` | number | ❌ | 300 | Whole-call hard budget (default 5 min) |

**Limits:** The entire `ocr_review` call is killed after **5 minutes** by
default (override with `tool_timeout_sec`). Per-file progress is bounded by
`timeout_minutes`. This prevents a slow review LLM from hanging the turn —
the command is killed via `CommandContext` the moment the budget expires.

### `ocr_scan`
Full-file scan of entire directories.

**Parameters:**
| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `path` | string | ✅ | Directory/file to scan |
| `model` | string | ❌ | Override model |
| `exclude` | string | ❌ | Exclusion patterns |
| `concurrency` | number | ❌ | Max concurrent |
| `timeout_minutes` | number | ❌ | Per-file timeout |

### `ocr_health`
Check OCR CLI status.

**Parameters:** None

---

## 🔌 MCP Tools (Dynamic from Servers)

Connected MCP servers expose their tools automatically. Common examples:

**Filesystem MCP Server:**
```
mcp_read(path=/file)
mcp_write(path=/file, content=...)
mcp_edit(path=/file, old=..., new=...)
mcp_search(path=/dir, pattern=*.go)
```

**Database MCP Server:**
```
mcp_query(sql=SELECT * FROM users)
mcp_schema(table=users)
```
