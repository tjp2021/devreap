package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	ReplaceBuiltinBlocklist bool          `yaml:"replace_builtin_blocklist"`
	Notify                  NotifyConfig  `yaml:"notify"`
	Patterns                []string      `yaml:"extra_patterns"` // paths to additional pattern files
	Weights                 WeightConfig  `yaml:"weights"`
	Hygiene                 HygieneConfig `yaml:"hygiene"`

	// LifecycleGrace is the per-class awake-time budget between a recorded
	// owner exit and orphan candidacy. It is a different setting from
	// GracePeriod, which stays the wait between SIGTERM and escalation, and
	// neither key is renamed.
	//
	// A user map merges with the built-in class table and never replaces it.
	LifecycleGrace map[string]time.Duration `yaml:"lifecycle_grace"`
}

// LifecycleWindow returns the awake-time budget for a class, and whether the
// class may ever be acted on. A missing class and a zero value both mean never.
func (c *Config) LifecycleWindow(class string) (time.Duration, bool) {
	window, known := c.LifecycleGrace[class]
	if !known || window <= 0 {
		return 0, false
	}
	return window, true
}

// NotifyConfig controls macOS notification behavior.
type NotifyConfig struct {
	Enabled bool `yaml:"enabled"`
}

// HygieneConfig names the machine-specific targets for the hygiene audit.
// Both lists are empty by default and an empty list skips its check. These
// are paths on one user's machine, so devreap ships no defaults for them.
type HygieneConfig struct {
	// GitRepos are paths to git repositories to scan for sensitive tracked
	// files. A leading "~/" is expanded. Empty skips the check.
	GitRepos []string `yaml:"git_repos"`
	// ZombieDotdirs are directory names directly under the home directory
	// that the user deleted and wants to stay deleted. devreap reports each
	// one that reappears. Empty skips the check.
	ZombieDotdirs []string `yaml:"zombie_dotdirs"`
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
		Weights:        DefaultWeights(),
		LifecycleGrace: DefaultLifecycleGrace(),
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

	// yaml.Unmarshal decodes a map key by key into whatever map the field
	// already holds, which would make merging an accident of the library rather
	// than a decision. Read the user's entries on their own and merge them
	// deliberately. See mergeLifecycleGrace for why this matters.
	var userFile struct {
		LifecycleGrace map[string]time.Duration `yaml:"lifecycle_grace"`
	}
	if err := yaml.Unmarshal(data, &userFile); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	merged, err := mergeLifecycleGrace(DefaultLifecycleGrace(), userFile.LifecycleGrace)
	if err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	cfg.LifecycleGrace = merged

	cfg.LogDir = expandPath(cfg.LogDir)
	cfg.PidFile = expandPath(cfg.PidFile)
	for i, repo := range cfg.Hygiene.GitRepos {
		cfg.Hygiene.GitRepos[i] = expandPath(repo)
	}

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

	for _, class := range knownLifecycleClasses() {
		window := c.LifecycleGrace[class]
		if window < 0 {
			return fmt.Errorf("lifecycle_grace for %q must not be negative, got %s", class, window)
		}
		if window > 24*time.Hour {
			return fmt.Errorf("lifecycle_grace for %q must be <= 24h, got %s", class, window)
		}
	}

	return nil
}

// mergeLifecycleGrace merges a user window map into the built-in class table.
// It never replaces the table.
//
// This repository has already paid for the other choice. The P0-8 finding in
// the 2026-08-19 verification record documents a yaml.Unmarshal into a slice
// that overwrote the whole value, so any user config containing a blocklist key
// silently discarded all 26 built-in protections including the database, shell,
// and window server entries. The test suite asserted that replacement as correct
// while the README promised the opposite. A map-valued key with per-class
// entries is the same hazard in a new place, so the rule is fixed here.
//
// Partial specification changes only what it names. A user cannot delete a class
// from the table, and an unknown class name is a load error rather than a silent
// addition.
func mergeLifecycleGrace(builtin, user map[string]time.Duration) (map[string]time.Duration, error) {
	merged := make(map[string]time.Duration, len(builtin))
	for class, window := range builtin {
		merged[class] = window
	}

	for class, window := range user {
		if _, known := builtin[class]; !known {
			return nil, fmt.Errorf("unknown lifecycle_grace class %q, known classes are %s",
				class, strings.Join(knownLifecycleClasses(), ", "))
		}
		if window < 0 {
			return nil, fmt.Errorf("lifecycle_grace for %q must not be negative, got %s", class, window)
		}
		if window > 0 && isNeverActionableClass(class) {
			// R8 keeps these two ineligible for as long as the condition holds,
			// so a positive window here could never take effect. Refusing it is
			// honest; accepting it would be a setting that does nothing.
			return nil, fmt.Errorf("lifecycle_grace for %q must stay never, because an %s process is never eligible at any age", class, class)
		}
		merged[class] = window
	}
	return merged, nil
}

func knownLifecycleClasses() []string {
	classes := make([]string, 0, len(DefaultLifecycleGrace()))
	for class := range DefaultLifecycleGrace() {
		classes = append(classes, class)
	}
	sort.Strings(classes)
	return classes
}

func isNeverActionableClass(class string) bool {
	for _, never := range NeverActionableClasses {
		if class == never {
			return true
		}
	}
	return false
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
