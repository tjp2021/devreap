package attribution

import (
	"testing"
	"time"
)

func TestLifecycleStateHelpers(t *testing.T) {
	all := []LifecycleState{
		StateActive, StateOwnerGone, StateGracePeriod, StateOrphanCandidate,
		StateConfirmedOrphan, StateReclaimRequested, StateReclaimed,
		StateReclaimFailed, StateUnattributed, StateExited,
	}
	for _, state := range all {
		if !state.Valid() {
			t.Errorf("%s should be a valid state", state)
		}
	}
	if LifecycleState("MADE_UP").Valid() {
		t.Error("an unknown state reported itself valid")
	}

	// R7: every state except EXITED and RECLAIMED has an edge back to ACTIVE,
	// so only those two are terminal.
	for _, state := range all {
		wantTerminal := state == StateExited || state == StateReclaimed
		if state.Terminal() != wantTerminal {
			t.Errorf("%s terminal: got %v, want %v", state, state.Terminal(), wantTerminal)
		}
	}
}

// TestReconcileStateTakesTheRestrictiveAnswer drives the rule the design names
// by example, plus the cases around it.
func TestReconcileStateTakesTheRestrictiveAnswer(t *testing.T) {
	tests := []struct {
		name     string
		snapshot LifecycleState
		journal  LifecycleState
		want     LifecycleState
	}{
		{
			name:     "the case the design names",
			snapshot: StateActive,
			journal:  StateOrphanCandidate,
			want:     StateGracePeriod,
		},
		{
			name:     "agreement is kept as it stands",
			snapshot: StateOrphanCandidate,
			journal:  StateOrphanCandidate,
			want:     StateOrphanCandidate,
		},
		{
			name:     "confirmation is withdrawn on any disagreement",
			snapshot: StateGracePeriod,
			journal:  StateConfirmedOrphan,
			want:     StateGracePeriod,
		},
		{
			name:     "a recorded owner exit survives a stale ACTIVE",
			snapshot: StateActive,
			journal:  StateOwnerGone,
			want:     StateOwnerGone,
		},
		{
			name:     "a gone process is gone whichever side says so",
			snapshot: StateOrphanCandidate,
			journal:  StateExited,
			want:     StateExited,
		},
		{
			name:     "unattributed wins, because it is never eligible",
			snapshot: StateUnattributed,
			journal:  StateConfirmedOrphan,
			want:     StateUnattributed,
		},
		{
			name:     "a pending reclaim is withdrawn",
			snapshot: StateGracePeriod,
			journal:  StateReclaimRequested,
			want:     StateGracePeriod,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ReconcileState(tt.snapshot, tt.journal); got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
			// The rule cannot depend on which side a state arrived from.
			if got := ReconcileState(tt.journal, tt.snapshot); got != tt.want {
				t.Errorf("reversed: got %s, want %s", got, tt.want)
			}
		})
	}
}

