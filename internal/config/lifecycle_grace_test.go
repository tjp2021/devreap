package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return path
}

func TestDefaultLifecycleGraceTable(t *testing.T) {
	cfg := Default()

	want := map[string]time.Duration{
		ClassHeadlessBrowser: 2 * time.Minute,
		ClassMCP:             5 * time.Minute,
		ClassDevServer:       30 * time.Minute,
		ClassMedia:           30 * time.Minute,
		ClassUnclassified:    LifecycleGraceNever,
		ClassUnattributed:    LifecycleGraceNever,
	}
	if len(cfg.LifecycleGrace) != len(want) {
		t.Fatalf("table holds %d classes, want %d: %v", len(cfg.LifecycleGrace), len(want), cfg.LifecycleGrace)
	}
	for class, window := range want {
		if got := cfg.LifecycleGrace[class]; got != window {
			t.Errorf("%s: got %s, want %s", class, got, window)
		}
	}

	// lifecycle_grace and grace_period are different settings, and neither is
	// renamed. Confusing them is the ambiguity two names exist to remove.
	if cfg.GracePeriod != DefaultGracePeriod {
		t.Errorf("grace_period: got %s, want %s", cfg.GracePeriod, DefaultGracePeriod)
	}
}

// TestLifecycleGraceMergesWithBuiltinDefaults exists because of the P0-8 finding
// in the 2026-08-19 verification record: a yaml.Unmarshal into a slice
// overwrote the whole value, so any user config containing a blocklist key
// silently discarded all 26 built-in protections, and the test suite asserted
// that replacement as correct while the README promised the opposite. A
// map-valued key with per-class entries is the same hazard in a new place, so
// the merge is asserted here before anything depends on it.
func TestLifecycleGraceMergesWithBuiltinDefaults(t *testing.T) {
	path := writeConfig(t, "lifecycle_grace:\n  mcp: 10m\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := cfg.LifecycleGrace[ClassMCP]; got != 10*time.Minute {
		t.Errorf("mcp: got %s, want the user's 10m", got)
	}
	untouched := map[string]time.Duration{
		ClassHeadlessBrowser: 2 * time.Minute,
		ClassDevServer:       30 * time.Minute,
		ClassMedia:           30 * time.Minute,
		ClassUnclassified:    LifecycleGraceNever,
		ClassUnattributed:    LifecycleGraceNever,
	}
	for class, window := range untouched {
		if got := cfg.LifecycleGrace[class]; got != window {
			t.Errorf("%s: got %s, want the built-in %s; a partial map must change only what it names", class, got, window)
		}
	}
	if len(cfg.LifecycleGrace) != len(untouched)+1 {
		t.Errorf("table holds %v, want every built-in class present", cfg.LifecycleGrace)
	}
}

func TestLifecycleGraceKeepsBuiltinsWhenTheConfigOmitsIt(t *testing.T) {
	path := writeConfig(t, "scan_interval: 45s\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for class, window := range DefaultLifecycleGrace() {
		if got := cfg.LifecycleGrace[class]; got != window {
			t.Errorf("%s: got %s, want %s", class, got, window)
		}
	}
}

// TestMissingOrZeroWindowMeansNever asserts the direction every unknown resolves
// in. Absence of a window is absence of permission to act, and no value skips
// the wait entirely.
func TestMissingOrZeroWindowMeansNever(t *testing.T) {
	path := writeConfig(t, "lifecycle_grace:\n  dev-server: 0s\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if window, actionable := cfg.LifecycleWindow(ClassDevServer); actionable || window != 0 {
		t.Errorf("a zero window read as %s actionable=%v, want never", window, actionable)
	}
	if _, actionable := cfg.LifecycleWindow(ClassUnclassified); actionable {
		t.Error("unclassified must never be actionable")
	}
	if _, actionable := cfg.LifecycleWindow(ClassUnattributed); actionable {
		t.Error("unattributed must never be actionable")
	}
	if _, actionable := cfg.LifecycleWindow("a-class-nobody-defined"); actionable {
		t.Error("a class missing from the table must read as never")
	}

	window, actionable := cfg.LifecycleWindow(ClassMCP)
	if !actionable || window != 5*time.Minute {
		t.Errorf("mcp: got %s actionable=%v, want 5m", window, actionable)
	}
}

func TestUnknownLifecycleGraceClassIsALoadError(t *testing.T) {
	path := writeConfig(t, "lifecycle_grace:\n  database: 10m\n")

	_, err := Load(path)
	if err == nil {
		t.Fatal("an unknown class loaded silently, which would add a class the engine never checks")
	}
	if !strings.Contains(err.Error(), "database") {
		t.Errorf("error %q does not name the offending class", err)
	}
	if !strings.Contains(err.Error(), ClassMCP) {
		t.Errorf("error %q does not list the known classes", err)
	}
}

func TestLifecycleGraceRejectsAWindowThatCouldNotTakeEffect(t *testing.T) {
	for _, class := range NeverActionableClasses {
		path := writeConfig(t, "lifecycle_grace:\n  "+class+": 10m\n")
		if _, err := Load(path); err == nil {
			t.Errorf("%s accepted a positive window, which R8 would override anyway", class)
		}
	}
}

func TestLifecycleGraceRejectsNegativeWindows(t *testing.T) {
	path := writeConfig(t, "lifecycle_grace:\n  mcp: -5m\n")
	if _, err := Load(path); err == nil {
		t.Fatal("a negative window loaded")
	}
}

func TestMergeLifecycleGraceDoesNotMutateTheBuiltinTable(t *testing.T) {
	builtin := DefaultLifecycleGrace()
	merged, err := mergeLifecycleGrace(builtin, map[string]time.Duration{ClassMCP: 42 * time.Minute})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if merged[ClassMCP] != 42*time.Minute {
		t.Errorf("merged mcp: got %s", merged[ClassMCP])
	}
	if builtin[ClassMCP] != 5*time.Minute {
		t.Errorf("the built-in table was mutated: mcp is now %s", builtin[ClassMCP])
	}
	if DefaultLifecycleGrace()[ClassMCP] != 5*time.Minute {
		t.Error("a later call to DefaultLifecycleGrace returned the merged value")
	}
}

func TestLifecycleGraceValidationBounds(t *testing.T) {
	cfg := Default()
	cfg.LifecycleGrace[ClassMCP] = 25 * time.Hour
	if err := cfg.Validate(); err == nil {
		t.Error("a window over 24h validated")
	}

	cfg = Default()
	cfg.LifecycleGrace[ClassMCP] = -time.Second
	if err := cfg.Validate(); err == nil {
		t.Error("a negative window validated")
	}

	if err := Default().Validate(); err != nil {
		t.Errorf("the default table does not validate: %v", err)
	}
}

func TestDefaultConfirmationCount(t *testing.T) {
	if DefaultConfirmationCount != 3 {
		t.Errorf("confirmation count: got %d, want 3", DefaultConfirmationCount)
	}
}
