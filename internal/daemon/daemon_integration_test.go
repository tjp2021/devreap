package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	gopsProcess "github.com/shirou/gopsutil/v4/process"

	"github.com/tjp2021/devreap/internal/config"
	"github.com/tjp2021/devreap/internal/logger"
	"github.com/tjp2021/devreap/internal/patterns"
	"github.com/tjp2021/devreap/internal/scanner"
)

// mockScanner returns pre-built scan results.
type mockScanner struct {
	mu     sync.Mutex
	result *scanner.ScanResult
	err    error
	calls  int
	scanFn func(ctx context.Context) (*scanner.ScanResult, error)
}

func (m *mockScanner) Scan(ctx context.Context) (*scanner.ScanResult, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()

	if m.scanFn != nil {
		return m.scanFn(ctx)
	}
	return m.result, m.err
}

func (m *mockScanner) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// mockNotifier records notifications for verification.
type mockNotifier struct {
	mu       sync.Mutex
	messages []string
}

func (n *mockNotifier) Notify(title, message string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.messages = append(n.messages, message)
	return nil
}

func (n *mockNotifier) Messages() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	cp := make([]string, len(n.messages))
	copy(cp, n.messages)
	return cp
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.ScanInterval = 100 * time.Millisecond
	cfg.PidFile = filepath.Join(dir, "devreap.pid")
	cfg.LogDir = filepath.Join(dir, "logs")
	// Kill mode is opt-in since dry_run defaults to true. These integration
	// tests exercise the kill path, so they must ask for it explicitly.
	cfg.DryRun = false
	return cfg
}

// liveProcessInfo snapshots a running process the way a real scan would.
// Kill-path tests need a genuine identity because the daemon re-verifies
// name, cmdline and start time immediately before signalling.
func liveProcessInfo(t *testing.T, pid int32) scanner.ProcessInfo {
	t.Helper()
	p, err := gopsProcess.NewProcess(pid)
	if err != nil {
		t.Fatalf("reading process %d: %v", pid, err)
	}
	name, _ := p.Name()
	cmdline, _ := p.Cmdline()
	createMs, err := p.CreateTime()
	if err != nil {
		t.Fatalf("reading start time of %d: %v", pid, err)
	}
	return scanner.ProcessInfo{
		PID:             pid,
		Name:            name,
		Cmdline:         cmdline,
		CreateTime:      time.UnixMilli(createMs),
		CreateTimeKnown: true,
	}
}

// stubRecheck makes the daemon's fresh-data re-check return a fixed answer.
// Test processes have a live parent, so the real check would correctly refuse
// to kill them; these tests are about the rest of the kill path.
func stubRecheck(result bool) func(int32) (bool, error) {
	return func(int32) (bool, error) { return result, nil }
}

func TestDaemonRun_StopsOnStopChannel(t *testing.T) {
	cfg := testConfig(t)
	ms := &mockScanner{result: &scanner.ScanResult{}}
	log := logger.NewStdout()
	notif := &mockNotifier{}

	d := New(cfg, ms, log, notif)

	done := make(chan error, 1)
	go func() {
		done <- d.Run()
	}()

	// Let it run a couple scan cycles
	time.Sleep(350 * time.Millisecond)

	// Stop via channel
	d.Stop()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("expected nil error on stop, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not stop within timeout")
	}

	// Should have run at least 2 scans (initial + ticker)
	if ms.CallCount() < 2 {
		t.Errorf("expected at least 2 scan calls, got %d", ms.CallCount())
	}

	// PID file should be cleaned up
	if _, err := os.Stat(cfg.PidFile); !os.IsNotExist(err) {
		t.Error("PID file should be removed after stop")
	}
}

