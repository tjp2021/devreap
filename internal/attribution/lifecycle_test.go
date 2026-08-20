package attribution

import (
	"testing"
	"time"

	"github.com/tjp2021/devreap/internal/config"
)

// fakeClock drives sleep and wake by hand. Advancing wall time without
// advancing the monotonic reading is exactly what a machine sleep looks like to
// the engine, which is how every sleep test here works without sleeping.
type fakeClock struct {
	wall time.Time
	mono time.Duration
}

func (c *fakeClock) Now() time.Time           { return c.wall }
func (c *fakeClock) Monotonic() time.Duration { return c.mono }
func (c *fakeClock) awake(d time.Duration)    { c.wall = c.wall.Add(d); c.mono += d }
func (c *fakeClock) asleep(d time.Duration)   { c.wall = c.wall.Add(d) }
func (c *fakeClock) at() time.Time            { return c.wall }
func newFakeClock() *fakeClock                { return &fakeClock{wall: lifecycleStart} }

var lifecycleStart = time.Date(2026, 8, 20, 5, 24, 11, 0, time.UTC)

const testScanInterval = 30 * time.Second

// fixture is one tracked MCP process owned by one session, which is the shape
// every transition test drives.
type fixture struct {
	t       *testing.T
	engine  *Engine
	clock   *fakeClock
	key     ProcKey
	rootKey ProcKey
	session string
	birth   BirthRecord

	// journal is every record the watcher would have appended, in order. The
	// restart tests reload from it rather than from the engine's memory.
	journal []Record
}

// record appends what the engine just wrote, which is what the watcher does on
// every cycle.
func (f *fixture) record() {
	for _, tr := range f.engine.Transitions() {
		t := tr
		f.journal = append(f.journal, Record{Type: RecordTransition, Transition: &t})
	}
}

// lastTransition returns the newest transition in the journal, which is the
// point a restart resumes from.
func (f *fixture) lastTransition() TransitionRecord {
	f.t.Helper()
	for i := len(f.journal) - 1; i >= 0; i-- {
		if f.journal[i].Transition != nil && f.journal[i].Transition.Key.Equal(f.key) {
			return *f.journal[i].Transition
		}
	}
	f.t.Fatal("the journal holds no transition for this process")
	return TransitionRecord{}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	clock := newFakeClock()
	engine := NewEngine(EngineConfig{
		Windows:       config.DefaultLifecycleGrace(),
		Confirmations: config.DefaultConfirmationCount,
		ScanInterval:  testScanInterval,
	}, clock)

	f := &fixture{
		t:       t,
		engine:  engine,
		clock:   clock,
		key:     NewProcKey(98925, lifecycleStart.Add(-time.Hour)),
		rootKey: NewProcKey(98888, lifecycleStart.Add(-2*time.Hour)),
		session: "5f1c9a2e",
	}
	f.birth = BirthRecord{
		ObservedAt: lifecycleStart,
		Source:     BirthSourcePoll,
		Key:        f.key,
		Class:      config.ClassMCP,
		Name:       "node",
		Owner: OwnershipClaim{
			SessionID:  f.session,
			Harness:    "claude-code-cli",
			Repo:       "/Users/dev/projects/example",
			RootKey:    f.rootKey,
			Confidence: ConfidenceObserved,
			Channels:   []Channel{ChannelWatchedAncestry},
		},
	}
	engine.OnBirth(f.birth)
	f.journal = append(f.journal, Record{Type: RecordBirth, Birth: &f.birth})
	f.record()
	return f
}

// ownerExits records the session root's exit through a trusted source.
func (f *fixture) ownerExits() OwnerExitRecord {
	f.t.Helper()
	exit := OwnerExitRecord{
		At:        f.clock.at(),
		SessionID: f.session,
		Harness:   "claude-code-cli",
		RootKey:   f.rootKey,
		Source:    ExitSourceKqueue,
	}
	f.engine.OnOwnerExit(exit)
	f.journal = append(f.journal, Record{Type: RecordOwnerExit, OwnerExit: &exit})
	f.record()
	return exit
}

// scan advances the clock by one awake scan interval and runs one cycle.
func (f *fixture) scan(obs ScanObservation) ScanOutcome {
	f.t.Helper()
	f.clock.awake(testScanInterval)
	obs.Key = f.key
	outcome := f.engine.Scan([]ScanObservation{obs})
	for i := range outcome.Transitions {
		tr := outcome.Transitions[i]
		f.journal = append(f.journal, Record{Type: RecordTransition, Transition: &tr})
	}
	return outcome
}

// healthyOrphan is the observation of a process whose owner is gone and whose
// strong lifecycle signal is present on a live read.
func healthyOrphan() ScanObservation {
	return ScanObservation{Present: true, Class: config.ClassMCP, Adopted: TriFalse, StrongSignal: TriTrue}
}

