package attribution

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tjp2021/devreap/internal/config"
)

// Watcher cadences. The heartbeat is decoupled from the poll on purpose: at
// roughly 400 bytes a record, one heartbeat a second is about 35 megabytes a
// day, which breaks the 32 megabyte ceiling in under a day and would push the
// coverage history out of the store long before a 7 day measurement finished.
const (
	DefaultPollInterval        = time.Second
	DefaultHeartbeatInterval   = time.Minute
	DefaultSnapshotInterval    = 5 * time.Minute
	DefaultMaintenanceInterval = time.Minute

	// StaleHeartbeatIntervals is how many heartbeat intervals of awake time may
	// pass with no heartbeat before attribution data is untrusted.
	StaleHeartbeatIntervals = 3

	// MaxConsecutivePanics stops the watcher after this many consecutive
	// panicking polls. The scanner keeps running with today's behavior.
	MaxConsecutivePanics = 3
)

// Finding kinds raised by the watcher.
const (
	// FindingWatcherStopped means the watcher stopped after repeated panics.
	// Attribution is untrusted and the scanner is unaffected.
	FindingWatcherStopped = "watcher_stopped"
	// FindingWatcherPanic means one poll panicked and was contained.
	FindingWatcherPanic = "watcher_poll_panic"
	// FindingExitWatchUnavailable means kqueue watches could not be opened, so
	// poll absence is the only exit source.
	FindingExitWatchUnavailable = "exit_watch_unavailable"
)

// Classifier returns the pattern class of a process, and empty when no pattern
// matches. The watcher never classifies anything itself: classification belongs
// to the existing pattern registry.
type Classifier func(name, args string) string

// SnapshotSource takes one bulk snapshot of the process table.
type SnapshotSource func(takenAt time.Time) (*Snapshot, error)

// ArgsReader reads one process's arguments and environment, redacted.
type ArgsReader func(pid int32, ownerUID uint32, r *Redactor) (*ProcArgs, error)

// CwdReader reads a process's working directory, which is the repository source
// for every harness that publishes no marker.
type CwdReader func(pid int32) (string, bool)

// WatcherConfig configures the watcher. Every field has a working default, and
// the injectable sources are what let the loop be tested without a live machine.
type WatcherConfig struct {
	PollInterval        time.Duration
	HeartbeatInterval   time.Duration
	ScanInterval        time.Duration
	SnapshotInterval    time.Duration
	MaintenanceInterval time.Duration

	Classifier Classifier
	Snapshots  SnapshotSource
	ReadArgs   ArgsReader
	ReadCwd    CwdReader
	ExitWatch  ExitWatch

	Logf func(format string, args ...any)
}

func (c *WatcherConfig) applyDefaults() {
	if c.PollInterval <= 0 {
		c.PollInterval = DefaultPollInterval
	}
	if c.HeartbeatInterval <= 0 {
		c.HeartbeatInterval = DefaultHeartbeatInterval
	}
	if c.ScanInterval <= 0 {
		c.ScanInterval = config.DefaultScanInterval
	}
	if c.SnapshotInterval <= 0 {
		c.SnapshotInterval = DefaultSnapshotInterval
	}
	if c.MaintenanceInterval <= 0 {
		c.MaintenanceInterval = DefaultMaintenanceInterval
	}
	if c.Snapshots == nil {
		c.Snapshots = SnapshotProcesses
	}
	if c.ReadArgs == nil {
		c.ReadArgs = ReadProcArgsOwned
	}
	if c.ReadCwd == nil {
		c.ReadCwd = ReadProcessCwd
	}
	if c.Classifier == nil {
		c.Classifier = func(string, string) string { return "" }
	}
	if c.Logf == nil {
		c.Logf = func(string, ...any) {}
	}
}

// tracked is what the watcher keeps in memory for one live process. Every live
// process is held, because ancestry chains need the full tree, while only
// session-attributed or pattern-matched records are persisted.
type tracked struct {
	entry           ProcEntry
	exe             string
	cmdline         string
	class           string
	hasTrackedChild bool
}