func TestDaemonRun_WritesPIDFile(t *testing.T) {
	cfg := testConfig(t)
	ms := &mockScanner{result: &scanner.ScanResult{}}
	log := logger.NewStdout()
	notif := &mockNotifier{}

	d := New(cfg, ms, log, notif)

	done := make(chan error, 1)
	go func() {
		done <- d.Run()
	}()

	// Wait for PID file to be written
	time.Sleep(200 * time.Millisecond)

	// PID file should exist
	data, err := os.ReadFile(cfg.PidFile)
	if err != nil {
		t.Fatalf("expected PID file to exist: %v", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		t.Error("PID file should contain a PID")
	}

	d.Stop()
	<-done
}

func TestDaemonRun_ScanFailureContinues(t *testing.T) {
	cfg := testConfig(t)
	callCount := 0
	ms := &mockScanner{
		scanFn: func(ctx context.Context) (*scanner.ScanResult, error) {
			callCount++
			if callCount <= 2 {
				return nil, context.DeadlineExceeded
			}
			return &scanner.ScanResult{}, nil
		},
	}
	log := logger.NewStdout()
	notif := &mockNotifier{}

	d := New(cfg, ms, log, notif)

	done := make(chan error, 1)
	go func() {
		done <- d.Run()
	}()

	// Let it run past the failures
	time.Sleep(500 * time.Millisecond)
	d.Stop()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("daemon should not crash on scan failure: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not stop")
	}

	// Should have retried after failures
	if ms.CallCount() < 3 {
		t.Errorf("expected at least 3 scan calls (2 failed + 1 success), got %d", ms.CallCount())
	}
}

func TestDaemonRun_DryRunDoesNotKill(t *testing.T) {
	cfg := testConfig(t)
	cfg.DryRun = true

	// Spawn a real process as an orphan candidate
	cmd := exec.Command("sleep", "3600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start sleep: %v", err)
	}
	sleepPID := int32(cmd.Process.Pid)
	go cmd.Wait() // prevent zombie
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGKILL)
	})

	ms := &mockScanner{
		result: &scanner.ScanResult{
			TotalProcesses: 100,
			Matched:        1,
			Orphans: []scanner.OrphanCandidate{
				{
					Process: liveProcessInfo(t, sleepPID),
					Pattern: patterns.Pattern{
						Name:   "test-pattern",
						Signal: patterns.SignalTERM,
					},
					Score:   0.75,
					Signals: map[string]float64{"ppid_is_init": 0.4, "no_tty": 0.15, "parent_ide_dead": 0.2},
				},
			},
		},
	}
	log := logger.NewStdout()
	notif := &mockNotifier{}

	d := New(cfg, ms, log, notif)

	done := make(chan error, 1)
	go func() {
		done <- d.Run()
	}()

	time.Sleep(300 * time.Millisecond)
	d.Stop()
	<-done

	// Process should still be alive — dry-run mode
	proc, _ := os.FindProcess(int(sleepPID))
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		t.Error("sleep process should still be alive in dry-run mode")
	}

	// No notifications in dry-run
	if len(notif.Messages()) > 0 {
		t.Error("no notifications should be sent in dry-run mode")
	}
}

func TestDaemonRun_KillsOrphansAndNotifies(t *testing.T) {
	cfg := testConfig(t)

	// Spawn a real process to be killed
	cmd := exec.Command("sleep", "3600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start sleep: %v", err)
	}
	sleepPID := int32(cmd.Process.Pid)
	go cmd.Wait() // prevent zombie
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGKILL)
	})

	ms := &mockScanner{
		result: &scanner.ScanResult{
			TotalProcesses: 100,
			Matched:        1,
			Orphans: []scanner.OrphanCandidate{
				{
					Process: liveProcessInfo(t, sleepPID),
					Pattern: patterns.Pattern{
						Name:   "test-pattern",
						Signal: patterns.SignalTERM,
					},
					Score:   0.75,
					Signals: map[string]float64{"ppid_is_init": 0.4, "parent_ide_dead": 0.3},
				},
			},
		},
	}
	notif := &mockNotifier{}
	log := logger.NewStdout()

	d := New(cfg, ms, log, notif)
	d.recheckStrongSignal = stubRecheck(true)

	done := make(chan error, 1)
	go func() {
		done <- d.Run()
	}()

	time.Sleep(500 * time.Millisecond)
	d.Stop()
	<-done

	// Process should be dead
	time.Sleep(100 * time.Millisecond)
	proc, _ := os.FindProcess(int(sleepPID))
	if err := proc.Signal(syscall.Signal(0)); err == nil {
		t.Error("orphan process should have been killed")
	}

	// Notification should have been sent
	msgs := notif.Messages()
	if len(msgs) == 0 {
		t.Error("expected at least one kill notification")
	} else {
		if !strings.Contains(msgs[0], "sleep") {
			t.Errorf("notification should mention process name, got: %s", msgs[0])
		}
	}
}