func (f *fixture) state() LifecycleState {
	f.t.Helper()
	state, ok := f.engine.State(f.key)
	if !ok {
		f.t.Fatal("the process is not tracked")
	}
	return state.State
}

func (f *fixture) process() ProcessState {
	f.t.Helper()
	state, _ := f.engine.State(f.key)
	return state
}

// driveTo scans until the process reaches a state, and fails rather than looping
// forever if it never does.
func (f *fixture) driveTo(target LifecycleState, obs ScanObservation) {
	f.t.Helper()
	const maxScans = 400
	for i := 0; i < maxScans; i++ {
		if f.state() == target {
			return
		}
		f.scan(obs)
	}
	f.t.Fatalf("process never reached %s, stopped at %s after %d scans", target, f.state(), maxScans)
}

// TestLifecycleTransitions drives the transition table. Each case names the row
// it covers, and the table is the design's, not a paraphrase of the code.
func TestLifecycleTransitions(t *testing.T) {
	t.Run("birth to ACTIVE with a live owner", func(t *testing.T) {
		f := newFixture(t)
		if got := f.state(); got != StateActive {
			t.Errorf("state = %s, want ACTIVE", got)
		}
	})

	t.Run("birth to UNATTRIBUTED with confidence none", func(t *testing.T) {
		f := newFixture(t)
		key := NewProcKey(777, lifecycleStart)
		f.engine.OnBirth(BirthRecord{Key: key, Owner: OwnershipClaim{Confidence: ConfidenceNone}})
		state, _ := f.engine.State(key)
		if state.State != StateUnattributed {
			t.Errorf("state = %s, want UNATTRIBUTED", state.State)
		}
	})

	t.Run("ACTIVE to OWNER_GONE on a recorded owner exit", func(t *testing.T) {
		f := newFixture(t)
		f.ownerExits()
		if got := f.state(); got != StateOwnerGone {
			t.Errorf("state = %s, want OWNER_GONE", got)
		}
	})

	t.Run("OWNER_GONE to GRACE_PERIOD on the first scan after the exit", func(t *testing.T) {
		f := newFixture(t)
		f.ownerExits()
		f.scan(healthyOrphan())
		if got := f.state(); got != StateGracePeriod {
			t.Errorf("state = %s, want GRACE_PERIOD", got)
		}
	})

	t.Run("OWNER_GONE to ACTIVE when a poll-absent owner is seen alive", func(t *testing.T) {
		f := newFixture(t)
		f.engine.OnOwnerExit(OwnerExitRecord{
			At: f.clock.at(), SessionID: f.session, RootKey: f.rootKey, Source: ExitSourcePollAbsent,
		})
		if got := f.state(); got != StateOwnerGone {
			t.Fatalf("state = %s, want OWNER_GONE", got)
		}
		obs := healthyOrphan()
		obs.OwnerAlive = TriTrue
		f.scan(obs)
		if got := f.state(); got != StateActive {
			t.Errorf("state = %s, want ACTIVE", got)
		}
	})

	t.Run("a kqueue exit is not withdrawn by an owner seen alive", func(t *testing.T) {
		f := newFixture(t)
		f.ownerExits()
		obs := healthyOrphan()
		obs.OwnerAlive = TriTrue
		f.scan(obs)
		if got := f.state(); got == StateActive {
			t.Error("a kqueue NOTE_EXIT means the process is gone and must not be revoked")
		}
	})

	t.Run("GRACE_PERIOD to ORPHAN_CANDIDATE when the window is spent", func(t *testing.T) {
		f := newFixture(t)
		f.ownerExits()
		f.driveTo(StateOrphanCandidate, healthyOrphan())
		spent := time.Duration(f.process().AwakeMillis) * time.Millisecond
		if spent < 5*time.Minute {
			t.Errorf("candidacy arrived after %s of awake time, want at least the 5 minute MCP window", spent)
		}
	})

	t.Run("GRACE_PERIOD to ACTIVE on adoption", func(t *testing.T) {
		f := newFixture(t)
		f.ownerExits()
		f.scan(healthyOrphan())
		obs := healthyOrphan()
		obs.Adopted = TriTrue
		f.scan(obs)
		if got := f.state(); got != StateActive {
			t.Errorf("state = %s, want ACTIVE", got)
		}
	})

	t.Run("ORPHAN_CANDIDATE to CONFIRMED_ORPHAN when confirmations are reached", func(t *testing.T) {
		f := newFixture(t)
		f.ownerExits()
		f.driveTo(StateConfirmedOrphan, healthyOrphan())
		if got := f.process().Confirmations; got < config.DefaultConfirmationCount {
			t.Errorf("confirmations = %d, want at least %d", got, config.DefaultConfirmationCount)
		}
	})

	t.Run("ORPHAN_CANDIDATE to GRACE_PERIOD resets both accumulators", func(t *testing.T) {
		f := newFixture(t)
		f.ownerExits()
		f.driveTo(StateOrphanCandidate, healthyOrphan())
		f.scan(healthyOrphan())

		obs := healthyOrphan()
		obs.StrongSignal = TriFalse
		f.scan(obs)

		if got := f.state(); got != StateGracePeriod {
			t.Fatalf("state = %s, want GRACE_PERIOD", got)
		}
		state := f.process()
		if state.Confirmations != 0 || state.AwakeMillis != 0 {
			t.Errorf("counter %d and accumulator %dms, want both reset", state.Confirmations, state.AwakeMillis)
		}
	})

	t.Run("ORPHAN_CANDIDATE to ACTIVE on adoption", func(t *testing.T) {
		f := newFixture(t)
		f.ownerExits()
		f.driveTo(StateOrphanCandidate, healthyOrphan())
		obs := healthyOrphan()
		obs.Adopted = TriTrue
		f.scan(obs)
		if got := f.state(); got != StateActive {
			t.Errorf("state = %s, want ACTIVE", got)
		}
	})

	t.Run("CONFIRMED_ORPHAN to ACTIVE on adoption", func(t *testing.T) {
		f := newFixture(t)
		f.ownerExits()
		f.driveTo(StateConfirmedOrphan, healthyOrphan())
		obs := healthyOrphan()
		obs.Adopted = TriTrue
		f.scan(obs)
		if got := f.state(); got != StateActive {
			t.Errorf("state = %s, want ACTIVE", got)
		}
	})

	t.Run("UNATTRIBUTED to ACTIVE on a claim upgrade", func(t *testing.T) {
		f := newFixture(t)
		key := NewProcKey(778, lifecycleStart)
		f.engine.OnBirth(BirthRecord{Key: key, Owner: OwnershipClaim{Confidence: ConfidenceNone}})
		f.engine.OnClaimUpgrade(ClaimUpgradeRecord{
			Key: key, From: ConfidenceNone, To: ConfidenceObserved,
			Evidence: ClaimUpgradeEvidence{SessionID: f.session, RootKey: f.rootKey},
		})
		state, _ := f.engine.State(key)
		if state.State != StateActive {
			t.Errorf("state = %s, want ACTIVE", state.State)
		}
	})

	t.Run("any state to EXITED when the key is gone", func(t *testing.T) {
		f := newFixture(t)
		f.ownerExits()
		f.driveTo(StateOrphanCandidate, healthyOrphan())
		f.scan(ScanObservation{Present: false})
		if got := f.state(); got != StateExited {
			t.Errorf("state = %s, want EXITED", got)
		}
	})
}

