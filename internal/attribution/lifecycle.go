package attribution

import (
	"sort"
	"time"

	"github.com/tjp2021/devreap/internal/config"
)

// Tri is a three-valued observation: true, false, or unreadable.
//
// The distinction is load-bearing. A confirmation only fails when a required
// condition is observed to be false. An unreadable condition is neither a
// confirmation nor a failure, so a transient read error cannot restart a window
// that a real observation had already earned.
type Tri int

const (
	// TriUnknown means the condition could not be read. It is never evidence in
	// either direction.
	TriUnknown Tri = iota
	TriTrue
	TriFalse
)

// String renders a three-valued observation for a transition's evidence.
func (t Tri) String() string {
	switch t {
	case TriTrue:
		return "true"
	case TriFalse:
		return "false"
	default:
		return "unknown"
	}
}

// Clock supplies the two readings the engine needs. Wall time stamps records,
// and the monotonic reading measures awake time: on darwin the monotonic clock
// stops while the machine sleeps, which is exactly the property the awake-time
// accumulators are built on.
//
// Tests inject a clock that advances wall time without advancing the monotonic
// reading, which is how a sleep is simulated.
type Clock interface {
	Now() time.Time
	Monotonic() time.Duration
}

// SystemClock is the production clock.
type SystemClock struct {
	origin time.Time
}

// NewSystemClock returns a clock reading the machine's wall and monotonic time.
func NewSystemClock() *SystemClock { return &SystemClock{origin: time.Now()} }

// Now returns wall time.
func (c *SystemClock) Now() time.Time { return time.Now() }

// Monotonic returns time since the clock was created, measured on the
// monotonic reading Go carries inside a time.Time. It does not advance while
// the machine sleeps.
func (c *SystemClock) Monotonic() time.Duration { return time.Since(c.origin) }

// Lifecycle triggers, written on every transition record so a user can
// reconstruct why any process reached any state.
const (
	TriggerBirth              = "birth"
	TriggerOwnerExit          = "owner_exit_recorded"
	TriggerFirstScanAfterExit = "first_scan_after_exit"
	TriggerWindowReached      = "lifecycle_grace_reached"
	TriggerConfirmed          = "confirmations_reached"
	TriggerConditionFalse     = "r6_condition_observed_false"
	TriggerAdoption           = "adoption"
	TriggerOwnerAlive         = "owner_observed_alive"
	TriggerClaimUpgrade       = "claim_upgrade_resolved_owner"
	TriggerProcessGone        = "process_key_absent"
	TriggerBackoffElapsed     = "reclaim_backoff_elapsed"
	TriggerReclaimRequested   = "reclaim_requested"
	TriggerReclaimSucceeded   = "reclaim_succeeded"
	TriggerReclaimFailed      = "reclaim_failed"
)

// Reclaim retry bounds. They belong to phase B, and they are defined here
// because the state machine that owns them ships in phase A.
const (
	// MaxReclaimAttempts caps reclaim attempts. After the fifth failure a record
	// is reclaim-exhausted: it stops returning to candidacy, stays report-only,
	// and keeps its recovery edge to ACTIVE.
	MaxReclaimAttempts = 5
	// ReclaimBackoffBase is the first backoff, measured in awake time.
	ReclaimBackoffBase = time.Minute
	// ReclaimBackoffCap is the ceiling the backoff doubles to.
	ReclaimBackoffCap = time.Hour
)

// EngineConfig configures the lifecycle engine. Every field has a working
// default, and the class windows come from the merged configuration table.
type EngineConfig struct {
	// Windows is the per-class awake-time budget between a recorded owner exit
	// and candidacy. A missing class and a zero value both mean never.
	Windows map[string]time.Duration
	// Confirmations is the number of confirming scans required on top of the
	// window.
	Confirmations int
	// ScanInterval is the cadence the engine expects to be called at. Gap
	// detection uses it as the threshold that separates a slow scan from sleep.
	ScanInterval time.Duration
}

func (c EngineConfig) window(class string) (time.Duration, bool) {
	window, known := c.Windows[class]
	if !known || window <= 0 {
		return 0, false
	}
	return window, true
}

