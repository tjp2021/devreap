package attribution

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func recordSize(t *testing.T, rec Record) int64 {
	t.Helper()
	line, err := encodeRecord(rec)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	return int64(len(line) + 1)
}

func openTestStore(t *testing.T, dir string, segmentSize int64, maxSegments int, now time.Time, live func(ProcKey) bool) *Store {
	t.Helper()
	s, err := OpenStore(StoreConfig{
		Dir:         dir,
		SegmentSize: segmentSize,
		MaxSegments: maxSegments,
		Now:         func() time.Time { return now },
		Live:        live,
	})
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestStoreCreatesOwnerOnlyFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "attribution")
	now := time.Date(2026, 8, 20, 6, 0, 0, 0, time.UTC)
	s := openTestStore(t, dir, DefaultSegmentSize, DefaultMaxSegments, now, nil)

	if err := s.AppendHeartbeat(HeartbeatRecord{At: now}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.WriteSnapshot(StoreSnapshot{}); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := info.Mode().Perm(); got != StoreDirMode {
		t.Errorf("directory mode: got %o, want %o", got, StoreDirMode)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the store wrote no files")
	}
	for _, entry := range entries {
		fileInfo, statErr := entry.Info()
		if statErr != nil {
			t.Fatalf("stat %s: %v", entry.Name(), statErr)
		}
		if got := fileInfo.Mode().Perm(); got != StoreFileMode {
			t.Errorf("file %s mode: got %o, want %o", entry.Name(), got, StoreFileMode)
		}
	}
}

func TestStoreAppendAndLoadEveryRecordType(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 20, 6, 0, 0, 0, time.UTC)
	s := openTestStore(t, dir, DefaultSegmentSize, DefaultMaxSegments, now, nil)

	key := NewProcKey(98925, now.Add(-time.Hour))
	if err := s.AppendBirth(BirthRecord{ObservedAt: now, Source: BirthSourcePoll, Key: key, Name: "node"}); err != nil {
		t.Fatalf("birth: %v", err)
	}
	if err := s.AppendOwnerExit(OwnerExitRecord{At: now, SessionID: "5f1c9a2e", Source: ExitSourceKqueue}); err != nil {
		t.Fatalf("owner exit: %v", err)
	}
	if err := s.AppendTransition(TransitionRecord{At: now, Key: key, From: StateActive, To: StateOwnerGone, Trigger: "owner_exit"}); err != nil {
		t.Fatalf("transition: %v", err)
	}
	if err := s.AppendClaimUpgrade(ClaimUpgradeRecord{At: now, Key: key, From: ConfidenceInferred, To: ConfidenceObserved, Reason: UpgradeAncestryResolved}); err != nil {
		t.Fatalf("claim upgrade: %v", err)
	}
	if err := s.AppendHeartbeat(HeartbeatRecord{At: now, Tracked: 96, Attributed: 90}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	load, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(load.Records) != 5 {
		t.Fatalf("loaded %d records, want 5", len(load.Records))
	}
	wantTypes := []RecordType{RecordBirth, RecordOwnerExit, RecordTransition, RecordClaimUpgrade, RecordHeartbeat}
	for i, want := range wantTypes {
		if load.Records[i].Type != want {
			t.Errorf("record %d: got %q, want %q", i, load.Records[i].Type, want)
		}
	}
	if load.Segments != 1 || load.Bytes <= 0 {
		t.Errorf("segments=%d bytes=%d, want one non-empty segment", load.Segments, load.Bytes)
	}
	if load.TornRecords != 0 || len(load.Findings) != 0 {
		t.Errorf("a clean store reported torn=%d findings=%+v", load.TornRecords, load.Findings)
	}
}