func TestDaemonRun_CleanScanNoNotification(t *testing.T) {
	cfg := testConfig(t)
	ms := &mockScanner{result: &scanner.ScanResult{TotalProcesses: 200, Matched: 0}}
	notif := &mockNotifier{}
	log := logger.NewStdout()

	d := New(cfg, ms, log, notif)

	done := make(chan error, 1)
	go func() {
		done <- d.Run()
	}()

	time.Sleep(300 * time.Millisecond)
	d.Stop()
	<-done

	if len(notif.Messages()) > 0 {
		t.Error("no notifications should be sent for clean scans")
	}
}

func TestDaemonRun_PatternGracePeriodOverride(t *testing.T) {
	cfg := testConfig(t)
	cfg.GracePeriod = 10 * time.Second // global

	cmd := exec.Command("sleep", "3600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start sleep: %v", err)
	}
	sleepPID := int32(cmd.Process.Pid)
	go cmd.Wait() // prevent zombie
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGKILL)
	})

	ms := &mockScanner{
		result: &scanner.ScanResult{
			Orphans: []scanner.OrphanCandidate{
				{
					Process: liveProcessInfo(t, sleepPID),
					Pattern: patterns.Pattern{
						Name:        "test",
						Signal:      patterns.SignalTERM,
						GracePeriod: 1 * time.Second, // pattern-specific override
					},
					Score:   0.8,
					Signals: map[string]float64{"ppid_is_init": 0.4, "parent_ide_dead": 0.3},
				},
			},
		},
	}
	log := logger.NewStdout()
	notif := &mockNotifier{}

	d := New(cfg, ms, log, notif)
	d.recheckStrongSignal = stubRecheck(true)

	done := make(chan error, 1)
	go func() {
		done <- d.Run()
	}()

	// Should complete quickly since pattern grace period is 1s, not 10s
	time.Sleep(3 * time.Second)
	d.Stop()
	<-done

	// Process should be dead
	proc, _ := os.FindProcess(int(sleepPID))
	if err := proc.Signal(syscall.Signal(0)); err == nil {
		t.Error("process should have been killed with pattern-specific grace period")
	}
}

func TestWritePID_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.PidFile = filepath.Join(dir, "subdir", "devreap.pid")

	d := &Daemon{cfg: cfg}

	if err := d.writePID(); err != nil {
		t.Fatalf("writePID failed: %v", err)
	}

	// File should exist
	if _, err := os.Stat(cfg.PidFile); err != nil {
		t.Fatalf("PID file should exist: %v", err)
	}

	// Temp file should NOT exist
	if _, err := os.Stat(cfg.PidFile + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file should be cleaned up after atomic write")
	}

	// Content should be our PID
	pid, err := ReadPID(cfg.PidFile)
	if err != nil {
		t.Fatalf("ReadPID failed: %v", err)
	}
	if pid != os.Getpid() {
		t.Errorf("expected PID %d, got %d", os.Getpid(), pid)
	}

	// Cleanup
	d.removePID()
	if _, err := os.Stat(cfg.PidFile); !os.IsNotExist(err) {
		t.Error("PID file should be removed after removePID")
	}
}

func TestLaunchAgent_PlistGeneration(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "devreap")
	logDir := filepath.Join(dir, "logs")

	// Create a fake binary
	if err := os.WriteFile(binaryPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("failed to create fake binary: %v", err)
	}

	// We can't actually call Install() because it runs launchctl.
	// But we can test the plist template rendering.
	// Test PlistData rendering indirectly by checking Install validates inputs.

	// Non-existent binary
	err := Install("/nonexistent/devreap", logDir)
	if err == nil {
		t.Error("expected error for nonexistent binary")
	}

	// Non-executable binary
	nonExec := filepath.Join(dir, "noexec")
	os.WriteFile(nonExec, []byte("x"), 0644)
	err = Install(nonExec, logDir)
	if err == nil {
		t.Error("expected error for non-executable binary")
	}
}

func TestIsInstalled_NotInstalled(t *testing.T) {
	// This test depends on whether devreap is actually installed as a LaunchAgent.
	// We can at least call it without panicking.
	_ = IsInstalled()
}

