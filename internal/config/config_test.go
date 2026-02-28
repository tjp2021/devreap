package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := Default()

	if cfg.KillThreshold != 0.6 {
		t.Errorf("expected threshold 0.6, got %f", cfg.KillThreshold)
	}

	if cfg.ScanInterval != DefaultScanInterval {
		t.Errorf("expected scan interval %v, got %v", DefaultScanInterval, cfg.ScanInterval)
	}

	if cfg.Weights.PPIDIsInit != 0.4 {
		t.Errorf("expected PPID weight 0.4, got %f", cfg.Weights.PPIDIsInit)
	}

	if cfg.Weights.ParentIDEDead != 0.3 {
		t.Errorf("expected IDE dead weight 0.3, got %f", cfg.Weights.ParentIDEDead)
	}

	if len(cfg.Blocklist) == 0 {
		t.Error("expected non-empty default blocklist")
	}
}

func TestDefaultConfigValidates(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Errorf("default config should validate, got: %v", err)
	}
}

func TestLoadMissingConfig(t *testing.T) {
	cfg, err := Load("/nonexistent/config.yaml")
	if err != nil {
		t.Fatalf("expected no error for missing config, got: %v", err)
	}

	// should return defaults
	if cfg.KillThreshold != 0.6 {
		t.Errorf("expected default threshold 0.6, got %f", cfg.KillThreshold)
	}
}

func TestLoadValidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := `
kill_threshold: 0.8
scan_interval: 60s
dry_run: true
weights:
  ppid_is_init: 0.5
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}

	if cfg.KillThreshold != 0.8 {
		t.Errorf("expected threshold 0.8, got %f", cfg.KillThreshold)
	}

	if !cfg.DryRun {
		t.Error("expected dry_run true")
	}

	if cfg.Weights.PPIDIsInit != 0.5 {
		t.Errorf("expected PPID weight 0.5, got %f", cfg.Weights.PPIDIsInit)
	}
}

func TestLoadInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	if err := os.WriteFile(path, []byte("not: valid: yaml: [[["), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil {
		t.Error("expected error for invalid YAML, got nil")
	}
}

// TestPartialWeightMerge verifies that setting ONE weight doesn't zero out the others.
// This was a real bug: yaml.Unmarshal replaces the entire struct, zeroing unset fields.
func TestPartialWeightMerge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// User only sets ppid_is_init — all other weights should remain at defaults
	content := `
weights:
  ppid_is_init: 0.5
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}

	defaults := DefaultWeights()

	if cfg.Weights.PPIDIsInit != 0.5 {
		t.Errorf("expected ppid_is_init = 0.5, got %f", cfg.Weights.PPIDIsInit)
	}
	if cfg.Weights.NoTTY != defaults.NoTTY {
		t.Errorf("expected no_tty = %f (default), got %f (zeroed!)", defaults.NoTTY, cfg.Weights.NoTTY)
	}
	if cfg.Weights.ParentIDEDead != defaults.ParentIDEDead {
		t.Errorf("expected parent_ide_dead = %f (default), got %f (zeroed!)", defaults.ParentIDEDead, cfg.Weights.ParentIDEDead)
	}
	if cfg.Weights.ExceededDur != defaults.ExceededDur {
		t.Errorf("expected exceeded_duration = %f (default), got %f (zeroed!)", defaults.ExceededDur, cfg.Weights.ExceededDur)
	}
	if cfg.Weights.HasListener != defaults.HasListener {
		t.Errorf("expected has_listener = %f (default), got %f (zeroed!)", defaults.HasListener, cfg.Weights.HasListener)
	}
}

// TestAllWeightsExplicitlyZero verifies user CAN set weights to 0 intentionally.
func TestAllWeightsExplicitlyZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := `
weights:
  ppid_is_init: 0
  no_tty: 0
  parent_ide_dead: 0
  exceeded_duration: 0
  has_listener: 0
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}

	if cfg.Weights.PPIDIsInit != 0 {
		t.Errorf("expected ppid_is_init = 0, got %f", cfg.Weights.PPIDIsInit)
	}
	if cfg.Weights.NoTTY != 0 {
		t.Errorf("expected no_tty = 0, got %f", cfg.Weights.NoTTY)
	}
}

// TestNoWeightsSection verifies that omitting weights entirely preserves all defaults.
func TestNoWeightsSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := `
kill_threshold: 0.7
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}

	defaults := DefaultWeights()
	if cfg.Weights != defaults {
		t.Errorf("expected default weights when weights section omitted, got %+v", cfg.Weights)
	}
}

