package attribution

import (
	"os"
	"testing"
	"time"

	"github.com/tjp2021/devreap/internal/config"
)

// fakeExitWatch records what the watcher asked for and delivers notices on
// demand, so the kqueue path is driven without a live process.
type fakeExitWatch struct {
	watched  map[int32]int
	unwatch  map[int32]int
	refuse   map[int32]bool
	pending  []ExitNotice
	closed   bool
	failNext bool
}

func newFakeExitWatch() *fakeExitWatch {
	return &fakeExitWatch{watched: map[int32]int{}, unwatch: map[int32]int{}, refuse: map[int32]bool{}}
}

func (f *fakeExitWatch) Watch(pid int32) bool {
	if f.refuse[pid] || f.failNext {
		f.failNext = false
		return false
	}
	f.watched[pid]++
	return true
}
func (f *fakeExitWatch) Unwatch(pid int32) { f.unwatch[pid]++ }
func (f *fakeExitWatch) Drain() []ExitNotice {
	out := f.pending
	f.pending = nil
	return out
}
func (f *fakeExitWatch) Close() error { f.closed = true; return nil }

// watcherRig drives the watcher a poll at a time against a table the test owns.
type watcherRig struct {
	t       *testing.T
	watcher *Watcher
	engine  *Engine
	store   *Store
	clock   *fakeClock
	exits   *fakeExitWatch

	table   []ProcEntry
	env     map[int32]map[string]string
	cwd     map[int32]string
	failEnv map[int32]bool
	snapErr error
	panicOn int
}

func selfUID() uint32 { return uint32(os.Getuid()) }

func procRow(pid, ppid, pgid int32, name, tty string, start time.Time) ProcEntry {
	return ProcEntry{
		PID: pid, PPID: ppid, PGID: pgid, Name: name,
		TTY: tty, TTYKnown: true,
		StartTime: NormalizeTime(start), StartTimeKnown: true,
		UID: selfUID(),
	}
}