// TestConfirmedOrphanRequiresAllFiveR6Conditions asserts no input sequence
// reaches CONFIRMED_ORPHAN without every condition of R6. Each case removes one
// condition and drives the same sequence that succeeds with all five.
func TestConfirmedOrphanRequiresAllFiveR6Conditions(t *testing.T) {
	t.Run("all five hold", func(t *testing.T) {
		f := newFixture(t)
		f.ownerExits()
		f.driveTo(StateConfirmedOrphan, healthyOrphan())
		if !f.engine.Eligible(f.key) {
			t.Error("a process satisfying all five conditions must be eligible")
		}
	})

	t.Run("no recorded owner exit", func(t *testing.T) {
		f := newFixture(t)
		for i := 0; i < 60; i++ {
			f.scan(healthyOrphan())
		}
		if got := f.state(); got == StateConfirmedOrphan {
			t.Error("a process whose owner never exited reached CONFIRMED_ORPHAN")
		}
		if f.engine.Eligible(f.key) {
			t.Error("eligible without a recorded owner exit")
		}
	})

	t.Run("owner exit from an untrusted source", func(t *testing.T) {
		f := newFixture(t)
		f.engine.OnOwnerExit(OwnerExitRecord{
			At: f.clock.at(), SessionID: f.session, RootKey: f.rootKey, Source: ExitSourceAgentHook,
		})
		for i := 0; i < 60; i++ {
			f.scan(healthyOrphan())
		}
		if got := f.state(); got == StateConfirmedOrphan {
			t.Error("the agent hook alone confirmed an orphan; it is enrichment only")
		}
	})

	t.Run("class window not reached", func(t *testing.T) {
		f := newFixture(t)
		f.ownerExits()
		f.scan(healthyOrphan())
		// Four minutes of awake time against the five minute MCP window.
		for i := 0; i < 8; i++ {
			f.scan(healthyOrphan())
		}
		if got := f.state(); got != StateGracePeriod {
			t.Errorf("state = %s before the window is spent, want GRACE_PERIOD", got)
		}
	})

	t.Run("confirmations not reached", func(t *testing.T) {
		f := newFixture(t)
		f.ownerExits()
		f.driveTo(StateOrphanCandidate, healthyOrphan())
		f.scan(healthyOrphan())
		f.scan(healthyOrphan())
		if got := f.state(); got == StateConfirmedOrphan {
			t.Errorf("confirmed after %d confirmations, want %d", f.process().Confirmations, config.DefaultConfirmationCount)
		}
	})

	t.Run("confidence below observed", func(t *testing.T) {
		f := newFixture(t)
		key := NewProcKey(779, lifecycleStart)
		f.engine.OnBirth(BirthRecord{
			Key: key, Class: config.ClassMCP,
			Owner: OwnershipClaim{SessionID: f.session, RootKey: f.rootKey, Confidence: ConfidenceInferred},
		})
		f.ownerExits()
		for i := 0; i < 60; i++ {
			f.clock.awake(testScanInterval)
			f.engine.Scan([]ScanObservation{{Key: key, Present: true, Class: config.ClassMCP, StrongSignal: TriTrue}})
		}
		state, _ := f.engine.State(key)
		if state.State == StateConfirmedOrphan {
			t.Error("an inferred claim reached CONFIRMED_ORPHAN; only observed may")
		}
		if f.engine.Eligible(key) {
			t.Error("an inferred claim is eligible")
		}
	})

	t.Run("strong signal absent on the live read", func(t *testing.T) {
		f := newFixture(t)
		f.ownerExits()
		f.driveTo(StateOrphanCandidate, healthyOrphan())

		weak := healthyOrphan()
		weak.StrongSignal = TriUnknown
		for i := 0; i < 10; i++ {
			f.scan(weak)
		}
		if got := f.state(); got == StateConfirmedOrphan {
			t.Error("confirmed with no strong signal on the live read")
		}
		if got := f.process().Confirmations; got != 0 {
			t.Errorf("confirmations = %d, want 0; an unreadable condition confirms nothing", got)
		}
	})
}

