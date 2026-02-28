package logger

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// levelNames maps Level to its JSON string representation.
var levelNames = [...]string{
	LevelDebug: "debug",
	LevelInfo:  "info",
	LevelWarn:  "warn",
	LevelError: "error",
}

func (l Level) String() string {
	if int(l) < len(levelNames) {
		return levelNames[l]
	}
	return "unknown"
}

// ParseLevel converts a string level name to Level.
func ParseLevel(s string) Level {
	switch s {
	case "debug":
		return LevelDebug
	case "info":
		return LevelInfo
	case "warn":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

type Entry struct {
	Time    string             `json:"time"`
	Level   string             `json:"level"`
	Message string             `json:"msg"`
	PID     int32              `json:"pid,omitempty"`
	Process string             `json:"process,omitempty"`
	Cmdline string             `json:"cmdline,omitempty"`
	Pattern string             `json:"pattern,omitempty"`
	Score   float64            `json:"score,omitempty"`
	Signals map[string]float64 `json:"signals,omitempty"`
	Action  string             `json:"action,omitempty"`
	Error   string             `json:"error,omitempty"`
}

type Logger struct {
	mu       sync.Mutex
	writer   io.Writer
	level    Level
	file     *os.File
	logPath  string
	maxSize  int64
	maxFiles int
}

func New(logDir string, maxSize int64, maxFiles int) (*Logger, error) {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("creating log dir: %w", err)
	}

	logPath := fmt.Sprintf("%s/devreap.log", logDir)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("opening log file: %w", err)
	}

	return &Logger{
		writer:   f,
		level:    LevelInfo,
		file:     f,
		logPath:  logPath,
		maxSize:  maxSize,
		maxFiles: maxFiles,
	}, nil
}

func NewStdout() *Logger {
	return &Logger{
		writer: os.Stdout,
		level:  LevelInfo,
	}
}

func (l *Logger) SetLevel(level Level) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = level
}

func (l *Logger) log(level Level, msg string, entry Entry) {
	if level < l.level {
		return
	}

	entry.Time = time.Now().UTC().Format(time.RFC3339)
	entry.Level = level.String()
	entry.Message = msg

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file != nil {
		l.checkRotation()
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	fmt.Fprintf(l.writer, "%s\n", data)
}

func (l *Logger) Info(msg string, entry ...Entry) {
	e := Entry{}
	if len(entry) > 0 {
		e = entry[0]
	}
	l.log(LevelInfo, msg, e)
}

func (l *Logger) Warn(msg string, entry ...Entry) {
	e := Entry{}
	if len(entry) > 0 {
		e = entry[0]
	}
	l.log(LevelWarn, msg, e)
}

func (l *Logger) Error(msg string, entry ...Entry) {
	e := Entry{}
	if len(entry) > 0 {
		e = entry[0]
	}
	l.log(LevelError, msg, e)
}

func (l *Logger) Debug(msg string, entry ...Entry) {
	e := Entry{}
	if len(entry) > 0 {
		e = entry[0]
	}
	l.log(LevelDebug, msg, e)
}

func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		return l.file.Close()
	}
	return nil
}
