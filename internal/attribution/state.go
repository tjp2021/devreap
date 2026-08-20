package attribution

import (
	"fmt"
	"time"
)

// LifecycleState is the one state every tracked process holds. Phase A writes
// and displays these; only phase B may act on one, and only on
// CONFIRMED_ORPHAN.
type LifecycleState string

const (
	// StateActive means the owner is alive, or a live parent holds the process.
	StateActive LifecycleState = "ACTIVE"
	// StateOwnerGone means an owner exit is recorded and the process is alive.
	StateOwnerGone LifecycleState = "OWNER_GONE"
	// StateGracePeriod means the class window is running down.
	StateGracePeriod LifecycleState = "GRACE_PERIOD"
	// StateOrphanCandidate means the window is spent and confirmations are
	// accumulating.
	StateOrphanCandidate LifecycleState = "ORPHAN_CANDIDATE"
	// StateConfirmedOrphan means all five conditions of R6 hold. It is the only
	// state phase B may act on.
	StateConfirmedOrphan LifecycleState = "CONFIRMED_ORPHAN"
	// StateReclaimRequested is phase B only.
	StateReclaimRequested LifecycleState = "RECLAIM_REQUESTED"
	// StateReclaimed is phase B only, and terminal.
	StateReclaimed LifecycleState = "RECLAIMED"
	// StateReclaimFailed is phase B only. It is report-only and keeps its
	// recovery edge, so it bounds attempts rather than the graph.
	StateReclaimFailed LifecycleState = "RECLAIM_FAILED"
	// StateUnattributed means no ownership channel resolved. It is never
	// action-eligible, at any age.
	StateUnattributed LifecycleState = "UNATTRIBUTED"
	// StateExited means the process key is gone. Terminal.
	StateExited LifecycleState = "EXITED"
)

// progress ranks the states along the path toward action. A higher rank is
// closer to eligibility. Off-ladder states are handled before this is consulted.
func (s LifecycleState) progress() int {
	switch s {
	case StateActive:
		return 0
	case StateOwnerGone:
		return 1
	case StateGracePeriod, StateReclaimFailed:
		return 2
	case StateOrphanCandidate:
		return 3
	case StateConfirmedOrphan:
		return 4
	case StateReclaimRequested:
		return 5
	default:
		return 0
	}
}

// Valid reports whether this is a state the engine knows.
func (s LifecycleState) Valid() bool {
	switch s {
	case StateActive, StateOwnerGone, StateGracePeriod, StateOrphanCandidate,
		StateConfirmedOrphan, StateReclaimRequested, StateReclaimed,
		StateReclaimFailed, StateUnattributed, StateExited:
		return true
	default:
		return false
	}
}

// Terminal reports whether the state has no edge back to ACTIVE. Only EXITED
// and RECLAIMED are terminal, which is what R7 requires of every other state.
func (s LifecycleState) Terminal() bool {
	return s == StateExited || s == StateReclaimed
}

// ReconcileState resolves a disagreement between the snapshot and the journal
// tail after a crash, and always takes the more restrictive answer.
//
// The interesting case is the one the design names. When the snapshot shows
// ACTIVE and the journal tail shows ORPHAN_CANDIDATE, the result is
// GRACE_PERIOD rather than either extreme: the recorded accumulator is kept, so
// progress survives, and candidacy is withdrawn, so eligibility does not. Every
// disagreement follows the same shape, capped at GRACE_PERIOD.
//
// A process either source calls gone, reclaimed, or unattributed takes that
// answer outright, because each is more restrictive than anything on the ladder.
func ReconcileState(snapshot, journal LifecycleState) LifecycleState {
	if snapshot == journal {
		return snapshot
	}
	switch {
	case snapshot == StateExited || journal == StateExited:
		return StateExited
	case snapshot == StateReclaimed || journal == StateReclaimed:
		return StateReclaimed
	case snapshot == StateUnattributed || journal == StateUnattributed:
		return StateUnattributed
	}

	advanced := snapshot
	if journal.progress() > snapshot.progress() {
		advanced = journal
	}
	if advanced.progress() > StateGracePeriod.progress() {
		return StateGracePeriod
	}
	return advanced
}