// TestUnreadableConditionHoldsTheCounter asserts the difference between false
// and unreadable. A transient read error must not restart a window a real
// observation already earned.
func TestUnreadableConditionHoldsTheCounter(t *testing.T) {
	f := newFixture(t)
	f.ownerExits()
	f.driveTo(StateOrphanCandidate, healthyOrphan())

	f.scan(healthyOrphan())
	earned := f.process().Confirmations
	if earned == 0 {
		t.Fatal("no confirmation was earned to hold")
	}
	accumulated := f.process().AwakeMillis

	unreadable := healthyOrphan()
	unreadable.StrongSignal = TriUnknown
	f.scan(unreadable)

	state := f.process()
	if state.Confirmations != earned {
		t.Errorf("confirmations = %d after an unreadable scan, want %d held", state.Confirmations, earned)
	}
	if state.AwakeMillis < accumulated {
		t.Errorf("accumulator fell from %dms to %dms on an unreadable scan", accumulated, state.AwakeMillis)
	}
	if got := f.state(); got != StateOrphanCandidate {
		t.Errorf("state = %s, want ORPHAN_CANDIDATE held", got)
	}
}

// TestSleepWakeGapPausesWindow simulates sleep by advancing wall time without
// advancing the monotonic clock. The gap credits zero awake time, both
// accumulators survive it unchanged, and a re-enumeration is requested on wake.
func TestSleepWakeGapPausesWindow(t *testing.T) {
	f := newFixture(t)
	f.ownerExits()
	f.driveTo(StateOrphanCandidate, healthyOrphan())
	f.scan(healthyOrphan())

	before := f.process()
	if before.Confirmations == 0 || before.AwakeMillis == 0 {
		t.Fatal("nothing was accumulated before the gap")
	}

	f.clock.asleep(8 * time.Hour)
	outcome := f.engine.Scan([]ScanObservation{{Key: f.key, Present: true, Class: config.ClassMCP, StrongSignal: TriTrue}})

	if outcome.AwakeDelta != 0 {
		t.Errorf("awake delta = %s across a sleep gap, want 0", outcome.AwakeDelta)
	}
	if outcome.SleepGap < 7*time.Hour {
		t.Errorf("sleep gap = %s, want about 8 hours", outcome.SleepGap)
	}
	if !outcome.NeedsReenumeration {
		t.Error("a wake must request a full re-enumeration before awake time is credited")
	}

	after := f.process()
	if after.AwakeMillis != before.AwakeMillis {
		t.Errorf("accumulator moved from %dms to %dms across a sleep gap", before.AwakeMillis, after.AwakeMillis)
	}
	if after.Confirmations != before.Confirmations {
		t.Errorf("counter moved from %d to %d across a sleep gap", before.Confirmations, after.Confirmations)
	}

	// The window resumes where it stopped rather than restarting.
	f.driveTo(StateConfirmedOrphan, healthyOrphan())
}

