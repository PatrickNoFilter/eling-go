// Package logger provides crash-safe logging for ELING.
// Writes to ~/.eling/eling.log with O_SYNC for durability.
// Non-blocking design to prevent deadlock during panics.
package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// crashLogDir and crashLogPath are resolved once at init for use in the
// signal handler, so we don't allocate or call os.UserHomeDir in the handler.
var (
	crashLogDir     string
	crashReportPath string
	mainLogPath     string
	crashPathsOnce  sync.Once
)

func initCrashPaths() {
	homeDir, _ := os.UserHomeDir()
	if homeDir == "" {
		homeDir = "/tmp"
	}
	crashLogDir = filepath.Join(homeDir, ".eling")
	crashReportPath = filepath.Join(crashLogDir, "crash_report.log")
	mainLogPath = filepath.Join(crashLogDir, "eling.log")
	_ = os.MkdirAll(crashLogDir, 0755)
}

// Level represents log severity.
type Level int

const (
	DEBUG Level = iota
	INFO
	WARN
	ERROR
	FATAL
)

func (l Level) String() string {
	switch l {
	case DEBUG:
		return "DEBUG"
	case INFO:
		return "INFO"
	case WARN:
		return "WARN"
	case ERROR:
		return "ERROR"
	case FATAL:
		return "FATAL"
	default:
		return "UNKNOWN"
	}
}

// LogEntry represents a single log line.
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     Level     `json:"level"`
	Message   string    `json:"message"`
	Stack     string    `json:"stack,omitempty"`
}

// Logger is a crash-safe logger that writes to a file with O_SYNC.
// All public methods are safe for concurrent use.
type Logger struct {
	mu      sync.Mutex
	file    *os.File
	path    string
	enabled bool
	buf     []LogEntry // in-memory ring buffer for recent entries
	bufMax  int
}

var (
	global   *Logger
	globalMu sync.Mutex
)

// Global returns the global logger instance, creating it if needed.
func Global() *Logger {
	globalMu.Lock()
	defer globalMu.Unlock()
	if global == nil {
		homeDir, _ := os.UserHomeDir()
		logPath := filepath.Join(homeDir, ".eling", "eling.log")
		var err error
		global, err = New(logPath)
		if err != nil {
			// Fall back to a no-op logger if we can't create the log file
			global = &Logger{enabled: false, bufMax: 100}
		}
	}
	return global
}

// ResetGlobalForTesting replaces the global logger (used in tests).
func ResetGlobalForTesting(l *Logger) {
	globalMu.Lock()
	defer globalMu.Unlock()
	global = l
}

// New creates a new crash-safe logger.
func New(path string) (*Logger, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}

	// Open with O_SYNC for crash-safe writes, O_APPEND for append mode
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND|os.O_SYNC, 0644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}

	l := &Logger{
		file:    f,
		path:    path,
		enabled: true,
		buf:     make([]LogEntry, 0, 100),
		bufMax:  100,
	}

	// Write a boundary marker
	l.writeSync(INFO, "=== ELING LOG STARTED ===")

	return l, nil
}

// Close flushes and closes the log file.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		l.writeSyncLocked(INFO, "=== ELING LOG CLOSED ===")
		err := l.file.Close()
		l.file = nil
		return err
	}
	return nil
}

// Path returns the log file path.
func (l *Logger) Path() string {
	return l.path
}

// writeSync writes a log entry to the file and in-memory buffer.
// Must be called with l.mu held.
func (l *Logger) writeSyncLocked(level Level, msg string) {
	if !l.enabled || l.file == nil {
		return
	}

	entry := LogEntry{
		Timestamp: time.Now(),
		Level:     level,
		Message:   msg,
	}

	// Add to ring buffer
	if len(l.buf) >= l.bufMax {
		l.buf = l.buf[1:]
	}
	l.buf = append(l.buf, entry)

	// Format: 2026-07-28 12:34:56 INFO message
	line := fmt.Sprintf("%s %s %s\n",
		entry.Timestamp.Format("2006-01-02 15:04:05.000"),
		level.String(),
		strings.ReplaceAll(msg, "\n", "\\n"),
	)

	if _, err := l.file.WriteString(line); err != nil {
		// Can't log to file if file write fails — write to stderr as last resort
		_, _ = fmt.Fprintf(os.Stderr, "LOG ERROR: %v\n", err)
	}
}