// TestSnapshotJournalDisagreementTakesRestrictive seeds a snapshot and a journal
// tail that disagree, and asserts recovery adopts the more restrictive state
// with the recorded accumulator preserved. It also asserts a member record with
// no surviving root is demoted to UNATTRIBUTED rather than joined to a guessed
// session.
func TestSnapshotJournalDisagreementTakesRestrictive(t *testing.T) {
	now := time.Date(2026, 8, 20, 6, 0, 0, 0, time.UTC)
	memberKey := NewProcKey(98925, now.Add(-2*time.Hour))
	orphanedKey := NewProcKey(99010, now.Add(-3*time.Hour))
	rootKey := NewProcKey(98888, now.Add(-4*time.Hour))

	snapshot := &StoreSnapshot{
		V:         RecordSchemaVersion,
		Type:      RecordSnapshot,
		WrittenAt: now.Add(-10 * time.Minute),
		Processes: []ProcessState{
			{
				Key: memberKey, State: StateActive, SessionID: "5f1c9a2e",
				RootKey: rootKey, Confidence: ConfidenceObserved, Class: "mcp",
			},
			{
				Key: orphanedKey, State: StateActive, SessionID: "deadbeef",
				RootKey:    NewProcKey(90000, now.Add(-5*time.Hour)),
				Confidence: ConfidenceObserved, Class: "dev-server",
			},
		},
		Sessions: []SessionState{
			{SessionID: "5f1c9a2e", Harness: "claude-code-cli", RootKey: rootKey},
		},
	}

	journal := []Record{
		{Type: RecordTransition, Transition: &TransitionRecord{
			At: now.Add(-5 * time.Minute), Key: memberKey, SessionID: "5f1c9a2e",
			From: StateGracePeriod, To: StateOrphanCandidate, Trigger: "window_reached",
			Confirmations: 2, AwakeMillis: 300_000,
		}},
	}

	recovered := Recover(snapshot, journal)

	member := recovered.Processes[memberKey.IndexKey()]
	if member.State != StateGracePeriod {
		t.Errorf("member state: got %s, want GRACE_PERIOD", member.State)
	}
	if member.AwakeMillis != 300_000 || member.Confirmations != 2 {
		t.Errorf("accumulators lost: awake=%d confirmations=%d, want 300000 and 2", member.AwakeMillis, member.Confirmations)
	}
	if member.SessionID != "5f1c9a2e" {
		t.Errorf("session: got %q, want the surviving session", member.SessionID)
	}

	orphaned := recovered.Processes[orphanedKey.IndexKey()]
	if orphaned.State != StateUnattributed {
		t.Errorf("orphaned member state: got %s, want UNATTRIBUTED", orphaned.State)
	}
	if orphaned.SessionID != "" || orphaned.Confidence != ConfidenceNone {
		t.Errorf("orphaned member kept a guessed session: %+v", orphaned)
	}

	kinds := map[string]int{}
	for _, f := range recovered.Findings {
		kinds[f.Kind]++
	}
	if kinds[FindingRecoveryDisagreement] != 1 {
		t.Errorf("findings %v, want one recovery disagreement", kinds)
	}
	if kinds[FindingOrphanedMember] != 1 {
		t.Errorf("findings %v, want one orphaned member", kinds)
	}
}

func TestRecoverFromJournalAlone(t *testing.T) {
	now := time.Date(2026, 8, 20, 6, 0, 0, 0, time.UTC)
	key := NewProcKey(98925, now.Add(-2*time.Hour))
	rootKey := NewProcKey(98888, now.Add(-3*time.Hour))
	exitAt := now.Add(-5 * time.Minute)

	journal := []Record{
		{Type: RecordBirth, Birth: &BirthRecord{
			ObservedAt: now.Add(-2 * time.Hour), Source: BirthSourcePoll, Key: key, Class: "mcp",
			Owner: OwnershipClaim{
				SessionID: "5f1c9a2e", Harness: "claude-code-cli", RootKey: rootKey,
				Confidence: ConfidenceObserved, Channels: []Channel{ChannelWatchedAncestry}, LinkDepth: 1,
			},
		}},
		{Type: RecordOwnerExit, OwnerExit: &OwnerExitRecord{
			At: exitAt, SessionID: "5f1c9a2e", Harness: "claude-code-cli",
			RootKey: rootKey, Source: ExitSourceKqueue, MembersAlive: 3,
		}},
		{Type: RecordTransition, Transition: &TransitionRecord{
			At: exitAt, Key: key, SessionID: "5f1c9a2e",
			From: StateActive, To: StateOwnerGone, Trigger: "owner_exit_recorded",
		}},
	}

	recovered := Recover(nil, journal)

	state := recovered.Processes[key.IndexKey()]
	if state.State != StateOwnerGone {
		t.Errorf("state: got %s, want OWNER_GONE", state.State)
	}
	if state.Confidence != ConfidenceObserved || state.Class != "mcp" {
		t.Errorf("claim lost in replay: %+v", state)
	}

	session := recovered.Sessions["5f1c9a2e"]
	if !session.OwnerExited() {
		t.Error("the recorded owner exit did not survive replay")
	}
	if session.OwnerExitAt == nil || !session.OwnerExitAt.Equal(exitAt) {
		t.Errorf("owner exit time: got %v, want %s", session.OwnerExitAt, exitAt)
	}
	if len(recovered.Findings) != 0 {
		t.Errorf("a clean replay raised findings: %+v", recovered.Findings)
	}
}

