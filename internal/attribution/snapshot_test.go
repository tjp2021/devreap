package attribution

import (
	"testing"
	"time"
)

func testEntry(pid, ppid, pgid int32, start time.Time) ProcEntry {
	return ProcEntry{
		PID:            pid,
		PPID:           ppid,
		PGID:           pgid,
		Name:           "node",
		TTY:            "ttys000",
		TTYKnown:       true,
		StartTime:      NormalizeTime(start),
		StartTimeKnown: !start.IsZero(),
		UID:            501,
	}
}

func TestNormalizeTimeMatchesTheRecordContract(t *testing.T) {
	in := time.Date(2026, 8, 19, 8, 20, 54, 113_456_789, time.FixedZone("CDT", -5*3600))
	got := NormalizeTime(in)
	if want := "2026-08-19T13:20:54.113Z"; got.Format(time.RFC3339Nano) != want {
		t.Errorf("got %s, want %s", got.Format(time.RFC3339Nano), want)
	}
	if !NormalizeTime(time.Time{}).IsZero() {
		t.Error("the zero time should stay zero")
	}
}

func TestProcKeyEqualityFailsClosed(t *testing.T) {
	start := time.Date(2026, 8, 19, 8, 20, 54, 113_000_000, time.UTC)
	known := NewProcKey(98925, start)
	same := NewProcKey(98925, start)
	unknown := ProcKey{PID: 98925}

	if !known.Equal(same) {
		t.Error("two keys with the same identifier and start time should match")
	}
	if known.Equal(NewProcKey(98925, start.Add(time.Second))) {
		t.Error("a different start time must not match")
	}
	if known.Equal(unknown) || unknown.Equal(known) || unknown.Equal(unknown) {
		t.Error("a key with no start time must never compare equal, in either direction")
	}
	if unknown.Verifiable() {
		t.Error("a key with no start time is not verifiable")
	}
	if got := unknown.IndexKey(); got != "98925@unknown" {
		t.Errorf("index key: got %q", got)
	}
}

// TestPIDReuseInvalidatesRecord is the property that makes identity the pair of
// identifier and start time. On this machine PID 99978 and PID 117 started one
// second apart, so a reused identifier is live pressure rather than theory.
func TestPIDReuseInvalidatesRecord(t *testing.T) {
	first := time.Date(2026, 8, 19, 8, 20, 54, 0, time.UTC)
	second := time.Date(2026, 8, 19, 9, 41, 2, 0, time.UTC)

	prev := NewSnapshot(first, []ProcEntry{testEntry(117, 98888, 117, first)})
	curr := NewSnapshot(second, []ProcEntry{testEntry(117, 1, 117, second)})

	born, exited := curr.Diff(prev)
	if len(born) != 1 || born[0].StartTime != NormalizeTime(second) {
		t.Fatalf("born: got %+v, want the reused identifier as a new process", born)
	}
	if len(exited) != 1 || exited[0].StartTime != NormalizeTime(first) {
		t.Fatalf("exited: got %+v, want the original process gone", exited)
	}

	// The stored key from before the reuse must not resolve to the new process.
	if _, ok := curr.Lookup(NewProcKey(117, first)); ok {
		t.Error("the old key matched the reused identifier")
	}
	if _, ok := curr.Lookup(NewProcKey(117, second)); !ok {
		t.Error("the current key did not match")
	}
}

func TestSnapshotDiffIgnoresUnchangedProcesses(t *testing.T) {
	start := time.Date(2026, 8, 19, 8, 20, 54, 0, time.UTC)
	entries := []ProcEntry{
		testEntry(98888, 900, 98888, start),
		testEntry(98925, 98888, 98888, start),
	}
	prev := NewSnapshot(start, entries)
	curr := NewSnapshot(start.Add(time.Second), append(entries, testEntry(99010, 98925, 98888, start.Add(time.Second))))

	born, exited := curr.Diff(prev)
	if len(born) != 1 || born[0].PID != 99010 {
		t.Errorf("born: got %+v, want only 99010", born)
	}
	if len(exited) != 0 {
		t.Errorf("exited: got %+v, want none", exited)
	}
}

