package attribution

import (
	"encoding/json"
	"fmt"
	"time"
)

// RecordSchemaVersion is the version stamped on every record this binary
// writes. A store holding an unrecognized version is ignored entirely rather
// than guessed at, so a newer devreap can change this without a migration.
const RecordSchemaVersion = 1

// RecordType names the shape of one journal entry.
type RecordType string

const (
	RecordBirth        RecordType = "birth"
	RecordOwnerExit    RecordType = "owner_exit"
	RecordTransition   RecordType = "transition"
	RecordClaimUpgrade RecordType = "claim_upgrade"
	RecordHeartbeat    RecordType = "heartbeat"
	RecordSnapshot     RecordType = "snapshot"
)

// BirthSource says how a birth came to be recorded.
type BirthSource string

const (
	// BirthSourcePoll is a birth the watcher saw appear between two polls, which
	// is the only source that can carry a witnessed spawn link.
	BirthSourcePoll BirthSource = "poll"
	// BirthSourceBackfill is a process that already existed when the watcher
	// started. Nobody witnessed its birth, so its claim is inferred at best.
	BirthSourceBackfill BirthSource = "backfill"
)

// Confidence is the ownership ladder. Only observed may ever gate an action,
// and that gate belongs to phase B.
type Confidence string

const (
	// ConfidenceObserved rests on a chain of spawn links the watcher recorded.
	ConfidenceObserved Confidence = "observed"
	// ConfidenceInferred rests on a marker or a process group alone. It is
	// reportable and never action-eligible, because any process can set any
	// variable.
	ConfidenceInferred Confidence = "inferred"
	// ConfidenceNone means no channel resolved an owner.
	ConfidenceNone Confidence = "none"
)

// Channel names one source that contributed to an ownership claim.
type Channel string

const (
	ChannelWatchedAncestry Channel = "watched_ancestry"
	ChannelEnv             Channel = "env"
	ChannelPGID            Channel = "pgid"
)

// ExitSource says how an owner's exit was observed.
type ExitSource string

const (
	ExitSourceKqueue     ExitSource = "kqueue_note_exit"
	ExitSourcePollAbsent ExitSource = "poll_absent"
	// ExitSourceAgentHook is enrichment. It gives an exact end time when the
	// user configured one, it does not fire when a session is killed, and it is
	// never trusted for eligibility.
	ExitSourceAgentHook ExitSource = "agent_hook"
)

// Trusted reports whether this source may contribute to eligibility. The agent
// hook is recorded for display and for earlier notification only.
func (s ExitSource) Trusted() bool {
	return s == ExitSourceKqueue || s == ExitSourcePollAbsent
}

// Claim upgrade reasons.
const (
	// UpgradeAncestryResolved means a journal replay recovered a parent birth
	// record that completes a previously broken chain.
	UpgradeAncestryResolved = "ancestry_chain_resolved"
	// UpgradeAdapterAddition means a new descriptor made an existing ancestor
	// recognizable as a session root.
	UpgradeAdapterAddition = "adapter_addition"
)

// OwnershipClaim records who started a process and how well that is known.
type OwnershipClaim struct {
	SessionID  string     `json:"session_id"`
	Harness    string     `json:"harness"`
	Repo       string     `json:"repo"`
	RootKey    ProcKey    `json:"root_key"`
	Confidence Confidence `json:"confidence"`
	Channels   []Channel  `json:"channels"`
	LinkDepth  int        `json:"link_depth"`
}

// BirthRecord is written once when a process is first seen, and is never
// modified. It is the source of truth for who started a process. A later claim
// upgrade is a separate record rather than an edit.
type BirthRecord struct {
	V          int            `json:"v"`
	Type       RecordType     `json:"type"`
	ObservedAt time.Time      `json:"observed_at"`
	Source     BirthSource    `json:"source"`
	Key        ProcKey        `json:"key"`
	ParentKey  ProcKey        `json:"parent_key"`
	PGID       int32          `json:"pgid"`
	TTY        string         `json:"tty"`
	Name       string         `json:"name"`
	Exe        string         `json:"exe"`
	Cmdline    string         `json:"cmdline"`
	Class      string         `json:"class"`
	Owner      OwnershipClaim `json:"owner"`

	// Unverifiable lists every field whose read failed. A record naming
	// start_time here can never gate an action.
	Unverifiable []string `json:"unverifiable"`
}

// UnverifiableStartTime is the entry that makes a record unusable for action.
const UnverifiableStartTime = "start_time"

// Actionable reports whether this record may ever contribute to an action. It
// says nothing about whether an action is warranted.
func (r BirthRecord) Actionable() bool {
	if !r.Key.Verifiable() {
		return false
	}
	for _, field := range r.Unverifiable {
		if field == UnverifiableStartTime {
			return false
		}
	}
	return r.Owner.Confidence == ConfidenceObserved
}