// ProcessState is what the store persists per tracked process, and what a
// restart resumes from.
type ProcessState struct {
	Key        ProcKey        `json:"key"`
	State      LifecycleState `json:"state"`
	SessionID  string         `json:"session_id,omitempty"`
	RootKey    ProcKey        `json:"root_key"`
	Confidence Confidence     `json:"confidence"`
	Class      string         `json:"class,omitempty"`

	// Confirmations and AwakeMillis are the two accumulators that count awake
	// time only. A sleep gap contributes zero to them and never resets them.
	Confirmations int   `json:"confirmations"`
	AwakeMillis   int64 `json:"awake_ms"`

	Reported        bool `json:"reported,omitempty"`
	ReclaimAttempts int  `json:"reclaim_attempts,omitempty"`
}

// SessionState is what the store persists per session.
type SessionState struct {
	SessionID       string     `json:"session_id"`
	Harness         string     `json:"harness"`
	Repo            string     `json:"repo,omitempty"`
	RootKey         ProcKey    `json:"root_key"`
	OwnerExitAt     *time.Time `json:"owner_exit_at,omitempty"`
	OwnerExitSource ExitSource `json:"owner_exit_source,omitempty"`
}

// OwnerExited reports whether a trusted exit source recorded this session's
// owner as gone. The agent hook alone does not count.
func (s SessionState) OwnerExited() bool {
	return s.OwnerExitAt != nil && s.OwnerExitSource.Trusted()
}

// StoreSnapshot is the compacted state written every 5 minutes and on clean
// shutdown, so a restart replays only the journal tail.
type StoreSnapshot struct {
	V         int            `json:"v"`
	Type      RecordType     `json:"type"`
	WrittenAt time.Time      `json:"written_at"`
	Processes []ProcessState `json:"processes"`
	Sessions  []SessionState `json:"sessions"`
}

// RecoveredState is the merged view a restart resumes from.
type RecoveredState struct {
	Processes map[string]ProcessState
	Sessions  map[string]SessionState
	Findings  []Finding
}

// Finding kinds raised by recovery and by the store.
const (
	// FindingOrphanedMember means a member record survived recovery with no
	// matching session root, so it was demoted rather than guessed at.
	FindingOrphanedMember = "orphaned_member_demoted"
	// FindingRecoveryDisagreement means the snapshot and the journal tail
	// disagreed about a process, and the more restrictive answer was taken.
	FindingRecoveryDisagreement = "recovery_disagreement"
)

// Recover merges a snapshot with the journal records written after it.
//
// Three rules govern the merge. The journal is newer, so it supplies the
// accumulators. A disagreement resolves to the more restrictive state. A member
// whose session root has no record after recovery is demoted to UNATTRIBUTED
// rather than joined to a guessed session.
func Recover(snapshot *StoreSnapshot, records []Record) *RecoveredState {
	recovered := &RecoveredState{
		Processes: make(map[string]ProcessState),
		Sessions:  make(map[string]SessionState),
	}

	snapshotStates := make(map[string]ProcessState)
	if snapshot != nil {
		for _, ps := range snapshot.Processes {
			key := ps.Key.IndexKey()
			snapshotStates[key] = ps
			recovered.Processes[key] = ps
		}
		for _, ss := range snapshot.Sessions {
			recovered.Sessions[ss.SessionID] = ss
		}
	}

	journalStates := make(map[string]ProcessState)
	for _, rec := range records {
		applyRecord(rec, recovered, journalStates)
	}

	for key, journalState := range journalStates {
		snapshotState, both := snapshotStates[key]
		if !both {
			recovered.Processes[key] = journalState
			continue
		}
		merged := journalState
		merged.State = ReconcileState(snapshotState.State, journalState.State)
		if merged.State != journalState.State || merged.State != snapshotState.State {
			recovered.Findings = append(recovered.Findings, Finding{
				Kind: FindingRecoveryDisagreement,
				Detail: fmt.Sprintf("process %s: snapshot said %s, journal said %s, recovered as %s",
					key, snapshotState.State, journalState.State, merged.State),
			})
		}
		recovered.Processes[key] = merged
	}

	demoteOrphanedMembers(recovered)
	return recovered
}

