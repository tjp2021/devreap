package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tjp2021/devreap/internal/attribution"
	"github.com/tjp2021/devreap/internal/config"
)

// liveChild starts a real process and returns its key, read from the same bulk
// snapshot the watcher uses. The read-only surfaces reconcile the store against
// the live machine, so a seeded record has to name a process that exists.
func liveChild(t *testing.T) attribution.ProcKey {
	t.Helper()

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the child: %v", err)
	}
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Signal(syscall.SIGKILL) })

	snapshot, err := attribution.SnapshotProcesses(time.Now())
	if err != nil {
		t.Skipf("bulk process snapshot unavailable on this platform: %v", err)
	}
	entry, found := snapshot.ByPID(int32(cmd.Process.Pid))
	if !found || !entry.StartTimeKnown {
		t.Fatalf("the child %d is not in the process table with a readable start time", cmd.Process.Pid)
	}
	return entry.Key()
}

// seedStore writes one session's records and returns the config path pointing at
// it, plus the session identifier.
func seedStore(t *testing.T) (cfgPath, sessionID string, memberKey attribution.ProcKey) {
	t.Helper()

	dir := t.TempDir()
	storeDir := filepath.Join(dir, "attribution")
	sessionID = "5f1c9a2e"

	rootKey := liveChild(t)
	memberKey = liveChild(t)

	store, err := attribution.OpenStore(attribution.StoreConfig{Dir: storeDir})
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().Add(-20 * time.Minute)
	redactor := attribution.NewRedactor()

	rootBirth := attribution.BirthRecord{
		ObservedAt: now,
		Source:     attribution.BirthSourcePoll,
		Key:        rootKey,
		PGID:       rootKey.PID,
		TTY:        "ttys000",
		Name:       "claude",
		Exe:        "/opt/homebrew/bin/claude",
		Cmdline:    "claude",
		Owner: attribution.OwnershipClaim{
			SessionID: sessionID, Harness: "claude-code-cli", Repo: "/Users/dev/projects/example",
			RootKey: rootKey, Confidence: attribution.ConfidenceObserved,
			Channels: []attribution.Channel{attribution.ChannelWatchedAncestry},
		},
	}
	if err := store.AppendBirth(rootBirth); err != nil {
		t.Fatalf("writing the root birth: %v", err)
	}

	// The command line goes through the same filter the watcher runs, so the
	// token below never reaches the record in the first place.
	secret := "--api-key xoxb-1234567890abcdefghij"
	memberBirth := attribution.BirthRecord{
		ObservedAt: now.Add(time.Second),
		Source:     attribution.BirthSourcePoll,
		Key:        memberKey,
		ParentKey:  rootKey,
		PGID:       rootKey.PID,
		TTY:        "ttys000",
		Name:       "node",
		Exe:        "/usr/local/bin/node",
		Cmdline:    redactor.CmdlineString("node ./server.js --port 7333 " + secret),
		Class:      config.ClassMCP,
		Owner: attribution.OwnershipClaim{
			SessionID: sessionID, Harness: "claude-code-cli", Repo: "/Users/dev/projects/example",
			RootKey: rootKey, Confidence: attribution.ConfidenceObserved,
			Channels:  []attribution.Channel{attribution.ChannelWatchedAncestry, attribution.ChannelPGID},
			LinkDepth: 1,
		},
	}
	if err := store.AppendBirth(memberBirth); err != nil {
		t.Fatalf("writing the member birth: %v", err)
	}

	exitAt := now.Add(8 * time.Minute)
	if err := store.AppendOwnerExit(attribution.OwnerExitRecord{
		At: exitAt, SessionID: sessionID, Harness: "claude-code-cli", RootKey: rootKey,
		Source: attribution.ExitSourceKqueue, MembersAlive: 1,
	}); err != nil {
		t.Fatalf("writing the owner exit: %v", err)
	}

	transitions := []attribution.TransitionRecord{
		{At: exitAt, Key: memberKey, SessionID: sessionID, From: attribution.StateActive,
			To: attribution.StateOwnerGone, Trigger: attribution.TriggerOwnerExit},
		{At: exitAt.Add(30 * time.Second), Key: memberKey, SessionID: sessionID,
			From: attribution.StateOwnerGone, To: attribution.StateGracePeriod,
			Trigger: attribution.TriggerFirstScanAfterExit},
		{At: exitAt.Add(6 * time.Minute), Key: memberKey, SessionID: sessionID,
			From: attribution.StateGracePeriod, To: attribution.StateOrphanCandidate,
			Trigger: attribution.TriggerWindowReached, AwakeMillis: 300_000, Confirmations: 1},
	}
	for _, transition := range transitions {
		if err := store.AppendTransition(transition); err != nil {
			t.Fatalf("writing a transition: %v", err)
		}
	}

	cfgPath = filepath.Join(dir, "config.yaml")
	contents := "attribution:\n  enabled: true\n  store_dir: " + storeDir + "\n"
	if err := os.WriteFile(cfgPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing the config: %v", err)
	}
	return cfgPath, sessionID, memberKey
}