func TestSnapshotDiffWithNoPreviousPollReportsNoBirths(t *testing.T) {
	start := time.Date(2026, 8, 19, 8, 20, 54, 0, time.UTC)
	curr := NewSnapshot(start, []ProcEntry{testEntry(98925, 98888, 98888, start)})

	born, exited := curr.Diff(nil)
	if born != nil || exited != nil {
		t.Errorf("first poll reported born=%v exited=%v, want nothing: every process is pre-existing", born, exited)
	}
}

func TestSnapshotLookupRefusesUnverifiableKey(t *testing.T) {
	start := time.Date(2026, 8, 19, 8, 20, 54, 0, time.UTC)
	unverifiable := ProcEntry{PID: 98925, PPID: 1, PGID: 98925, Name: "node"}
	s := NewSnapshot(start, []ProcEntry{unverifiable})

	if _, ok := s.Lookup(ProcKey{PID: 98925}); ok {
		t.Error("a lookup that cannot compare start times must fail closed")
	}
	if _, ok := s.ByPID(98925); !ok {
		t.Error("the process should still be visible by identifier")
	}
}

func TestSnapshotAncestorsWalkNearestFirst(t *testing.T) {
	start := time.Date(2026, 8, 19, 8, 20, 54, 0, time.UTC)
	s := NewSnapshot(start, []ProcEntry{
		testEntry(500, 1, 500, start),     // editor bundle
		testEntry(600, 500, 500, start),   // plugin helper
		testEntry(700, 600, 700, start),   // agent binary
		testEntry(800, 700, 700, start),   // server the agent started
		testEntry(900, 12345, 900, start), // parent outside the snapshot
	})

	got := s.Ancestors(800, MaxLinkDepth)
	want := []int32{700, 600, 500}
	if len(got) != len(want) {
		t.Fatalf("got %d ancestors, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].PID != want[i] {
			t.Errorf("ancestor %d: got %d, want %d", i, got[i].PID, want[i])
		}
	}

	if chain := s.Chain(800, MaxLinkDepth); len(chain) != 4 || chain[0].PID != 800 {
		t.Errorf("chain should start with the process itself: %+v", chain)
	}
	if got := s.Ancestors(900, MaxLinkDepth); len(got) != 0 {
		t.Errorf("a parent outside the snapshot should end the walk, got %+v", got)
	}
	if got := s.Ancestors(4242, MaxLinkDepth); got != nil {
		t.Errorf("an unknown process has no ancestors, got %+v", got)
	}
}

func TestSnapshotAncestorsBoundAndCycleStop(t *testing.T) {
	start := time.Date(2026, 8, 19, 8, 20, 54, 0, time.UTC)

	deep := make([]ProcEntry, 0, 64)
	for pid := int32(2); pid <= 65; pid++ {
		deep = append(deep, testEntry(pid, pid-1, pid, start))
	}
	s := NewSnapshot(start, deep)
	if got := len(s.Ancestors(65, MaxLinkDepth)); got != MaxLinkDepth {
		t.Errorf("deep chain: got %d ancestors, want the bound of %d", got, MaxLinkDepth)
	}
	if got := len(s.Ancestors(65, 4)); got != 4 {
		t.Errorf("explicit bound: got %d ancestors, want 4", got)
	}

	// A cycle cannot happen in a real process table, and the walk still has to
	// terminate if the kernel ever reports one.
	cyclic := NewSnapshot(start, []ProcEntry{
		testEntry(10, 11, 10, start),
		testEntry(11, 10, 10, start),
	})
	if got := len(cyclic.Ancestors(10, MaxLinkDepth)); got != 1 {
		t.Errorf("cycle: got %d ancestors, want the walk to stop at 1", got)
	}
}

func TestProcEntryGroupLeader(t *testing.T) {
	start := time.Date(2026, 8, 19, 8, 20, 54, 0, time.UTC)
	if !testEntry(98888, 900, 98888, start).IsGroupLeader() {
		t.Error("a process whose group equals its identifier leads the group")
	}
	if testEntry(98925, 98888, 98888, start).IsGroupLeader() {
		t.Error("a child in its parent's group does not lead it")
	}
}