// OwnerExitRecord records a session root's exit as an event with a timestamp
// and a source, rather than computing it at read time.
type OwnerExitRecord struct {
	V             int        `json:"v"`
	Type          RecordType `json:"type"`
	At            time.Time  `json:"at"`
	SessionID     string     `json:"session_id"`
	Harness       string     `json:"harness"`
	RootKey       ProcKey    `json:"root_key"`
	Source        ExitSource `json:"source"`
	MembersAlive  int        `json:"members_alive"`
	RSSAliveBytes uint64     `json:"rss_alive_bytes"`
}

// TransitionRecord records one lifecycle state change with its trigger and its
// evidence. It also carries the confirmation counter and the accumulated awake
// milliseconds, which is what makes a window survive a restart: the engine
// resumes from the last record rather than from a wall-clock stamp.
type TransitionRecord struct {
	V             int            `json:"v"`
	Type          RecordType     `json:"type"`
	At            time.Time      `json:"at"`
	Key           ProcKey        `json:"key"`
	SessionID     string         `json:"session_id,omitempty"`
	From          LifecycleState `json:"from"`
	To            LifecycleState `json:"to"`
	Trigger       string         `json:"trigger"`
	Evidence      map[string]any `json:"evidence,omitempty"`
	Confirmations int            `json:"confirmations"`
	AwakeMillis   int64          `json:"awake_ms"`

	// Reported marks whether the user has already seen a confirmed orphan.
	// Reporting is a display flag rather than a state, so a confirmed process
	// stays in CONFIRMED_ORPHAN and remains reachable by the phase B edge.
	Reported bool `json:"reported,omitempty"`

	// ReclaimAttempts counts failed reclaim attempts. Phase B bounds it at 5.
	ReclaimAttempts int `json:"reclaim_attempts,omitempty"`
}

// ClaimUpgradeEvidence carries what made a spawn link provable.
type ClaimUpgradeEvidence struct {
	RootKey   ProcKey `json:"root_key"`
	LinkDepth int     `json:"link_depth"`
	SessionID string  `json:"session_id,omitempty"`
	Harness   string  `json:"harness,omitempty"`
}

// ClaimUpgradeRecord raises a claim from inferred to observed when a spawn link
// that already existed becomes provable. The direction is one way: nothing
// downgrades a claim, and only a key mismatch invalidates one.
type ClaimUpgradeRecord struct {
	V        int                  `json:"v"`
	Type     RecordType           `json:"type"`
	At       time.Time            `json:"at"`
	Key      ProcKey              `json:"key"`
	From     Confidence           `json:"from"`
	To       Confidence           `json:"to"`
	Reason   string               `json:"reason"`
	Evidence ClaimUpgradeEvidence `json:"evidence"`
}

// HeartbeatRecord is the watcher's health and coverage series, written every 60
// seconds with counters aggregated over that minute.
//
// The cadence is decoupled from the poll cadence on purpose. At roughly 400
// bytes a record, one heartbeat a second is about 35 megabytes a day, which
// breaks the 32 megabyte ceiling in under a day and would push the coverage
// history out of the store long before a 7 day measurement could finish.
type HeartbeatRecord struct {
	V    int        `json:"v"`
	Type RecordType `json:"type"`
	At   time.Time  `json:"at"`

	Polls              int   `json:"polls"`
	BirthsSeen         int   `json:"births_seen"`
	BirthsPersisted    int   `json:"births_persisted"`
	EnvReadFailures    int   `json:"env_read_failures"`
	SleepGapMillis     int64 `json:"sleep_gap_ms"`
	PollDurationMicros int64 `json:"poll_duration_us"`

	Tracked      int   `json:"tracked"`
	Attributed   int   `json:"attributed"`
	Upgraded     int   `json:"upgraded"`
	JournalBytes int64 `json:"journal_bytes"`
}

// Coverage is the share of tracked processes carrying an ownership claim,
// counted after upgrades are applied. It returns 0 when nothing was tracked.
func (r HeartbeatRecord) Coverage() float64 {
	if r.Tracked <= 0 {
		return 0
	}
	return float64(r.Attributed) / float64(r.Tracked)
}

// Record is one decoded journal entry. Exactly one pointer is set, and Type
// says which.
type Record struct {
	Type         RecordType
	Birth        *BirthRecord
	OwnerExit    *OwnerExitRecord
	Transition   *TransitionRecord
	ClaimUpgrade *ClaimUpgradeRecord
	Heartbeat    *HeartbeatRecord
}

// At returns the record's timestamp, which retention and ordering both use.
func (r Record) At() time.Time {
	switch {
	case r.Birth != nil:
		return r.Birth.ObservedAt
	case r.OwnerExit != nil:
		return r.OwnerExit.At
	case r.Transition != nil:
		return r.Transition.At
	case r.ClaimUpgrade != nil:
		return r.ClaimUpgrade.At
	case r.Heartbeat != nil:
		return r.Heartbeat.At
	default:
		return time.Time{}
	}
}

// Key returns the process this record is about, and false for records that are
// about the store rather than about one process.
func (r Record) Key() (ProcKey, bool) {
	switch {
	case r.Birth != nil:
		return r.Birth.Key, true
	case r.Transition != nil:
		return r.Transition.Key, true
	case r.ClaimUpgrade != nil:
		return r.ClaimUpgrade.Key, true
	case r.OwnerExit != nil:
		return r.OwnerExit.RootKey, true
	default:
		return ProcKey{}, false
	}
}