// rootInfo is one session root the watcher is watching for exit.
type rootInfo struct {
	key       ProcKey
	sessionID string
	harness   string
	watched   bool
	exited    bool
}

// counters aggregate one heartbeat interval.
type counters struct {
	polls           int
	birthsSeen      int
	birthsPersisted int
	envReadFailures int
	sleepGapMillis  int64
	pollMicros      int64
	upgraded        int
}

// Watcher polls the process table, records births with ownership, watches
// session roots for exit, and drives the lifecycle engine.
//
// It never signals a process and holds no kill path in its code. Its failure
// modes are contained: every poll body runs under a deferred recover, three
// consecutive panicking polls stop it, and a stopped watcher leaves the scanner
// running with today's behavior.
type Watcher struct {
	cfg      WatcherConfig
	store    *Store
	registry *HarnessRegistry
	resolver *Resolver
	redactor *Redactor
	engine   *Engine
	index    *SpawnIndex
	clock    Clock

	tracked map[string]*tracked
	roots   map[string]*rootInfo
	cwds    map[string]string

	prev *Snapshot

	counters      counters
	lastHeartbeat time.Duration
	lastScan      time.Duration
	lastSnapshot  time.Duration
	started       bool

	// lastPollMono and lastPollWall are the pair gap detection compares. When
	// they disagree by more than one poll interval, the difference is sleep.
	lastPollMono time.Duration
	lastPollWall time.Time

	panics int

	// stopped and heartbeatMono are read from other goroutines, so they are
	// atomic rather than guarded: doctor asks whether the watcher is healthy at
	// any moment, and the answer must reflect the clock at the moment it asks.
	stopped       atomic.Bool
	begun         atomic.Bool
	heartbeatMono atomic.Int64

	// mu guards the live key set the background maintenance pass consults and
	// the findings doctor reads.
	mu       sync.Mutex
	liveKeys map[string]struct{}
	findings []Finding

	maintaining sync.Mutex

	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once
}

// NewWatcher builds a watcher over an open store and a harness registry.
func NewWatcher(cfg WatcherConfig, store *Store, registry *HarnessRegistry, engine *Engine, clock Clock) *Watcher {
	cfg.applyDefaults()
	if clock == nil {
		clock = NewSystemClock()
	}
	if cfg.ExitWatch == nil {
		watch, err := NewKqueueExitWatch()
		if err != nil {
			watch = NewNoopExitWatch()
		}
		cfg.ExitWatch = watch
	}

	return &Watcher{
		cfg:      cfg,
		store:    store,
		registry: registry,
		resolver: NewResolver(registry),
		redactor: NewRedactor(registry.MarkerNames()...),
		engine:   engine,
		index:    NewSpawnIndex(),
		clock:    clock,
		tracked:  make(map[string]*tracked),
		roots:    make(map[string]*rootInfo),
		cwds:     make(map[string]string),
		liveKeys: make(map[string]struct{}),
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

// Engine returns the lifecycle engine the watcher drives.
func (w *Watcher) Engine() *Engine { return w.engine }

// Findings returns the conditions doctor should report.
func (w *Watcher) Findings() []Finding {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]Finding, len(w.findings))
	copy(out, w.findings)
	return out
}

func (w *Watcher) addFinding(kind, detail string) {
	w.mu.Lock()
	w.findings = append(w.findings, Finding{Kind: kind, Detail: detail})
	w.mu.Unlock()
}

// Healthy reports whether attribution data may be trusted. It is false when the
// watcher has stopped, and when three heartbeat intervals of awake time have
// passed with no heartbeat.
//
// Staleness is measured in awake time everywhere it is measured, so an overnight
// sleep never reads as a dead watcher.
func (w *Watcher) Healthy() bool {
	if w.stopped.Load() {
		return false
	}
	if !w.begun.Load() {
		// Nothing has been measured yet, so nothing is stale yet.
		return true
	}
	return w.LastHeartbeatAge() <= w.staleAfter()
}

