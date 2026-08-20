package attribution

import (
	"fmt"
	"sort"
	"time"
)

// MaxLinkDepth bounds every ancestry walk in this package. Beyond it a walk
// stops and the process is left unattributed rather than joined to a distant
// session.
const MaxLinkDepth = 32

// NormalizeTime puts a timestamp in the one form records use: UTC, truncated to
// the millisecond. Every record in the journal is written through this, so a
// stored key compares byte for byte with the key that produced it.
func NormalizeTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	return t.UTC().Truncate(time.Millisecond)
}

// ProcKey identifies a process as the pair of its identifier and its start
// time. The identifier alone is not an identity: on this machine PID 99978 and
// PID 117 started one second apart, so the counter wrapped and reused low
// numbers inside a single second.
//
// A key whose start time could not be read is unverifiable. It stays visible
// and it can never gate an action.
type ProcKey struct {
	PID       int32     `json:"pid"`
	StartTime time.Time `json:"start_time"`
}

// NewProcKey builds a key with the start time normalized.
func NewProcKey(pid int32, startTime time.Time) ProcKey {
	return ProcKey{PID: pid, StartTime: NormalizeTime(startTime)}
}

// Verifiable reports whether the start time is known. An unverifiable key can
// be displayed and can never gate an action.
func (k ProcKey) Verifiable() bool { return !k.StartTime.IsZero() }

// Zero reports whether the key names no process at all.
func (k ProcKey) Zero() bool { return k.PID == 0 && k.StartTime.IsZero() }

// Equal compares two keys and fails closed. Two keys are equal only when both
// start times are known and identical, so a lookup that cannot compare start
// times returns no match rather than a guess.
func (k ProcKey) Equal(other ProcKey) bool {
	if !k.Verifiable() || !other.Verifiable() {
		return false
	}
	return k.PID == other.PID && k.StartTime.Equal(other.StartTime)
}

// IndexKey is the string form used for map keys and journal indexes.
func (k ProcKey) IndexKey() string {
	if !k.Verifiable() {
		return fmt.Sprintf("%d@unknown", k.PID)
	}
	return fmt.Sprintf("%d@%s", k.PID, k.StartTime.UTC().Format(time.RFC3339Nano))
}

// String renders the key for a log line.
func (k ProcKey) String() string { return k.IndexKey() }

// ProcEntry is one row of the bulk process snapshot. Every field comes from the
// same kinfo_proc, so the parent identifier, the process group, the terminal,
// the start time, and the owner all describe the same instant.
//
// The *Known fields follow the convention the scanner already uses: a zero
// value is indistinguishable from a real answer, so a consumer must check
// whether the read succeeded before treating the value as evidence.
type ProcEntry struct {
	PID  int32
	PPID int32
	PGID int32

	// Name is the short process name the kernel keeps, which is truncated to
	// 16 characters and is not the executable path.
	Name string

	// TTY is the controlling terminal, empty when there is none.
	TTY string
	// TTYKnown separates "no controlling terminal" from "could not resolve the
	// device", which are different facts.
	TTYKnown bool

	StartTime time.Time
	// StartTimeKnown is false when the kernel reported no start time. Such a
	// process is unverifiable and can never gate an action.
	StartTimeKnown bool

	// UID owns the process. The reader refuses any process this does not match.
	UID uint32
}

// Key returns the identity of this process.
func (e ProcEntry) Key() ProcKey {
	if !e.StartTimeKnown {
		return ProcKey{PID: e.PID}
	}
	return NewProcKey(e.PID, e.StartTime)
}

// IsGroupLeader reports whether the process leads its own process group, which
// is one of the three conditions the generic session descriptor requires.
func (e ProcEntry) IsGroupLeader() bool { return e.PGID == e.PID }

// Snapshot is one poll of the process table, indexed for lookup and diffing.
// It is immutable once built.
type Snapshot struct {
	takenAt time.Time
	entries []ProcEntry
	byKey   map[string]ProcEntry
	byPID   map[int32]ProcEntry
}