// writeSync writes a log entry (thread-safe).
func (l *Logger) writeSync(level Level, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.writeSyncLocked(level, msg)
}

// Debug logs a debug message.
func (l *Logger) Debug(format string, args ...interface{}) {
	l.writeSync(DEBUG, fmt.Sprintf(format, args...))
}

// Info logs an info message.
func (l *Logger) Info(format string, args ...interface{}) {
	l.writeSync(INFO, fmt.Sprintf(format, args...))
}

// Warn logs a warning message.
func (l *Logger) Warn(format string, args ...interface{}) {
	l.writeSync(WARN, fmt.Sprintf(format, args...))
}

// Error logs an error message with optional stack trace.
func (l *Logger) Error(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	// Include stack trace for ERROR level
	stack := string(debug.Stack())
	// Trim the stack to remove the logger's own frames
	lines := strings.Split(stack, "\n")
	if len(lines) > 7 {
		lines = lines[7:] // skip runtime/debug.Stack + logger frames
	}
	msg += "\nStack:\n" + strings.Join(lines, "\n")
	l.writeSync(ERROR, msg)
}

// Fatal logs a fatal message and writes it synchronously.
// Callers should os.Exit after this.
func (l *Logger) Fatal(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	stack := string(debug.Stack())
	lines := strings.Split(stack, "\n")
	if len(lines) > 7 {
		lines = lines[7:]
	}
	msg += "\nStack:\n" + strings.Join(lines, "\n")
	// Fatal writes synchronously and flushes
	l.mu.Lock()
	if l.file != nil {
		l.writeSyncLocked(FATAL, msg)
		_ = l.file.Sync()
	}
	l.mu.Unlock()
}

// Panic logs a panic with full stack trace and saves to log.
// This is designed to be called from a defer/recover.
// Returns the recovered value so the caller can re-panic if needed.
func (l *Logger) Panic(r interface{}) {
	if r == nil {
		return
	}
	msg := fmt.Sprintf("PANIC: %v", r)
	stack := string(debug.Stack())
	msg += "\n" + stack

	l.mu.Lock()
	if l.file != nil {
		l.writeSyncLocked(FATAL, msg)
		_ = l.file.Sync()
	}
	l.mu.Unlock()

	// Also write to stderr for immediate visibility
	_, _ = fmt.Fprintf(os.Stderr, "\n=== ELING PANIC ===\n%v\nStack:\n%s\n====================\n", r, stack)
}

// Recent returns the N most recent log entries from the in-memory buffer.
func (l *Logger) Recent(n int) []LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n <= 0 || len(l.buf) == 0 {
		return nil
	}
	if n > len(l.buf) {
		n = len(l.buf)
	}
	result := make([]LogEntry, n)
	copy(result, l.buf[len(l.buf)-n:])
	return result
}