func (w *Watcher) staleAfter() time.Duration {
	return time.Duration(StaleHeartbeatIntervals) * w.cfg.HeartbeatInterval
}

// LastHeartbeatAge reports the awake time since the last heartbeat.
func (w *Watcher) LastHeartbeatAge() time.Duration {
	if !w.begun.Load() {
		return 0
	}
	age := w.clock.Monotonic() - time.Duration(w.heartbeatMono.Load())
	if age < 0 {
		return 0
	}
	return age
}

// Run drives the watcher until Stop is called. It is meant to run on its own
// goroutine inside the existing daemon process, so there is one supervised
// process and one lifecycle to reason about.
func (w *Watcher) Run() {
	defer close(w.doneCh)
	defer w.shutdown()

	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	maintenance := time.NewTicker(w.cfg.MaintenanceInterval)
	defer maintenance.Stop()

	w.safePoll()
	for {
		select {
		case <-ticker.C:
			if w.stopped.Load() {
				return
			}
			w.safePoll()
		case <-maintenance.C:
			go w.maintain()
		case <-w.stopCh:
			return
		}
	}
}

// Stop asks the watcher to shut down and waits for it. Safe to call more than
// once.
func (w *Watcher) Stop() {
	w.stopOnce.Do(func() { close(w.stopCh) })
	<-w.doneCh
}

// shutdown writes a final snapshot and releases the exit watches, so a restart
// replays only the journal tail.
func (w *Watcher) shutdown() {
	w.writeSnapshot()
	if w.cfg.ExitWatch != nil {
		_ = w.cfg.ExitWatch.Close()
	}
	w.stopped.Store(true)
}

// safePoll runs one poll under a deferred recover.
//
// A panic in parsing a malformed argument buffer kills the poll rather than the
// daemon. Three consecutive panicking polls stop the watcher, mark attribution
// untrusted, and leave the scanner running exactly as it runs today.
func (w *Watcher) safePoll() {
	defer func() {
		if r := recover(); r != nil {
			w.panics++
			w.cfg.Logf("attribution watcher poll panicked (%d consecutive): %v", w.panics, r)
			w.addFinding(FindingWatcherPanic, fmt.Sprintf("poll panicked: %v", r))
			if w.panics >= MaxConsecutivePanics {
				w.markStopped(fmt.Sprintf("%d consecutive panicking polls", w.panics))
			}
		}
	}()

	if err := w.poll(); err != nil {
		w.cfg.Logf("attribution watcher poll failed: %v", err)
		return
	}
	w.panics = 0
}

func (w *Watcher) markStopped(reason string) {
	w.stopped.Store(true)
	// A stopped watcher freezes the lifecycle immediately rather than waiting
	// for a poll that will never come.
	w.engine.SetTrusted(false)
	w.addFinding(FindingWatcherStopped, "the watcher stopped: "+reason+"; the scanner is unaffected")
	w.stopOnce.Do(func() { close(w.stopCh) })
}

// Stopped reports whether the watcher has stopped itself.
func (w *Watcher) Stopped() bool { return w.stopped.Load() }

// poll is one cycle: one bulk snapshot, a key diff, full metadata for the
// processes that are new in this poll, and nothing else on the steady-state
// path. The capacity budget rests on that distinction.
func (w *Watcher) poll() error {
	mono := w.clock.Monotonic()
	started := w.clock.Now()

	snap, err := w.cfg.Snapshots(started)
	if err != nil {
		return fmt.Errorf("bulk snapshot: %w", err)
	}

	gap := w.detectGap(mono, started)
	if !w.started {
		w.started = true
		w.lastHeartbeat, w.lastScan, w.lastSnapshot = mono, mono, mono
		w.heartbeatMono.Store(int64(mono))
		w.begun.Store(true)
	}

	// A process born during a sleep gap was witnessed by nobody, and neither was
	// anything alive on the first poll. Both fall to backfill, where a claim is
	// inferred at best. A gap also forces the full re-enumeration the design
	// requires on wake, which is what the first branch below performs.
	witnessed := w.prev != nil && gap == 0

	var born []ProcEntry
	if w.prev == nil {
		born = snap.Entries()
	} else {
		born, _ = snap.Diff(w.prev)
	}

	for _, entry := range born {
		w.onBirth(snap, entry, witnessed)
	}

	w.reapExited(snap)
	w.recordOwnerExits(snap)
	w.refreshRootWatches(snap)

	w.prev = snap
	w.publishLive(snap)

	w.counters.polls++
	w.counters.sleepGapMillis += gap.Milliseconds()
	w.counters.pollMicros += time.Since(started).Microseconds()

	w.maybeScan(mono, snap)
	w.maybeHeartbeat(mono, snap)
	w.maybeSnapshot(mono)
	w.refreshHealth(mono)
	return nil
}

