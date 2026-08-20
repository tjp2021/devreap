package attribution

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Store defaults. The journal rotates at 4 megabyte segments and keeps 8 of
// them, which is the 32 megabyte ceiling. Against the measured budget of about
// 3 megabytes a day a segment fills roughly every 32 hours, so ordinary use
// rotates about 5 times across a 7 day window and the mechanism is exercised
// without contriving it.
const (
	DefaultSegmentSize = 4 << 20
	DefaultMaxSegments = 8

	// StoreDirMode and StoreFileMode are owner-only. A birth record holds
	// command lines and repository paths even after redaction, so the store is
	// stricter than the log directory and carries its own rotation rather than
	// reusing the logger's.
	StoreDirMode  os.FileMode = 0o700
	StoreFileMode os.FileMode = 0o600

	journalName  = "journal.jsonl"
	snapshotName = "snapshot.json"

	// maxRecordLine bounds one journal line while loading.
	maxRecordLine = 1 << 20
)

// Retention floors by record type. A floor is the age before eviction is
// permitted, not a deletion deadline. The 8 day floor gives the 7 day exit
// criteria a day of margin, so a measurement window never ends against the edge
// of retention.
const (
	ExitedBirthRetention = 7 * 24 * time.Hour
	RecordRetentionFloor = 8 * 24 * time.Hour
)

// Store errors.
var (
	// ErrUnknownSchemaVersion means the store holds a version this binary does
	// not recognize. The store is ignored entirely rather than guessed at.
	ErrUnknownSchemaVersion = errors.New("attribution: unrecognized store schema version")
)

// Finding kinds raised by the store.
const (
	// FindingStoreCeilingExceeded means compaction could not bring the journal
	// under the ceiling without evicting a record inside its retention floor,
	// so it evicted nothing further. The ceiling is enforced by compaction
	// rather than absolute, and the condition ends when the floor passes.
	FindingStoreCeilingExceeded = "store_ceiling_exceeded"
	// FindingStoreTornTail means the loader discarded an unparseable tail and
	// kept the valid prefix.
	FindingStoreTornTail = "store_torn_tail"
	// FindingStoreUnknownRecord means a record shape this binary does not know
	// was skipped, leaving the records after it intact.
	FindingStoreUnknownRecord = "store_unknown_record"
	// FindingSnapshotIgnored means the snapshot could not be used, so recovery
	// replays the journal alone.
	FindingSnapshotIgnored = "snapshot_ignored"
)

// StoreConfig configures a Store. Every field has a working default.
type StoreConfig struct {
	// Dir is the store directory. It is created at mode 0700.
	Dir string
	// SegmentSize is the size one journal segment reaches before rotation.
	SegmentSize int64
	// MaxSegments is the segment count the ceiling is built from, counting the
	// active segment.
	MaxSegments int
	// Now injects the clock, which the retention tests drive.
	Now func() time.Time
	// Live reports whether a process is still running. A record belonging to a
	// live process is never evicted. When nil, every process is treated as live,
	// which evicts nothing and is the safe direction.
	Live func(ProcKey) bool
}

// Store is the append-only journal, its snapshot, and its own rotation.
//
// The shape beats an embedded database for this workload: one writer, one
// reader, and a fixed set of queries. An append-only file is crash-safe by
// construction, because a torn final line is discarded at load and every record
// before it stands. The condition that would reverse the decision is a second
// writer process or ad-hoc historical queries.
//
// The store enforces its ceiling through compaction. Rotation is a size guard
// that closes a full segment and opens the next one, and it never chooses which
// records to drop. When every record in the oldest segment is inside its
// retention floor, the ceiling is exceeded transiently and a finding says so.
type Store struct {
	mu           sync.Mutex
	dir          string
	journalPath  string
	snapshotPath string
	segmentSize  int64
	maxSegments  int
	now          func() time.Time
	live         func(ProcKey) bool

	file     *os.File
	size     int64
	findings []Finding

	// compacting guards the one recursion this design allows: compaction
	// rewrites the journal, a rewrite rotates, and a rotation would otherwise
	// compact again.
	compacting bool
}

