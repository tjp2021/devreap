package killer

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/process"

	"github.com/tjp2021/devreap/internal/patterns"
)

// startSleepProcess spawns a real "sleep" process and returns its PID.
// It starts a goroutine that calls Wait() so the child doesn't become a zombie
// when killed (zombies are still reported as "running" by gopsutil).
func startSleepProcess(t *testing.T) int32 {
	t.Helper()
	cmd := exec.Command("sleep", "3600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start sleep process: %v", err)
	}
	// Wait in background so child is reaped (not zombie) after signal
	go cmd.Wait()
	t.Cleanup(func() {
		// Best-effort cleanup in case test fails to kill it
		cmd.Process.Signal(syscall.SIGKILL)
	})
	return int32(cmd.Process.Pid)
}

func isProcessAlive(pid int32) bool {
	proc, err := os.FindProcess(int(pid))
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// liveIdentity snapshots a running process the way a scan would, so kill tests
// verify against a real identity instead of a name alone.
func liveIdentity(t *testing.T, pid int32) Identity {
	t.Helper()
	p, err := process.NewProcess(pid)
	if err != nil {
		t.Fatalf("reading process %d: %v", pid, err)
	}
	name, err := p.Name()
	if err != nil {
		t.Fatalf("reading name of %d: %v", pid, err)
	}
	cmdline, err := p.Cmdline()
	if err != nil {
		t.Fatalf("reading cmdline of %d: %v", pid, err)
	}
	createMs, err := p.CreateTime()
	if err != nil {
		t.Fatalf("reading start time of %d: %v", pid, err)
	}
	return Identity{
		PID:        pid,
		Name:       name,
		Cmdline:    cmdline,
		CreateTime: time.UnixMilli(createMs),
	}
}

func TestKill_SuccessfulKill(t *testing.T) {
	pid := startSleepProcess(t)

	// Verify it's alive first
	if !isProcessAlive(pid) {
		t.Fatal("sleep process should be alive before kill")
	}

	result := Kill(liveIdentity(t, pid), patterns.SignalTERM, 2*time.Second, nil)

	if !result.Success {
		t.Errorf("expected kill to succeed, got error: %s", result.Error)
	}
	if result.PID != pid {
		t.Errorf("expected PID %d, got %d", pid, result.PID)
	}
	if result.Name != "sleep" {
		t.Errorf("expected name 'sleep', got %q", result.Name)
	}

	// Verify it's dead
	time.Sleep(100 * time.Millisecond)
	if isProcessAlive(pid) {
		t.Error("process should be dead after kill")
	}
}

func TestKill_SIGINTStrategy(t *testing.T) {
	pid := startSleepProcess(t)

	result := Kill(liveIdentity(t, pid), patterns.SignalINT, 2*time.Second, nil)

	if !result.Success {
		t.Errorf("expected SIGINT kill to succeed, got error: %s", result.Error)
	}

	time.Sleep(100 * time.Millisecond)
	if isProcessAlive(pid) {
		t.Error("process should be dead after SIGINT kill")
	}
}

func TestKill_PIDReuseDetection(t *testing.T) {
	pid := startSleepProcess(t)

	// Kill with wrong expected name — should detect PID reuse
	wrongName := liveIdentity(t, pid)
	wrongName.Name = "definitely-not-sleep"
	result := Kill(wrongName, patterns.SignalTERM, time.Second, nil)

	if result.Success {
		t.Error("expected kill to fail due to PID reuse detection")
	}
	if result.Error == "" {
		t.Error("expected error message about PID reuse")
	}

	// Process should still be alive — we didn't actually kill it
	if !isProcessAlive(pid) {
		t.Error("process should still be alive after PID reuse detection prevented kill")
	}
}

func TestKill_BlocklistedProcess(t *testing.T) {
	pid := startSleepProcess(t)

	result := Kill(liveIdentity(t, pid), patterns.SignalTERM, time.Second, []string{"sleep"})

	if result.Success {
		t.Error("expected kill to fail for blocklisted process")
	}

	// Process should still be alive
	if !isProcessAlive(pid) {
		t.Error("blocklisted process should not be killed")
	}
}

func TestKill_DeadProcess(t *testing.T) {
	// Start and immediately kill a process to get a dead PID
	cmd := exec.Command("sleep", "3600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start: %v", err)
	}
	pid := int32(cmd.Process.Pid)
	cmd.Process.Signal(syscall.SIGKILL)
	cmd.Wait() // reap so it's truly gone, not zombie

	// Brief pause for OS to fully clean up
	time.Sleep(100 * time.Millisecond)

	// Try to kill a process that's already dead
	result := Kill(Identity{PID: pid, Name: "sleep", Cmdline: "sleep 3600", CreateTime: time.Now()}, patterns.SignalTERM, time.Second, nil)

	// Should fail gracefully (process not found) — not panic
	// The PID may have been reused, so we can't assert specific behavior,
	// only that it doesn't crash
	_ = result
}