// detectGap compares a wall-clock stamp against the monotonic clock. When the
// two disagree by more than one poll interval, the difference is a sleep gap.
func (w *Watcher) detectGap(mono time.Duration, wall time.Time) time.Duration {
	if !w.started {
		w.lastPollMono, w.lastPollWall = mono, wall
		return 0
	}
	awake := mono - w.lastPollMono
	if awake < 0 {
		awake = 0
	}
	drift := wall.Sub(w.lastPollWall) - awake
	w.lastPollMono, w.lastPollWall = mono, wall
	if drift > w.cfg.PollInterval {
		return drift
	}
	return 0
}

// onBirth collects full metadata for one new process and records it.
//
// This is the only expensive path in a poll, and it runs for processes that are
// new in this poll rather than for the whole table.
func (w *Watcher) onBirth(snap *Snapshot, entry ProcEntry, witnessed bool) {
	w.counters.birthsSeen++

	// Same-user only. The reader refuses another account's process anyway; this
	// keeps the refusal off the syscall path entirely.
	var args *ProcArgs
	if int(entry.UID) == os.Getuid() {
		read, err := w.cfg.ReadArgs(entry.PID, entry.UID, w.redactor)
		if err != nil {
			w.counters.envReadFailures++
		} else {
			args = read
		}
	}

	item := &tracked{entry: entry}
	var env map[string]string
	if args != nil {
		item.exe, item.cmdline, env = args.Exe, args.Cmdline, args.Env
	}
	item.class = w.cfg.Classifier(entry.Name, item.cmdline)
	w.tracked[entry.Key().IndexKey()] = item

	// A tracked child is what separates a session root from any other process
	// group leader, so the parent learns about it before the chain is walked.
	parentEntry, parentKnown := snap.ByPID(entry.PPID)
	if item.class != "" && parentKnown {
		if parent := w.tracked[parentEntry.Key().IndexKey()]; parent != nil {
			parent.hasTrackedChild = true
		}
	}

	parentKey := ProcKey{}
	if parentKnown {
		// The parent's start time is read live from the same snapshot, so the
		// recorded key names the parent that actually existed at this moment
		// rather than whatever later holds the number.
		parentKey = parentEntry.Key()
	}

	chain := w.chainFor(snap, entry.PID)
	in := ResolveInput{
		Entry:     entry,
		ParentKey: parentKey,
		Exe:       item.exe,
		Cmdline:   item.cmdline,
		Env:       env,
		Chain:     chain,
		Witnessed: witnessed,
	}
	in.RootCwd = w.rootCwd(chain)

	link, claim := w.resolver.ResolveLink(w.index, in)
	w.index.Add(link)

	if link.Root && claim.Attributed() {
		w.noteRoot(entry, claim)
	}

	// Volume control: only session-attributed or pattern-matched records are
	// persisted. Everything else stays in memory, where the ancestry chain needs
	// it and the journal does not.
	if !claim.Attributed() && item.class == "" {
		return
	}

	source := BirthSourcePoll
	if !witnessed {
		source = BirthSourceBackfill
	}
	birth := BirthRecord{
		ObservedAt:   snap.TakenAt(),
		Source:       source,
		Key:          entry.Key(),
		ParentKey:    parentKey,
		PGID:         entry.PGID,
		TTY:          entry.TTY,
		Name:         entry.Name,
		Exe:          item.exe,
		Cmdline:      item.cmdline,
		Class:        item.class,
		Owner:        claim,
		Unverifiable: unverifiableFields(entry, args),
	}
	if err := w.store.AppendBirth(birth); err != nil {
		w.cfg.Logf("attribution: writing birth record: %v", err)
		return
	}
	w.counters.birthsPersisted++
	w.engine.OnBirth(birth)
}