func TestRecoverAppliesClaimUpgradeWithoutTouchingTheBirth(t *testing.T) {
	now := time.Date(2026, 8, 20, 6, 0, 0, 0, time.UTC)
	key := NewProcKey(98925, now.Add(-2*time.Hour))
	rootKey := NewProcKey(98888, now.Add(-3*time.Hour))

	birth := &BirthRecord{
		ObservedAt: now.Add(-2 * time.Hour), Source: BirthSourceBackfill, Key: key,
		Owner: OwnershipClaim{
			SessionID: "5f1c9a2e", Harness: "claude-code-cli", RootKey: rootKey,
			Confidence: ConfidenceInferred, Channels: []Channel{ChannelEnv},
		},
	}
	journal := []Record{
		{Type: RecordBirth, Birth: birth},
		{Type: RecordClaimUpgrade, ClaimUpgrade: &ClaimUpgradeRecord{
			At: now.Add(-time.Minute), Key: key, From: ConfidenceInferred, To: ConfidenceObserved,
			Reason:   UpgradeAncestryResolved,
			Evidence: ClaimUpgradeEvidence{RootKey: rootKey, LinkDepth: 2, SessionID: "5f1c9a2e"},
		}},
	}

	recovered := Recover(nil, journal)

	state := recovered.Processes[key.IndexKey()]
	if state.Confidence != ConfidenceObserved {
		t.Errorf("confidence: got %s, want observed after the upgrade", state.Confidence)
	}
	if birth.Owner.Confidence != ConfidenceInferred {
		t.Error("the birth record was modified; an upgrade must be a separate record")
	}
}

func TestRecoverDemotesUnattributedBirthsAndKeepsThemVisible(t *testing.T) {
	now := time.Date(2026, 8, 20, 6, 0, 0, 0, time.UTC)
	key := NewProcKey(99010, now.Add(-time.Hour))

	recovered := Recover(nil, []Record{
		{Type: RecordBirth, Birth: &BirthRecord{
			ObservedAt: now.Add(-time.Hour), Source: BirthSourcePoll, Key: key,
			Owner: OwnershipClaim{Confidence: ConfidenceNone},
		}},
	})

	state, ok := recovered.Processes[key.IndexKey()]
	if !ok {
		t.Fatal("an unattributed process vanished from recovery; it must stay visible")
	}
	if state.State != StateUnattributed {
		t.Errorf("state: got %s, want UNATTRIBUTED", state.State)
	}
}

func TestRecoverPrefersATrustedExitSourceOverTheHook(t *testing.T) {
	now := time.Date(2026, 8, 20, 6, 0, 0, 0, time.UTC)
	rootKey := NewProcKey(98888, now.Add(-3*time.Hour))
	hookAt := now.Add(-10 * time.Minute)
	observedAt := now.Add(-9 * time.Minute)

	recovered := Recover(nil, []Record{
		{Type: RecordOwnerExit, OwnerExit: &OwnerExitRecord{
			At: hookAt, SessionID: "5f1c9a2e", RootKey: rootKey, Source: ExitSourceAgentHook,
		}},
		{Type: RecordOwnerExit, OwnerExit: &OwnerExitRecord{
			At: observedAt, SessionID: "5f1c9a2e", RootKey: rootKey, Source: ExitSourcePollAbsent,
		}},
	})

	session := recovered.Sessions["5f1c9a2e"]
	if session.OwnerExitSource != ExitSourcePollAbsent {
		t.Errorf("source: got %s, want the trusted one", session.OwnerExitSource)
	}
	if !session.OwnerExited() {
		t.Error("a trusted source should count as an owner exit")
	}

	hookOnly := Recover(nil, []Record{
		{Type: RecordOwnerExit, OwnerExit: &OwnerExitRecord{
			At: hookAt, SessionID: "a71b0c34", RootKey: rootKey, Source: ExitSourceAgentHook,
		}},
	})
	if hookOnly.Sessions["a71b0c34"].OwnerExited() {
		t.Error("the hook alone must not count as an owner exit")
	}
}
