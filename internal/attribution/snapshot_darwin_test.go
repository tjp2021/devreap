//go:build darwin

package attribution

import (
	"os"
	"testing"
	"time"
)

func TestSnapshotProcessesReadsTheLiveTable(t *testing.T) {
	takenAt := time.Now()
	snap, err := SnapshotProcesses(takenAt)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.Len() < 20 {
		t.Fatalf("snapshot holds %d processes, which is too few to be the real table", snap.Len())
	}
	if !snap.TakenAt().Equal(NormalizeTime(takenAt)) {
		t.Errorf("taken at %s, want %s", snap.TakenAt(), NormalizeTime(takenAt))
	}

	self, ok := snap.ByPID(int32(os.Getpid()))
	if !ok {
		t.Fatal("the snapshot does not contain the test binary")
	}
	if self.PPID != int32(os.Getppid()) {
		t.Errorf("parent: got %d, want %d", self.PPID, os.Getppid())
	}
	if int(self.UID) != os.Getuid() {
		t.Errorf("owner: got %d, want %d", self.UID, os.Getuid())
	}
	if !self.StartTimeKnown || self.StartTime.After(time.Now()) {
		t.Errorf("start time: got %s, known=%v", self.StartTime, self.StartTimeKnown)
	}
	if self.Name == "" {
		t.Error("no process name")
	}
	if self.PGID <= 0 {
		t.Errorf("process group: got %d", self.PGID)
	}
	if !self.Key().Verifiable() {
		t.Error("the test binary should have a verifiable key")
	}
}

// TestSnapshotProcessesFindsLaunchd checks the walk against a process every
// macOS machine has, and confirms the owner is read rather than assumed.
func TestSnapshotProcessesFindsLaunchd(t *testing.T) {
	snap, err := SnapshotProcesses(time.Now())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	launchd, ok := snap.ByPID(1)
	if !ok {
		t.Fatal("process 1 is missing from the snapshot")
	}
	if launchd.UID != 0 {
		t.Errorf("process 1 owner: got %d, want 0", launchd.UID)
	}
	if !launchd.TTYKnown || launchd.TTY != "" {
		t.Errorf("process 1 terminal: got %q known=%v, want a known absence", launchd.TTY, launchd.TTYKnown)
	}
}

// TestConsecutiveSnapshotsAgreeOnLongLivedProcesses asserts the diff is stable:
// a process alive across two polls is neither born nor gone.
func TestConsecutiveSnapshotsAgreeOnLongLivedProcesses(t *testing.T) {
	first, err := SnapshotProcesses(time.Now())
	if err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	second, err := SnapshotProcesses(time.Now())
	if err != nil {
		t.Fatalf("second snapshot: %v", err)
	}

	born, exited := second.Diff(first)
	selfPID := int32(os.Getpid())
	for _, e := range born {
		if e.PID == selfPID {
			t.Error("the test binary was reported as newly born between two polls")
		}
	}
	for _, e := range exited {
		if e.PID == selfPID {
			t.Error("the test binary was reported as gone between two polls")
		}
	}
	if _, ok := second.Lookup(first.Entries()[0].Key()); !ok && first.Entries()[0].PID == 1 {
		t.Error("process 1 did not survive between two polls")
	}
}