// TestStoreTornTailRecovery asserts the property that makes an append-only file
// crash-safe: a torn final line is discarded and every record before it stands.
func TestStoreTornTailRecovery(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 20, 6, 0, 0, 0, time.UTC)
	s := openTestStore(t, dir, DefaultSegmentSize, DefaultMaxSegments, now, nil)

	for i := 0; i < 3; i++ {
		if err := s.AppendHeartbeat(HeartbeatRecord{At: now, Polls: i}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	journal := filepath.Join(dir, journalName)
	f, err := os.OpenFile(journal, os.O_APPEND|os.O_WRONLY, StoreFileMode)
	if err != nil {
		t.Fatalf("opening journal: %v", err)
	}
	if _, err := f.WriteString(`{"v":1,"type":"heartbeat","at":"2026-08-2`); err != nil {
		t.Fatalf("writing torn tail: %v", err)
	}
	f.Close()

	reopened := openTestStore(t, dir, DefaultSegmentSize, DefaultMaxSegments, now, nil)
	load, err := reopened.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(load.Records) != 3 {
		t.Errorf("loaded %d records, want the 3 valid ones", len(load.Records))
	}
	if load.TornRecords != 1 {
		t.Errorf("torn records: got %d, want 1", load.TornRecords)
	}
	if len(load.Findings) != 1 || load.Findings[0].Kind != FindingStoreTornTail {
		t.Errorf("findings: %+v, want one torn-tail finding", load.Findings)
	}
}

func TestStoreUnknownSchemaVersionIsIgnoredEntirely(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 20, 6, 0, 0, 0, time.UTC)

	journal := filepath.Join(dir, journalName)
	if err := os.MkdirAll(dir, StoreDirMode); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	contents := `{"v":1,"type":"heartbeat","at":"2026-08-20T06:00:00Z"}` + "\n" +
		`{"v":7,"type":"heartbeat","at":"2026-08-20T06:01:00Z"}` + "\n"
	if err := os.WriteFile(journal, []byte(contents), StoreFileMode); err != nil {
		t.Fatalf("writing journal: %v", err)
	}

	s := openTestStore(t, dir, DefaultSegmentSize, DefaultMaxSegments, now, nil)
	load, err := s.Load()
	if err == nil {
		t.Fatalf("loaded %d records, want the store ignored entirely", len(load.Records))
	}
	if load != nil {
		t.Errorf("a rejected store returned %+v, want nothing", load)
	}
}