func TestValidation_ThresholdTooLow(t *testing.T) {
	cfg := Default()
	cfg.KillThreshold = 0.05
	if err := cfg.Validate(); err == nil {
		t.Error("expected validation error for threshold 0.05")
	}
}

func TestValidation_ThresholdTooHigh(t *testing.T) {
	cfg := Default()
	cfg.KillThreshold = 1.5
	if err := cfg.Validate(); err == nil {
		t.Error("expected validation error for threshold 1.5")
	}
}

func TestValidation_NegativeWeight(t *testing.T) {
	cfg := Default()
	cfg.Weights.PPIDIsInit = -0.1
	if err := cfg.Validate(); err == nil {
		t.Error("expected validation error for negative weight")
	}
}

func TestValidation_WeightTooHigh(t *testing.T) {
	cfg := Default()
	cfg.Weights.PPIDIsInit = 5.0
	if err := cfg.Validate(); err == nil {
		t.Error("expected validation error for weight > 1.0")
	}
}

func TestValidation_IntervalTooShort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := `scan_interval: 100ms`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil {
		t.Error("expected validation error for scan_interval: 100ms")
	}
}

func TestValidation_InvalidConfigReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := `
kill_threshold: -5
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil {
		t.Error("expected validation error for invalid config")
	}
}

func TestValidation_GracePeriodTooShort(t *testing.T) {
	cfg := Default()
	cfg.GracePeriod = 500 * time.Millisecond
	if err := cfg.Validate(); err == nil {
		t.Error("expected validation error for grace_period < 1s")
	}
}

func TestValidation_IntervalTooLong(t *testing.T) {
	cfg := Default()
	cfg.ScanInterval = 25 * time.Hour
	if err := cfg.Validate(); err == nil {
		t.Error("expected validation error for scan_interval > 24h")
	}
}

func TestValidation_MaxLogSizeBoundary(t *testing.T) {
	cfg := Default()

	cfg.MaxLogSize = 1023
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for max_log_size < 1024")
	}

	cfg.MaxLogSize = 1024
	if err := cfg.Validate(); err != nil {
		t.Errorf("max_log_size=1024 should be valid: %v", err)
	}
}

func TestValidation_MaxLogFilesMinimum(t *testing.T) {
	cfg := Default()
	cfg.MaxLogFiles = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for max_log_files < 1")
	}
}

func TestExpandPath(t *testing.T) {
	// Non-tilde path should be unchanged
	got := expandPath("/usr/local/bin")
	if got != "/usr/local/bin" {
		t.Errorf("expected /usr/local/bin, got %s", got)
	}

	// Tilde path should expand
	got = expandPath("~/test")
	if got == "~/test" {
		t.Error("expected tilde to be expanded")
	}

	// Empty path
	got = expandPath("")
	if got != "" {
		t.Errorf("expected empty string, got %s", got)
	}
}

func TestLoadConfigWithAllOptions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "full.yaml")

	content := `
scan_interval: 45s
kill_threshold: 0.7
grace_period: 3s
dry_run: true
notify:
  enabled: false
blocklist:
  - postgres
  - redis
allowlist:
  - my-special-server
weights:
  ppid_is_init: 0.35
  no_tty: 0.1
  parent_ide_dead: 0.25
  exceeded_duration: 0.2
  has_listener: 0.15
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}

	if cfg.ScanInterval != 45*time.Second {
		t.Errorf("expected 45s, got %v", cfg.ScanInterval)
	}
	if cfg.KillThreshold != 0.7 {
		t.Errorf("expected 0.7, got %f", cfg.KillThreshold)
	}
	if cfg.GracePeriod != 3*time.Second {
		t.Errorf("expected 3s, got %v", cfg.GracePeriod)
	}
	if !cfg.DryRun {
		t.Error("expected dry_run true")
	}
	if cfg.Notify.Enabled {
		t.Error("expected notify disabled")
	}
	if len(cfg.Blocklist) != 2 {
		t.Errorf("expected 2 blocklist entries, got %d", len(cfg.Blocklist))
	}
	if len(cfg.Allowlist) != 1 {
		t.Errorf("expected 1 allowlist entry, got %d", len(cfg.Allowlist))
	}
	if cfg.Weights.PPIDIsInit != 0.35 {
		t.Errorf("expected ppid weight 0.35, got %f", cfg.Weights.PPIDIsInit)
	}
	if cfg.Weights.HasListener != 0.15 {
		t.Errorf("expected has_listener 0.15, got %f", cfg.Weights.HasListener)
	}
}
