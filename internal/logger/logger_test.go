package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoggerBasic(t *testing.T) {
	// Create logger in temp directory
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "test.log")

	l, err := New(logPath)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer l.Close()

	// Write some messages
	l.Info("Info message: %s", "hello")
	l.Warn("Warning message: %d", 42)
	l.Error("Error message: %v", os.ErrNotExist)
	l.Debug("Debug message")

	// Verify the file was written
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("Failed to read log: %v", err)
	}
	content := string(data)

	// Check for expected content
	if !strings.Contains(content, "INFO") {
		t.Error("Missing INFO level")
	}
	if !strings.Contains(content, "WARN") {
		t.Error("Missing WARN level")
	}
	if !strings.Contains(content, "ERROR") {
		t.Error("Missing ERROR level")
	}
	if !strings.Contains(content, "ELING LOG STARTED") {
		t.Error("Missing startup marker")
	}
}

func TestLoggerPanic(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "panic.log")

	l, err := New(logPath)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer l.Close()

	// Test panic logging
	l.Panic("test panic value")

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("Failed to read log: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "PANIC") {
		t.Error("Missing PANIC in log")
	}
	if !strings.Contains(content, "Stack") {
		t.Error("Missing stack trace in log")
	}
}

func TestRecentEntries(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "recent.log")

	l, err := New(logPath)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer l.Close()

	// Write 10 messages
	for i := 0; i < 10; i++ {
		l.Info("Message %d", i)
	}

	// Get recent 5
	recent := l.Recent(5)
	if len(recent) != 5 {
		t.Fatalf("Expected 5 recent entries, got %d", len(recent))
	}

	if !strings.Contains(recent[0].Message, "Message 5") {
		t.Errorf("Expected first recent to be Message 5, got: %s", recent[0].Message)
	}
}

func TestCrashDetection(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "crash.log")
	pidFile := filepath.Join(tmpDir, "test.pid")

	l, err := New(logPath)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer l.Close()

	// Reset global for testing
	ResetGlobalForTesting(l)

	// No PID file → no crash
	crashed, _ := l.DetectCrash(pidFile)
	if crashed {
		t.Error("Expected no crash when PID file doesn't exist")
	}

	// Write a stale PID file
	if err := os.WriteFile(pidFile, []byte("99999"), 0644); err != nil {
		t.Fatalf("Failed to write PID file: %v", err)
	}

	// PID file exists but process doesn't → crash detected
	crashed, reason := l.DetectCrash(pidFile)
	if !crashed {
		t.Error("Expected crash detection with stale PID file")
	}
	if !strings.Contains(reason, "99999") {
		t.Error("Expected PID in crash reason")
	}
}

func TestWriteCrashReport(t *testing.T) {
	tmpDir := t.TempDir()

	// Override home dir temporarily
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	// Write crash report
	WriteCrashReport("test error", "test stack trace")

	// Check crash report file
	crashPath := filepath.Join(tmpDir, ".eling", "crash_report.log")
	data, err := os.ReadFile(crashPath)
	if err != nil {
		t.Fatalf("Failed to read crash report: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "CRASH REPORT") {
		t.Error("Missing crash report header")
	}
	if !strings.Contains(content, "test error") {
		t.Error("Missing error info in crash report")
	}
}

func TestSafePanicRecover(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "safe.log")

	l, err := New(logPath)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}
	defer l.Close()

	ResetGlobalForTesting(l)

	// Test that SafePanicRecover recovers from panic
	panicked := true
	func() {
		defer func() {
			panicked = SafePanicRecover(recover(), "testContext")
		}()
		panic("test panic")
	}()

	if !panicked {
		t.Error("Expected SafePanicRecover to return true")
	}

	// Test with no panic
	panicked = false
	func() {
		defer func() {
			panicked = SafePanicRecover(recover(), "testContext")
		}()
	}()

	if panicked {
		t.Error("Expected SafePanicRecover to return false when no panic")
	}
}
