package patterns

import "time"

// SignalStrategy defines how a process should be terminated.
type SignalStrategy string

const (
	SignalDefault SignalStrategy = "default"  // SIGTERM → SIGKILL
	SignalINT     SignalStrategy = "sigint"   // SIGINT → SIGTERM → SIGKILL (ffmpeg)
	SignalTERM    SignalStrategy = "sigterm"  // SIGTERM → SIGKILL
)

// Pattern defines a class of processes to monitor for orphan detection.
type Pattern struct {
	Name        string        `yaml:"name"`
	Category    string        `yaml:"category"`
	Command     string        `yaml:"command"`     // regex for process name
	Args        string        `yaml:"args"`        // regex for process args (optional)
	MaxDuration time.Duration `yaml:"max_duration"`
	Signal      SignalStrategy `yaml:"signal"`
	GracePeriod time.Duration `yaml:"grace_period"` // override global
	Description string        `yaml:"description"`
}

// PatternFile is the top-level structure of a YAML pattern definition file.
type PatternFile struct {
	Patterns []Pattern `yaml:"patterns"`
}