// unverifiableFields lists every field whose read failed. A record naming the
// start time here can never gate an action.
func unverifiableFields(entry ProcEntry, args *ProcArgs) []string {
	var out []string
	if !entry.StartTimeKnown {
		out = append(out, UnverifiableStartTime)
	}
	if !entry.TTYKnown {
		out = append(out, "tty")
	}
	if args == nil {
		out = append(out, "cmdline", "env")
	}
	return out
}

// chainFor builds the process and its ancestors, nearest first, with the fields
// root recognition needs. The registry never reads a process itself.
func (w *Watcher) chainFor(snap *Snapshot, pid int32) []RootCandidate {
	entries := snap.Chain(pid, MaxLinkDepth)
	out := make([]RootCandidate, 0, len(entries))
	for _, entry := range entries {
		candidate := RootCandidate{Entry: entry}
		if item := w.tracked[entry.Key().IndexKey()]; item != nil {
			candidate.Exe = item.exe
			candidate.Cmdline = item.cmdline
			candidate.HasTrackedChild = item.hasTrackedChild
		}
		out = append(out, candidate)
	}
	return out
}

// rootCwd reads the working directory of the resolved root once and caches it,
// because it is the repository for every harness that publishes no marker.
func (w *Watcher) rootCwd(chain []RootCandidate) string {
	match, ok := w.registry.ResolveRoot(chain)
	if !ok {
		return ""
	}
	key := match.Root.Entry.Key().IndexKey()
	if cwd, cached := w.cwds[key]; cached {
		return cwd
	}
	cwd, read := w.cfg.ReadCwd(match.Root.Entry.PID)
	if !read {
		return ""
	}
	w.cwds[key] = cwd
	return cwd
}

// noteRoot registers a session root and attaches its exit watch.
func (w *Watcher) noteRoot(entry ProcEntry, claim OwnershipClaim) {
	key := entry.Key().IndexKey()
	if _, known := w.roots[key]; known {
		return
	}
	info := &rootInfo{key: entry.Key(), sessionID: claim.SessionID, harness: claim.Harness}
	info.watched = w.cfg.ExitWatch.Watch(entry.PID)
	w.roots[key] = info
}

// refreshRootWatches re-attaches watches that could not attach earlier and drops
// watches whose identifier no longer holds the process that earned them.
//
// The second half is the attach race seen from the other side: a watch attaches
// to an identifier, and an identifier can be reused. Comparing the live start
// time against the recorded key is what keeps a recycled identifier from
// inheriting a session root's watch.
func (w *Watcher) refreshRootWatches(snap *Snapshot) {
	for _, info := range w.roots {
		if info.exited {
			continue
		}
		live, present := snap.Lookup(info.key)
		if !present {
			continue
		}
		if KeyMismatch(info.key, live) {
			w.cfg.ExitWatch.Unwatch(info.key.PID)
			info.watched = false
			continue
		}
		if !info.watched {
			info.watched = w.cfg.ExitWatch.Watch(info.key.PID)
		}
	}
}

// recordOwnerExits turns a root's disappearance into a recorded event.
//
// kqueue supplies the exact instant when a watch was attached. Poll absence is
// the fallback source, and it covers every root whose watch could not attach and
// every platform without kqueue.
func (w *Watcher) recordOwnerExits(snap *Snapshot) {
	byPID := make(map[int32]*rootInfo, len(w.roots))
	for _, info := range w.roots {
		if !info.exited {
			byPID[info.key.PID] = info
		}
	}

	for _, notice := range w.cfg.ExitWatch.Drain() {
		if info, known := byPID[notice.PID]; known {
			w.writeOwnerExit(info, notice.At, ExitSourceKqueue, snap)
		}
	}

	for _, info := range w.roots {
		if info.exited {
			continue
		}
		if _, present := snap.Lookup(info.key); present {
			continue
		}
		w.writeOwnerExit(info, snap.TakenAt(), ExitSourcePollAbsent, snap)
	}
}