func newWatcherRig(t *testing.T, opts ...func(*WatcherConfig)) *watcherRig {
	t.Helper()

	store, err := OpenStore(StoreConfig{Dir: t.TempDir(), Now: func() time.Time { return lifecycleStart }})
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	registry, err := NewHarnessRegistry()
	if err != nil {
		t.Fatalf("loading descriptors: %v", err)
	}

	rig := &watcherRig{
		t:       t,
		clock:   newFakeClock(),
		exits:   newFakeExitWatch(),
		env:     map[int32]map[string]string{},
		cwd:     map[int32]string{},
		failEnv: map[int32]bool{},
		store:   store,
	}

	cfg := WatcherConfig{
		PollInterval:      time.Second,
		HeartbeatInterval: time.Minute,
		ScanInterval:      testScanInterval,
		SnapshotInterval:  5 * time.Minute,
		ExitWatch:         rig.exits,
		Classifier: func(name, cmdline string) string {
			if name == "node" || name == "chrome" {
				return config.ClassMCP
			}
			return ""
		},
		Snapshots: func(at time.Time) (*Snapshot, error) {
			if rig.snapErr != nil {
				return nil, rig.snapErr
			}
			return NewSnapshot(at, rig.table), nil
		},
		ReadArgs: func(pid int32, uid uint32, r *Redactor) (*ProcArgs, error) {
			if rig.panicOn == int(pid) {
				panic("malformed argument buffer")
			}
			if rig.failEnv[pid] {
				return nil, ErrProcargsParse
			}
			return &ProcArgs{
				PID:     pid,
				Exe:     rig.exeFor(pid),
				Cmdline: rig.cmdlineFor(pid),
				Env:     rig.env[pid],
			}, nil
		},
		ReadCwd: func(pid int32) (string, bool) {
			cwd, ok := rig.cwd[pid]
			return cwd, ok
		},
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	rig.engine = NewEngine(EngineConfig{
		Windows:       config.DefaultLifecycleGrace(),
		Confirmations: config.DefaultConfirmationCount,
		ScanInterval:  testScanInterval,
	}, rig.clock)
	rig.watcher = NewWatcher(cfg, store, registry, rig.engine, rig.clock)
	return rig
}

func (r *watcherRig) exeFor(pid int32) string {
	for _, entry := range r.table {
		if entry.PID != pid {
			continue
		}
		switch entry.Name {
		case "claude":
			return "/opt/homebrew/bin/claude"
		case "node":
			return "/usr/local/bin/node"
		}
	}
	return ""
}

func (r *watcherRig) cmdlineFor(pid int32) string {
	for _, entry := range r.table {
		if entry.PID == pid {
			return entry.Name + " --serve"
		}
	}
	return ""
}

// poll advances the clock by one awake poll interval and runs one cycle.
func (r *watcherRig) poll() {
	r.t.Helper()
	r.clock.awake(time.Second)
	r.watcher.safePoll()
}

// sleepPoll advances wall time without the monotonic clock, which is a sleep.
func (r *watcherRig) sleepPoll(d time.Duration) {
	r.t.Helper()
	r.clock.asleep(d)
	r.watcher.safePoll()
}

func (r *watcherRig) load() []Record {
	r.t.Helper()
	result, err := r.store.Load()
	if err != nil {
		r.t.Fatalf("loading store: %v", err)
	}
	return result.Records
}

func (r *watcherRig) births() []BirthRecord {
	r.t.Helper()
	var out []BirthRecord
	for _, rec := range r.load() {
		if rec.Birth != nil {
			out = append(out, *rec.Birth)
		}
	}
	return out
}

func (r *watcherRig) birthFor(pid int32) (BirthRecord, bool) {
	r.t.Helper()
	for _, birth := range r.births() {
		if birth.Key.PID == pid {
			return birth, true
		}
	}
	return BirthRecord{}, false
}

// claudeSession is the terminal harness shape: launchd, a shell, a recognized
// root, and one child.
func (r *watcherRig) claudeSession() (root, child ProcEntry) {
	base := lifecycleStart
	root = procRow(100, 50, 100, "claude", "ttys000", base)
	child = procRow(200, 100, 100, "node", "ttys000", base.Add(time.Second))
	r.cwd[100] = "/Users/dev/projects/example"
	return root, child
}

// TestWatcherRecordsWitnessedBirthsAsObserved is the mechanism end to end: the
// watcher sees the spawn link with its own eyes, so the child is observed with
// no marker anywhere.
func TestWatcherRecordsWitnessedBirthsAsObserved(t *testing.T) {
	rig := newWatcherRig(t)
	root, child := rig.claudeSession()

	rig.table = []ProcEntry{procRow(1, 0, 1, "launchd", "", lifecycleStart)}
	rig.poll()

	rig.table = append(rig.table, root)
	rig.poll()

	rig.table = append(rig.table, child)
	rig.poll()

	birth, ok := rig.birthFor(child.PID)
	if !ok {
		t.Fatal("no birth record was written for the child")
	}
	if birth.Source != BirthSourcePoll {
		t.Errorf("source = %q, want poll", birth.Source)
	}
	if birth.Owner.Confidence != ConfidenceObserved {
		t.Fatalf("confidence = %q, want observed", birth.Owner.Confidence)
	}
	if birth.Owner.Harness != "claude-code-cli" {
		t.Errorf("harness = %q, want claude-code-cli", birth.Owner.Harness)
	}
	if birth.Owner.LinkDepth != 1 {
		t.Errorf("link depth = %d, want 1", birth.Owner.LinkDepth)
	}
	if !birth.ParentKey.Equal(root.Key()) {
		t.Errorf("parent key = %v, want the root's live key", birth.ParentKey)
	}
	if birth.Owner.Repo != "/Users/dev/projects/example" {
		t.Errorf("repo = %q, want the root working directory", birth.Owner.Repo)
	}
	if birth.Class != config.ClassMCP {
		t.Errorf("class = %q, want mcp", birth.Class)
	}
}

// TestWatcherBackfillsPreExistingProcesses asserts the cold-start cost. Nobody
// witnessed anything on the first poll, so every claim is inferred at best.
func TestWatcherBackfillsPreExistingProcesses(t *testing.T) {
	rig := newWatcherRig(t)
	root, child := rig.claudeSession()
	rig.env[child.PID] = map[string]string{"CLAUDE_CODE_SESSION_ID": "5f1c9a2e"}

	rig.table = []ProcEntry{procRow(1, 0, 1, "launchd", "", lifecycleStart), root, child}
	rig.poll()

	birth, ok := rig.birthFor(child.PID)
	if !ok {
		t.Fatal("no birth record was written for the pre-existing child")
	}
	if birth.Source != BirthSourceBackfill {
		t.Errorf("source = %q, want backfill", birth.Source)
	}
	if birth.Owner.Confidence != ConfidenceInferred {
		t.Errorf("confidence = %q, want inferred", birth.Owner.Confidence)
	}
	if birth.Owner.Actionable() {
		t.Error("a backfilled claim must never be action-eligible")
	}
}

// TestWatcherTreatsSleepGapBirthsAsUnwitnessed asserts the gap rule. A process
// born while the machine slept was seen by nobody, so it falls to backfill even
// though the diff reports it as new.
func TestWatcherTreatsSleepGapBirthsAsUnwitnessed(t *testing.T) {
	rig := newWatcherRig(t)
	root, child := rig.claudeSession()

	rig.table = []ProcEntry{procRow(1, 0, 1, "launchd", "", lifecycleStart), root}
	rig.poll()
	rig.poll()

	rig.table = append(rig.table, child)
	rig.sleepPoll(8 * time.Hour)

	birth, ok := rig.birthFor(child.PID)
	if !ok {
		t.Fatal("no birth record was written")
	}
	if birth.Source != BirthSourceBackfill {
		t.Errorf("source = %q, want backfill across a sleep gap", birth.Source)
	}
	if birth.Owner.Confidence == ConfidenceObserved {
		t.Error("a birth nobody witnessed reached observed confidence")
	}
}

// TestWatcherPersistsOnlyAttributedOrClassifiedRecords covers volume control.
// Every live process is held in memory, because ancestry chains need the full
// tree, and only the records that matter reach the journal.
func TestWatcherPersistsOnlyAttributedOrClassifiedRecords(t *testing.T) {
	rig := newWatcherRig(t)

	noise := procRow(400, 1, 400, "cfprefsd", "", lifecycleStart)
	rig.table = []ProcEntry{procRow(1, 0, 1, "launchd", "", lifecycleStart), noise}
	rig.poll()

	if _, ok := rig.birthFor(noise.PID); ok {
		t.Error("an unattributed, unclassified process reached the journal")
	}
	if rig.watcher.index.Len() == 0 {
		t.Error("the spawn index dropped the process, so ancestry chains would break")
	}
}

// TestWatcherNeverWritesRawEnvironmentValues asserts the redaction choke point
// holds at the record layer, not only inside the filter.
func TestWatcherNeverWritesRawEnvironmentValues(t *testing.T) {
	rig := newWatcherRig(t)
	root, child := rig.claudeSession()

	rig.table = []ProcEntry{procRow(1, 0, 1, "launchd", "", lifecycleStart), root}
	rig.poll()

	rig.env[child.PID] = map[string]string{"CLAUDE_CODE_SESSION_ID": "5f1c9a2e"}
	rig.table = append(rig.table, child)
	rig.poll()

	for _, rec := range rig.load() {
		line, err := encodeRecord(rec)
		if err != nil {
			t.Fatalf("re-encoding: %v", err)
		}
		for _, secret := range []string{"SLACK_TOKEN", "xoxb-", "AWS_SECRET", "ghp_"} {
			if contains(string(line), secret) {
				t.Errorf("record carries %q: %s", secret, line)
			}
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// TestWatcherRecordsOwnerExitFromKqueue asserts the primary death mechanism.
func TestWatcherRecordsOwnerExitFromKqueue(t *testing.T) {
	rig := newWatcherRig(t)
	root, child := rig.claudeSession()

	rig.table = []ProcEntry{procRow(1, 0, 1, "launchd", "", lifecycleStart), root}
	rig.poll()
	rig.table = append(rig.table, child)
	rig.poll()

	if rig.exits.watched[root.PID] == 0 {
		t.Fatal("no exit watch was attached to the session root")
	}

	// The root exits, and the kernel reports it before the next poll.
	exitAt := rig.clock.at()
	rig.exits.pending = append(rig.exits.pending, ExitNotice{PID: root.PID, At: exitAt})
	rig.table = []ProcEntry{procRow(1, 0, 1, "launchd", "", lifecycleStart), child}
	rig.poll()

	exit, ok := rig.ownerExit()
	if !ok {
		t.Fatal("no owner exit record was written")
	}
	if exit.Source != ExitSourceKqueue {
		t.Errorf("source = %q, want kqueue_note_exit", exit.Source)
	}
	if !exit.RootKey.Equal(root.Key()) {
		t.Errorf("root key = %v, want the session root", exit.RootKey)
	}
	if !exit.At.Equal(NormalizeTime(exitAt)) {
		t.Errorf("exit at %s, want the kernel's instant %s", exit.At, NormalizeTime(exitAt))
	}
}

func (r *watcherRig) ownerExit() (OwnerExitRecord, bool) {
	r.t.Helper()
	for _, rec := range r.load() {
		if rec.OwnerExit != nil {
			return *rec.OwnerExit, true
		}
	}
	return OwnerExitRecord{}, false
}

// TestWatcherFallsBackToPollAbsence asserts the fallback source. A watch that
// could not attach costs exactness rather than detection.
func TestWatcherFallsBackToPollAbsence(t *testing.T) {
	rig := newWatcherRig(t)
	root, child := rig.claudeSession()
	rig.exits.refuse[root.PID] = true

	rig.table = []ProcEntry{procRow(1, 0, 1, "launchd", "", lifecycleStart), root}
	rig.poll()
	rig.table = append(rig.table, child)
	rig.poll()

	rig.table = []ProcEntry{procRow(1, 0, 1, "launchd", "", lifecycleStart), child}
	rig.poll()

	exit, ok := rig.ownerExit()
	if !ok {
		t.Fatal("a refused watch produced no owner exit at all")
	}
	if exit.Source != ExitSourcePollAbsent {
		t.Errorf("source = %q, want poll_absent", exit.Source)
	}
	if !exit.Source.Trusted() {
		t.Error("poll absence must be trusted for eligibility")
	}
}

// TestWatcherDropsAWatchOnIdentifierReuse asserts the attach race from the other
// side. A recycled identifier must not inherit a session root's watch.
func TestWatcherDropsAWatchOnIdentifierReuse(t *testing.T) {
	rig := newWatcherRig(t)
	root, child := rig.claudeSession()

	rig.table = []ProcEntry{procRow(1, 0, 1, "launchd", "", lifecycleStart), root}
	rig.poll()
	rig.table = append(rig.table, child)
	rig.poll()

	// The identifier is reused by a different process one hour later.
	reused := procRow(root.PID, 1, root.PID, "claude", "ttys000", lifecycleStart.Add(time.Hour))
	rig.table = []ProcEntry{procRow(1, 0, 1, "launchd", "", lifecycleStart), reused, child}
	rig.poll()

	if rig.exits.unwatch[root.PID] == 0 {
		t.Error("the watch on a reused identifier was not dropped")
	}
}

// TestWatcherPanicIsolationStopsAfterThree asserts the containment rule. A panic
// kills the poll rather than the daemon, and three consecutive panics stop the
// watcher and leave the scanner alone.
func TestWatcherPanicIsolationStopsAfterThree(t *testing.T) {
	rig := newWatcherRig(t)
	root, _ := rig.claudeSession()
	rig.panicOn = int(root.PID)

	rig.table = []ProcEntry{procRow(1, 0, 1, "launchd", "", lifecycleStart)}
	rig.poll()

	for i := 1; i <= MaxConsecutivePanics; i++ {
		rig.table = []ProcEntry{
			procRow(1, 0, 1, "launchd", "", lifecycleStart),
			procRow(root.PID+int32(i), 1, root.PID+int32(i), "claude", "ttys000", lifecycleStart),
		}
		rig.panicOn = int(root.PID) + i
		rig.poll()

		if i < MaxConsecutivePanics && rig.watcher.Stopped() {
			t.Fatalf("the watcher stopped after %d panics, want %d", i, MaxConsecutivePanics)
		}
	}

	if !rig.watcher.Stopped() {
		t.Fatal("the watcher did not stop after three consecutive panicking polls")
	}
	if rig.watcher.Healthy() {
		t.Error("a stopped watcher must mark attribution untrusted")
	}
	if rig.engine.Trusted() {
		t.Error("the engine still trusts a stopped watcher")
	}

	kinds := map[string]int{}
	for _, f := range rig.watcher.Findings() {
		kinds[f.Kind]++
	}
	if kinds[FindingWatcherStopped] != 1 {
		t.Errorf("findings %v, want one watcher_stopped", kinds)
	}
	if kinds[FindingWatcherPanic] != MaxConsecutivePanics {
		t.Errorf("findings %v, want %d contained panics", kinds, MaxConsecutivePanics)
	}
}

// TestWatcherPanicCounterResetsOnASuccessfulPoll asserts the panics must be
// consecutive.
func TestWatcherPanicCounterResetsOnASuccessfulPoll(t *testing.T) {
	rig := newWatcherRig(t)
	rig.table = []ProcEntry{procRow(1, 0, 1, "launchd", "", lifecycleStart)}
	rig.poll()

	for i := 0; i < 6; i++ {
		bad := procRow(int32(500+i), 1, int32(500+i), "claude", "ttys000", lifecycleStart)
		rig.panicOn = int(bad.PID)
		rig.table = append(rig.table, bad)
		rig.poll()

		rig.panicOn = 0
		rig.poll()
	}
	if rig.watcher.Stopped() {
		t.Error("alternating panics stopped the watcher; they were never consecutive")
	}
}

// TestWatcherHeartbeatCarriesTheCounters asserts the observability series,
// including the sleep gap the design measures uptime against.
func TestWatcherHeartbeatCarriesTheCounters(t *testing.T) {
	rig := newWatcherRig(t)
	root, child := rig.claudeSession()
	rig.failEnv[child.PID] = true

	rig.table = []ProcEntry{procRow(1, 0, 1, "launchd", "", lifecycleStart), root}
	rig.poll()
	rig.table = append(rig.table, child)
	rig.poll()

	rig.sleepPoll(30 * time.Minute)
	for i := 0; i < 70; i++ {
		rig.poll()
	}

	var beat HeartbeatRecord
	found := false
	for _, rec := range rig.load() {
		if rec.Heartbeat != nil {
			beat, found = *rec.Heartbeat, true
			break
		}
	}
	if !found {
		t.Fatal("no heartbeat was written after a minute of awake time")
	}
	if beat.Polls == 0 {
		t.Error("the heartbeat counted no polls")
	}
	if beat.SleepGapMillis < (29 * time.Minute).Milliseconds() {
		t.Errorf("sleep gap = %dms, want about 30 minutes", beat.SleepGapMillis)
	}
	if beat.EnvReadFailures == 0 {
		t.Error("the heartbeat counted no environment read failures")
	}
	if beat.Tracked == 0 {
		t.Error("the heartbeat tracked nothing")
	}
	if beat.JournalBytes == 0 {
		t.Error("the heartbeat reported an empty journal")
	}
}

// TestWatcherStalenessIsMeasuredInAwakeTime asserts an overnight sleep never
// reads as a dead watcher, and that a genuinely silent watcher does.
func TestWatcherStalenessIsMeasuredInAwakeTime(t *testing.T) {
	rig := newWatcherRig(t)
	rig.table = []ProcEntry{procRow(1, 0, 1, "launchd", "", lifecycleStart)}
	rig.poll()

	rig.sleepPoll(10 * time.Hour)
	if !rig.watcher.Healthy() {
		t.Error("a ten hour sleep marked the watcher dead; staleness is awake time")
	}
	if !rig.engine.Trusted() {
		t.Error("a sleeping machine must not freeze the lifecycle")
	}

	// Three heartbeat intervals of awake time pass with the watcher silent. It
	// writes no heartbeat because it is not polling, which is the only condition
	// staleness ever describes.
	rig.clock.awake(4 * time.Minute)
	if rig.watcher.Healthy() {
		t.Error("four minutes of awake silence left the watcher healthy")
	}
	if age := rig.watcher.LastHeartbeatAge(); age < 4*time.Minute {
		t.Errorf("last heartbeat age = %s, want at least four minutes", age)
	}

	// A watcher that resumes polling writes a heartbeat and is current again.
	rig.poll()
	if !rig.watcher.Healthy() {
		t.Error("the watcher stayed stale after it resumed polling")
	}
}

// TestWatcherDrivesTheLifecycleEngine asserts the wiring: the engine advances on
// the scan cadence from the same snapshot the index was built from.
func TestWatcherDrivesTheLifecycleEngine(t *testing.T) {
	rig := newWatcherRig(t)
	root, child := rig.claudeSession()

	rig.table = []ProcEntry{procRow(1, 0, 1, "launchd", "", lifecycleStart), root}
	rig.poll()
	rig.table = append(rig.table, child)
	rig.poll()

	state, ok := rig.engine.State(child.Key())
	if !ok {
		t.Fatal("the child is not tracked by the engine")
	}
	if state.State != StateActive {
		t.Fatalf("state = %s, want ACTIVE", state.State)
	}

	// The owner exits and the child is reparented to launchd, which is the
	// strong lifecycle signal.
	rig.exits.pending = append(rig.exits.pending, ExitNotice{PID: root.PID, At: rig.clock.at()})
	orphan := child
	orphan.PPID = 1
	rig.table = []ProcEntry{procRow(1, 0, 1, "launchd", "", lifecycleStart), orphan}
	rig.poll()

	if state, _ := rig.engine.State(child.Key()); state.State != StateOwnerGone {
		t.Fatalf("state = %s after the owner exited, want OWNER_GONE", state.State)
	}

	for i := 0; i < 1200; i++ {
		rig.poll()
		if s, _ := rig.engine.State(child.Key()); s.State == StateConfirmedOrphan {
			break
		}
	}
	final, _ := rig.engine.State(child.Key())
	if final.State != StateConfirmedOrphan {
		t.Fatalf("state = %s after the window and confirmations, want CONFIRMED_ORPHAN", final.State)
	}

	// Transitions reached the journal, which is what makes the window survive a
	// restart.
	transitions := 0
	for _, rec := range rig.load() {
		if rec.Transition != nil {
			transitions++
		}
	}
	if transitions == 0 {
		t.Error("no transition records were written")
	}
}

// TestWatcherSnapshotFailureDoesNotStopIt asserts a read error is a skipped
// poll rather than a dead watcher.
func TestWatcherSnapshotFailureDoesNotStopIt(t *testing.T) {
	rig := newWatcherRig(t)
	rig.table = []ProcEntry{procRow(1, 0, 1, "launchd", "", lifecycleStart)}
	rig.poll()

	rig.snapErr = ErrNotSupported
	for i := 0; i < 5; i++ {
		rig.poll()
	}
	if rig.watcher.Stopped() {
		t.Error("a failing snapshot stopped the watcher; it is not a panic")
	}

	rig.snapErr = nil
	rig.poll()
	if rig.watcher.Stopped() {
		t.Error("the watcher did not recover after the snapshot source came back")
	}
}

// TestWatcherWritesASnapshotOnShutdown asserts a clean stop leaves a snapshot,
// so a restart replays only the journal tail.
func TestWatcherWritesASnapshotOnShutdown(t *testing.T) {
	rig := newWatcherRig(t)
	root, child := rig.claudeSession()
	rig.table = []ProcEntry{procRow(1, 0, 1, "launchd", "", lifecycleStart), root, child}
	rig.poll()

	rig.watcher.shutdown()

	snapshot, ok := rig.store.ReadSnapshot()
	if !ok {
		t.Fatal("no store snapshot was written on shutdown")
	}
	if len(snapshot.Processes) == 0 {
		t.Error("the snapshot holds no processes")
	}
	if !rig.exits.closed {
		t.Error("the exit watches were not released")
	}
}
