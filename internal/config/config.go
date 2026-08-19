package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all devreap configuration, loaded from YAML with sensible defaults.
type Config struct {
	ScanInterval  time.Duration `yaml:"scan_interval"`
	KillThreshold float64       `yaml:"kill_threshold"`
	GracePeriod   time.Duration `yaml:"grace_period"`
	DryRun        bool          `yaml:"dry_run"`
	LogDir        string        `yaml:"log_dir"`
	MaxLogSize    int64         `yaml:"max_log_size"`
	MaxLogFiles   int           `yaml:"max_log_files"`
	PidFile       string        `yaml:"pid_file"`
	Blocklist     []string      `yaml:"blocklist"`
	Allowlist     []string      `yaml:"allowlist"`
	// ReplaceBuiltinBlocklist makes a user blocklist replace the built-in one
	// instead of adding to it. Default false: user entries are added to the
	// built-in list, so writing a blocklist can never remove protection for
	// postgres, sshd, WindowServer, launchd, and the rest.
	ReplaceBuiltinBlocklist bool `yaml:"replace_builtin_blocklist"`
	Notify        NotifyConfig  `yaml:"notify"`
	Patterns      []string      `yaml:"extra_patterns"` // paths to additional pattern files
	Weights       WeightConfig  `yaml:"weights"`
}

// NotifyConfig controls macOS notification behavior.
type NotifyConfig struct {
	Enabled bool `yaml:"enabled"`
}

// WeightConfig defines the scoring weights for each orphan detection signal.
type WeightConfig struct {
	PPIDIsInit    float64 `yaml:"ppid_is_init"`
	NoTTY         float64 `yaml:"no_tty"`
	ParentIDEDead float64 `yaml:"parent_ide_dead"`
	ExceededDur   float64 `yaml:"exceeded_duration"`
	// HasListener is retained so existing config files still parse, but the
	// scorer ignores it. A listening socket means a process is serving, not
	// that it was orphaned. Setting this has no effect.
	//
	// Deprecated: no longer contributes to the orphan score.
	HasListener float64 `yaml:"has_listener"`
}

// Default returns a Config with all default values.
func Default() *Config {
	return &Config{
		ScanInterval:  DefaultScanInterval,
		KillThreshold: DefaultKillThreshold,
		GracePeriod:   DefaultGracePeriod,
		DryRun:        DefaultDryRun,
		LogDir:        expandPath(DefaultLogDir),
		MaxLogSize:    DefaultMaxLogSize,
		MaxLogFiles:   DefaultMaxLogFiles,
		PidFile:       expandPath(DefaultPidFile),
		Blocklist:     DefaultBlocklist,
		Notify: NotifyConfig{
			Enabled: DefaultNotifyEnabled,
		},
		Weights: DefaultWeights(),
	}
}

// DefaultWeights returns the default signal weights.
func DefaultWeights() WeightConfig {
	return WeightConfig{
		PPIDIsInit:    0.4,
		NoTTY:         0.15,
		ParentIDEDead: 0.3,
		ExceededDur:   0.25,
		HasListener:   0, // deprecated, ignored by the scorer
	}
}

// Load reads a YAML config file, merging with defaults. Missing file is not an error.
func Load(path string) (*Config, error) {
	cfg := Default()

	path = expandPath(path)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil // no config file is fine
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}

	// Save defaults before unmarshal so we can detect what the user actually set
	defaultWeights := DefaultWeights()

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	// yaml.Unmarshal overwrites the whole slice when the file has a
	// "blocklist" key, which silently dropped every built-in protection.
	// Add the user's entries to the built-in list unless they explicitly
	// asked to replace it.
	if !cfg.ReplaceBuiltinBlocklist {
		cfg.Blocklist = mergeBlocklist(DefaultBlocklist, cfg.Blocklist)
	}

	// Merge weights: if the user's config included a "weights" key,
	// yaml.Unmarshal will have zeroed any unset fields. Restore defaults
	// for any field the user didn't explicitly set.
	// We detect this by checking if the raw YAML actually contained each key.
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err == nil {
		if weightsRaw, ok := raw["weights"]; ok {
			if wm, ok := weightsRaw.(map[string]interface{}); ok {
				if _, set := wm["ppid_is_init"]; !set {
					cfg.Weights.PPIDIsInit = defaultWeights.PPIDIsInit
				}
				if _, set := wm["no_tty"]; !set {
					cfg.Weights.NoTTY = defaultWeights.NoTTY
				}
				if _, set := wm["parent_ide_dead"]; !set {
					cfg.Weights.ParentIDEDead = defaultWeights.ParentIDEDead
				}
				if _, set := wm["exceeded_duration"]; !set {
					cfg.Weights.ExceededDur = defaultWeights.ExceededDur
				}
				if _, set := wm["has_listener"]; !set {
					cfg.Weights.HasListener = defaultWeights.HasListener
				}
			}
		}
	}

	cfg.LogDir = expandPath(cfg.LogDir)
	cfg.PidFile = expandPath(cfg.PidFile)

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return cfg, nil
}

// Validate checks config values are within acceptable ranges.
func (c *Config) Validate() error {
	if c.ScanInterval < time.Second {
		return fmt.Errorf("scan_interval must be >= 1s, got %s", c.ScanInterval)
	}
	if c.ScanInterval > 24*time.Hour {
		return fmt.Errorf("scan_interval must be <= 24h, got %s", c.ScanInterval)
	}
	if c.KillThreshold < 0.1 || c.KillThreshold > 1.0 {
		return fmt.Errorf("kill_threshold must be between 0.1 and 1.0, got %.2f", c.KillThreshold)
	}
	if c.GracePeriod < time.Second {
		return fmt.Errorf("grace_period must be >= 1s, got %s", c.GracePeriod)
	}
	if c.MaxLogSize < 1024 {
		return fmt.Errorf("max_log_size must be >= 1024 bytes, got %d", c.MaxLogSize)
	}
	if c.MaxLogFiles < 1 {
		return fmt.Errorf("max_log_files must be >= 1, got %d", c.MaxLogFiles)
	}

	// Validate weights are non-negative and <= 1.0
	weights := []struct {
		name  string
		value float64
	}{
		{"ppid_is_init", c.Weights.PPIDIsInit},
		{"no_tty", c.Weights.NoTTY},
		{"parent_ide_dead", c.Weights.ParentIDEDead},
		{"exceeded_duration", c.Weights.ExceededDur},
		{"has_listener", c.Weights.HasListener},
	}
	for _, w := range weights {
		if w.value < 0 || w.value > 1.0 {
			return fmt.Errorf("weight %q must be between 0 and 1.0, got %.2f", w.name, w.value)
		}
	}

	return nil
}

// mergeBlocklist returns the built-in entries followed by any user entries
// that are not already present. Comparison is case-insensitive because the
// blocklist is matched against process names. Neither input is mutated.
func mergeBlocklist(builtin, user []string) []string {
	merged := make([]string, 0, len(builtin)+len(user))
	seen := make(map[string]struct{}, len(builtin)+len(user))

	for _, list := range [][]string{builtin, user} {
		for _, entry := range list {
			if entry == "" {
				continue
			}
			key := strings.ToLower(entry)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, entry)
		}
	}
	return merged
}

func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}
