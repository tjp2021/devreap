package logger

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestLevelOrdering(t *testing.T) {
	if LevelDebug >= LevelInfo {
		t.Error("debug should be < info")
	}
	if LevelInfo >= LevelWarn {
		t.Error("info should be < warn")
	}
	if LevelWarn >= LevelError {
		t.Error("warn should be < error")
	}
}

func TestLevelString(t *testing.T) {
	tests := []struct {
		level Level
		want  string
	}{
		{LevelDebug, "debug"},
		{LevelInfo, "info"},
		{LevelWarn, "warn"},
		{LevelError, "error"},
	}
	for _, tt := range tests {
		if got := tt.level.String(); got != tt.want {
			t.Errorf("Level(%d).String() = %q, want %q", tt.level, got, tt.want)
		}
	}
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input string
		want  Level
	}{
		{"debug", LevelDebug},
		{"info", LevelInfo},
		{"warn", LevelWarn},
		{"error", LevelError},
		{"unknown", LevelInfo}, // defaults to info
	}
	for _, tt := range tests {
		if got := ParseLevel(tt.input); got != tt.want {
			t.Errorf("ParseLevel(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestLoggerFiltersDebugAtInfoLevel(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{
		writer: &buf,
		level:  LevelInfo,
	}

	l.Debug("should be filtered")
	l.Info("should appear")

	output := buf.String()
	if strings.Contains(output, "should be filtered") {
		t.Error("debug message should be filtered when level is info")
	}
	if !strings.Contains(output, "should appear") {
		t.Error("info message should appear when level is info")
	}
}

func TestLoggerOutputIsValidJSON(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{
		writer: &buf,
		level:  LevelInfo,
	}

	l.Info("test message", Entry{
		PID:     1234,
		Process: "node",
		Score:   0.85,
		Action:  "kill",
	})

	var entry Entry
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("log output is not valid JSON: %v\nOutput: %s", err, buf.String())
	}

	if entry.Message != "test message" {
		t.Errorf("expected msg 'test message', got %q", entry.Message)
	}
	if entry.Level != "info" {
		t.Errorf("expected level 'info', got %q", entry.Level)
	}
	if entry.PID != 1234 {
		t.Errorf("expected PID 1234, got %d", entry.PID)
	}
	if entry.Time == "" {
		t.Error("expected non-empty time field")
	}
}

func TestLoggerErrorLevel(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{
		writer: &buf,
		level:  LevelError,
	}

	l.Info("filtered")
	l.Warn("filtered")
	l.Error("visible")

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 1 {
		t.Errorf("expected 1 line at error level, got %d: %s", len(lines), output)
	}
}

func TestNewStdoutLogger(t *testing.T) {
	l := NewStdout()
	if l == nil {
		t.Fatal("NewStdout returned nil")
	}
	if l.level != LevelInfo {
		t.Errorf("expected default level info, got %d", l.level)
	}
}

func TestLoggerSetLevel(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{
		writer: &buf,
		level:  LevelInfo,
	}

	l.SetLevel(LevelDebug)
	l.Debug("now visible")

	if !strings.Contains(buf.String(), "now visible") {
		t.Error("debug message should appear after SetLevel(debug)")
	}
}

func TestLoggerAllLevelsWithEntry(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{writer: &buf, level: LevelDebug}

	entry := Entry{PID: 42, Process: "test"}

	l.Debug("debug msg", entry)
	l.Info("info msg", entry)
	l.Warn("warn msg", entry)
	l.Error("error msg", entry)

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 4 {
		t.Errorf("expected 4 log lines, got %d", len(lines))
	}

	// Verify each line has the right level
	expected := []string{"debug", "info", "warn", "error"}
	for i, line := range lines {
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("line %d not valid JSON: %v", i, err)
			continue
		}
		if e.Level != expected[i] {
			t.Errorf("line %d: expected level %q, got %q", i, expected[i], e.Level)
		}
		if e.PID != 42 {
			t.Errorf("line %d: expected PID 42, got %d", i, e.PID)
		}
	}
}

func TestLoggerAllLevelsWithoutEntry(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{writer: &buf, level: LevelDebug}

	// No entry argument — should use empty Entry
	l.Debug("d")
	l.Info("i")
	l.Warn("w")
	l.Error("e")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 4 {
		t.Errorf("expected 4 lines, got %d", len(lines))
	}
}

func TestLevelStringUnknown(t *testing.T) {
	l := Level(99)
	if l.String() != "unknown" {
		t.Errorf("expected 'unknown' for invalid level, got %q", l.String())
	}
}