func applyRecord(rec Record, recovered *RecoveredState, journalStates map[string]ProcessState) {
	switch {
	case rec.Birth != nil:
		birth := rec.Birth
		key := birth.Key.IndexKey()
		state := StateActive
		if birth.Owner.Confidence == ConfidenceNone {
			state = StateUnattributed
		}
		existing, seen := journalStates[key]
		if !seen {
			existing = ProcessState{Key: birth.Key, State: state}
		}
		existing.SessionID = birth.Owner.SessionID
		existing.RootKey = birth.Owner.RootKey
		existing.Confidence = birth.Owner.Confidence
		existing.Class = birth.Class
		journalStates[key] = existing

		if birth.Owner.SessionID != "" {
			if _, known := recovered.Sessions[birth.Owner.SessionID]; !known {
				recovered.Sessions[birth.Owner.SessionID] = SessionState{
					SessionID: birth.Owner.SessionID,
					Harness:   birth.Owner.Harness,
					Repo:      birth.Owner.Repo,
					RootKey:   birth.Owner.RootKey,
				}
			}
		}

	case rec.ClaimUpgrade != nil:
		upgrade := rec.ClaimUpgrade
		key := upgrade.Key.IndexKey()
		state, seen := journalStates[key]
		if !seen {
			state = ProcessState{Key: upgrade.Key, State: StateActive}
		}
		// Nothing lowers a claim. Only a key mismatch invalidates one, and a
		// mismatched key never reaches this record.
		if upgrade.To == ConfidenceObserved || state.Confidence == ConfidenceNone {
			state.Confidence = upgrade.To
		}
		if upgrade.Evidence.SessionID != "" {
			state.SessionID = upgrade.Evidence.SessionID
		}
		if !upgrade.Evidence.RootKey.Zero() {
			state.RootKey = upgrade.Evidence.RootKey
		}
		if state.State == StateUnattributed && upgrade.To != ConfidenceNone {
			state.State = StateActive
		}
		journalStates[key] = state

	case rec.Transition != nil:
		transition := rec.Transition
		key := transition.Key.IndexKey()
		state, seen := journalStates[key]
		if !seen {
			state = ProcessState{Key: transition.Key}
		}
		state.State = transition.To
		state.Confirmations = transition.Confirmations
		state.AwakeMillis = transition.AwakeMillis
		state.Reported = transition.Reported
		state.ReclaimAttempts = transition.ReclaimAttempts
		if transition.SessionID != "" {
			state.SessionID = transition.SessionID
		}
		journalStates[key] = state

	case rec.OwnerExit != nil:
		exit := rec.OwnerExit
		session, known := recovered.Sessions[exit.SessionID]
		if !known {
			session = SessionState{SessionID: exit.SessionID, Harness: exit.Harness, RootKey: exit.RootKey}
		}
		at := exit.At
		// A trusted source never loses to the hook, which is enrichment only.
		if session.OwnerExitAt == nil || (!session.OwnerExitSource.Trusted() && exit.Source.Trusted()) {
			session.OwnerExitAt = &at
			session.OwnerExitSource = exit.Source
		}
		recovered.Sessions[exit.SessionID] = session
	}
}

// demoteOrphanedMembers applies the last recovery rule: a member whose session
// root has no record after recovery is demoted to UNATTRIBUTED. Joining it to a
// guessed session would invent the one fact this design refuses to invent.
func demoteOrphanedMembers(recovered *RecoveredState) {
	for key, state := range recovered.Processes {
		if state.SessionID == "" || state.State == StateExited {
			continue
		}
		if _, known := recovered.Sessions[state.SessionID]; known {
			continue
		}
		recovered.Findings = append(recovered.Findings, Finding{
			Kind:   FindingOrphanedMember,
			Detail: fmt.Sprintf("process %s claimed session %q, which has no record after recovery", key, state.SessionID),
		})
		state.State = StateUnattributed
		state.Confidence = ConfidenceNone
		state.SessionID = ""
		state.RootKey = ProcKey{}
		recovered.Processes[key] = state
	}
}