// NewSnapshot indexes a set of entries taken at one instant.
func NewSnapshot(takenAt time.Time, entries []ProcEntry) *Snapshot {
	s := &Snapshot{
		takenAt: NormalizeTime(takenAt),
		entries: make([]ProcEntry, len(entries)),
		byKey:   make(map[string]ProcEntry, len(entries)),
		byPID:   make(map[int32]ProcEntry, len(entries)),
	}
	copy(s.entries, entries)
	sort.Slice(s.entries, func(i, j int) bool { return s.entries[i].PID < s.entries[j].PID })
	for _, e := range s.entries {
		s.byKey[e.Key().IndexKey()] = e
		s.byPID[e.PID] = e
	}
	return s
}

// TakenAt reports when the poll ran.
func (s *Snapshot) TakenAt() time.Time { return s.takenAt }

// Len reports how many processes the poll saw.
func (s *Snapshot) Len() int { return len(s.entries) }

// Entries returns the processes ordered by identifier.
func (s *Snapshot) Entries() []ProcEntry {
	out := make([]ProcEntry, len(s.entries))
	copy(out, s.entries)
	return out
}

// Lookup finds a process by identity. An unverifiable key never matches,
// because a lookup that cannot compare start times fails closed.
func (s *Snapshot) Lookup(key ProcKey) (ProcEntry, bool) {
	if !key.Verifiable() {
		return ProcEntry{}, false
	}
	e, ok := s.byKey[key.IndexKey()]
	return e, ok
}

// ByPID finds a process by identifier alone. It is for walking a parent chain
// within one snapshot, where identifiers are unique. It is never an identity
// check across snapshots: use Lookup for that.
func (s *Snapshot) ByPID(pid int32) (ProcEntry, bool) {
	e, ok := s.byPID[pid]
	return e, ok
}

// Diff reports which processes are new in this snapshot and which are gone
// since the previous one. Both answers compare keys rather than identifiers, so
// a reused identifier reads as one process ending and a different one starting
// rather than as the same process continuing.
//
// A nil previous snapshot yields nothing. On the first poll every process is
// pre-existing rather than newly born, and calling them births would record
// spawn links the watcher never witnessed.
func (s *Snapshot) Diff(prev *Snapshot) (born, exited []ProcEntry) {
	if prev == nil {
		return nil, nil
	}
	for _, e := range s.entries {
		if _, ok := prev.byKey[e.Key().IndexKey()]; !ok {
			born = append(born, e)
		}
	}
	for _, e := range prev.entries {
		if _, ok := s.byKey[e.Key().IndexKey()]; !ok {
			exited = append(exited, e)
		}
	}
	return born, exited
}

// Ancestors walks the parent chain from the given process outward, nearest
// first, and does not include the process itself. The walk stops at process 1,
// at a parent this snapshot does not hold, at maxDepth links, and at a cycle,
// because a bounded walk is the only kind this package performs.
func (s *Snapshot) Ancestors(pid int32, maxDepth int) []ProcEntry {
	if maxDepth <= 0 || maxDepth > MaxLinkDepth {
		maxDepth = MaxLinkDepth
	}
	start, ok := s.byPID[pid]
	if !ok {
		return nil
	}

	seen := map[int32]struct{}{pid: {}}
	var out []ProcEntry
	next := start.PPID
	for depth := 0; depth < maxDepth; depth++ {
		if next <= 1 {
			return out
		}
		if _, loop := seen[next]; loop {
			return out
		}
		parent, found := s.byPID[next]
		if !found {
			return out
		}
		seen[next] = struct{}{}
		out = append(out, parent)
		next = parent.PPID
	}
	return out
}

// Chain returns the process itself followed by its ancestors, which is the form
// root resolution walks.
func (s *Snapshot) Chain(pid int32, maxDepth int) []ProcEntry {
	self, ok := s.byPID[pid]
	if !ok {
		return nil
	}
	return append([]ProcEntry{self}, s.Ancestors(pid, maxDepth)...)
}