// OpenStore creates or opens the store under dir.
func OpenStore(cfg StoreConfig) (*Store, error) {
	if cfg.Dir == "" {
		return nil, fmt.Errorf("attribution store: no directory configured")
	}
	if cfg.SegmentSize <= 0 {
		cfg.SegmentSize = DefaultSegmentSize
	}
	if cfg.MaxSegments <= 0 {
		cfg.MaxSegments = DefaultMaxSegments
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	if err := os.MkdirAll(cfg.Dir, StoreDirMode); err != nil {
		return nil, fmt.Errorf("creating store directory: %w", err)
	}
	// MkdirAll leaves an existing directory's mode alone, and umask can only
	// remove bits from a new one. Set the mode explicitly either way.
	if err := os.Chmod(cfg.Dir, StoreDirMode); err != nil {
		return nil, fmt.Errorf("securing store directory: %w", err)
	}

	s := &Store{
		dir:          cfg.Dir,
		journalPath:  filepath.Join(cfg.Dir, journalName),
		snapshotPath: filepath.Join(cfg.Dir, snapshotName),
		segmentSize:  cfg.SegmentSize,
		maxSegments:  cfg.MaxSegments,
		now:          cfg.Now,
		live:         cfg.Live,
	}
	if err := s.openJournal(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) openJournal() error {
	f, err := os.OpenFile(s.journalPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, StoreFileMode)
	if err != nil {
		return fmt.Errorf("opening journal: %w", err)
	}
	if err := f.Chmod(StoreFileMode); err != nil {
		f.Close()
		return fmt.Errorf("securing journal: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("measuring journal: %w", err)
	}
	s.file = f
	s.size = info.Size()
	return nil
}

// Close releases the journal file.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

// Dir returns the store directory.
func (s *Store) Dir() string { return s.dir }

// Ceiling returns the size compaction holds the journal under.
func (s *Store) Ceiling() int64 { return s.segmentSize * int64(s.maxSegments) }

// Findings returns the conditions doctor should report.
func (s *Store) Findings() []Finding {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Finding, len(s.findings))
	copy(out, s.findings)
	return out
}

func (s *Store) addFinding(kind, detail string) {
	s.findings = append(s.findings, Finding{Kind: kind, Detail: detail})
}

// AppendBirth writes a birth record. The record is written once and never
// modified: a later claim upgrade is a separate record.
func (s *Store) AppendBirth(r BirthRecord) error {
	return s.append(Record{Birth: &r})
}

// AppendOwnerExit writes an owner exit event.
func (s *Store) AppendOwnerExit(r OwnerExitRecord) error {
	return s.append(Record{OwnerExit: &r})
}

// AppendTransition writes a lifecycle transition with its accumulators.
func (s *Store) AppendTransition(r TransitionRecord) error {
	return s.append(Record{Transition: &r})
}

// AppendClaimUpgrade writes a claim upgrade alongside the untouched birth
// record it amends.
func (s *Store) AppendClaimUpgrade(r ClaimUpgradeRecord) error {
	return s.append(Record{ClaimUpgrade: &r})
}

// AppendHeartbeat writes one minute of watcher counters.
func (s *Store) AppendHeartbeat(r HeartbeatRecord) error {
	return s.append(Record{Heartbeat: &r})
}

func (s *Store) append(rec Record) error {
	line, err := encodeRecord(rec)
	if err != nil {
		return fmt.Errorf("encoding record: %w", err)
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLineLocked(line)
}

func (s *Store) writeLineLocked(line []byte) error {
	if s.file == nil {
		return fmt.Errorf("attribution store is closed")
	}
	if s.size > 0 && s.size+int64(len(line)) > s.segmentSize {
		if err := s.rotateLocked(); err != nil {
			return err
		}
	}
	n, err := s.file.Write(line)
	s.size += int64(n)
	if err != nil {
		return fmt.Errorf("writing record: %w", err)
	}
	return nil
}

// rotateLocked closes the full segment and opens the next one. It is a size
// guard and nothing else: it never decides which records to drop. When the shift
// pushes a segment past the configured count, compaction runs, and whatever
// compaction may not evict stays on disk with a finding.
func (s *Store) rotateLocked() error {
	if err := s.file.Close(); err != nil {
		return fmt.Errorf("closing journal for rotation: %w", err)
	}
	s.file = nil

	// Shift from the highest segment that exists rather than from the
	// configured count. Starting at the count would rename a segment onto an
	// existing one and lose it, and rotation is a size guard that must never
	// drop a record.
	for i := s.highestSegmentIndex(); i >= 1; i-- {
		from := s.segmentPath(i)
		if _, err := os.Stat(from); err != nil {
			continue
		}
		if err := os.Rename(from, s.segmentPath(i+1)); err != nil {
			return fmt.Errorf("rotating segment %d: %w", i, err)
		}
	}
	if err := os.Rename(s.journalPath, s.segmentPath(1)); err != nil {
		return fmt.Errorf("rotating journal: %w", err)
	}
	if err := s.openJournal(); err != nil {
		return err
	}

	if s.overflowSegments() > 0 && !s.compacting {
		if _, err := s.compactLocked(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) segmentPath(index int) string {
	return fmt.Sprintf("%s.%d", s.journalPath, index)
}

// overflowSegments counts rotated segments past the configured count. A rotated
// segment numbered at or above the count means the store holds more files than
// the ceiling allows, because the active segment takes one of the places.
func (s *Store) overflowSegments() int {
	count := 0
	for _, path := range s.segmentPaths() {
		if index := segmentIndex(path); index >= s.maxSegments {
			count++
		}
	}
	return count
}

func (s *Store) highestSegmentIndex() int {
	highest := 0
	for _, path := range s.segmentPaths() {
		if index := segmentIndex(path); index > highest {
			highest = index
		}
	}
	return highest
}

// compactionTarget is the size compaction evicts down to. It is one segment
// below the ceiling, so the active segment has room to fill before the next
// rotation, and the total stays under the ceiling the whole time rather than
// sitting on it. A single-segment store has no room to reserve and targets the
// ceiling itself.
func (s *Store) compactionTarget() int64 {
	if s.maxSegments <= 1 {
		return s.segmentSize
	}
	return s.segmentSize * int64(s.maxSegments-1)
}

// segmentPaths returns every journal file, oldest first, with the active
// segment last.
func (s *Store) segmentPaths() []string {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var rotated []string
	active := false
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		switch {
		case name == journalName:
			active = true
		case strings.HasPrefix(name, journalName+"."):
			rotated = append(rotated, filepath.Join(s.dir, name))
		}
	}
	sort.Slice(rotated, func(i, j int) bool {
		return segmentIndex(rotated[i]) > segmentIndex(rotated[j])
	})
	if active {
		rotated = append(rotated, s.journalPath)
	}
	return rotated
}

func segmentIndex(path string) int {
	dot := strings.LastIndex(path, ".")
	if dot < 0 {
		return 0
	}
	index, err := strconv.Atoi(path[dot+1:])
	if err != nil {
		return 0
	}
	return index
}

// LoadResult is what a restart reads back.
type LoadResult struct {
	Records  []Record
	Segments int
	Bytes    int64
	// TornRecords counts lines discarded from the unparseable tail.
	TornRecords int
	// SkippedRecords counts records of a shape this binary does not know.
	SkippedRecords int
	Findings       []Finding
}

// Load reads every segment oldest first.
//
// A torn final line is discarded and every record before it stands, which is
// what makes an append-only file crash-safe. The first line that will not parse
// ends the load: the valid prefix is kept and everything after it is dropped,
// because a file that is corrupt in the middle has no trustworthy remainder.
//
// A store holding an unrecognized schema version is ignored entirely rather
// than guessed at, and the caller starts with an empty store.
func (s *Store) Load() (*LoadResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Store) loadLocked() (*LoadResult, error) {
	result := &LoadResult{}
	paths := s.segmentPaths()

	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		result.Segments++
		result.Bytes += info.Size()
	}

	for _, path := range paths {
		torn, err := s.loadSegment(path, result)
		if err != nil {
			return nil, err
		}
		if torn {
			// The remainder of the store is not trustworthy, so the loader stops
			// here and keeps the valid prefix.
			break
		}
	}
	return result, nil
}

func (s *Store) loadSegment(path string, result *LoadResult) (torn bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxRecordLine)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		rec, decodeErr := decodeRecord(line)
		switch {
		case decodeErr == nil:
			result.Records = append(result.Records, rec)
		case errors.Is(decodeErr, ErrUnknownSchemaVersion):
			return false, fmt.Errorf("%w in %s", ErrUnknownSchemaVersion, filepath.Base(path))
		case errors.Is(decodeErr, errUnknownRecordType):
			result.SkippedRecords++
			result.Findings = append(result.Findings, Finding{
				Kind:   FindingStoreUnknownRecord,
				Detail: fmt.Sprintf("%s: %v; the record was skipped", filepath.Base(path), decodeErr),
			})
		default:
			result.TornRecords++
			result.Findings = append(result.Findings, Finding{
				Kind:   FindingStoreTornTail,
				Detail: fmt.Sprintf("%s: %v; the valid prefix was kept", filepath.Base(path), decodeErr),
			})
			return true, nil
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		result.TornRecords++
		result.Findings = append(result.Findings, Finding{
			Kind:   FindingStoreTornTail,
			Detail: fmt.Sprintf("%s: %v; the valid prefix was kept", filepath.Base(path), scanErr),
		})
		return true, nil
	}
	return false, nil
}

// WriteSnapshot writes the compacted state, so a restart replays only the
// journal tail. It writes to a temporary file and renames, so a crash during
// the write leaves the previous snapshot intact.
func (s *Store) WriteSnapshot(snapshot StoreSnapshot) error {
	snapshot.V = RecordSchemaVersion
	snapshot.Type = RecordSnapshot
	snapshot.WrittenAt = NormalizeTime(s.now())

	data, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encoding snapshot: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tmp := s.snapshotPath + ".tmp"
	if err := os.WriteFile(tmp, data, StoreFileMode); err != nil {
		return fmt.Errorf("writing snapshot: %w", err)
	}
	if err := os.Chmod(tmp, StoreFileMode); err != nil {
		return fmt.Errorf("securing snapshot: %w", err)
	}
	if err := os.Rename(tmp, s.snapshotPath); err != nil {
		return fmt.Errorf("replacing snapshot: %w", err)
	}
	return nil
}

// ReadSnapshot reads the last snapshot. A missing, unreadable, or unrecognized
// snapshot is not an error: recovery falls back to the journal alone, and a
// finding records why.
func (s *Store) ReadSnapshot() (*StoreSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.snapshotPath)
	if err != nil {
		if !os.IsNotExist(err) {
			s.addFinding(FindingSnapshotIgnored, fmt.Sprintf("reading snapshot: %v", err))
		}
		return nil, false
	}

	var snapshot StoreSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		s.addFinding(FindingSnapshotIgnored, fmt.Sprintf("parsing snapshot: %v", err))
		return nil, false
	}
	if snapshot.V != RecordSchemaVersion {
		s.addFinding(FindingSnapshotIgnored, fmt.Sprintf("snapshot version %d is not %d", snapshot.V, RecordSchemaVersion))
		return nil, false
	}
	return &snapshot, true
}