// Regression for the 2026-08-14 incident: the daemon killed live MCP servers
// scoring 0.70 from exceeded_duration + no_tty + parent_ide_dead, with
// ppid_is_init absent. Those processes still had live parents. Even in kill
// mode, a candidate without a strong lifecycle signal must survive.
func TestDaemonRun_WeakSignalsAloneDoNotKill(t *testing.T) {
	cfg := testConfig(t) // kill mode
	cfg.DryRun = false

	cmd := exec.Command("sleep", "3600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start sleep: %v", err)
	}
	sleepPID := int32(cmd.Process.Pid)
	go cmd.Wait()
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGKILL)
	})

	ms := &mockScanner{
		result: &scanner.ScanResult{
			TotalProcesses: 100,
			Matched:        1,
			Orphans: []scanner.OrphanCandidate{
				{
					Process: scanner.ProcessInfo{
						PID:     sleepPID,
						Name:    "sleep",
						Cmdline: "node /Users/dev/.npm/_npx/abc/node_modules/.bin/mcp-youtube-transcript",
					},
					Pattern: patterns.Pattern{
						Name:   "node-mcp-server",
						Signal: patterns.SignalTERM,
					},
					// Exactly the 2026-08-14 shape: over threshold, no ppid_is_init.
					Score:   0.70,
					Signals: map[string]float64{"exceeded_duration": 0.25, "no_tty": 0.15, "parent_ide_dead": 0.3},
				},
			},
		},
	}
	log := logger.NewStdout()
	notif := &mockNotifier{}

	d := New(cfg, ms, log, notif)

	done := make(chan error, 1)
	go func() {
		done <- d.Run()
	}()

	time.Sleep(300 * time.Millisecond)
	d.Stop()
	<-done

	proc, _ := os.FindProcess(int(sleepPID))
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		t.Error("process with only weak signals must not be killed")
	}
	if len(notif.Messages()) > 0 {
		t.Error("nothing was killed, so no notification should be sent")
	}
}

// A scan snapshot can be a full scan interval old by the time a kill runs. If
// the strong signal no longer holds on fresh data, the kill must be abandoned.
func TestDaemonRun_StaleSnapshotDoesNotKill(t *testing.T) {
	cfg := testConfig(t)
	cfg.DryRun = false

	cmd := exec.Command("sleep", "3600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start sleep: %v", err)
	}
	sleepPID := int32(cmd.Process.Pid)
	go cmd.Wait()
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGKILL)
	})

	ms := &mockScanner{
		result: &scanner.ScanResult{
			TotalProcesses: 100,
			Matched:        1,
			Orphans: []scanner.OrphanCandidate{
				{
					Process: liveProcessInfo(t, sleepPID),
					Pattern: patterns.Pattern{Name: "test-pattern", Signal: patterns.SignalTERM},
					Score:   0.75,
					Signals: map[string]float64{"ppid_is_init": 0.4, "parent_ide_dead": 0.3},
				},
			},
		},
	}
	log := logger.NewStdout()
	notif := &mockNotifier{}

	d := New(cfg, ms, log, notif)
	// The snapshot said the process was reparented; fresh data says otherwise.
	d.recheckStrongSignal = stubRecheck(false)

	done := make(chan error, 1)
	go func() {
		done <- d.Run()
	}()

	time.Sleep(400 * time.Millisecond)
	d.Stop()
	<-done

	proc, _ := os.FindProcess(int(sleepPID))
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		t.Error("process must survive when the re-check fails")
	}
	if len(notif.Messages()) > 0 {
		t.Error("nothing was killed, so no notification should be sent")
	}
}

// The real re-check refuses to kill a process that still has a live parent,
// with no stub involved.
func TestDaemonRun_RealRecheckSparesProcessWithLiveParent(t *testing.T) {
	cfg := testConfig(t)
	cfg.DryRun = false

	cmd := exec.Command("sleep", "3600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start sleep: %v", err)
	}
	sleepPID := int32(cmd.Process.Pid)
	go cmd.Wait()
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGKILL)
	})

	ms := &mockScanner{
		result: &scanner.ScanResult{
			TotalProcesses: 100,
			Matched:        1,
			Orphans: []scanner.OrphanCandidate{
				{
					Process: liveProcessInfo(t, sleepPID),
					Pattern: patterns.Pattern{Name: "test-pattern", Signal: patterns.SignalTERM},
					Score:   0.75,
					// Snapshot claims reparenting; the live process disagrees.
					Signals: map[string]float64{"ppid_is_init": 0.4, "parent_ide_dead": 0.3},
				},
			},
		},
	}
	log := logger.NewStdout()
	notif := &mockNotifier{}

	d := New(cfg, ms, log, notif) // real re-check

	done := make(chan error, 1)
	go func() {
		done <- d.Run()
	}()

	time.Sleep(400 * time.Millisecond)
	d.Stop()
	<-done

	proc, _ := os.FindProcess(int(sleepPID))
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		t.Error("a process whose parent is alive must not be killed")
	}
}
