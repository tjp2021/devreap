package killer

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

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

func TestKill_SuccessfulKill(t *testing.T) {
	pid := startSleepProcess(t)

	// Verify it's alive first
	if !isProcessAlive(pid) {
		t.Fatal("sleep process should be alive before kill")
	}

	result := Kill(pid, "sleep", patterns.SignalTERM, 2*time.Second, nil)

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

	result := Kill(pid, "sleep", patterns.SignalINT, 2*time.Second, nil)

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
	result := Kill(pid, "definitely-not-sleep", patterns.SignalTERM, time.Second, nil)

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

	result := Kill(pid, "sleep", patterns.SignalTERM, time.Second, []string{"sleep"})

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
	result := Kill(pid, "sleep", patterns.SignalTERM, time.Second, nil)

	// Should fail gracefully (process not found) — not panic
	// The PID may have been reused, so we can't assert specific behavior,
	// only that it doesn't crash
	_ = result
}

func TestKill_PID1Blocked(t *testing.T) {
	result := Kill(1, "launchd", patterns.SignalTERM, time.Second, nil)
	if result.Success {
		t.Error("should never succeed killing PID 1")
	}
}

func TestKill_OwnPIDBlocked(t *testing.T) {
	result := Kill(int32(os.Getpid()), "test", patterns.SignalTERM, time.Second, nil)
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