// CompactResult reports what one compaction did.
type CompactResult struct {
	BytesBefore int64
	BytesAfter  int64
	Kept        int
	Evicted     map[RecordType]int
	// CeilingExceeded means compaction stopped short of the ceiling because
	// every remaining candidate was inside its retention floor.
	CeilingExceeded bool
	Findings        []Finding
}

// Compact enforces the ceiling through type-aware eviction.
//
// The order is fixed: exited-process births first, oldest first, then owner exit
// records past their floor, then heartbeats past their floor, then transitions
// and claim upgrades past theirs. A record belonging to a live process is never
// evicted. If eviction would have to touch a record younger than its floor, the
// store evicts nothing further and raises a finding, because silently dropping
// the measurement series would invalidate the coverage number rather than merely
// shrink the file.
func (s *Store) Compact(live func(ProcKey) bool) (CompactResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if live != nil {
		s.live = live
	}
	return s.compactLocked()
}

func (s *Store) compactLocked() (CompactResult, error) {
	result := CompactResult{Evicted: make(map[RecordType]int)}

	s.compacting = true
	defer func() { s.compacting = false }()

	load, err := s.loadLocked()
	if err != nil {
		return result, err
	}
	result.BytesBefore = load.Bytes

	lines := make([][]byte, len(load.Records))
	sizes := make([]int64, len(load.Records))
	var total int64
	for i, rec := range load.Records {
		line, encErr := encodeRecord(rec)
		if encErr != nil {
			return result, fmt.Errorf("re-encoding record during compaction: %w", encErr)
		}
		line = append(line, '\n')
		lines[i] = line
		sizes[i] = int64(len(line))
		total += sizes[i]
	}

	ceiling := s.Ceiling()
	target := s.compactionTarget()
	evicted := make([]bool, len(load.Records))

	if total > target {
		for _, group := range s.evictionGroups(load.Records) {
			for _, idx := range group {
				if total <= target {
					break
				}
				evicted[idx] = true
				total -= sizes[idx]
				result.Evicted[load.Records[idx].Type]++
			}
			if total <= target {
				break
			}
		}
	}

	if total > ceiling {
		result.CeilingExceeded = true
		finding := Finding{
			Kind: FindingStoreCeilingExceeded,
			Detail: fmt.Sprintf("journal holds %d bytes against a %d byte ceiling; every remaining record is inside its retention floor or belongs to a live process",
				total, ceiling),
		}
		result.Findings = append(result.Findings, finding)
		s.findings = append(s.findings, finding)
	}

	kept := make([][]byte, 0, len(lines))
	for i, line := range lines {
		if evicted[i] {
			continue
		}
		kept = append(kept, line)
	}
	result.Kept = len(kept)
	result.BytesAfter = total

	if err := s.rewriteLocked(kept); err != nil {
		return result, err
	}
	return result, nil
}

