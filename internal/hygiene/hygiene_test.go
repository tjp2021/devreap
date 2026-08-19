package hygiene

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjp2021/devreap/internal/config"
)

func TestNewChecker(t *testing.T) {
	c, err := New(config.HygieneConfig{})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if c.homeDir == "" {
		t.Fatal("homeDir should not be empty")
	}
}

func TestRunAll(t *testing.T) {
	c, err := New(config.HygieneConfig{})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	result := c.RunAll()
	if result == nil {
		t.Fatal("RunAll() returned nil")
	}
	// Result should have an Issues slice (possibly empty)
	// Just verify it doesn't panic
}

func TestCheckBrokenLaunchAgentsIgnoresLabelPrefix(t *testing.T) {
	if _, err := exec.LookPath("plutil"); err != nil {
		t.Skip("plutil is not available on this platform")
	}

	home := t.TempDir()
	agentDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}

	missing := filepath.Join(home, "missing-binary")
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>org.example.unrelated</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
	</array>
</dict>
</plist>
`, missing)
	agentPath := filepath.Join(agentDir, "org.example.unrelated.plist")
	if err := os.WriteFile(agentPath, []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}

	r := &Result{}
	(&Checker{homeDir: home}).checkBrokenLaunchAgents(r)

	if len(r.Issues) != 1 {
		t.Fatalf("expected 1 issue for a broken agent with an unrelated label, got %d", len(r.Issues))
	}
	if r.Issues[0].Check != "BROKEN_LAUNCHAGENT" {
		t.Errorf("expected BROKEN_LAUNCHAGENT check, got %s", r.Issues[0].Check)
	}
	if !strings.Contains(r.Issues[0].Message, missing) {
		t.Errorf("issue %q does not name the missing target %q", r.Issues[0].Message, missing)
	}
}

func TestCheckBrokenLaunchAgentsSkipsHealthyAgent(t *testing.T) {
	if _, err := exec.LookPath("plutil"); err != nil {
		t.Skip("plutil is not available on this platform")
	}

	home := t.TempDir()
	agentDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(home, "present-binary")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>org.example.healthy</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
	</array>
</dict>
</plist>
`, target)
	if err := os.WriteFile(filepath.Join(agentDir, "org.example.healthy.plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}

	r := &Result{}
	(&Checker{homeDir: home}).checkBrokenLaunchAgents(r)

	if len(r.Issues) != 0 {
		t.Fatalf("expected 0 issues for a healthy agent, got %d", len(r.Issues))
	}
}

func TestCheckZombieDotdirs(t *testing.T) {
	tmpHome := t.TempDir()
	c := &Checker{
		homeDir: tmpHome,
		cfg:     config.HygieneConfig{ZombieDotdirs: []string{".dead-tool"}},
	}

	zombieDir := filepath.Join(tmpHome, ".dead-tool")
	if err := os.MkdirAll(zombieDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Put a file in it so du reports a size
	os.WriteFile(filepath.Join(zombieDir, "test"), []byte("data"), 0644)

	r := &Result{}
	c.checkZombieDotdirs(r)

	if len(r.Issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(r.Issues))
	}
	if r.Issues[0].Check != "ZOMBIE_DOTDIR" {
		t.Errorf("expected ZOMBIE_DOTDIR check, got %s", r.Issues[0].Check)
	}
	if !strings.Contains(r.Issues[0].Message, zombieDir) {
		t.Errorf("issue %q does not name the directory %q", r.Issues[0].Message, zombieDir)
	}
}

func TestCheckZombieDotdirs_Clean(t *testing.T) {
	c := &Checker{
		homeDir: t.TempDir(),
		cfg:     config.HygieneConfig{ZombieDotdirs: []string{".dead-tool"}},
	}
	r := &Result{}
	c.checkZombieDotdirs(r)

	if len(r.Issues) != 0 {
		t.Fatalf("expected 0 issues, got %d", len(r.Issues))
	}
}

// With no configured dotdirs the check reports nothing, even when a
// directory that another user might call dead is sitting right there.
func TestCheckZombieDotdirsSkippedWhenUnconfigured(t *testing.T) {
	tmpHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpHome, ".some-tool"), 0755); err != nil {
		t.Fatal(err)
	}

	r := &Result{}
	(&Checker{homeDir: tmpHome}).checkZombieDotdirs(r)

	if len(r.Issues) != 0 {
		t.Fatalf("expected 0 issues without configured dotdirs, got %d", len(r.Issues))
	}
}