func (w *Watcher) writeOwnerExit(info *rootInfo, at time.Time, source ExitSource, snap *Snapshot) {
	if info.exited {
		return
	}
	info.exited = true
	w.cfg.ExitWatch.Unwatch(info.key.PID)

	alive, rss := w.sessionFootprint(info.sessionID, snap)
	exit := OwnerExitRecord{
		At:            at,
		SessionID:     info.sessionID,
		Harness:       info.harness,
		RootKey:       info.key,
		Source:        source,
		MembersAlive:  alive,
		RSSAliveBytes: rss,
	}
	if err := w.store.AppendOwnerExit(exit); err != nil {
		w.cfg.Logf("attribution: writing owner exit: %v", err)
	}
	w.engine.OnOwnerExit(exit)
}

// sessionFootprint counts the members still alive when their owner exited.
func (w *Watcher) sessionFootprint(sessionID string, snap *Snapshot) (int, uint64) {
	alive := 0
	for _, state := range w.engine.States() {
		if state.SessionID != sessionID || state.State.Terminal() {
			continue
		}
		if _, present := snap.Lookup(state.Key); present {
			alive++
		}
	}
	return alive, 0
}

// reapExited drops processes whose key is gone from the in-memory tree. The
// records they produced stay in the journal, which is what keeps an observed
// claim true after its whole ancestry exits.
func (w *Watcher) reapExited(snap *Snapshot) {
	for key, item := range w.tracked {
		if _, present := snap.Lookup(item.entry.Key()); present {
			continue
		}
		delete(w.tracked, key)
		delete(w.cwds, key)
		w.index.Remove(item.entry.Key())
	}
}

// publishLive records which keys are live, for the background maintenance pass.
// A record belonging to a live process is never evicted.
func (w *Watcher) publishLive(snap *Snapshot) {
	live := make(map[string]struct{}, snap.Len())
	for _, entry := range snap.Entries() {
		live[entry.Key().IndexKey()] = struct{}{}
	}
	w.mu.Lock()
	w.liveKeys = live
	w.mu.Unlock()
}

// isLive answers the store's liveness question from the last published poll.
func (w *Watcher) isLive(key ProcKey) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, live := w.liveKeys[key.IndexKey()]
	return live
}

// maybeScan drives the lifecycle engine at the scan cadence.
//
// The observations come from the same kinfo_proc array the index was built from
// rather than from a second enumeration, so the engine can never disagree with
// the snapshot that produced the ownership records.
func (w *Watcher) maybeScan(mono time.Duration, snap *Snapshot) {
	if mono-w.lastScan < w.cfg.ScanInterval {
		return
	}
	w.lastScan = mono

	states := w.engine.States()
	observations := make([]ScanObservation, 0, len(states))
	for _, state := range states {
		if state.State.Terminal() {
			continue
		}
		observations = append(observations, w.observe(state, snap))
	}

	outcome := w.engine.Scan(observations)
	for _, transition := range outcome.Transitions {
		if err := w.store.AppendTransition(transition); err != nil {
			w.cfg.Logf("attribution: writing transition: %v", err)
		}
	}
}

