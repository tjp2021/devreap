package config

import (
	"os"
	"path/filepath"
	"strings"
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

// A fresh install must observe, not kill. Flipping this default is a breaking
// safety change, so it gets its own test rather than riding along in a
// broader assertion.
func TestDefaultIsObserveOnly(t *testing.T) {
	if !Default().DryRun {
		t.Fatal("default config must be dry-run; killing is opt-in")
	}
}

// A config file that never mentions dry_run must stay observe-only.
func TestDryRunStaysTrueWhenConfigOmitsIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("scan_interval: 45s\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.DryRun {
		t.Error("dry_run must remain true when the config file omits it")
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
	// A user blocklist adds to the built-in list, it does not replace it.
	// "postgres" and "redis" are already built in, so the merged list keeps
	// every built-in entry and gains nothing.
	if len(cfg.Blocklist) != len(DefaultBlocklist) {
		t.Errorf("expected %d merged blocklist entries, got %d", len(DefaultBlocklist), len(cfg.Blocklist))
	}
	for _, want := range []string{"postgres", "redis", "sshd", "WindowServer", "launchd"} {
		if !containsFold(cfg.Blocklist, want) {
			t.Errorf("merged blocklist lost %q", want)
		}
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

func containsFold(list []string, want string) bool {
	for _, entry := range list {
		if strings.EqualFold(entry, want) {
			return true
		}
	}
	return false
}

// Regression: a user blocklist used to replace the built-in list wholesale,
// so adding one custom entry silently removed protection for postgres, sshd,
// WindowServer and every other built-in.
func TestUserBlocklistAddsToBuiltin(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "blocklist:\n  - my-database\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !containsFold(cfg.Blocklist, "my-database") {
		t.Error("user blocklist entry missing")
	}
	for _, builtin := range DefaultBlocklist {
		if !containsFold(cfg.Blocklist, builtin) {
			t.Errorf("built-in blocklist entry %q was dropped", builtin)
		}
	}
	if len(cfg.Blocklist) != len(DefaultBlocklist)+1 {
		t.Errorf("expected %d entries, got %d", len(DefaultBlocklist)+1, len(cfg.Blocklist))
	}
}

func TestReplaceBuiltinBlocklistIsExplicitOptIn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "replace_builtin_blocklist: true\nblocklist:\n  - only-this\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(cfg.Blocklist) != 1 || cfg.Blocklist[0] != "only-this" {
		t.Errorf("expected the user list verbatim, got %v", cfg.Blocklist)
	}
}

// Omitting the key entirely must keep the built-in list intact.
func TestNoUserBlocklistKeepsBuiltin(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("scan_interval: 45s\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Blocklist) != len(DefaultBlocklist) {
		t.Errorf("expected %d entries, got %d", len(DefaultBlocklist), len(cfg.Blocklist))
	}
}

// mergeBlocklist must not mutate the package-level DefaultBlocklist.
func TestMergeBlocklistDoesNotMutateDefaults(t *testing.T) {
	before := len(DefaultBlocklist)
	firstBefore := DefaultBlocklist[0]

	_ = mergeBlocklist(DefaultBlocklist, []string{"extra-one", "extra-two"})

	if len(DefaultBlocklist) != before || DefaultBlocklist[0] != firstBefore {
		t.Error("mergeBlocklist mutated DefaultBlocklist")
	}
}

// The hygiene checks target paths that only exist on one user's machine, so
// the defaults must stay empty and leave those checks switched off.
func TestHygieneTargetsDefaultToEmpty(t *testing.T) {
	cfg := Default()

	if len(cfg.Hygiene.GitRepos) != 0 {
		t.Errorf("expected no default git repos, got %v", cfg.Hygiene.GitRepos)
	}
	if len(cfg.Hygiene.ZombieDotdirs) != 0 {
		t.Errorf("expected no default zombie dotdirs, got %v", cfg.Hygiene.ZombieDotdirs)
	}
}

func TestLoadHygieneTargets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := `
hygiene:
  git_repos:
    - ~/code/my-repo
    - /srv/other-repo
  zombie_dotdirs:
    - .dead-tool
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(home, "code/my-repo"), "/srv/other-repo"}
	if len(cfg.Hygiene.GitRepos) != len(want) {
		t.Fatalf("expected %d git repos, got %v", len(want), cfg.Hygiene.GitRepos)
	}
	for i, repo := range want {
		if cfg.Hygiene.GitRepos[i] != repo {
			t.Errorf("git repo %d = %q, want %q", i, cfg.Hygiene.GitRepos[i], repo)
		}
	}

	if len(cfg.Hygiene.ZombieDotdirs) != 1 || cfg.Hygiene.ZombieDotdirs[0] != ".dead-tool" {
		t.Errorf("zombie dotdirs = %v, want [.dead-tool]", cfg.Hygiene.ZombieDotdirs)
	}
}

// A config file written for a newer devreap must still load on an older
// binary. The loader is deliberately non-strict: unknown keys are ignored
// rather than rejected, so adding a key never breaks a running daemon.
func TestUnknownConfigKeysAreIgnored(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := `
dry_run: true
some_future_key: 42
another:
  nested: value
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unknown keys must not fail the load, got: %v", err)
	}
	if !cfg.DryRun {
		t.Error("expected dry_run true alongside the unknown keys")
	}
}
