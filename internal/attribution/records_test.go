package attribution

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("parsing %q: %v", value, err)
	}
	return parsed
}

func decodeToMap(t *testing.T, rec Record) map[string]any {
	t.Helper()
	line, err := encodeRecord(rec)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	return got
}

// TestBirthRecordMatchesTheContract asserts the written record has exactly the
// fields the design specifies, with the timestamps in the stated form. A record
// contract that drifts silently is a store nobody can read back.
func TestBirthRecordMatchesTheContract(t *testing.T) {
	birth := BirthRecord{
		ObservedAt: mustTime(t, "2026-08-20T05:24:11.412Z"),
		Source:     BirthSourcePoll,
		Key:        NewProcKey(98925, mustTime(t, "2026-08-19T08:20:54.113Z")),
		ParentKey:  NewProcKey(98888, mustTime(t, "2026-08-19T08:20:53.902Z")),
		PGID:       98888,
		TTY:        "ttys000",
		Name:       "node",
		Exe:        "/home/dev/.local/bin/node",
		Cmdline:    "node ./server.js --port 7333",
		Class:      "mcp",
		Owner: OwnershipClaim{
			SessionID:  "5f1c9a2e",
			Harness:    "claude-code-cli",
			Repo:       "/home/dev/projects/example",
			RootKey:    NewProcKey(98888, mustTime(t, "2026-08-19T08:20:53.902Z")),
			Confidence: ConfidenceObserved,
			Channels:   []Channel{ChannelWatchedAncestry, ChannelEnv, ChannelPGID},
			LinkDepth:  1,
		},
	}

	want := map[string]any{
		"v":           float64(1),
		"type":        "birth",
		"observed_at": "2026-08-20T05:24:11.412Z",
		"source":      "poll",
		"key":         map[string]any{"pid": float64(98925), "start_time": "2026-08-19T08:20:54.113Z"},
		"parent_key":  map[string]any{"pid": float64(98888), "start_time": "2026-08-19T08:20:53.902Z"},
		"pgid":        float64(98888),
		"tty":         "ttys000",
		"name":        "node",
		"exe":         "/home/dev/.local/bin/node",
		"cmdline":     "node ./server.js --port 7333",
		"class":       "mcp",
		"owner": map[string]any{
			"session_id": "5f1c9a2e",
			"harness":    "claude-code-cli",
			"repo":       "/home/dev/projects/example",
			"root_key":   map[string]any{"pid": float64(98888), "start_time": "2026-08-19T08:20:53.902Z"},
			"confidence": "observed",
			"channels":   []any{"watched_ancestry", "env", "pgid"},
			"link_depth": float64(1),
		},
		"unverifiable": []any{},
	}

	if got := decodeToMap(t, Record{Birth: &birth}); !reflect.DeepEqual(got, want) {
		t.Errorf("birth record does not match the contract\n got: %#v\nwant: %#v", got, want)
	}
}

func TestOwnerExitRecordMatchesTheContract(t *testing.T) {
	exit := OwnerExitRecord{
		At:            mustTime(t, "2026-08-20T05:31:02.004Z"),
		SessionID:     "5f1c9a2e",
		Harness:       "claude-code-cli",
		RootKey:       NewProcKey(98888, mustTime(t, "2026-08-19T08:20:53.902Z")),
		Source:        ExitSourceKqueue,
		MembersAlive:  11,
		RSSAliveBytes: 4512345678,
	}

	want := map[string]any{
		"v":               float64(1),
		"type":            "owner_exit",
		"at":              "2026-08-20T05:31:02.004Z",
		"session_id":      "5f1c9a2e",
		"harness":         "claude-code-cli",
		"root_key":        map[string]any{"pid": float64(98888), "start_time": "2026-08-19T08:20:53.902Z"},
		"source":          "kqueue_note_exit",
		"members_alive":   float64(11),
		"rss_alive_bytes": float64(4512345678),
	}

	if got := decodeToMap(t, Record{OwnerExit: &exit}); !reflect.DeepEqual(got, want) {
		t.Errorf("owner exit record does not match the contract\n got: %#v\nwant: %#v", got, want)
	}
}

func TestClaimUpgradeRecordMatchesTheContract(t *testing.T) {
	upgrade := ClaimUpgradeRecord{
		At:     mustTime(t, "2026-08-20T06:02:44.118Z"),
		Key:    NewProcKey(98925, mustTime(t, "2026-08-19T08:20:54.113Z")),
		From:   ConfidenceInferred,
		To:     ConfidenceObserved,
		Reason: UpgradeAncestryResolved,
		Evidence: ClaimUpgradeEvidence{
			RootKey:   NewProcKey(98888, mustTime(t, "2026-08-19T08:20:53.902Z")),
			LinkDepth: 2,
		},
	}

	want := map[string]any{
		"v":      float64(1),
		"type":   "claim_upgrade",
		"at":     "2026-08-20T06:02:44.118Z",
		"key":    map[string]any{"pid": float64(98925), "start_time": "2026-08-19T08:20:54.113Z"},
		"from":   "inferred",
		"to":     "observed",
		"reason": "ancestry_chain_resolved",
		"evidence": map[string]any{
			"root_key":   map[string]any{"pid": float64(98888), "start_time": "2026-08-19T08:20:53.902Z"},
			"link_depth": float64(2),
		},
	}

	if got := decodeToMap(t, Record{ClaimUpgrade: &upgrade}); !reflect.DeepEqual(got, want) {
		t.Errorf("claim upgrade record does not match the contract\n got: %#v\nwant: %#v", got, want)
	}
}