// TestOvernightBurstProfileCompletesWindow drives the measured overnight profile:
// repeated 11 second awake bursts separated by maintenance sleep. A rule
// requiring a gap-free window would never complete the 5 minute MCP window
// overnight, and a rule that reset on any gap would return the accumulator to
// zero about every minute.
func TestOvernightBurstProfileCompletesWindow(t *testing.T) {
	f := newFixture(t)
	f.ownerExits()
	f.scan(healthyOrphan())

	const burst = 11 * time.Second
	const maintenanceSleep = 12 * time.Minute

	for i := 0; i < 200 && f.state() != StateConfirmedOrphan; i++ {
		f.clock.awake(burst)
		f.engine.Scan([]ScanObservation{{Key: f.key, Present: true, Class: config.ClassMCP, StrongSignal: TriTrue}})
		f.clock.asleep(maintenanceSleep)
		f.engine.Scan([]ScanObservation{{Key: f.key, Present: true, Class: config.ClassMCP, StrongSignal: TriTrue}})
	}

	if got := f.state(); got != StateConfirmedOrphan {
		t.Fatalf("state = %s after an overnight burst profile, want CONFIRMED_ORPHAN", got)
	}
	spent := time.Duration(f.process().AwakeMillis) * time.Millisecond
	if spent < 5*time.Minute {
		t.Errorf("completed on %s of awake time, want at least the 5 minute window", spent)
	}
}

// TestAccumulatorSurvivesRestart writes transition records mid-window, discards
// the in-memory state, reloads from the journal, and asserts the accumulator and
// the confirmation count resume at their recorded values rather than at zero.
func TestAccumulatorSurvivesRestart(t *testing.T) {
	restart := func(t *testing.T, f *fixture, snapshot *StoreSnapshot) ProcessState {
		t.Helper()
		restarted := NewEngine(EngineConfig{
			Windows:       config.DefaultLifecycleGrace(),
			Confirmations: config.DefaultConfirmationCount,
			ScanInterval:  testScanInterval,
		}, newFakeClock())
		restarted.Restore(Recover(snapshot, f.journal))
		state, ok := restarted.State(f.key)
		if !ok {
			t.Fatal("the process was lost across the restart")
		}
		return state
	}

	t.Run("from the journal, mid-window", func(t *testing.T) {
		f := newFixture(t)
		f.ownerExits()
		f.driveTo(StateOrphanCandidate, healthyOrphan())

		// A restart resumes from the last record, which is what the design says
		// it costs: the time actually spent down, not the progress already
		// earned.
		last := f.lastTransition()
		if last.AwakeMillis < (5 * time.Minute).Milliseconds() {
			t.Fatalf("the last record carries %dms of awake time, want the spent window", last.AwakeMillis)
		}

		after := restart(t, f, nil)
		if after.State != StateOrphanCandidate {
			t.Errorf("state resumed as %s, want ORPHAN_CANDIDATE", after.State)
		}
		if after.AwakeMillis != last.AwakeMillis {
			t.Errorf("accumulator resumed at %dms, want the recorded %dms", after.AwakeMillis, last.AwakeMillis)
		}
		if after.AwakeMillis == 0 {
			t.Error("the accumulator restarted at zero, discarding earned progress")
		}
		if after.Confirmations != last.Confirmations {
			t.Errorf("counter resumed at %d, want the recorded %d", after.Confirmations, last.Confirmations)
		}
		if after.Class != config.ClassMCP {
			t.Errorf("class resumed as %q, want the birth record's %q", after.Class, config.ClassMCP)
		}
	})

	t.Run("from the journal, with confirmations earned", func(t *testing.T) {
		f := newFixture(t)
		f.ownerExits()
		f.driveTo(StateConfirmedOrphan, healthyOrphan())

		last := f.lastTransition()
		if last.Confirmations < config.DefaultConfirmationCount {
			t.Fatalf("the confirming record carries %d confirmations", last.Confirmations)
		}

		after := restart(t, f, nil)
		if after.State != StateConfirmedOrphan {
			t.Errorf("state resumed as %s, want CONFIRMED_ORPHAN", after.State)
		}
		if after.Confirmations != last.Confirmations {
			t.Errorf("counter resumed at %d, want %d", after.Confirmations, last.Confirmations)
		}
	})

	t.Run("from a snapshot written between transitions", func(t *testing.T) {
		f := newFixture(t)
		f.ownerExits()
		f.driveTo(StateOrphanCandidate, healthyOrphan())
		// One more scan earns a confirmation, which writes no transition record.
		// The 5 minute snapshot is what carries that progress across a restart.
		f.scan(healthyOrphan())

		before := f.process()
		if before.Confirmations == 0 {
			t.Fatal("no confirmation was earned between transitions")
		}
		snapshot := &StoreSnapshot{
			V:         RecordSchemaVersion,
			Type:      RecordSnapshot,
			WrittenAt: NormalizeTime(f.clock.at()),
			Processes: f.engine.States(),
			Sessions:  f.engine.Sessions(),
		}

		after := restart(t, f, snapshot)
		if after.Confirmations != before.Confirmations {
			t.Errorf("counter resumed at %d, want the snapshot's %d", after.Confirmations, before.Confirmations)
		}
		if after.AwakeMillis != before.AwakeMillis {
			t.Errorf("accumulator resumed at %dms, want the snapshot's %dms", after.AwakeMillis, before.AwakeMillis)
		}
	})
}