// evictionGroups returns candidate record indexes in the fixed eviction order,
// each group oldest first. Only records past their retention floor appear, and a
// birth belonging to a live process never does.
func (s *Store) evictionGroups(records []Record) [][]int {
	now := s.now()
	live := s.live
	if live == nil {
		// With no liveness oracle every process counts as live, so no birth
		// record is evictable. That is the safe direction.
		live = func(ProcKey) bool { return true }
	}

	var exitedBirths, ownerExits, heartbeats, audit []int
	for i, rec := range records {
		age := now.Sub(rec.At())
		switch rec.Type {
		case RecordBirth:
			if !live(rec.Birth.Key) && age >= ExitedBirthRetention {
				exitedBirths = append(exitedBirths, i)
			}
		case RecordOwnerExit:
			if age >= RecordRetentionFloor {
				ownerExits = append(ownerExits, i)
			}
		case RecordHeartbeat:
			if age >= RecordRetentionFloor {
				heartbeats = append(heartbeats, i)
			}
		case RecordTransition, RecordClaimUpgrade:
			if age >= RecordRetentionFloor {
				audit = append(audit, i)
			}
		}
	}

	groups := [][]int{exitedBirths, ownerExits, heartbeats, audit}
	for _, group := range groups {
		sort.SliceStable(group, func(a, b int) bool {
			return records[group[a]].At().Before(records[group[b]].At())
		})
	}
	return groups
}

// rewriteLocked lays the kept records back out into fresh segments.
func (s *Store) rewriteLocked(lines [][]byte) error {
	if s.file != nil {
		if err := s.file.Close(); err != nil {
			return fmt.Errorf("closing journal for compaction: %w", err)
		}
		s.file = nil
	}
	for _, path := range s.segmentPaths() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing %s during compaction: %w", path, err)
		}
	}
	if err := s.openJournal(); err != nil {
		return err
	}
	for _, line := range lines {
		if err := s.writeLineLocked(line); err != nil {
			return err
		}
	}
	return nil
}

// Size reports the number of bytes the journal occupies across all segments.
func (s *Store) Size() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	var total int64
	for _, path := range s.segmentPaths() {
		if info, err := os.Stat(path); err == nil {
			total += info.Size()
		}
	}
	return total
}

// SegmentCount reports how many journal files exist, counting the active one.
func (s *Store) SegmentCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.segmentPaths())
}