func TestStoreSkipsUnknownRecordTypeAndKeepsTheRest(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 20, 6, 0, 0, 0, time.UTC)

	if err := os.MkdirAll(dir, StoreDirMode); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	contents := `{"v":1,"type":"heartbeat","at":"2026-08-20T06:00:00Z"}` + "\n" +
		`{"v":1,"type":"a_shape_from_the_future","at":"2026-08-20T06:00:30Z"}` + "\n" +
		`{"v":1,"type":"heartbeat","at":"2026-08-20T06:01:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, journalName), []byte(contents), StoreFileMode); err != nil {
		t.Fatalf("writing journal: %v", err)
	}

	s := openTestStore(t, dir, DefaultSegmentSize, DefaultMaxSegments, now, nil)
	load, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(load.Records) != 2 {
		t.Errorf("loaded %d records, want the 2 known ones", len(load.Records))
	}
	if load.SkippedRecords != 1 {
		t.Errorf("skipped: got %d, want 1", load.SkippedRecords)
	}
	if load.TornRecords != 0 {
		t.Error("an unknown shape must not be treated as corruption")
	}
}

// TestForcedRotationStaysUnderCeiling forces the segment size down and asserts
// the journal rotates, keeps the configured segment count, and stays under the
// ceiling once compaction has evictable records to work with.
func TestForcedRotationStaysUnderCeiling(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 20, 6, 0, 0, 0, time.UTC)
	old := now.Add(-10 * 24 * time.Hour)

	const segmentSize = 512
	const maxSegments = 4
	s := openTestStore(t, dir, segmentSize, maxSegments, now, func(ProcKey) bool { return false })

	const written = 200
	for i := 0; i < written; i++ {
		if err := s.AppendHeartbeat(HeartbeatRecord{At: old, Polls: i, Tracked: 96, Attributed: 90}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	if got := s.SegmentCount(); got < 2 {
		t.Errorf("journal holds %d segments, so rotation never ran", got)
	}

	// The append path rotates and never evicts. Compaction reads every segment
	// and rewrites the journal, which is far too much work to put inside a one
	// second poll, so an append only marks the pass as owed.
	if !s.CompactionDue() {
		t.Fatal("rotation past the segment count did not mark compaction due")
	}

	result, ran, err := s.Maintain(func(ProcKey) bool { return false })
	if err != nil {
		t.Fatalf("maintain: %v", err)
	}
	if !ran {
		t.Fatal("maintenance did not run with a compaction due")
	}
	if s.CompactionDue() {
		t.Error("compaction stayed due after a maintenance pass")
	}
	if len(result.Evicted) == 0 {
		t.Error("maintenance evicted nothing under ceiling pressure")
	}

	ceiling := int64(segmentSize * maxSegments)
	if got := s.Size(); got > ceiling {
		t.Errorf("journal holds %d bytes against a %d byte ceiling after maintenance", got, ceiling)
	}
	if got := s.SegmentCount(); got > maxSegments {
		t.Errorf("journal holds %d segments after maintenance, want at most %d", got, maxSegments)
	}

	load, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(load.Records) >= written {
		t.Errorf("kept %d of %d records, so nothing was evicted under pressure", len(load.Records), written)
	}
	if len(load.Records) == 0 {
		t.Error("compaction emptied the journal")
	}

	// A second pass with nothing owed is a no-op rather than another rewrite.
	if _, ran, err := s.Maintain(nil); err != nil || ran {
		t.Errorf("maintenance ran again with nothing due: ran=%v err=%v", ran, err)
	}
}

// TestRetentionFloorsAndEvictionOrder drives the fixed order: exited births
// first, then owner exits past their floor, then heartbeats past theirs. A live
// process's record is never evicted, and a record inside its floor survives even
// when an older record of another type is dropped.
func TestRetentionFloorsAndEvictionOrder(t *testing.T) {
	now := time.Date(2026, 8, 20, 6, 0, 0, 0, time.UTC)
	old := now.Add(-10 * 24 * time.Hour)
	recent := now.Add(-time.Hour)

	exitedKey := NewProcKey(1001, old)
	liveKey := NewProcKey(1002, old)
	live := func(k ProcKey) bool { return k.Equal(liveKey) }

	exitedBirth := Record{Birth: &BirthRecord{ObservedAt: old, Source: BirthSourcePoll, Key: exitedKey, Name: "node", Class: "mcp"}}
	liveBirth := Record{Birth: &BirthRecord{ObservedAt: old, Source: BirthSourcePoll, Key: liveKey, Name: "node", Class: "mcp"}}
	ownerExit := Record{OwnerExit: &OwnerExitRecord{At: old, SessionID: "5f1c9a2e", Source: ExitSourceKqueue}}
	oldBeat := Record{Heartbeat: &HeartbeatRecord{At: old, Polls: 60}}
	recentBeat := Record{Heartbeat: &HeartbeatRecord{At: recent, Polls: 60}}

	sizeExitedBirth := recordSize(t, exitedBirth)
	sizeOwnerExit := recordSize(t, ownerExit)

	writeAll := func(t *testing.T, dir string) int64 {
		t.Helper()
		s := openTestStore(t, dir, DefaultSegmentSize, DefaultMaxSegments, now, live)
		if err := s.AppendBirth(*exitedBirth.Birth); err != nil {
			t.Fatalf("exited birth: %v", err)
		}
		if err := s.AppendBirth(*liveBirth.Birth); err != nil {
			t.Fatalf("live birth: %v", err)
		}
		if err := s.AppendOwnerExit(*ownerExit.OwnerExit); err != nil {
			t.Fatalf("owner exit: %v", err)
		}
		if err := s.AppendHeartbeat(*oldBeat.Heartbeat); err != nil {
			t.Fatalf("old heartbeat: %v", err)
		}
		if err := s.AppendHeartbeat(*recentBeat.Heartbeat); err != nil {
			t.Fatalf("recent heartbeat: %v", err)
		}
		total := s.Size()
		if err := s.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		return total
	}

	t.Run("pressure for one record evicts the exited birth only", func(t *testing.T) {
		dir := t.TempDir()
		total := writeAll(t, dir)

		// Sized so compaction targets exactly everything but the exited birth.
		s := openTestStore(t, dir, total-sizeExitedBirth, 2, now, live)
		result, err := s.Compact(nil)
		if err != nil {
			t.Fatalf("compact: %v", err)
		}
		if result.CeilingExceeded {
			t.Errorf("compaction could not reach the ceiling: %+v", result)
		}
		if got := result.Evicted[RecordBirth]; got != 1 {
			t.Errorf("evicted %d births, want 1", got)
		}
		if len(result.Evicted) != 1 {
			t.Errorf("evicted %v, want only the exited birth", result.Evicted)
		}

		load, err := s.Load()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if len(load.Records) != 4 {
			t.Fatalf("kept %d records, want 4", len(load.Records))
		}
		for _, rec := range load.Records {
			if rec.Birth != nil && rec.Birth.Key.Equal(exitedKey) {
				t.Error("the exited birth survived eviction")
			}
		}
		var keptLiveBirth bool
		for _, rec := range load.Records {
			if rec.Birth != nil && rec.Birth.Key.Equal(liveKey) {
				keptLiveBirth = true
			}
		}
		if !keptLiveBirth {
			t.Error("a live process's birth record was evicted")
		}
	})

	t.Run("more pressure takes the owner exit before any heartbeat", func(t *testing.T) {
		dir := t.TempDir()
		total := writeAll(t, dir)

		s := openTestStore(t, dir, total-sizeExitedBirth-sizeOwnerExit, 2, now, live)
		result, err := s.Compact(nil)
		if err != nil {
			t.Fatalf("compact: %v", err)
		}
		if result.CeilingExceeded {
			t.Errorf("compaction could not reach the ceiling: %+v", result)
		}
		if result.Evicted[RecordBirth] != 1 || result.Evicted[RecordOwnerExit] != 1 {
			t.Errorf("evicted %v, want one birth and one owner exit", result.Evicted)
		}
		if result.Evicted[RecordHeartbeat] != 0 {
			t.Error("a heartbeat was evicted before the owner exit group was exhausted")
		}

		load, err := s.Load()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		beats := 0
		for _, rec := range load.Records {
			if rec.Heartbeat != nil {
				beats++
			}
		}
		if beats != 2 {
			t.Errorf("kept %d heartbeats, want both: the old one is still in the eviction order behind owner exits", beats)
		}
	})
}

// TestCeilingExceededUnderFloorPressureRaisesAFinding is the other half of the
// eviction rule. Compaction refuses to drop a record inside its retention floor,
// so the ceiling is exceeded transiently and says so rather than invalidating
// the measurement series.
func TestCeilingExceededUnderFloorPressureRaisesAFinding(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 20, 6, 0, 0, 0, time.UTC)
	recent := now.Add(-time.Hour)

	s := openTestStore(t, dir, DefaultSegmentSize, DefaultMaxSegments, now, func(ProcKey) bool { return false })
	for i := 0; i < 10; i++ {
		if err := s.AppendHeartbeat(HeartbeatRecord{At: recent, Polls: i}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	total := s.Size()
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// A ceiling well under what the store already holds, with every record
	// inside its 8 day floor.
	tight := openTestStore(t, dir, total/4, 1, now, func(ProcKey) bool { return false })
	result, err := tight.Compact(nil)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if !result.CeilingExceeded {
		t.Fatal("compaction reported success while every record was inside its floor")
	}
	if len(result.Evicted) != 0 {
		t.Errorf("evicted %v inside the retention floor", result.Evicted)
	}
	if len(result.Findings) != 1 || result.Findings[0].Kind != FindingStoreCeilingExceeded {
		t.Errorf("findings: %+v, want one ceiling finding", result.Findings)
	}

	load, err := tight.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(load.Records) != 10 {
		t.Errorf("kept %d records, want all 10: nothing inside a floor may be dropped", len(load.Records))
	}
	if got := tight.Size(); got <= tight.Ceiling() {
		t.Errorf("store holds %d bytes against a %d byte ceiling, so the test did not exercise the exceedance", got, tight.Ceiling())
	}
	found := false
	for _, f := range tight.Findings() {
		if f.Kind == FindingStoreCeilingExceeded {
			found = true
		}
	}
	if !found {
		t.Error("the store did not keep the ceiling finding for doctor")
	}
}

func TestStoreSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 20, 6, 0, 0, 0, time.UTC)
	s := openTestStore(t, dir, DefaultSegmentSize, DefaultMaxSegments, now, nil)

	if _, ok := s.ReadSnapshot(); ok {
		t.Error("an empty store returned a snapshot")
	}

	key := NewProcKey(98925, now.Add(-time.Hour))
	err := s.WriteSnapshot(StoreSnapshot{
		Processes: []ProcessState{{
			Key: key, State: StateGracePeriod, SessionID: "5f1c9a2e",
			Confidence: ConfidenceObserved, Confirmations: 2, AwakeMillis: 240_000,
		}},
		Sessions: []SessionState{{SessionID: "5f1c9a2e", Harness: "claude-code-cli", RootKey: NewProcKey(98888, now.Add(-2*time.Hour))}},
	})
	if err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	got, ok := s.ReadSnapshot()
	if !ok {
		t.Fatal("the snapshot did not read back")
	}
	if got.V != RecordSchemaVersion || got.Type != RecordSnapshot {
		t.Errorf("envelope: v=%d type=%q", got.V, got.Type)
	}
	if !got.WrittenAt.Equal(NormalizeTime(now)) {
		t.Errorf("written at %s, want %s", got.WrittenAt, NormalizeTime(now))
	}
	if len(got.Processes) != 1 || got.Processes[0].AwakeMillis != 240_000 || got.Processes[0].Confirmations != 2 {
		t.Errorf("accumulators did not survive the snapshot: %+v", got.Processes)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].Harness != "claude-code-cli" {
		t.Errorf("sessions: %+v", got.Sessions)
	}
}

func TestStoreSnapshotWithUnknownVersionIsIgnored(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 20, 6, 0, 0, 0, time.UTC)
	s := openTestStore(t, dir, DefaultSegmentSize, DefaultMaxSegments, now, nil)

	if err := os.WriteFile(filepath.Join(dir, snapshotName), []byte(`{"v":9,"type":"snapshot"}`), StoreFileMode); err != nil {
		t.Fatalf("writing snapshot: %v", err)
	}
	if _, ok := s.ReadSnapshot(); ok {
		t.Error("an unrecognized snapshot version was used rather than ignored")
	}
	findings := s.Findings()
	if len(findings) != 1 || findings[0].Kind != FindingSnapshotIgnored {
		t.Errorf("findings: %+v, want one ignored-snapshot finding", findings)
	}
}

func TestStoreWorksFromEmpty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh")
	now := time.Date(2026, 8, 20, 6, 0, 0, 0, time.UTC)
	s := openTestStore(t, dir, DefaultSegmentSize, DefaultMaxSegments, now, nil)

	load, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(load.Records) != 0 {
		t.Errorf("an empty store loaded %d records", len(load.Records))
	}
	if s.Size() != 0 {
		t.Errorf("an empty store holds %d bytes", s.Size())
	}
	if got := s.Ceiling(); got != DefaultSegmentSize*DefaultMaxSegments {
		t.Errorf("ceiling: got %d, want %d", got, DefaultSegmentSize*DefaultMaxSegments)
	}
}