// TestUntrustedDataPausesAccumulators stops the heartbeat and asserts the
// accumulators pause rather than advance, and resume on recovery. Time nobody
// observed cannot be evidence that a process stayed orphaned.
func TestUntrustedDataPausesAccumulators(t *testing.T) {
	f := newFixture(t)
	f.ownerExits()
	f.scan(healthyOrphan())
	f.scan(healthyOrphan())
	before := f.process().AwakeMillis

	f.engine.SetTrusted(false)
	for i := 0; i < 6; i++ {
		f.scan(healthyOrphan())
	}
	during := f.process()
	if during.AwakeMillis != before {
		t.Errorf("accumulator advanced from %dms to %dms while data was untrusted", before, during.AwakeMillis)
	}
	if during.State != StateGracePeriod {
		t.Errorf("state = %s while untrusted, want the lifecycle frozen at GRACE_PERIOD", during.State)
	}

	f.engine.SetTrusted(true)
	f.scan(healthyOrphan())
	if got := f.process().AwakeMillis; got <= before {
		t.Errorf("accumulator = %dms after recovery, want it advancing again from %dms", got, before)
	}
}

// TestUntrustedDataBlocksEligibility asserts phase B gating refuses every
// process until the watcher recovers.
func TestUntrustedDataBlocksEligibility(t *testing.T) {
	f := newFixture(t)
	f.ownerExits()
	f.driveTo(StateConfirmedOrphan, healthyOrphan())
	if !f.engine.Eligible(f.key) {
		t.Fatal("the process should be eligible before the watcher goes stale")
	}
	f.engine.SetTrusted(false)
	if f.engine.Eligible(f.key) {
		t.Error("untrusted data must refuse every process")
	}
}

// TestUnattributedNeverExpiresOutOfItsWindow asserts R8. No owner means no owner
// death, at any age, and only a claim upgrade ends the condition.
func TestUnattributedNeverExpiresOutOfItsWindow(t *testing.T) {
	f := newFixture(t)
	key := NewProcKey(780, lifecycleStart)
	f.engine.OnBirth(BirthRecord{Key: key, Class: config.ClassMCP, Owner: OwnershipClaim{Confidence: ConfidenceNone}})

	for i := 0; i < 200; i++ {
		f.clock.awake(testScanInterval)
		f.engine.Scan([]ScanObservation{{Key: key, Present: true, Class: config.ClassMCP, StrongSignal: TriTrue}})
	}
	state, _ := f.engine.State(key)
	if state.State != StateUnattributed {
		t.Errorf("state = %s after 100 minutes, want UNATTRIBUTED held", state.State)
	}
	if f.engine.Eligible(key) {
		t.Error("an unattributed process became eligible")
	}
}

// TestUnclassifiedProcessHoldsInGracePeriod asserts the other half of R8, and
// that the condition ends when a later scan classifies the process.
func TestUnclassifiedProcessHoldsInGracePeriod(t *testing.T) {
	f := newFixture(t)
	key := NewProcKey(781, lifecycleStart)
	f.engine.OnBirth(BirthRecord{
		Key: key,
		Owner: OwnershipClaim{
			SessionID: f.session, RootKey: f.rootKey, Confidence: ConfidenceObserved,
		},
	})
	f.ownerExits()

	unclassified := ScanObservation{Key: key, Present: true, StrongSignal: TriTrue}
	for i := 0; i < 40; i++ {
		f.clock.awake(testScanInterval)
		f.engine.Scan([]ScanObservation{unclassified})
	}
	state, _ := f.engine.State(key)
	if state.State != StateGracePeriod {
		t.Fatalf("state = %s, want GRACE_PERIOD held while unclassified", state.State)
	}

	// A pattern matches on a later scan, which is how the condition ends.
	classified := ScanObservation{Key: key, Present: true, Class: config.ClassMCP, StrongSignal: TriTrue}
	for i := 0; i < 40; i++ {
		f.clock.awake(testScanInterval)
		f.engine.Scan([]ScanObservation{classified})
		if s, _ := f.engine.State(key); s.State == StateConfirmedOrphan {
			return
		}
	}
	t.Error("a process classified by a later scan never progressed")
}