func TestTransitionRecordCarriesTheAccumulators(t *testing.T) {
	got := decodeToMap(t, Record{Transition: &TransitionRecord{
		At:            mustTime(t, "2026-08-20T06:10:00.000Z"),
		Key:           NewProcKey(98925, mustTime(t, "2026-08-19T08:20:54.113Z")),
		From:          StateGracePeriod,
		To:            StateOrphanCandidate,
		Trigger:       "window_reached",
		Evidence:      map[string]any{"class": "mcp"},
		Confirmations: 2,
		AwakeMillis:   300_000,
	}})

	for _, field := range []string{"v", "type", "at", "key", "from", "to", "trigger", "evidence", "confirmations", "awake_ms"} {
		if _, ok := got[field]; !ok {
			t.Errorf("transition record is missing %q: %#v", field, got)
		}
	}
	if got["confirmations"] != float64(2) || got["awake_ms"] != float64(300_000) {
		t.Errorf("accumulators did not survive encoding: %#v", got)
	}
	if _, present := got["reported"]; present {
		t.Error("the reported flag should be omitted when it is false")
	}
}

func TestHeartbeatRecordCarriesTheCoverageSeries(t *testing.T) {
	beat := HeartbeatRecord{
		At:                 mustTime(t, "2026-08-20T06:11:00.000Z"),
		Polls:              60,
		BirthsSeen:         4,
		BirthsPersisted:    1,
		EnvReadFailures:    0,
		SleepGapMillis:     0,
		PollDurationMicros: 14_200,
		Tracked:            96,
		Attributed:         90,
		Upgraded:           2,
		JournalBytes:       1024,
	}
	got := decodeToMap(t, Record{Heartbeat: &beat})
	for _, field := range []string{
		"polls", "births_seen", "births_persisted", "env_read_failures",
		"sleep_gap_ms", "poll_duration_us", "tracked", "attributed", "upgraded", "journal_bytes",
	} {
		if _, ok := got[field]; !ok {
			t.Errorf("heartbeat is missing %q: %#v", field, got)
		}
	}
	if coverage := beat.Coverage(); coverage < 0.937 || coverage > 0.938 {
		t.Errorf("coverage: got %f, want 90/96", coverage)
	}
	if (HeartbeatRecord{}).Coverage() != 0 {
		t.Error("coverage over nothing tracked should be zero rather than undefined")
	}
}

func TestRecordRoundTrip(t *testing.T) {
	original := BirthRecord{
		ObservedAt: mustTime(t, "2026-08-20T05:24:11.412Z"),
		Source:     BirthSourcePoll,
		Key:        NewProcKey(98925, mustTime(t, "2026-08-19T08:20:54.113Z")),
		Name:       "node",
		Class:      "mcp",
		Owner:      OwnershipClaim{Confidence: ConfidenceObserved, SessionID: "5f1c9a2e"},
	}
	line, err := encodeRecord(Record{Birth: &original})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	rec, err := decodeRecord(line)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rec.Type != RecordBirth || rec.Birth == nil {
		t.Fatalf("decoded the wrong shape: %+v", rec)
	}
	if !rec.Birth.Key.Equal(original.Key) {
		t.Errorf("key: got %s, want %s", rec.Birth.Key, original.Key)
	}
	if !rec.At().Equal(original.ObservedAt) {
		t.Errorf("timestamp: got %s, want %s", rec.At(), original.ObservedAt)
	}
	if key, ok := rec.Key(); !ok || !key.Equal(original.Key) {
		t.Errorf("record key: got %s ok=%v", key, ok)
	}
}

func TestDecodeRejectsUnknownVersionAndType(t *testing.T) {
	if _, err := decodeRecord([]byte(`{"v":99,"type":"birth"}`)); !errors.Is(err, ErrUnknownSchemaVersion) {
		t.Errorf("unknown version: got %v, want ErrUnknownSchemaVersion", err)
	}
	if _, err := decodeRecord([]byte(`{"v":1,"type":"future_shape"}`)); !errors.Is(err, errUnknownRecordType) {
		t.Errorf("unknown type: got %v, want errUnknownRecordType", err)
	}
	if _, err := decodeRecord([]byte(`{"v":1,"type":"birth"`)); err == nil {
		t.Error("a torn line decoded without error")
	}
}

func TestBirthRecordActionability(t *testing.T) {
	verifiable := BirthRecord{
		Key:   NewProcKey(98925, mustTime(t, "2026-08-19T08:20:54.113Z")),
		Owner: OwnershipClaim{Confidence: ConfidenceObserved},
	}
	if !verifiable.Actionable() {
		t.Error("an observed claim with a verifiable key should be actionable")
	}

	inferred := verifiable
	inferred.Owner.Confidence = ConfidenceInferred
	if inferred.Actionable() {
		t.Error("an inferred claim must never be actionable")
	}

	unreadable := verifiable
	unreadable.Unverifiable = []string{UnverifiableStartTime}
	if unreadable.Actionable() {
		t.Error("a record naming start_time as unverifiable must never be actionable")
	}

	noKey := verifiable
	noKey.Key = ProcKey{PID: 98925}
	if noKey.Actionable() {
		t.Error("a key with no start time must never be actionable")
	}
}

func TestExitSourceTrust(t *testing.T) {
	if !ExitSourceKqueue.Trusted() || !ExitSourcePollAbsent.Trusted() {
		t.Error("the two observed sources must be trusted")
	}
	if ExitSourceAgentHook.Trusted() {
		t.Error("the agent hook is enrichment and must never be trusted for eligibility")
	}
}