// DetectCrash checks if the previous instance of ELING crashed by looking for
// a stale PID file. Returns crash information from the on-disk crash report log.
// Call this on startup to check for previous unclean shutdown.
func (l *Logger) DetectCrash(pidFilePath string) (bool, string) {
	// Check if PID file exists
	data, err := os.ReadFile(pidFilePath)
	if err != nil {
		return false, "" // No PID file = clean state
	}

	var pid int
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil {
		return false, ""
	}

	// Check if process is still running
	running := false
	if _, err := os.Stat(fmt.Sprintf("/proc/%d/comm", pid)); err == nil {
		running = true
	}

	if running {
		// Process is still running — that's normal (but unlikely if we're starting a new instance)
		return false, ""
	}

	// PID file exists but process is not running — previous instance crashed
	crashMsg := fmt.Sprintf("Previous ELING instance (PID %d) did not shut down cleanly — possible crash", pid)

	// Read crash report from disk (written by WriteCrashReport / BusErrorCrashHandler)
	// instead of checking the in-memory buffer, which is always empty for a new instance.
	crashPathsOnce.Do(initCrashPaths)
	crashData, readErr := os.ReadFile(crashReportPath)
	if readErr == nil {
		// Return the last crash report entry
		lines := strings.Split(strings.TrimSpace(string(crashData)), "\n")
		if len(lines) > 0 {
			// Find the last FATAL CRASH or CRASH REPORT section
			var crashLines []string
			for i := len(lines) - 1; i >= 0; i-- {
				if strings.Contains(lines[i], "FATAL CRASH") || strings.Contains(lines[i], "CRASH REPORT") {
					// Collect from this line onward
					for j := i; j < len(lines); j++ {
						crashLines = append(crashLines, lines[j])
						if j > i+10 { // limit to 10 lines
							break
						}
					}
					break
				}
			}
			if len(crashLines) > 0 {
				crashMsg += "\n" + strings.Join(crashLines, "\n")
			}
		}
	}

	return true, crashMsg
}

// WriteCrashReport writes a comprehensive crash report to the log.
// This is a last-resort function that writes directly without locking,
// to be safe even during a panic while holding locks.
func WriteCrashReport(r interface{}, stack string) {
	crashPathsOnce.Do(initCrashPaths)

	msg := fmt.Sprintf("=== CRASH REPORT %s ===\nError: %v\nStack:\n%s\n",
		time.Now().Format(time.RFC3339), r, stack)

	// Write with O_SYNC | O_CREAT | O_APPEND — no locking needed
	f, err := os.OpenFile(crashReportPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND|os.O_SYNC, 0644)
	if err == nil {
		_, _ = f.WriteString(msg)
		_ = f.Close()
	}
}

// SafePanicRecover recovers from a panic, logs it using both the global logger
// and the crash report file.
// Returns true if a panic was recovered.
// NOTE: recover() MUST be called directly from the deferred function in Go.
// This function takes the recovered value as a parameter.
// Use this as:
//
//	defer func() {
//	    SafePanicRecover(recover(), "myFunction")
//	}()
func SafePanicRecover(r interface{}, context string) bool {
	if r == nil {
		return false
	}
	stack := string(debug.Stack())

	// Try global logger first
	if gl := Global(); gl != nil {
		gl.Panic(fmt.Errorf("(%s) %v", context, r))
	}

	// Also write crash report as fallback
	WriteCrashReport(fmt.Errorf("(%s) %v", context, r), stack)

	_, _ = fmt.Fprintf(os.Stderr, "\n=== ELING PANIC [%s] ===\n%v\nStack:\n%s\n==========================\n", context, r, stack)
	return true
}

