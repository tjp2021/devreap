package attribution

import (
	"errors"
	"os"
	"sort"
	"time"
)

// SessionView is one session as the read-only surfaces present it.
type SessionView struct {
	SessionID   string     `json:"session_id"`
	Harness     string     `json:"harness"`
	Repo        string     `json:"repo,omitempty"`
	RootKey     ProcKey    `json:"root_key"`
	OwnerExitAt *time.Time `json:"owner_exit_at,omitempty"`
	OwnerSource ExitSource `json:"owner_exit_source,omitempty"`

	Processes []ProcessView `json:"processes"`
	RSSBytes  uint64        `json:"rss_bytes"`
}

// OwnerAlive reports whether this session's owner is still running.
func (s SessionView) OwnerAlive() bool { return s.OwnerExitAt == nil }

// OwnerExitedAgo reports how long ago the owner exited, and false when it has
// not.
func (s SessionView) OwnerExitedAgo(now time.Time) (time.Duration, bool) {
	if s.OwnerExitAt == nil {
		return 0, false
	}
	ago := now.Sub(*s.OwnerExitAt)
	if ago < 0 {
		ago = 0
	}
	return ago, true
}

// ProcessView is one tracked process as the read-only surfaces present it.
type ProcessView struct {
	Key           ProcKey        `json:"key"`
	Name          string         `json:"name"`
	Cmdline       string         `json:"cmdline"`
	Class         string         `json:"class,omitempty"`
	State         LifecycleState `json:"state"`
	Confidence    Confidence     `json:"confidence"`
	Channels      []Channel      `json:"channels,omitempty"`
	LinkDepth     int            `json:"link_depth"`
	ParentKey     ProcKey        `json:"parent_key"`
	SessionID     string         `json:"session_id,omitempty"`
	Confirmations int            `json:"confirmations"`
	AwakeMillis   int64          `json:"awake_ms"`
	Reported      bool           `json:"reported,omitempty"`
	RSSBytes      uint64         `json:"rss_bytes"`
	Alive         bool           `json:"alive"`
}

// View is a read-only reconstruction of the store for the CLI surfaces. It runs
// in a separate process from the daemon, so it reads the journal rather than any
// live memory.
type View struct {
	GeneratedAt time.Time `json:"generated_at"`

	Sessions []SessionView `json:"sessions"`
	// Unattributed holds the processes no channel resolved, shown separately so
	// coverage gaps are visible rather than hidden.
	Unattributed []ProcessView `json:"unattributed"`

	// Tracked and Attributed are the coverage numerator and denominator:
	// pattern-matched processes, and how many of them carry an observed claim.
	Tracked    int     `json:"tracked"`
	Attributed int     `json:"attributed"`
	Coverage   float64 `json:"coverage"`

	StateCounts map[LifecycleState]int `json:"state_counts"`

	LastHeartbeat *HeartbeatRecord `json:"last_heartbeat,omitempty"`
	SnapshotAt    *time.Time       `json:"snapshot_at,omitempty"`
	JournalBytes  int64            `json:"journal_bytes"`
	Segments      int              `json:"segments"`
	SchemaVersion int              `json:"schema_version"`

	Findings []Finding `json:"findings,omitempty"`

	// records keeps what the evidence export needs beyond the summary.
	births      map[string]BirthRecord
	transitions map[string][]TransitionRecord
	exits       map[string]OwnerExitRecord
	upgrades    map[string][]ClaimUpgradeRecord
}

// LiveLookup reports the resident memory of a live process, and false when the
// process is gone. The view reconciles the store against it, because resident
// memory is a live number and the journal is a record of the past.
type LiveLookup func(key ProcKey) (uint64, bool)