// ScanObservation is what one scan cycle observed about one tracked process.
// Every condition that can fail to read is three-valued, because unknown is
// never evidence.
type ScanObservation struct {
	Key ProcKey

	// Present reports whether the process key is still in the process table.
	Present bool

	// Class is the pattern class, empty while the process is unclassified. A
	// process that a later scan classifies picks up its window then, which is how
	// the unclassified condition ends.
	Class string

	// Adopted reports whether the process gained a live parent, meaning its
	// current parent is neither 1 nor absent and resolves to a live process whose
	// identifier and start time are both readable. An unreadable parent is not
	// adoption.
	Adopted Tri

	// OwnerAlive reports whether the session root key was observed alive again.
	OwnerAlive Tri

	// StrongSignal reports the existing strong lifecycle signal on a live read,
	// which is the fifth condition of R6.
	StrongSignal Tri
}

// procRecord is the engine's per-process state plus the last observation that
// touched it, which is what lets eligibility be recomputed live rather than read
// from a stored verdict.
type procRecord struct {
	state ProcessState
	last  ScanObservation
	// backoff is the awake time that must pass before a failed reclaim returns
	// to candidacy.
	backoff time.Duration
	// waited is the awake time accumulated against that backoff.
	waited time.Duration
}

// Engine is the lifecycle state machine. It consumes birth records, owner exit
// events, claim upgrades, and scan results, applies the transition rules, and
// writes each transition with its evidence.
//
// It computes eligibility and exposes it as a restriction, never as a reason to
// act. Nothing in this type signals a process.
type Engine struct {
	cfg    EngineConfig
	clock  Clock
	states map[string]*procRecord

	sessions map[string]SessionState

	started  bool
	lastMono time.Duration
	lastWall time.Time

	// trusted reports whether attribution data is current. Untrusted data
	// freezes the lifecycle: no state advances toward candidacy, and the
	// awake-time accumulators pause exactly as they pause across sleep.
	trusted bool

	pending []TransitionRecord
}

// NewEngine returns an engine with the given configuration and clock.
func NewEngine(cfg EngineConfig, clock Clock) *Engine {
	if cfg.Confirmations <= 0 {
		cfg.Confirmations = config.DefaultConfirmationCount
	}
	if cfg.ScanInterval <= 0 {
		cfg.ScanInterval = config.DefaultScanInterval
	}
	if cfg.Windows == nil {
		cfg.Windows = config.DefaultLifecycleGrace()
	}
	if clock == nil {
		clock = NewSystemClock()
	}
	return &Engine{
		cfg:      cfg,
		clock:    clock,
		states:   make(map[string]*procRecord),
		sessions: make(map[string]SessionState),
		trusted:  true,
	}
}

// SetTrusted marks whether attribution data is current. A stale watcher makes
// the data untrusted, which freezes every accumulator until it recovers.
func (e *Engine) SetTrusted(trusted bool) { e.trusted = trusted }

// Trusted reports whether attribution data is currently trusted.
func (e *Engine) Trusted() bool { return e.trusted }

// Transitions drains the transitions written since the last call. The caller
// appends them to the store, which is what makes the accumulators survive a
// restart.
func (e *Engine) Transitions() []TransitionRecord {
	out := e.pending
	e.pending = nil
	return out
}

// State returns the tracked state for a process.
func (e *Engine) State(key ProcKey) (ProcessState, bool) {
	rec, ok := e.states[key.IndexKey()]
	if !ok {
		return ProcessState{}, false
	}
	return rec.state, true
}

// States returns every tracked process state, ordered by key so output is
// stable.
func (e *Engine) States() []ProcessState {
	out := make([]ProcessState, 0, len(e.states))
	for _, rec := range e.states {
		out = append(out, rec.state)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key.IndexKey() < out[j].Key.IndexKey() })
	return out
}

