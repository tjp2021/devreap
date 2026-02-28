package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildBinary compiles the devreap binary for testing and returns the path.
// Cached across tests in a single test run via t.TempDir at the package level.
var testBinary string

func TestMain(m *testing.M) {
	// Build the binary once for all CLI tests
	dir, err := os.MkdirTemp("", "devreap-test-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	testBinary = filepath.Join(dir, "devreap")
	cmd := exec.Command("go", "build", "-o", testBinary, "../../cmd/devreap")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic("failed to build test binary: " + err.Error())
	}

	os.Exit(m.Run())
}

func runDevreap(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(testBinary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func TestCLI_Version(t *testing.T) {
	out, _, err := runDevreap(t, "version")
	if err != nil {
		t.Fatalf("version command failed: %v", err)
	}
	if !strings.Contains(out, "devreap") {
		t.Errorf("version output should contain 'devreap', got: %s", out)
	}
}

func TestCLI_Scan(t *testing.T) {
	out, _, err := runDevreap(t, "scan")
	if err != nil {
		t.Fatalf("scan command failed: %v", err)
	}
	if !strings.Contains(out, "Scanned") {
		t.Errorf("scan output should contain 'Scanned', got: %s", out)
	}
	if !strings.Contains(out, "processes") {
		t.Errorf("scan output should contain 'processes', got: %s", out)
	}
}

func TestCLI_ScanJSON(t *testing.T) {
	out, _, err := runDevreap(t, "scan", "--json")
	if err != nil {
		t.Fatalf("scan --json failed: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("scan --json output is not valid JSON: %v\nOutput: %s", err, out)
	}

	// Should have required fields
	for _, field := range []string{"total_processes", "matched", "orphan_count"} {
		if _, ok := result[field]; !ok {
			t.Errorf("scan JSON missing field: %s", field)
		}
	}
}

func TestCLI_ScanVerbose(t *testing.T) {
	out, _, err := runDevreap(t, "scan", "-v")
	if err != nil {
		t.Fatalf("scan -v failed: %v", err)
	}
	// Verbose shows "All pattern-matched processes" or regular output if none match
	if !strings.Contains(out, "Scanned") {
		t.Errorf("scan -v should still contain 'Scanned', got: %s", out)
	}
}

func TestCLI_ScanVerboseJSON(t *testing.T) {
	out, _, err := runDevreap(t, "scan", "-v", "--json")
	if err != nil {
		t.Fatalf("scan -v --json failed: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("scan -v --json is not valid JSON: %v", err)
	}
}

func TestCLI_Patterns(t *testing.T) {
	out, _, err := runDevreap(t, "patterns")
	if err != nil {
		t.Fatalf("patterns command failed: %v", err)
	}
	if !strings.Contains(out, "Loaded") {
		t.Errorf("patterns output should contain 'Loaded', got: %s", out)
	}
	if !strings.Contains(out, "18 patterns") {
		t.Errorf("patterns output should contain '18 patterns', got: %s", out)
	}

	// Check some expected pattern names
	for _, name := range []string{"nextjs-dev", "ffmpeg", "chrome-headless"} {
		if !strings.Contains(out, name) {
			t.Errorf("patterns output should contain %q", name)
		}
	}
}

func TestCLI_Status(t *testing.T) {
	out, _, err := runDevreap(t, "status")
	if err != nil {
		t.Fatalf("status command failed: %v", err)
	}
	if !strings.Contains(out, "devreap status") {
		t.Errorf("status output should contain 'devreap status', got: %s", out)
	}
	if !strings.Contains(out, "Daemon:") {
		t.Errorf("status output should contain 'Daemon:', got: %s", out)
	}
	// Daemon shouldn't be running in tests
	if !strings.Contains(out, "stopped") {
		t.Errorf("daemon should be stopped in test, got: %s", out)
	}
}

func TestCLI_Doctor(t *testing.T) {
	out, _, err := runDevreap(t, "doctor")
	if err != nil {
		t.Fatalf("doctor command failed: %v", err)
	}
	if !strings.Contains(out, "devreap doctor") {
		t.Errorf("doctor output should contain 'devreap doctor', got: %s", out)
	}
	// Should pass at least the pattern loading check
	if !strings.Contains(out, "patterns") {
		t.Errorf("doctor should check patterns, got: %s", out)
	}
}

func TestCLI_Stop_NotRunning(t *testing.T) {
	_, stderr, err := runDevreap(t, "stop")
	if err == nil {
		t.Error("stop should fail when daemon isn't running")
	}
	combined := stderr
	if !strings.Contains(combined, "not running") {
		t.Errorf("stop error should mention daemon not running, got: %s", combined)
	}
}

func TestCLI_Kill_NoPID(t *testing.T) {
	_, stderr, err := runDevreap(t, "kill")
	if err == nil {
		t.Error("kill without PID should fail")
	}
	_ = stderr
}

func TestCLI_Kill_InvalidPID(t *testing.T) {
	_, stderr, err := runDevreap(t, "kill", "not-a-number")
	if err == nil {
		t.Error("kill with invalid PID should fail")
	}
	if !strings.Contains(stderr, "invalid PID") {
		t.Errorf("kill error should mention invalid PID, got: %s", stderr)
	}
}

func TestCLI_Kill_NonexistentPID(t *testing.T) {
	_, stderr, err := runDevreap(t, "kill", "99999999")
	if err == nil {
		t.Error("kill of nonexistent PID should fail")
	}
	_ = stderr
}

func TestCLI_Kill_Port_NotFound(t *testing.T) {
	// Port 1 should never have a process listening
	_, stderr, err := runDevreap(t, "kill", "--port", "1")
	if err == nil {
		t.Error("kill --port 1 should fail (no process on port 1)")
	}
	if !strings.Contains(stderr, "no process found") {
		t.Errorf("should mention no process found, got: %s", stderr)
	}
}

func TestCLI_Logs_NoLogFile(t *testing.T) {
	// Use a config pointing to nonexistent log dir
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgPath, []byte("log_dir: "+filepath.Join(dir, "nonexistent")+"\n"), 0644)

	out, _, err := runDevreap(t, "--config", cfgPath, "logs")
	if err != nil {
		// Some implementations return error, some print message
		_ = err
	}
	// Should handle gracefully either way
	_ = out
}

func TestCLI_Help(t *testing.T) {
	out, _, err := runDevreap(t, "--help")
	if err != nil {
		t.Fatalf("--help failed: %v", err)
	}
	if !strings.Contains(out, "devreap") {
		t.Errorf("help should contain 'devreap', got: %s", out)
	}
	// Check all subcommands are listed
	for _, cmd := range []string{"scan", "start", "stop", "status", "kill", "logs", "install", "uninstall", "doctor", "patterns", "version"} {
		if !strings.Contains(out, cmd) {
			t.Errorf("help should list '%s' command", cmd)
		}
	}
}

func TestCLI_InvalidCommand(t *testing.T) {
	_, _, err := runDevreap(t, "nonexistent-command")
	if err == nil {
		t.Error("invalid command should fail")
	}
}

func TestCLI_CustomConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	cfg := `
scan_interval: 10s
kill_threshold: 0.8
dry_run: true
`
	os.WriteFile(cfgPath, []byte(cfg), 0644)

	out, _, err := runDevreap(t, "--config", cfgPath, "status")
	if err != nil {
		t.Fatalf("status with custom config failed: %v", err)
	}
	if !strings.Contains(out, "0.80") {
		t.Errorf("status should show custom threshold 0.80, got: %s", out)
	}
	if !strings.Contains(out, "10s") {
		t.Errorf("status should show custom interval 10s, got: %s", out)
	}
}

func TestCLI_InvalidConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	// Kill threshold > 1.0 should fail validation
	os.WriteFile(cfgPath, []byte("kill_threshold: 2.0\n"), 0644)

	_, stderr, err := runDevreap(t, "--config", cfgPath, "scan")
	if err == nil {
		t.Error("invalid config should cause scan to fail")
	}
	_ = stderr
}