func TestKill_PID1Blocked(t *testing.T) {
	result := Kill(Identity{PID: 1, Name: "launchd"}, patterns.SignalTERM, time.Second, nil)
	if result.Success {
		t.Error("should never succeed killing PID 1")
	}
}

func TestKill_OwnPIDBlocked(t *testing.T) {
	result := Kill(Identity{PID: int32(os.Getpid()), Name: "test"}, patterns.SignalTERM, time.Second, nil)
	if result.Success {
		t.Error("should never succeed killing own PID")
	}
}

func TestKillByPID_Success(t *testing.T) {
	pid := startSleepProcess(t)

	result := KillByPID(pid, nil, 2*time.Second)

	if !result.Success {
		t.Errorf("expected KillByPID to succeed, got error: %s", result.Error)
	}

	time.Sleep(100 * time.Millisecond)
	if isProcessAlive(pid) {
		t.Error("process should be dead after KillByPID")
	}
}

func TestKillByPID_NonexistentPID(t *testing.T) {
	result := KillByPID(99999999, nil, time.Second)
	if result.Success {
		t.Error("expected failure for nonexistent PID")
	}
}

func TestWaitForDeath(t *testing.T) {
	cmd := exec.Command("sleep", "3600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start: %v", err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGKILL)
		cmd.Wait()
	})

	// Import gopsutil process to create a Process object
	// Instead, test via Kill which uses waitForDeath internally
	// The successful kill tests above already exercise this path

	// Test timeout behavior: don't kill the process, waitForDeath should return false
	// This is tested indirectly through Kill's "process still running after all signals"
}

// A name match alone is not identity. Two node processes share a name, and a
// respawned server reuses it, so the cmdline must match too.
func TestKill_AbortsWhenCmdlineChanged(t *testing.T) {
	pid := startSleepProcess(t)

	id := liveIdentity(t, pid)
	id.Cmdline = "node /some/other/thing.js"

	result := Kill(id, patterns.SignalTERM, time.Second, nil)

	if result.Success {
		t.Error("kill must abort when the cmdline no longer matches the scan snapshot")
	}
	if !isProcessAlive(pid) {
		t.Error("process must survive a failed identity check")
	}
}

// A restarted process reuses name and cmdline but gets a new start time.
func TestKill_AbortsWhenStartTimeChanged(t *testing.T) {
	pid := startSleepProcess(t)

	id := liveIdentity(t, pid)
	id.CreateTime = id.CreateTime.Add(-1 * time.Hour)

	result := Kill(id, patterns.SignalTERM, time.Second, nil)

	if result.Success {
		t.Error("kill must abort when the start time no longer matches the scan snapshot")
	}
	if !isProcessAlive(pid) {
		t.Error("process must survive a failed identity check")
	}
}

// An identity that never recorded a start time cannot be verified at all, so
// it must not be killed.
func TestKill_AbortsWhenStartTimeUnknown(t *testing.T) {
	pid := startSleepProcess(t)

	id := liveIdentity(t, pid)
	id.CreateTime = time.Time{}

	result := Kill(id, patterns.SignalTERM, time.Second, nil)

	if result.Success {
		t.Error("kill must abort when the snapshot has no start time")
	}
	if !isProcessAlive(pid) {
		t.Error("process must survive an unverifiable identity")
	}
}
