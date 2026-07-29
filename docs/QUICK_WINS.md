# Quick Wins - Recent Improvements

## 1. Fixed REPL Mode Commands

**Before:** The REPL mode (non-TTY) only supported `/quit`. Any other command showed "Commands: /quit".

**After:** Full command support in REPL mode:
- `/help`, `/stats`, `/tools`, `/skills`, `/memory`, `/recall <query>`, `/save`, `/session`, `/providers`, `/clear`, `/quit`

## 2. Graceful Shutdown

**Added:** Signal handling for SIGINT/SIGTERM that saves state before exiting.

## 3. Auto-Save Timer

**Added:** Background goroutine that saves state every 5 minutes when `session.auto_save` is true (default).

## 4. Fixed `math_eval` Skill

**Before:** Only parsed a single float value (e.g., `fmt.Sscanf("42", "%f", &result)`), didn't actually evaluate expressions.

**After:** Full safe arithmetic expression evaluator using Go's AST parser:
- Supports `+`, `-`, `*`, `/`, `%`, parentheses
- Supports math functions: `abs()`, `sqrt()`, `sin()`, `cos()`, `round()`, `floor()`, `ceil()`
- Supports constants: `pi`, `e`
- Safe: only allows mathematical operations, no code execution

## 5. Added `/sessions` Command

**Added:** Both TUI and REPL now support listing saved sessions with `/sessions`.

## 6. Added `create_backup` Tool

**New tool:** Creates a timestamped ZIP backup of the eling project, excluding compiled binaries, existing zips, .git, node_modules, vendor, cache directories, and other non-essential files.

## 7. Added `codebase-intelligence` Skill

**New tool:** Meta-skill that documents the codebase analysis tools available via the codebase-memory-mcp integration.

## 8. Fixed Memory Race Condition

**Before:** `StartDecay` could panic by closing an already-closed channel. `StopDecay` could panic on double-close.

**After:** Both `StartDecay` and `StopDecay` use a safe close pattern with a select guard to prevent double-close panics.

## 9. Created Documentation

**Added:** `docs/README.md` with architecture overview and project structure documentation.