// LoadView reconstructs the read-only view from a store directory.
//
// A missing store is not an error: it yields an empty view, because the tool
// works correctly with an empty store and reports a coverage gap rather than a
// failure.
func LoadView(dir string, live LiveLookup, now time.Time) (*View, error) {
	view := &View{
		GeneratedAt:   NormalizeTime(now),
		StateCounts:   make(map[LifecycleState]int),
		SchemaVersion: RecordSchemaVersion,
		births:        make(map[string]BirthRecord),
		transitions:   make(map[string][]TransitionRecord),
		exits:         make(map[string]OwnerExitRecord),
		upgrades:      make(map[string][]ClaimUpgradeRecord),
	}

	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return view, nil
		}
		return nil, err
	}

	store, err := OpenStore(StoreConfig{Dir: dir, Now: func() time.Time { return now }})
	if err != nil {
		return nil, err
	}
	defer func() { _ = store.Close() }()

	snapshot, _ := store.ReadSnapshot()
	load, loadErr := store.Load()
	switch {
	case loadErr == nil:
	case errors.Is(loadErr, ErrUnknownSchemaVersion):
		view.Findings = append(view.Findings, Finding{
			Kind:   FindingSnapshotIgnored,
			Detail: "the store holds an unrecognized schema version and was ignored entirely",
		})
		return view, nil
	default:
		return nil, loadErr
	}

	view.JournalBytes = load.Bytes
	view.Segments = load.Segments
	view.Findings = append(view.Findings, load.Findings...)
	view.Findings = append(view.Findings, store.Findings()...)
	if snapshot != nil {
		at := NormalizeTime(snapshot.WrittenAt)
		view.SnapshotAt = &at
	}

	for _, rec := range load.Records {
		switch {
		case rec.Birth != nil:
			view.births[rec.Birth.Key.IndexKey()] = *rec.Birth
		case rec.Transition != nil:
			key := rec.Transition.Key.IndexKey()
			view.transitions[key] = append(view.transitions[key], *rec.Transition)
		case rec.OwnerExit != nil:
			view.exits[rec.OwnerExit.SessionID] = *rec.OwnerExit
		case rec.ClaimUpgrade != nil:
			key := rec.ClaimUpgrade.Key.IndexKey()
			view.upgrades[key] = append(view.upgrades[key], *rec.ClaimUpgrade)
		case rec.Heartbeat != nil:
			beat := *rec.Heartbeat
			view.LastHeartbeat = &beat
		}
	}

	recovered := Recover(snapshot, load.Records)
	view.build(recovered, live)
	return view, nil
}

// build turns recovered state into the presentation form, reconciling each
// record against a live read.
func (v *View) build(recovered *RecoveredState, live LiveLookup) {
	v.Findings = append(v.Findings, recovered.Findings...)

	sessions := make(map[string]*SessionView)
	for id, state := range recovered.Sessions {
		session := &SessionView{
			SessionID: id,
			Harness:   state.Harness,
			Repo:      state.Repo,
			RootKey:   state.RootKey,
		}
		if state.OwnerExitAt != nil {
			at := *state.OwnerExitAt
			session.OwnerExitAt = &at
			session.OwnerSource = state.OwnerExitSource
		}
		sessions[id] = session
	}

	for _, state := range recovered.Processes {
		if state.State == StateExited || state.State == StateReclaimed {
			continue
		}
		process := v.processView(state, live)
		if !process.Alive {
			// A record whose process is gone is history rather than a view of the
			// machine as it is now.
			continue
		}

		v.StateCounts[state.State]++
		if process.Class != "" {
			v.Tracked++
			if process.Confidence == ConfidenceObserved && process.SessionID != "" {
				v.Attributed++
			}
		}

		session, known := sessions[state.SessionID]
		if state.SessionID == "" || !known {
			v.Unattributed = append(v.Unattributed, process)
			continue
		}
		session.Processes = append(session.Processes, process)
		session.RSSBytes += process.RSSBytes
	}

	for _, session := range sessions {
		if len(session.Processes) == 0 && session.OwnerAlive() {
			continue
		}
		sort.Slice(session.Processes, func(i, j int) bool {
			return session.Processes[i].RSSBytes > session.Processes[j].RSSBytes
		})
		v.Sessions = append(v.Sessions, *session)
	}

	// The view sorts by resident memory, which is the number the user is
	// deciding on.
	sort.Slice(v.Sessions, func(i, j int) bool {
		if v.Sessions[i].RSSBytes != v.Sessions[j].RSSBytes {
			return v.Sessions[i].RSSBytes > v.Sessions[j].RSSBytes
		}
		return v.Sessions[i].SessionID < v.Sessions[j].SessionID
	})
	sort.Slice(v.Unattributed, func(i, j int) bool {
		return v.Unattributed[i].RSSBytes > v.Unattributed[j].RSSBytes
	})

	if v.Tracked > 0 {
		v.Coverage = float64(v.Attributed) / float64(v.Tracked)
	}
}