// recordEnvelope reads only the fields every record shares, so the loader can
// check the schema version and dispatch without guessing at the rest.
type recordEnvelope struct {
	V    int        `json:"v"`
	Type RecordType `json:"type"`
}

// errUnknownRecordType marks a record shape this binary does not know. It is
// skipped rather than treated as corruption, so a newer writer's additions do
// not discard the records after them.
var errUnknownRecordType = fmt.Errorf("unknown record type")

// decodeRecord parses one journal line.
func decodeRecord(line []byte) (Record, error) {
	var env recordEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		return Record{}, fmt.Errorf("decoding record envelope: %w", err)
	}
	if env.V != RecordSchemaVersion {
		return Record{}, fmt.Errorf("%w: record version %d", ErrUnknownSchemaVersion, env.V)
	}

	rec := Record{Type: env.Type}
	switch env.Type {
	case RecordBirth:
		rec.Birth = &BirthRecord{}
		return rec, unmarshalInto(line, rec.Birth)
	case RecordOwnerExit:
		rec.OwnerExit = &OwnerExitRecord{}
		return rec, unmarshalInto(line, rec.OwnerExit)
	case RecordTransition:
		rec.Transition = &TransitionRecord{}
		return rec, unmarshalInto(line, rec.Transition)
	case RecordClaimUpgrade:
		rec.ClaimUpgrade = &ClaimUpgradeRecord{}
		return rec, unmarshalInto(line, rec.ClaimUpgrade)
	case RecordHeartbeat:
		rec.Heartbeat = &HeartbeatRecord{}
		return rec, unmarshalInto(line, rec.Heartbeat)
	default:
		return Record{}, fmt.Errorf("%w: %q", errUnknownRecordType, env.Type)
	}
}

// normalizeKey puts a key in the stored form, so a key written now compares
// with a key written by any other path.
func normalizeKey(k ProcKey) ProcKey {
	k.StartTime = NormalizeTime(k.StartTime)
	return k
}

func unmarshalInto(line []byte, target any) error {
	if err := json.Unmarshal(line, target); err != nil {
		return fmt.Errorf("decoding record body: %w", err)
	}
	return nil
}

// encodeRecord serializes a record for the journal, stamping the envelope so a
// caller cannot write one without a version and a type.
func encodeRecord(rec Record) ([]byte, error) {
	switch {
	case rec.Birth != nil:
		rec.Birth.V, rec.Birth.Type = RecordSchemaVersion, RecordBirth
		rec.Birth.ObservedAt = NormalizeTime(rec.Birth.ObservedAt)
		rec.Birth.Key = normalizeKey(rec.Birth.Key)
		rec.Birth.ParentKey = normalizeKey(rec.Birth.ParentKey)
		rec.Birth.Owner.RootKey = normalizeKey(rec.Birth.Owner.RootKey)
		// The contract writes empty lists rather than nulls, so a reader never
		// has to tell one from the other.
		if rec.Birth.Unverifiable == nil {
			rec.Birth.Unverifiable = []string{}
		}
		if rec.Birth.Owner.Channels == nil {
			rec.Birth.Owner.Channels = []Channel{}
		}
		return json.Marshal(rec.Birth)
	case rec.OwnerExit != nil:
		rec.OwnerExit.V, rec.OwnerExit.Type = RecordSchemaVersion, RecordOwnerExit
		rec.OwnerExit.At = NormalizeTime(rec.OwnerExit.At)
		rec.OwnerExit.RootKey = normalizeKey(rec.OwnerExit.RootKey)
		return json.Marshal(rec.OwnerExit)
	case rec.Transition != nil:
		rec.Transition.V, rec.Transition.Type = RecordSchemaVersion, RecordTransition
		rec.Transition.At = NormalizeTime(rec.Transition.At)
		rec.Transition.Key = normalizeKey(rec.Transition.Key)
		return json.Marshal(rec.Transition)
	case rec.ClaimUpgrade != nil:
		rec.ClaimUpgrade.V, rec.ClaimUpgrade.Type = RecordSchemaVersion, RecordClaimUpgrade
		rec.ClaimUpgrade.At = NormalizeTime(rec.ClaimUpgrade.At)
		rec.ClaimUpgrade.Key = normalizeKey(rec.ClaimUpgrade.Key)
		rec.ClaimUpgrade.Evidence.RootKey = normalizeKey(rec.ClaimUpgrade.Evidence.RootKey)
		return json.Marshal(rec.ClaimUpgrade)
	case rec.Heartbeat != nil:
		rec.Heartbeat.V, rec.Heartbeat.Type = RecordSchemaVersion, RecordHeartbeat
		rec.Heartbeat.At = NormalizeTime(rec.Heartbeat.At)
		return json.Marshal(rec.Heartbeat)
	default:
		return nil, fmt.Errorf("record holds no body")
	}
}