// TestStateMachineReachabilityAndRecovery asserts RECLAIM_REQUESTED is reachable
// from CONFIRMED_ORPHAN, that every state except EXITED and RECLAIMED has an edge
// back to ACTIVE, and that no state is a dead end.
func TestStateMachineReachabilityAndRecovery(t *testing.T) {
	t.Run("RECLAIM_REQUESTED is reachable from CONFIRMED_ORPHAN", func(t *testing.T) {
		f := newFixture(t)
		f.ownerExits()
		f.driveTo(StateConfirmedOrphan, healthyOrphan())
		if !f.engine.RequestReclaim(f.key) {
			t.Fatal("the phase B edge is unreachable from CONFIRMED_ORPHAN")
		}
		if got := f.state(); got != StateReclaimRequested {
			t.Fatalf("state = %s, want RECLAIM_REQUESTED", got)
		}
	})

	t.Run("RECLAIM_REQUESTED reaches both outcomes", func(t *testing.T) {
		for _, succeeded := range []bool{true, false} {
			f := newFixture(t)
			f.ownerExits()
			f.driveTo(StateConfirmedOrphan, healthyOrphan())
			f.engine.RequestReclaim(f.key)
			f.engine.CompleteReclaim(f.key, succeeded)

			want := StateReclaimed
			if !succeeded {
				want = StateReclaimFailed
			}
			if got := f.state(); got != want {
				t.Errorf("succeeded=%v gave %s, want %s", succeeded, got, want)
			}
		}
	})

	// reachStates builds one fixture per non-terminal state and asserts the
	// recovery edge back to ACTIVE.
	reach := map[LifecycleState]func(*fixture){
		StateOwnerGone: func(f *fixture) {
			f.ownerExits()
		},
		StateGracePeriod: func(f *fixture) {
			f.ownerExits()
			f.scan(healthyOrphan())
		},
		StateOrphanCandidate: func(f *fixture) {
			f.ownerExits()
			f.driveTo(StateOrphanCandidate, healthyOrphan())
		},
		StateConfirmedOrphan: func(f *fixture) {
			f.ownerExits()
			f.driveTo(StateConfirmedOrphan, healthyOrphan())
		},
		StateReclaimRequested: func(f *fixture) {
			f.ownerExits()
			f.driveTo(StateConfirmedOrphan, healthyOrphan())
			f.engine.RequestReclaim(f.key)
		},
		StateReclaimFailed: func(f *fixture) {
			f.ownerExits()
			f.driveTo(StateConfirmedOrphan, healthyOrphan())
			f.engine.RequestReclaim(f.key)
			f.engine.CompleteReclaim(f.key, false)
		},
	}

	for state, drive := range reach {
		t.Run(string(state)+" has an edge back to ACTIVE", func(t *testing.T) {
			f := newFixture(t)
			drive(f)
			if got := f.state(); got != state {
				t.Fatalf("setup reached %s, want %s", got, state)
			}

			if state == StateReclaimRequested {
				f.engine.AbandonReclaim(f.key)
			} else {
				obs := healthyOrphan()
				obs.Adopted = TriTrue
				f.scan(obs)
			}
			if got := f.state(); got != StateActive {
				t.Errorf("recovery from %s gave %s, want ACTIVE", state, got)
			}
		})
	}

	t.Run("UNATTRIBUTED has an edge back to ACTIVE", func(t *testing.T) {
		f := newFixture(t)
		key := NewProcKey(782, lifecycleStart)
		f.engine.OnBirth(BirthRecord{Key: key, Owner: OwnershipClaim{Confidence: ConfidenceNone}})
		f.engine.OnClaimUpgrade(ClaimUpgradeRecord{
			Key: key, From: ConfidenceNone, To: ConfidenceObserved,
			Evidence: ClaimUpgradeEvidence{SessionID: f.session, RootKey: f.rootKey},
		})
		state, _ := f.engine.State(key)
		if state.State != StateActive {
			t.Errorf("state = %s, want ACTIVE", state.State)
		}
	})

	t.Run("only EXITED and RECLAIMED are terminal", func(t *testing.T) {
		all := []LifecycleState{
			StateActive, StateOwnerGone, StateGracePeriod, StateOrphanCandidate,
			StateConfirmedOrphan, StateReclaimRequested, StateReclaimed,
			StateReclaimFailed, StateUnattributed, StateExited,
		}
		for _, state := range all {
			if !state.Valid() {
				t.Errorf("%s is not a state the engine knows", state)
			}
			wantTerminal := state == StateExited || state == StateReclaimed
			if state.Terminal() != wantTerminal {
				t.Errorf("%s terminal = %v, want %v", state, state.Terminal(), wantTerminal)
			}
		}
	})
}