// TestCLI_TopRendersSessions asserts the read-only view groups by session,
// totals memory, and says how long ago the owner exited.
func TestCLI_TopRendersSessions(t *testing.T) {
	cfgPath, sessionID, memberKey := seedStore(t)

	out, stderr, err := runDevreap(t, "--config", cfgPath, "top")
	if err != nil {
		t.Fatalf("top failed: %v\n%s", err, stderr)
	}

	for _, want := range []string{sessionID, "claude-code-cli", "exited", "ORPHAN_CANDIDATE"} {
		if !strings.Contains(out, want) {
			t.Errorf("top output is missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "node ./server.js") {
		t.Errorf("top output does not show the member command line:\n%s", out)
	}
	if !strings.Contains(out, "1/3") {
		t.Errorf("top output does not show the confirmation progress:\n%s", out)
	}
	if !strings.Contains(out, "takes no action") {
		t.Errorf("top output does not say it is read-only:\n%s", out)
	}
	_ = memberKey
}

// TestCLI_TopJSON asserts the machine-readable form carries the same view.
func TestCLI_TopJSON(t *testing.T) {
	cfgPath, sessionID, memberKey := seedStore(t)

	out, stderr, err := runDevreap(t, "--config", cfgPath, "top", "--json")
	if err != nil {
		t.Fatalf("top --json failed: %v\n%s", err, stderr)
	}

	var view attribution.View
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatalf("top --json emitted invalid JSON: %v\n%s", err, out)
	}
	if len(view.Sessions) != 1 {
		t.Fatalf("view holds %d sessions, want 1:\n%s", len(view.Sessions), out)
	}
	session := view.Sessions[0]
	if session.SessionID != sessionID {
		t.Errorf("session = %q, want %q", session.SessionID, sessionID)
	}
	if session.OwnerExitAt == nil {
		t.Error("the session does not carry an owner exit time")
	}
	// The root belongs to its own session, so the tree holds the root and the
	// member it spawned.
	if len(session.Processes) != 2 {
		t.Fatalf("session holds %d processes, want the root and its member:\n%s", len(session.Processes), out)
	}
	var member *attribution.ProcessView
	for i := range session.Processes {
		if session.Processes[i].Key.PID == memberKey.PID {
			member = &session.Processes[i]
		}
	}
	if member == nil {
		t.Fatalf("the member is missing from the session tree:\n%s", out)
	}
	if member.State != attribution.StateOrphanCandidate {
		t.Errorf("state = %s, want ORPHAN_CANDIDATE", member.State)
	}
	if member.LinkDepth != 1 {
		t.Errorf("link depth = %d, want 1", member.LinkDepth)
	}
	if session.RSSBytes == 0 {
		t.Error("the session reports no resident memory, so nothing was reconciled against the live machine")
	}

	// Only the member carries a pattern class, and coverage counts
	// pattern-matched processes.
	if view.Tracked != 1 || view.Attributed != 1 {
		t.Errorf("coverage counted %d of %d, want 1 of 1", view.Attributed, view.Tracked)
	}
	if view.Coverage != 1 {
		t.Errorf("coverage = %v, want 1", view.Coverage)
	}
}

// TestEvidenceExportRedacted asserts the exported document holds the spawn tree,
// the timings, the owner exit, and every transition, and that it carries nothing
// the redaction filter would have dropped.
func TestEvidenceExportRedacted(t *testing.T) {
	cfgPath, sessionID, memberKey := seedStore(t)

	out, stderr, err := runDevreap(t, "--config", cfgPath, "evidence", sessionID)
	if err != nil {
		t.Fatalf("evidence failed: %v\n%s", err, stderr)
	}

	var evidence attribution.Evidence
	if err := json.Unmarshal([]byte(out), &evidence); err != nil {
		t.Fatalf("evidence emitted invalid JSON: %v\n%s", err, out)
	}

	if evidence.Session.SessionID != sessionID {
		t.Errorf("session = %q, want %q", evidence.Session.SessionID, sessionID)
	}
	if evidence.Root == nil {
		t.Error("the export carries no root birth record")
	}
	if evidence.Exit == nil {
		t.Fatal("the export carries no owner exit event")
	}
	if evidence.Exit.Source != attribution.ExitSourceKqueue {
		t.Errorf("exit source = %q, want kqueue_note_exit", evidence.Exit.Source)
	}

	if len(evidence.SpawnTree) != 2 {
		t.Errorf("spawn tree holds %d records, want the root and the member", len(evidence.SpawnTree))
	}
	var member *attribution.BirthRecord
	for i := range evidence.SpawnTree {
		if evidence.SpawnTree[i].Key.PID == memberKey.PID {
			member = &evidence.SpawnTree[i]
		}
		if evidence.SpawnTree[i].ObservedAt.IsZero() {
			t.Error("a spawn tree entry carries no birth timing")
		}
	}
	if member == nil {
		t.Fatal("the member is missing from the spawn tree")
	}
	if member.Owner.LinkDepth != 1 {
		t.Errorf("member link depth = %d, want 1", member.Owner.LinkDepth)
	}
	if member.ParentKey.PID == 0 {
		t.Error("the member carries no parent key, so the spawn tree has no edges")
	}

	if len(evidence.Transitions) != 3 {
		t.Errorf("export holds %d transitions, want every one recorded", len(evidence.Transitions))
	}
	for _, transition := range evidence.Transitions {
		if transition.Trigger == "" {
			t.Errorf("transition %s to %s carries no trigger", transition.From, transition.To)
		}
	}

	// The redaction filter is the choke point on every output path.
	if strings.Contains(out, "xoxb-") {
		t.Errorf("the export carries a token-shaped argument:\n%s", out)
	}
	if !strings.Contains(out, attribution.Redacted) {
		t.Errorf("the secret-shaped argument was not masked:\n%s", out)
	}
	if strings.Contains(out, "node ./server.js --port 7333") && strings.Contains(out, "xoxb") {
		t.Error("the raw command line reached the export")
	}
}

// TestCLI_EvidenceUnknownSessionListsKnownOnes asserts the failure is useful
// rather than silent.
func TestCLI_EvidenceUnknownSessionListsKnownOnes(t *testing.T) {
	cfgPath, sessionID, _ := seedStore(t)

	_, stderr, err := runDevreap(t, "--config", cfgPath, "evidence", "nosuchsession")
	if err == nil {
		t.Fatal("an unknown session exported successfully")
	}
	if !strings.Contains(stderr, sessionID) {
		t.Errorf("the error does not name the known sessions: %s", stderr)
	}
}

// TestCLI_TopRefusesWhenAttributionIsOff asserts the surfaces say why they are
// empty rather than printing an empty table.
func TestCLI_TopRefusesWhenAttributionIsOff(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("attribution:\n  enabled: false\n"), 0o600); err != nil {
		t.Fatalf("writing the config: %v", err)
	}

	if _, stderr, err := runDevreap(t, "--config", cfgPath, "top"); err == nil {
		t.Error("top succeeded with attribution off")
	} else if !strings.Contains(stderr, "attribution is off") {
		t.Errorf("the error does not explain why: %s", stderr)
	}
}

// TestCLI_StatusAndDoctorReportAttribution asserts both surfaces read the store.
func TestCLI_StatusAndDoctorReportAttribution(t *testing.T) {
	cfgPath, _, _ := seedStore(t)

	status, stderr, err := runDevreap(t, "--config", cfgPath, "status")
	if err != nil {
		t.Fatalf("status failed: %v\n%s", err, stderr)
	}
	for _, want := range []string{"Attribution:", "Coverage:", "observe-only"} {
		if !strings.Contains(status, want) {
			t.Errorf("status is missing %q:\n%s", want, status)
		}
	}
	if !strings.Contains(status, "Kill gating:  off") {
		t.Errorf("status does not report phase B gating as off:\n%s", status)
	}

	doctor, stderr, err := runDevreap(t, "--config", cfgPath, "doctor")
	if err != nil {
		t.Fatalf("doctor failed: %v\n%s", err, stderr)
	}
	for _, want := range []string{"[Attribution]", "Store schema version", "owner-only"} {
		if !strings.Contains(doctor, want) {
			t.Errorf("doctor is missing %q:\n%s", want, doctor)
		}
	}
}