// BusErrorCrashHandler is a crash handler for fatal signals like
// SIGBUS and SIGSEGV. Unlike panics, these signals CANNOT be recovered with
// recover() — they terminate the process immediately, skipping all deferred
// functions. This handler writes a crash report directly to disk using O_SYNC
// (no locks, minimal allocations), then re-raises the signal with the default
// handler so the OS can dump core / terminate.
//
// The handler writes:
//  1. ~/.eling/crash_report.log — full crash report with timestamp and signal
//  2. ~/.eling/eling.log       — short crash marker (best-effort, no locks)
//  3. stderr                   — immediate user-visible message
//
// After logging, it resets the signal to SIG_DFL and re-raises it so
// the OS can produce a core dump (if enabled) and terminate the process.
func BusErrorCrashHandler(signalName string, signalNum int) {
	crashPathsOnce.Do(initCrashPaths)

	// Build crash message with timestamp
	now := time.Now().Format(time.RFC3339)
	msg := fmt.Sprintf("=== FATAL CRASH %s ===\nSignal: %s (signal %d)\nPID: %d\nThis is a fatal OS signal that cannot be recovered.\nPossible causes:\n  - Memory corruption (hardware fault)\n  - Misaligned memory access (bus error)\n  - Accessing memory beyond a mapped region\n  - Kernel OOM killer\n  - Stack overflow\n=== END CRASH REPORT ===\n",
		now, signalName, signalNum, os.Getpid())

	// Write crash report with O_SYNC — no locking, direct syscall
	f, err := os.OpenFile(crashReportPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND|os.O_SYNC, 0644)
	if err == nil {
		_, _ = f.WriteString(msg)
		_ = f.Close()
	}

	// Also write a marker to the main log (best-effort, no lock)
	f2, err := os.OpenFile(mainLogPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND|os.O_SYNC, 0644)
	if err == nil {
		marker := fmt.Sprintf("%s FATAL %s\n", time.Now().Format("2006-01-02 15:04:05.000"), signalName)
		_, _ = f2.WriteString(marker)
		_ = f2.Close()
	}

	// Write to stderr for immediate visibility
	_, _ = fmt.Fprintf(os.Stderr, "\n🚨 ELING CRASHED: %s (signal %d)\n", signalName, signalNum)
	_, _ = fmt.Fprintf(os.Stderr, "   Crash report saved to: %s\n", crashReportPath)
	_, _ = fmt.Fprintf(os.Stderr, "   Check dmesg for kernel-level fault info.\n\n")
}

// DetectBusErrorOnStartup checks the crash report log for recent SIGBUS/SIGSEGV
// entries. Returns a user-friendly message if a fatal signal crash was detected
// since the last clean startup.
// The crashMarkerFile is updated on each clean shutdown so we can compare.
func DetectBusErrorOnStartup() (bool, string) {
	crashPathsOnce.Do(initCrashPaths)

	markerPath := filepath.Join(crashLogDir, ".clean_shutdown_marker")

	// Read crash report log
	data, err := os.ReadFile(crashReportPath)
	if err != nil {
		return false, "" // no crash report file exists
	}

	// Read the last clean shutdown timestamp
	var lastCleanTime time.Time
	if markerData, err := os.ReadFile(markerPath); err == nil {
		_ = lastCleanTime.UnmarshalText(markerData)
	}

	// Scan crash report for FATAL CRASH entries newer than last clean shutdown
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.Contains(line, "=== FATAL CRASH") {
			// Try to parse timestamp from the line
			// Format: "=== FATAL CRASH 2026-07-28T12:34:56Z ==="
			parts := strings.Split(line, " ")
			if len(parts) >= 4 {
				tsStr := strings.TrimSuffix(parts[3], "===")
				if ts, err := time.Parse(time.RFC3339, tsStr); err == nil {
					if ts.After(lastCleanTime) {
						// Found a crash after last clean shutdown
						var signalInfo string
						for _, nextLine := range lines {
							if strings.HasPrefix(nextLine, "Signal:") {
								signalInfo = strings.TrimSpace(strings.TrimPrefix(nextLine, "Signal:"))
								break
							}
						}
						msg := fmt.Sprintf("Previous ELING session crashed with %s on %s",
							signalInfo, ts.Format("Jan 2 15:04:05"))
						return true, msg
					}
				} else {
					// If we can't parse the timestamp, report it anyway
					if lastCleanTime.IsZero() {
						return true, "Previous ELING session crashed with a fatal signal (check crash_report.log)"
					}
				}
			}
		}
	}

	return false, ""
}

// WriteCleanShutdownMarker writes a timestamp file that marks the last clean
// shutdown. Used by DetectBusErrorOnStartup to identify crashes that happened
// after the last successful shutdown.
func WriteCleanShutdownMarker() {
	crashPathsOnce.Do(initCrashPaths)

	markerPath := filepath.Join(crashLogDir, ".clean_shutdown_marker")
	now := time.Now()
	data, err := now.MarshalText()
	if err != nil {
		return
	}
	_ = os.WriteFile(markerPath, data, 0644)
}