// Sessions returns every known session, ordered by identifier.
func (e *Engine) Sessions() []SessionState {
	out := make([]SessionState, 0, len(e.sessions))
	for _, s := range e.sessions {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out
}

// Session returns one session's recorded state.
func (e *Engine) Session(id string) (SessionState, bool) {
	s, ok := e.sessions[id]
	return s, ok
}

// Counts returns the number of tracked processes in each lifecycle state, which
// is what status prints.
func (e *Engine) Counts() map[LifecycleState]int {
	counts := make(map[LifecycleState]int)
	for _, rec := range e.states {
		counts[rec.state.State]++
	}
	return counts
}

// Restore seeds the engine from recovered state after a restart. The
// accumulators resume at their recorded values rather than at zero, so a restart
// costs the time actually spent down rather than the progress already earned.
func (e *Engine) Restore(recovered *RecoveredState) {
	if recovered == nil {
		return
	}
	for key, state := range recovered.Processes {
		e.states[key] = &procRecord{state: state}
	}
	for id, session := range recovered.Sessions {
		e.sessions[id] = session
	}
}

// OnBirth records a process the watcher saw appear. A birth with no resolved
// owner starts at UNATTRIBUTED, which is never action-eligible at any age.
func (e *Engine) OnBirth(birth BirthRecord) {
	key := birth.Key.IndexKey()
	state := StateActive
	if birth.Owner.Confidence == ConfidenceNone {
		state = StateUnattributed
	}

	rec := &procRecord{state: ProcessState{
		Key:        birth.Key,
		State:      state,
		SessionID:  birth.Owner.SessionID,
		RootKey:    birth.Owner.RootKey,
		Confidence: birth.Owner.Confidence,
		Class:      birth.Class,
	}}
	e.states[key] = rec

	if birth.Owner.SessionID != "" {
		if _, known := e.sessions[birth.Owner.SessionID]; !known {
			e.sessions[birth.Owner.SessionID] = SessionState{
				SessionID: birth.Owner.SessionID,
				Harness:   birth.Owner.Harness,
				Repo:      birth.Owner.Repo,
				RootKey:   birth.Owner.RootKey,
			}
		}
	}
	e.emit(rec, "", state, TriggerBirth, map[string]any{"confidence": string(birth.Owner.Confidence)})
}

// OnOwnerExit records a session root's exit. Owner death is an event with a
// timestamp and a source, not something computed at read time.
func (e *Engine) OnOwnerExit(exit OwnerExitRecord) {
	session, known := e.sessions[exit.SessionID]
	if !known {
		session = SessionState{SessionID: exit.SessionID, Harness: exit.Harness, RootKey: exit.RootKey}
	}
	at := NormalizeTime(exit.At)
	// A trusted source never loses to the hook, which is enrichment only.
	if session.OwnerExitAt == nil || (!session.OwnerExitSource.Trusted() && exit.Source.Trusted()) {
		session.OwnerExitAt = &at
		session.OwnerExitSource = exit.Source
	}
	e.sessions[exit.SessionID] = session

	if !session.OwnerExited() {
		return
	}
	for _, rec := range e.states {
		if rec.state.SessionID != exit.SessionID || rec.state.State != StateActive {
			continue
		}
		e.transition(rec, StateOwnerGone, TriggerOwnerExit, map[string]any{
			"source":     string(exit.Source),
			"session_id": exit.SessionID,
		})
	}
}

// OnClaimUpgrade applies a claim upgrade, which is the only way an unattributed
// process becomes eligible to progress at all.
func (e *Engine) OnClaimUpgrade(upgrade ClaimUpgradeRecord) {
	rec, ok := e.states[upgrade.Key.IndexKey()]
	if !ok {
		return
	}
	if upgrade.To == ConfidenceNone {
		return
	}
	rec.state.Confidence = upgrade.To
	if upgrade.Evidence.SessionID != "" {
		rec.state.SessionID = upgrade.Evidence.SessionID
	}
	if !upgrade.Evidence.RootKey.Zero() {
		rec.state.RootKey = upgrade.Evidence.RootKey
	}
	if rec.state.State == StateUnattributed {
		e.transition(rec, StateActive, TriggerClaimUpgrade, map[string]any{
			"to":         string(upgrade.To),
			"session_id": upgrade.Evidence.SessionID,
		})
	}
}

// MarkReported sets the display flag on a confirmed orphan. Reporting is a flag
// rather than a state, so a confirmed process stays in CONFIRMED_ORPHAN and
// remains reachable by the phase B edge. An earlier draft made REPORTED a state,
// which stranded every confirmed process in a dead end.
func (e *Engine) MarkReported(key ProcKey) {
	if rec, ok := e.states[key.IndexKey()]; ok {
		rec.state.Reported = true
	}
}

// ScanOutcome reports what one lifecycle scan did.
type ScanOutcome struct {
	// AwakeDelta is the awake time credited to every accumulator this scan.
	AwakeDelta time.Duration
	// SleepGap is the wall time that passed without the monotonic clock moving.
	SleepGap time.Duration
	// NeedsReenumeration is set after a sleep gap. The watcher performs a full
	// re-enumeration on wake before any awake time is credited.
	NeedsReenumeration bool
	// Transitions are the state changes this scan produced.
	Transitions []TransitionRecord
}

// Scan advances the state machine by one cycle.
//
// It credits awake time, detects sleep gaps, and applies the transition table to
// every observation. Sleep pauses every window and every confirmation counter,
// and never resets or invalidates them: a gap contributes zero to the
// accumulator and the window resumes where it stopped.
func (e *Engine) Scan(observations []ScanObservation) ScanOutcome {
	awake, gap := e.tick()
	outcome := ScanOutcome{AwakeDelta: awake, SleepGap: gap, NeedsReenumeration: gap > 0}

	// Time nobody observed cannot be evidence that a process stayed orphaned.
	if !e.trusted {
		awake = 0
	}

	for _, obs := range observations {
		rec, ok := e.states[obs.Key.IndexKey()]
		if !ok {
			continue
		}
		rec.last = obs
		if obs.Class != "" {
			rec.state.Class = obs.Class
		}
		e.step(rec, obs, awake)
	}

	outcome.Transitions = e.Transitions()
	return outcome
}

// tick reads both clocks and splits the elapsed wall time into awake time and a
// sleep gap. When the two disagree by more than one scan interval, the
// difference is a sleep gap.
func (e *Engine) tick() (awake, gap time.Duration) {
	mono := e.clock.Monotonic()
	wall := e.clock.Now()

	if !e.started {
		e.started = true
		e.lastMono, e.lastWall = mono, wall
		return 0, 0
	}

	awake = mono - e.lastMono
	if awake < 0 {
		awake = 0
	}
	wallDelta := wall.Sub(e.lastWall)
	if drift := wallDelta - awake; drift > e.cfg.ScanInterval {
		gap = drift
	}

	e.lastMono, e.lastWall = mono, wall
	return awake, gap
}

func (e *Engine) step(rec *procRecord, obs ScanObservation, awake time.Duration) {
	// The process key is gone. This edge is available from every state.
	if !obs.Present {
		if !rec.state.State.Terminal() {
			e.transition(rec, StateExited, TriggerProcessGone, nil)
		}
		return
	}
	if rec.state.State.Terminal() {
		return
	}

	// Recovery is always available, and it is a restrictive direction, so it
	// applies even while attribution data is untrusted.
	if obs.Adopted == TriTrue && rec.state.State != StateActive {
		e.recoverToActive(rec, TriggerAdoption)
		return
	}

	if !e.trusted {
		return
	}

	switch rec.state.State {
	case StateActive:
		if e.ownerExited(rec) {
			e.transition(rec, StateOwnerGone, TriggerOwnerExit, map[string]any{
				"session_id": rec.state.SessionID,
			})
		}

	case StateOwnerGone:
		// A poll-absent exit is the weaker source, so an owner seen alive again
		// withdraws it. A kqueue exit is not revocable: the process is gone.
		if obs.OwnerAlive == TriTrue && e.exitSource(rec) == ExitSourcePollAbsent {
			e.recoverToActive(rec, TriggerOwnerAlive)
			return
		}
		e.transition(rec, StateGracePeriod, TriggerFirstScanAfterExit, nil)

	case StateGracePeriod:
		rec.state.AwakeMillis += awake.Milliseconds()
		window, actionable := e.cfg.window(e.classOf(rec))
		// R8: an unattributed or unclassified process never expires out of its
		// window. Absence of a window is absence of permission to act.
		if !actionable || !rec.state.Confidence.observed() {
			return
		}
		if time.Duration(rec.state.AwakeMillis)*time.Millisecond >= window {
			e.transition(rec, StateOrphanCandidate, TriggerWindowReached, map[string]any{
				"class":  e.classOf(rec),
				"window": window.String(),
			})
		}

	case StateOrphanCandidate:
		rec.state.AwakeMillis += awake.Milliseconds()
		e.confirm(rec, obs, awake)

	case StateConfirmedOrphan:
		// The table gives this state two exits: adoption, handled above, and the
		// phase B reclaim edge. No unlisted edge is added here. Eligibility is
		// recomputed live by Eligible, so a confirmation that no longer holds
		// cannot gate anything even while the record keeps its state.
		rec.state.AwakeMillis += awake.Milliseconds()

	case StateReclaimFailed:
		e.retryBackoff(rec, awake)

	case StateUnattributed:
		// Only a claim upgrade ends this condition, and it is the only way an
		// unattributed process becomes eligible.
	}
}

// confirm applies the R6 conditions to a candidate.
//
// A condition observed false resets both the counter and the accumulator, which
// is what the transition table requires. An unreadable condition holds the
// counter where it is and resets nothing.
func (e *Engine) confirm(rec *procRecord, obs ScanObservation, awake time.Duration) {
	if failed, why := e.conditionObservedFalse(rec, obs); failed {
		rec.state.Confirmations = 0
		rec.state.AwakeMillis = 0
		e.transition(rec, StateGracePeriod, TriggerConditionFalse, map[string]any{"condition": why})
		return
	}
	if obs.StrongSignal != TriTrue {
		return
	}
	// A confirmation counts awake progress. A frozen clock cannot grind out
	// confirmations, and neither can a scan that credited no awake time.
	if awake <= 0 {
		return
	}

	rec.state.Confirmations++
	if rec.state.Confirmations < e.cfg.Confirmations {
		return
	}
	if ok, _ := e.r6(rec, obs); !ok {
		return
	}
	e.transition(rec, StateConfirmedOrphan, TriggerConfirmed, map[string]any{
		"confirmations": rec.state.Confirmations,
		"awake_ms":      rec.state.AwakeMillis,
	})
}

// conditionObservedFalse reports whether a required condition was observed to be
// false, which is different from being unreadable.
func (e *Engine) conditionObservedFalse(rec *procRecord, obs ScanObservation) (bool, string) {
	if !e.ownerExited(rec) {
		return true, "owner_exit_recorded"
	}
	if !rec.state.Confidence.observed() {
		return true, "confidence_observed"
	}
	if _, actionable := e.cfg.window(e.classOf(rec)); !actionable {
		return true, "class_window"
	}
	if obs.StrongSignal == TriFalse {
		return true, "strong_signal"
	}
	return false, ""
}

// r6 reports whether all five conditions of R6 hold right now. It is the only
// place the five are read together, and Eligible is the only consumer that may
// turn the answer into a restriction.
func (e *Engine) r6(rec *procRecord, obs ScanObservation) (bool, string) {
	if !e.ownerExited(rec) {
		return false, "owner_exit_recorded"
	}
	window, actionable := e.cfg.window(e.classOf(rec))
	if !actionable {
		return false, "class_window"
	}
	if time.Duration(rec.state.AwakeMillis)*time.Millisecond < window {
		return false, "lifecycle_grace"
	}
	if rec.state.Confirmations < e.cfg.Confirmations {
		return false, "confirmations"
	}
	if !rec.state.Confidence.observed() {
		return false, "confidence_observed"
	}
	if obs.StrongSignal != TriTrue {
		return false, "strong_signal"
	}
	return true, ""
}

// Eligible reports whether a process satisfies every condition phase B would
// require, recomputed against the last observation rather than read from a
// stored verdict. Phase A never calls this on a kill path.
//
// It is a restriction and never a reason to act. A process this returns true for
// is one the existing path already permits; a process it returns false for is
// one attribution removes from that set.
func (e *Engine) Eligible(key ProcKey) bool {
	rec, ok := e.states[key.IndexKey()]
	if !ok {
		return false
	}
	if !e.trusted {
		return false
	}
	if rec.state.State != StateConfirmedOrphan {
		return false
	}
	if !rec.state.Key.Verifiable() {
		return false
	}
	ok, _ = e.r6(rec, rec.last)
	return ok
}

// retryBackoff runs the phase B retry bound. Exhaustion bounds the action rather
// than the recovery: an exhausted record stays report-only and keeps its edge
// back to ACTIVE.
func (e *Engine) retryBackoff(rec *procRecord, awake time.Duration) {
	if rec.state.ReclaimAttempts >= MaxReclaimAttempts {
		return
	}
	if rec.backoff <= 0 {
		rec.backoff = backoffFor(rec.state.ReclaimAttempts)
	}
	rec.waited += awake
	if rec.waited < rec.backoff {
		return
	}
	rec.waited, rec.backoff = 0, 0
	// A failed attempt re-earns its confirmations before any retry.
	rec.state.Confirmations = 0
	e.transition(rec, StateOrphanCandidate, TriggerBackoffElapsed, map[string]any{
		"attempts": rec.state.ReclaimAttempts,
	})
}

// backoffFor doubles from one minute of awake time to a one hour cap. The
// argument is the number of failures so far, so the first failure waits the base
// rather than twice it.
func backoffFor(attempts int) time.Duration {
	if attempts <= 1 {
		return ReclaimBackoffBase
	}
	backoff := ReclaimBackoffBase
	for i := 1; i < attempts; i++ {
		backoff *= 2
		if backoff >= ReclaimBackoffCap {
			return ReclaimBackoffCap
		}
	}
	return backoff
}

// ReclaimExhausted reports whether a record has spent its attempts. Such a
// record stops returning to candidacy and stays report-only.
func (e *Engine) ReclaimExhausted(key ProcKey) bool {
	rec, ok := e.states[key.IndexKey()]
	return ok && rec.state.ReclaimAttempts >= MaxReclaimAttempts
}

// RequestReclaim takes the phase B edge out of CONFIRMED_ORPHAN. Phase A never
// calls it, and it refuses any state but CONFIRMED_ORPHAN, so the edge cannot be
// reached by accident.
func (e *Engine) RequestReclaim(key ProcKey) bool {
	rec, ok := e.states[key.IndexKey()]
	if !ok || rec.state.State != StateConfirmedOrphan {
		return false
	}
	e.transition(rec, StateReclaimRequested, TriggerReclaimRequested, nil)
	return true
}

// CompleteReclaim records the outcome of a phase B reclaim attempt. A successful
// signal is terminal. A failure counts an attempt and returns the record to
// RECLAIM_FAILED, which is report-only.
func (e *Engine) CompleteReclaim(key ProcKey, succeeded bool) {
	rec, ok := e.states[key.IndexKey()]
	if !ok || rec.state.State != StateReclaimRequested {
		return
	}
	if succeeded {
		e.transition(rec, StateReclaimed, TriggerReclaimSucceeded, nil)
		return
	}
	rec.state.ReclaimAttempts++
	rec.backoff, rec.waited = 0, 0
	e.transition(rec, StateReclaimFailed, TriggerReclaimFailed, map[string]any{
		"attempts":   rec.state.ReclaimAttempts,
		"exhausted":  rec.state.ReclaimAttempts >= MaxReclaimAttempts,
		"next_after": backoffFor(rec.state.ReclaimAttempts).String(),
	})
}

// AbandonReclaim takes the recovery edge out of RECLAIM_REQUESTED when live
// re-verification observed adoption. The kill is abandoned.
func (e *Engine) AbandonReclaim(key ProcKey) {
	rec, ok := e.states[key.IndexKey()]
	if !ok || rec.state.State != StateReclaimRequested {
		return
	}
	e.recoverToActive(rec, TriggerAdoption)
}

// recoverToActive takes the edge back to ACTIVE that every non-terminal state
// has. Both accumulators reset, because the premise they measured, an owner that
// died and stayed dead, no longer holds.
func (e *Engine) recoverToActive(rec *procRecord, trigger string) {
	rec.state.Confirmations = 0
	rec.state.AwakeMillis = 0
	rec.state.Reported = false
	rec.backoff, rec.waited = 0, 0
	e.transition(rec, StateActive, trigger, nil)
}

func (e *Engine) ownerExited(rec *procRecord) bool {
	if rec.state.SessionID == "" {
		return false
	}
	session, known := e.sessions[rec.state.SessionID]
	return known && session.OwnerExited()
}

func (e *Engine) exitSource(rec *procRecord) ExitSource {
	if session, known := e.sessions[rec.state.SessionID]; known {
		return session.OwnerExitSource
	}
	return ""
}

// classOf returns the class the window is keyed by. A process with no pattern
// class is unclassified, and an unattributed process is unattributed whatever
// its pattern says, because no owner means no owner death at any age.
func (e *Engine) classOf(rec *procRecord) string {
	if !rec.state.Confidence.observed() || rec.state.SessionID == "" {
		return config.ClassUnattributed
	}
	if rec.state.Class == "" {
		return config.ClassUnclassified
	}
	return rec.state.Class
}

func (c Confidence) observed() bool { return c == ConfidenceObserved }

func (e *Engine) transition(rec *procRecord, to LifecycleState, trigger string, evidence map[string]any) {
	from := rec.state.State
	if from == to {
		return
	}
	rec.state.State = to
	e.emit(rec, from, to, trigger, evidence)
}

// emit writes the transition record. Every transition carries its trigger, its
// evidence, the confirmation counter, and the accumulated awake milliseconds,
// which is what makes a window survive a restart.
func (e *Engine) emit(rec *procRecord, from, to LifecycleState, trigger string, evidence map[string]any) {
	e.pending = append(e.pending, TransitionRecord{
		At:              NormalizeTime(e.clock.Now()),
		Key:             rec.state.Key,
		SessionID:       rec.state.SessionID,
		From:            from,
		To:              to,
		Trigger:         trigger,
		Evidence:        evidence,
		Confirmations:   rec.state.Confirmations,
		AwakeMillis:     rec.state.AwakeMillis,
		Reported:        rec.state.Reported,
		ReclaimAttempts: rec.state.ReclaimAttempts,
	})
}