// TestReclaimRetryBoundExhausts fails five reclaim attempts and asserts the
// record becomes reclaim-exhausted, stops returning to candidacy, remains
// report-only, and still recovers to ACTIVE on adoption.
func TestReclaimRetryBoundExhausts(t *testing.T) {
	f := newFixture(t)
	f.ownerExits()

	for attempt := 1; attempt <= MaxReclaimAttempts; attempt++ {
		f.driveTo(StateConfirmedOrphan, healthyOrphan())
		if !f.engine.RequestReclaim(f.key) {
			t.Fatalf("attempt %d: the reclaim edge was refused", attempt)
		}
		f.engine.CompleteReclaim(f.key, false)

		if got := f.state(); got != StateReclaimFailed {
			t.Fatalf("attempt %d: state = %s, want RECLAIM_FAILED", attempt, got)
		}
		if got := f.process().ReclaimAttempts; got != attempt {
			t.Fatalf("attempt %d recorded %d attempts", attempt, got)
		}
		if attempt == MaxReclaimAttempts {
			break
		}
		// The backoff is awake time, and a failed attempt re-earns its
		// confirmations before any retry.
		f.driveTo(StateOrphanCandidate, healthyOrphan())
		if got := f.process().Confirmations; got != 0 {
			t.Errorf("attempt %d returned to candidacy with %d confirmations, want 0", attempt, got)
		}
	}

	if !f.engine.ReclaimExhausted(f.key) {
		t.Fatal("the record is not marked reclaim-exhausted after five failures")
	}

	// Exhaustion bounds the action, not the graph. The record stops returning to
	// candidacy however long it waits.
	for i := 0; i < 200; i++ {
		f.scan(healthyOrphan())
	}
	if got := f.state(); got != StateReclaimFailed {
		t.Errorf("state = %s after exhaustion, want RECLAIM_FAILED held", got)
	}
	if f.engine.Eligible(f.key) {
		t.Error("an exhausted record is eligible; it must stay report-only")
	}

	obs := healthyOrphan()
	obs.Adopted = TriTrue
	f.scan(obs)
	if got := f.state(); got != StateActive {
		t.Errorf("an exhausted record failed to recover on adoption, state = %s", got)
	}
}

// TestReclaimBackoffDoublesToItsCap asserts the bound the design states: one
// minute of awake time, doubling, capped at one hour.
func TestReclaimBackoffDoublesToItsCap(t *testing.T) {
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{1, time.Minute},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{4, 8 * time.Minute},
		{5, 16 * time.Minute},
		{12, time.Hour},
	}
	for _, tc := range cases {
		if got := backoffFor(tc.attempts); got != tc.want {
			t.Errorf("backoffFor(%d) = %s, want %s", tc.attempts, got, tc.want)
		}
	}
}

// TestReportedIsADisplayFlagNotAState asserts the fix for the earlier draft that
// stranded every confirmed process in a dead end.
func TestReportedIsADisplayFlagNotAState(t *testing.T) {
	f := newFixture(t)
	f.ownerExits()
	f.driveTo(StateConfirmedOrphan, healthyOrphan())

	f.engine.MarkReported(f.key)
	state := f.process()
	if !state.Reported {
		t.Error("the reported flag was not set")
	}
	if state.State != StateConfirmedOrphan {
		t.Errorf("state = %s after reporting, want CONFIRMED_ORPHAN held", state.State)
	}
	if !f.engine.RequestReclaim(f.key) {
		t.Error("a reported process is unreachable by the phase B edge")
	}
}

// TestCountsAndSessionsExposeTheView covers what status and top read.
func TestCountsAndSessionsExposeTheView(t *testing.T) {
	f := newFixture(t)
	f.ownerExits()
	f.driveTo(StateGracePeriod, healthyOrphan())

	counts := f.engine.Counts()
	if counts[StateGracePeriod] != 1 {
		t.Errorf("counts = %v, want one process in GRACE_PERIOD", counts)
	}
	sessions := f.engine.Sessions()
	if len(sessions) != 1 || sessions[0].SessionID != f.session {
		t.Fatalf("sessions = %+v, want the one session", sessions)
	}
	if !sessions[0].OwnerExited() {
		t.Error("the session does not report its owner as exited")
	}
	if states := f.engine.States(); len(states) != 1 {
		t.Errorf("states = %+v, want one tracked process", states)
	}
}
