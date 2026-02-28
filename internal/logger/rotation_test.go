package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRotation_TriggersAtMaxSize(t *testing.T) {
	dir := t.TempDir()
	// maxSize=100 bytes so we trigger rotation quickly
	l, err := New(dir, 100, 3)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	// Write enough entries to exceed 100 bytes
	for i := 0; i < 10; i++ {
		l.Info(fmt.Sprintf("log entry number %d with some padding text", i), Entry{})
	}

	// Check that rotation happened — devreap.log.1 should exist
	if _, err := os.Stat(filepath.Join(dir, "devreap.log.1")); os.IsNotExist(err) {
		t.Error("expected devreap.log.1 to exist after rotation")
	}

	// Current log file should still be writable
	l.Info("post-rotation entry", Entry{})
}

func TestRotation_ShiftsMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, 50, 3) // very small max to trigger multiple rotations
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	// Write many entries to trigger multiple rotations
	for i := 0; i < 50; i++ {
		l.Info(fmt.Sprintf("entry %d with padding to fill the buffer up quickly", i), Entry{})
	}

	// Should have multiple rotated files
	logFile := filepath.Join(dir, "devreap.log")
	if _, err := os.Stat(logFile); err != nil {
		t.Errorf("current log file should exist: %v", err)
	}
	if _, err := os.Stat(logFile + ".1"); os.IsNotExist(err) {
		t.Error("expected .1 rotated file")
	}
}

func TestRotation_DeletesExcessFiles(t *testing.T) {
	dir := t.TempDir()
	maxFiles := 2
	l, err := New(dir, 50, maxFiles) // keep only 2 rotated files
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	// Write a lot to force many rotations
	for i := 0; i < 100; i++ {
		l.Info(fmt.Sprintf("entry %d with padding to fill the rotation buffer faster", i), Entry{})
	}

	// Excess file beyond maxFiles should be removed
	excess := filepath.Join(dir, fmt.Sprintf("devreap.log.%d", maxFiles+1))
	if _, err := os.Stat(excess); !os.IsNotExist(err) {
		t.Errorf("expected excess log file %s to be deleted", excess)
	}
}

func TestRotation_NoRotationWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, 0, 0) // maxSize=0 disables rotation
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	for i := 0; i < 20; i++ {
		l.Info(fmt.Sprintf("entry %d", i), Entry{})
	}

	// No rotated files should exist
	if _, err := os.Stat(filepath.Join(dir, "devreap.log.1")); !os.IsNotExist(err) {
		t.Error("no rotation should happen when maxSize=0")
	}
}

func TestLoggerClose_NilFile(t *testing.T) {
	l := NewStdout()
	// Should not panic
	if err := l.Close(); err != nil {
		t.Errorf("Close on stdout logger should return nil: %v", err)
	}
}

func TestLoggerClose_DoubleClose(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, 1024, 3)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := l.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second close — file is already closed. This might error but should not panic.
	_ = l.Close()
}

func TestLoggerConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, 10*1024, 3)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			l.Info(fmt.Sprintf("concurrent entry %d", n), Entry{
				PID:     int32(n),
				Process: "test",
			})
		}(i)
	}
	wg.Wait()

	// Read the log and verify all entries are valid JSON lines
	data, err := os.ReadFile(filepath.Join(dir, "devreap.log"))
	if err != nil {
		t.Fatalf("reading log: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 100 {
		t.Errorf("expected at least 100 log lines, got %d", len(lines))
	}

	for i, line := range lines {
		if !strings.HasPrefix(line, "{") || !strings.HasSuffix(line, "}") {
			t.Errorf("line %d is not valid JSON: %s", i, line)
		}
	}
}

func TestLoggerNew_InvalidDir(t *testing.T) {
	_, err := New("/nonexistent/deeply/nested/path/that/cannot/exist", 1024, 3)
	if err == nil {
		t.Error("expected error for invalid log directory")
	}
}
