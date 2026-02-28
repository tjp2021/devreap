package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

func TestStopSafeToCallMultipleTimes(t *testing.T) {
	d := &Daemon{
		stopCh: make(chan struct{}),
	}

	// Should not panic on multiple calls
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.Stop() // concurrent Stop calls must not panic
		}()
	}
	wg.Wait()

	// Channel should be closed
	select {
	case <-d.stopCh:
		// expected
	default:
		t.Error("expected stopCh to be closed after Stop()")
	}
}

func TestWriteAndReadPID(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "test.pid")

	// Write our own PID
	pid := os.Getpid()
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(pid)), 0644); err != nil {
		t.Fatalf("write PID: %v", err)
	}

	got, err := ReadPID(pidFile)
	if err != nil {
		t.Fatalf("ReadPID: %v", err)
	}
	if got != pid {
		t.Errorf("expected PID %d, got %d", pid, got)
	}
}

func TestReadPID_InvalidContent(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "test.pid")

	if err := os.WriteFile(pidFile, []byte("not-a-number"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := ReadPID(pidFile)
	if err == nil {
		t.Error("expected error for invalid PID content")
	}
}

func TestReadPID_MissingFile(t *testing.T) {
	_, err := ReadPID("/nonexistent/path/daemon.pid")
	if err == nil {
		t.Error("expected error for missing PID file")
	}
}

func TestIsRunning_NonDevreapProcess(t *testing.T) {
	// IsRunning verifies the process name is "devreap" to prevent PID reuse false positives.
	// Our test process is named "daemon.test", so IsRunning should return false
	// even though the PID is alive.
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "test.pid")

	pid := os.Getpid()
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(pid)), 0644); err != nil {
		t.Fatal(err)
	}

	if IsRunning(pidFile) {
		t.Error("expected IsRunning to be false for non-devreap process (PID reuse protection)")
	}
}

func TestIsRunning_DeadProcess(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "test.pid")

	// Use a PID that's almost certainly not running
	if err := os.WriteFile(pidFile, []byte("99999999"), 0644); err != nil {
		t.Fatal(err)
	}

	if IsRunning(pidFile) {
		t.Error("expected IsRunning to be false for dead PID")
	}
}

func TestIsRunning_MissingFile(t *testing.T) {
	if IsRunning("/nonexistent/daemon.pid") {
		t.Error("expected IsRunning to be false for missing PID file")
	}
}