// observe reads the conditions the engine needs for one process. Every one is
// three-valued, because a condition that could not be read is never evidence.
func (w *Watcher) observe(state ProcessState, snap *Snapshot) ScanObservation {
	obs := ScanObservation{Key: state.Key, Adopted: TriUnknown, OwnerAlive: TriUnknown, StrongSignal: TriUnknown}

	entry, present := snap.Lookup(state.Key)
	obs.Present = present
	if !present {
		return obs
	}
	if item := w.tracked[state.Key.IndexKey()]; item != nil {
		obs.Class = item.class
	}

	// The strong lifecycle signal is ppid_is_init, read from the same snapshot.
	obs.StrongSignal = TriFalse
	if entry.PPID == 1 {
		obs.StrongSignal = TriTrue
	}

	// Adoption has one precise meaning: the current parent is neither 1 nor
	// absent, and it resolves to a live process whose identifier and start time
	// are both readable. An unreadable parent is not adoption, because unknown
	// never counts as evidence in either direction.
	switch {
	case entry.PPID <= 1:
		obs.Adopted = TriFalse
	default:
		parent, known := snap.ByPID(entry.PPID)
		switch {
		case !known:
			obs.Adopted = TriUnknown
		case !parent.StartTimeKnown:
			obs.Adopted = TriUnknown
		default:
			obs.Adopted = TriTrue
		}
	}

	if !state.RootKey.Zero() {
		if _, alive := snap.Lookup(state.RootKey); alive {
			obs.OwnerAlive = TriTrue
		} else {
			obs.OwnerAlive = TriFalse
		}
	}
	return obs
}

// maybeHeartbeat writes one minute of aggregated counters.
func (w *Watcher) maybeHeartbeat(mono time.Duration, snap *Snapshot) {
	if mono-w.lastHeartbeat < w.cfg.HeartbeatInterval {
		return
	}
	w.lastHeartbeat = mono
	w.heartbeatMono.Store(int64(mono))

	tracked, attributed := 0, 0
	for _, state := range w.engine.States() {
		if state.State.Terminal() {
			continue
		}
		tracked++
		if state.Confidence != ConfidenceNone && state.SessionID != "" {
			attributed++
		}
	}

	beat := HeartbeatRecord{
		At:                 snap.TakenAt(),
		Polls:              w.counters.polls,
		BirthsSeen:         w.counters.birthsSeen,
		BirthsPersisted:    w.counters.birthsPersisted,
		EnvReadFailures:    w.counters.envReadFailures,
		SleepGapMillis:     w.counters.sleepGapMillis,
		PollDurationMicros: w.averagePollMicros(),
		Tracked:            tracked,
		Attributed:         attributed,
		Upgraded:           w.counters.upgraded,
		JournalBytes:       w.store.Size(),
	}
	if err := w.store.AppendHeartbeat(beat); err != nil {
		w.cfg.Logf("attribution: writing heartbeat: %v", err)
	}
	w.counters = counters{}
}

func (w *Watcher) averagePollMicros() int64 {
	if w.counters.polls == 0 {
		return 0
	}
	return w.counters.pollMicros / int64(w.counters.polls)
}

// maybeSnapshot writes the compacted state every five minutes, so a restart
// replays only the journal tail.
func (w *Watcher) maybeSnapshot(mono time.Duration) {
	if mono-w.lastSnapshot < w.cfg.SnapshotInterval {
		return
	}
	w.lastSnapshot = mono
	w.writeSnapshot()
}

func (w *Watcher) writeSnapshot() {
	if w.store == nil || w.engine == nil {
		return
	}
	snapshot := StoreSnapshot{Processes: w.engine.States(), Sessions: w.engine.Sessions()}
	if err := w.store.WriteSnapshot(snapshot); err != nil {
		w.cfg.Logf("attribution: writing store snapshot: %v", err)
	}
}

// maintain runs the store's compaction pass off the poll path. Only one runs at
// a time, and a slow pass delays the next one rather than stacking.
func (w *Watcher) maintain() {
	if !w.maintaining.TryLock() {
		return
	}
	defer w.maintaining.Unlock()

	if _, ran, err := w.store.Maintain(w.isLive); err != nil {
		w.cfg.Logf("attribution: store maintenance failed: %v", err)
	} else if ran {
		w.cfg.Logf("attribution: store compaction ran")
	}
}

// refreshHealth marks attribution untrusted when three heartbeat intervals of
// awake time pass with no heartbeat. Sleep never reads as a dead watcher,
// because the measurement is awake time.
func (w *Watcher) refreshHealth(mono time.Duration) {
	_ = mono
	w.engine.SetTrusted(w.Healthy())
}