func TestSessionCWDReadsRecordedPath(t *testing.T) {
	want := filepath.Join(t.TempDir(), "project-with-hyphens")
	event, err := json.Marshal(map[string]string{"cwd": want})
	if err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(session, append(event, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := sessionCWD(session); got != want {
		t.Fatalf("sessionCWD() = %q, want %q", got, want)
	}
}

func TestCheckGhostSessionsUsesRecordedCWD(t *testing.T) {
	home := t.TempDir()
	projectDir := filepath.Join(home, ".claude", "projects", "ambiguous-dash-name")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(home, "missing-project-with-hyphens")
	event, err := json.Marshal(map[string]string{"cwd": missing})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "session.jsonl"), append(event, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	r := &Result{}
	(&Checker{homeDir: home}).checkGhostSessions(r)
	if len(r.Issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(r.Issues))
	}
	if !strings.Contains(r.Issues[0].Message, missing) {
		t.Fatalf("issue %q does not include recorded cwd %q", r.Issues[0].Message, missing)
	}
}

func TestCronExecutableSkipsEnvironmentAssignments(t *testing.T) {
	got := cronExecutable([]string{"PATH=/usr/local/bin:/usr/bin", "python3", "/tmp/job.py"})
	if got != "python3" {
		t.Fatalf("cronExecutable() = %q, want python3", got)
	}
}

func TestCheckDownloadsBuildup(t *testing.T) {
	tmpHome := t.TempDir()
	c := &Checker{homeDir: tmpHome}

	dlDir := filepath.Join(tmpHome, "Downloads")
	os.MkdirAll(dlDir, 0755)

	// Create 51 files
	for i := 0; i < 51; i++ {
		os.WriteFile(filepath.Join(dlDir, fmt.Sprintf("file_%d.txt", i)), []byte("x"), 0644)
	}

	r := &Result{}
	c.checkDownloadsBuildup(r)

	if len(r.Issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(r.Issues))
	}
	if r.Issues[0].Check != "DOWNLOADS_BUILDUP" {
		t.Errorf("expected DOWNLOADS_BUILDUP, got %s", r.Issues[0].Check)
	}
}

func TestCheckDownloadsBuildup_Clean(t *testing.T) {
	tmpHome := t.TempDir()
	c := &Checker{homeDir: tmpHome}

	dlDir := filepath.Join(tmpHome, "Downloads")
	os.MkdirAll(dlDir, 0755)

	// Create 10 files (under threshold)
	for i := 0; i < 10; i++ {
		os.WriteFile(filepath.Join(dlDir, fmt.Sprintf("file_%d.txt", i)), []byte("x"), 0644)
	}

	r := &Result{}
	c.checkDownloadsBuildup(r)

	if len(r.Issues) != 0 {
		t.Fatalf("expected 0 issues, got %d", len(r.Issues))
	}
}

func TestDirSizeMB(t *testing.T) {
	// Non-existent dir should return 0
	if mb := dirSizeMB("/nonexistent/dir/abc123"); mb != 0 {
		t.Errorf("expected 0 for nonexistent dir, got %d", mb)
	}

	// Temp dir with some content
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "test"), make([]byte, 1024), 0644)
	mb := dirSizeMB(tmpDir)
	// Should be 0 or 1 MB (small content)
	if mb < 0 {
		t.Errorf("expected non-negative size, got %d", mb)
	}
}