func (v *View) processView(state ProcessState, live LiveLookup) ProcessView {
	process := ProcessView{
		Key:           state.Key,
		Class:         state.Class,
		State:         state.State,
		Confidence:    state.Confidence,
		SessionID:     state.SessionID,
		Confirmations: state.Confirmations,
		AwakeMillis:   state.AwakeMillis,
		Reported:      state.Reported,
		Alive:         true,
	}
	if birth, known := v.births[state.Key.IndexKey()]; known {
		process.Name = birth.Name
		process.Cmdline = birth.Cmdline
		process.ParentKey = birth.ParentKey
		process.Channels = birth.Owner.Channels
		process.LinkDepth = birth.Owner.LinkDepth
		if process.Class == "" {
			process.Class = birth.Class
		}
	}
	if live != nil {
		rss, alive := live(state.Key)
		process.RSSBytes, process.Alive = rss, alive
	}
	return process
}

// UnattributedRSS totals the resident memory of the processes no channel
// resolved.
func (v *View) UnattributedRSS() uint64 {
	var total uint64
	for _, process := range v.Unattributed {
		total += process.RSSBytes
	}
	return total
}

// Evidence is one session exported as a single document, which is the artifact a
// developer attaches to an upstream bug report.
type Evidence struct {
	GeneratedAt time.Time `json:"generated_at"`
	SchemaVer   int       `json:"schema_version"`

	Session SessionView      `json:"session"`
	Root    *BirthRecord     `json:"root_birth,omitempty"`
	Exit    *OwnerExitRecord `json:"owner_exit,omitempty"`

	// SpawnTree holds the birth record of every member, carrying its key, its
	// parent key, and its link depth.
	SpawnTree []BirthRecord `json:"spawn_tree"`
	// Transitions is the full lifecycle history of every member, in order.
	Transitions []TransitionRecord `json:"transitions"`
	// Upgrades are the claim upgrades that amended a member's birth record.
	Upgrades []ClaimUpgradeRecord `json:"claim_upgrades,omitempty"`
}

// Evidence exports one session. It returns false when no session by that
// identifier is recorded.
//
// Every field comes from records that already passed the redaction filter on the
// way in, so the export carries no value the filter would have dropped.
func (v *View) Evidence(sessionID string, now time.Time) (*Evidence, bool) {
	var session *SessionView
	for i := range v.Sessions {
		if v.Sessions[i].SessionID == sessionID {
			session = &v.Sessions[i]
			break
		}
	}
	if session == nil {
		return nil, false
	}

	out := &Evidence{
		GeneratedAt: NormalizeTime(now),
		SchemaVer:   RecordSchemaVersion,
		Session:     *session,
		SpawnTree:   []BirthRecord{},
		Transitions: []TransitionRecord{},
	}

	if birth, known := v.births[session.RootKey.IndexKey()]; known {
		root := birth
		out.Root = &root
	}
	if exit, known := v.exits[sessionID]; known {
		record := exit
		out.Exit = &record
	}

	for _, birth := range v.births {
		if birth.Owner.SessionID != sessionID {
			continue
		}
		out.SpawnTree = append(out.SpawnTree, birth)
		key := birth.Key.IndexKey()
		out.Transitions = append(out.Transitions, v.transitions[key]...)
		out.Upgrades = append(out.Upgrades, v.upgrades[key]...)
	}

	sort.Slice(out.SpawnTree, func(i, j int) bool {
		if out.SpawnTree[i].Owner.LinkDepth != out.SpawnTree[j].Owner.LinkDepth {
			return out.SpawnTree[i].Owner.LinkDepth < out.SpawnTree[j].Owner.LinkDepth
		}
		return out.SpawnTree[i].ObservedAt.Before(out.SpawnTree[j].ObservedAt)
	})
	sort.Slice(out.Transitions, func(i, j int) bool {
		return out.Transitions[i].At.Before(out.Transitions[j].At)
	})
	return out, true
}

// SessionIDs returns every recorded session identifier, ordered.
func (v *View) SessionIDs() []string {
	out := make([]string, 0, len(v.Sessions))
	for _, session := range v.Sessions {
		out = append(out, session.SessionID)
	}
	sort.Strings(out)
	return out
}
