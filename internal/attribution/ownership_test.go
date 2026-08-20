package attribution

import (
	"path/filepath"
	"testing"
	"time"
)

var ownershipStart = time.Date(2026, 8, 20, 5, 24, 11, 0, time.UTC)

// ownerEntry builds a snapshot row. Every field the resolver reads comes from
// one kinfo_proc, so a test that sets them separately still describes one
// instant.
func ownerEntry(pid, ppid, pgid int32, name, tty string) ProcEntry {
	return ProcEntry{
		PID:            pid,
		PPID:           ppid,
		PGID:           pgid,
		Name:           name,
		TTY:            tty,
		TTYKnown:       true,
		StartTime:      ownershipStart.Add(time.Duration(pid) * time.Millisecond),
		StartTimeKnown: true,
		UID:            501,
	}
}

func ownerCandidate(e ProcEntry, exe, cmdline string) RootCandidate {
	return RootCandidate{Entry: e, Exe: exe, Cmdline: cmdline, HasTrackedChild: true}
}

// record resolves one process and files its link, which is what the watcher does
// on every poll. Tests build a tree by calling it parent first.
func record(idx *SpawnIndex, r *Resolver, in ResolveInput) OwnershipClaim {
	link, claim := r.ResolveLink(idx, in)
	idx.Add(link)
	return claim
}

// claudeTree builds the terminal harness shape: a recognized root and one child
// it spawned, both witnessed.
func claudeTree(t *testing.T) (*Resolver, *SpawnIndex, ResolveInput, ResolveInput) {
	t.Helper()
	r := NewResolver(newTestRegistry(t))
	idx := NewSpawnIndex()

	rootEntry := ownerEntry(100, 50, 100, "claude", "ttys000")
	rootCand := ownerCandidate(rootEntry, "/opt/homebrew/bin/claude", "claude")
	rootIn := ResolveInput{
		Entry:     rootEntry,
		ParentKey: NewProcKey(50, ownershipStart),
		Exe:       "/opt/homebrew/bin/claude",
		Cmdline:   "claude",
		Chain:     []RootCandidate{rootCand},
		RootCwd:   "/Users/dev/projects/example",
		Witnessed: true,
	}

	childEntry := ownerEntry(200, 100, 100, "node", "ttys000")
	childCand := ownerCandidate(childEntry, "/usr/local/bin/node", "node ./server.js")
	childIn := ResolveInput{
		Entry:     childEntry,
		ParentKey: rootEntry.Key(),
		Exe:       "/usr/local/bin/node",
		Cmdline:   "node ./server.js",
		Chain:     []RootCandidate{childCand, rootCand},
		RootCwd:   "/Users/dev/projects/example",
		Witnessed: true,
	}
	return r, idx, rootIn, childIn
}

func hasChan(channels []Channel, want Channel) bool { return hasChannel(channels, want) }

// TestWatchedAncestryReachesObservedWithoutAnyMarker is the mechanism the whole
// design rests on. No environment variable, no hook, no vendor cooperation: the
// watcher saw the spawn link and that is enough.
func TestWatchedAncestryReachesObservedWithoutAnyMarker(t *testing.T) {
	r, idx, rootIn, childIn := claudeTree(t)

	rootClaim := record(idx, r, rootIn)
	if rootClaim.Confidence != ConfidenceObserved {
		t.Fatalf("root confidence = %q, want observed", rootClaim.Confidence)
	}
	if rootClaim.Harness != "claude-code-cli" {
		t.Errorf("root harness = %q, want claude-code-cli", rootClaim.Harness)
	}
	if rootClaim.LinkDepth != 0 {
		t.Errorf("root link depth = %d, want 0", rootClaim.LinkDepth)
	}

	childClaim := record(idx, r, childIn)
	if childClaim.Confidence != ConfidenceObserved {
		t.Fatalf("child confidence = %q, want observed", childClaim.Confidence)
	}
	if childClaim.SessionID != rootClaim.SessionID {
		t.Errorf("child session %q, want the root's %q", childClaim.SessionID, rootClaim.SessionID)
	}
	if childClaim.LinkDepth != 1 {
		t.Errorf("child link depth = %d, want 1", childClaim.LinkDepth)
	}
	if !hasChan(childClaim.Channels, ChannelWatchedAncestry) {
		t.Errorf("child channels = %v, want watched_ancestry", childClaim.Channels)
	}
	if !childClaim.Actionable() {
		t.Error("an observed claim must be action-eligible for phase B")
	}
}

// TestObservedSurvivesTheWholeAncestryExiting asserts the durability property:
// the record is a fact already written down, and the parent's death cannot erase
// it.
func TestObservedSurvivesTheWholeAncestryExiting(t *testing.T) {
	r, idx, rootIn, childIn := claudeTree(t)
	record(idx, r, rootIn)
	claim := record(idx, r, childIn)

	link, ok := idx.Lookup(childIn.Entry.Key())
	if !ok {
		t.Fatal("child link is missing from the index")
	}
	idx.Remove(rootIn.Entry.Key())

	if link.Claim.Confidence != ConfidenceObserved || claim.Confidence != ConfidenceObserved {
		t.Fatalf("claim fell to %q after the root exited, want observed", link.Claim.Confidence)
	}
}

// TestDeepChainKeepsObservedAndCountsDepth walks three links, which is the
// editor-extension shape the design measured live.
func TestDeepChainKeepsObservedAndCountsDepth(t *testing.T) {
	r := NewResolver(newTestRegistry(t))
	idx := NewSpawnIndex()

	rootEntry := ownerEntry(300, 1, 300, "codex", "")
	rootEntry.TTY, rootEntry.TTYKnown = "", true
	exe := "/Users/dev/.vscode/extensions/openai.codex/bin/codex"
	rootCand := ownerCandidate(rootEntry, exe, exe)
	chain := []RootCandidate{rootCand}
	record(idx, r, ResolveInput{
		Entry: rootEntry, ParentKey: NewProcKey(1, ownershipStart), Exe: exe,
		Chain: chain, Witnessed: true, RootCwd: "/Users/dev/projects/tools",
	})

	parentKey := rootEntry.Key()
	var last OwnershipClaim
	for depth := 1; depth <= 3; depth++ {
		entry := ownerEntry(int32(300+depth), int32(299+depth), 300, "node", "")
		entry.TTY, entry.TTYKnown = "", true
		cand := ownerCandidate(entry, "/usr/local/bin/node", "node worker.js")
		chain = append([]RootCandidate{cand}, chain...)
		last = record(idx, r, ResolveInput{
			Entry: entry, ParentKey: parentKey, Exe: "/usr/local/bin/node",
			Chain: chain, Witnessed: true,
		})
		if last.Confidence != ConfidenceObserved {
			t.Fatalf("depth %d confidence = %q, want observed", depth, last.Confidence)
		}
		if last.LinkDepth != depth {
			t.Errorf("depth %d link depth = %d", depth, last.LinkDepth)
		}
		parentKey = entry.Key()
	}
	if last.Harness != "codex-editor-extension" {
		t.Errorf("harness = %q, want codex-editor-extension", last.Harness)
	}
}

// TestHarnessNeutralityWithoutAdapterFile builds a spawn chain from a root that
// matches no adapter entry and publishes no markers, and asserts the descendants
// still reach observed confidence with the unknown-harness label.
//
// The same assertions run with the adapter data file removed entirely, because
// the design promises a harness nobody has heard of is attributed on the day it
// ships. A recognition gap may only cost a label.
func TestHarnessNeutralityWithoutAdapterFile(t *testing.T) {
	build := func(t *testing.T, registry *HarnessRegistry) OwnershipClaim {
		t.Helper()
		r := NewResolver(registry)
		idx := NewSpawnIndex()

		rootEntry := ownerEntry(400, 60, 400, "brandnewagent", "ttys004")
		rootCand := ownerCandidate(rootEntry, "/opt/vendor/brandnewagent", "brandnewagent --serve")
		rootClaim := record(idx, r, ResolveInput{
			Entry:     rootEntry,
			ParentKey: NewProcKey(60, ownershipStart),
			Exe:       "/opt/vendor/brandnewagent",
			Cmdline:   "brandnewagent --serve",
			Chain:     []RootCandidate{rootCand},
			RootCwd:   "/Users/dev/projects/lab",
			Witnessed: true,
		})
		if rootClaim.Harness != UnknownHarnessLabel {
			t.Errorf("root label = %q, want %q", rootClaim.Harness, UnknownHarnessLabel)
		}

		childEntry := ownerEntry(401, 400, 400, "node", "ttys004")
		childCand := ownerCandidate(childEntry, "/usr/local/bin/node", "node mcp-server.js")
		return record(idx, r, ResolveInput{
			Entry:     childEntry,
			ParentKey: rootEntry.Key(),
			Exe:       "/usr/local/bin/node",
			Cmdline:   "node mcp-server.js",
			Chain:     []RootCandidate{childCand, rootCand},
			Witnessed: true,
		})
	}

	t.Run("built-in table present", func(t *testing.T) {
		claim := build(t, newTestRegistry(t))
		if claim.Confidence != ConfidenceObserved {
			t.Fatalf("confidence = %q, want observed", claim.Confidence)
		}
		if claim.Harness != UnknownHarnessLabel {
			t.Errorf("label = %q, want %q", claim.Harness, UnknownHarnessLabel)
		}
	})

	t.Run("adapter data file removed", func(t *testing.T) {
		registry := newTestRegistry(t)
		registry.LoadUserFile(filepath.Join(t.TempDir(), "harnesses.yaml"))
		if findings := registry.Findings(); len(findings) != 0 {
			t.Fatalf("a missing user file must be normal, got findings %+v", findings)
		}
		claim := build(t, registry)
		if claim.Confidence != ConfidenceObserved {
			t.Fatalf("confidence = %q, want observed with no adapter file", claim.Confidence)
		}
		if claim.Harness != UnknownHarnessLabel {
			t.Errorf("label = %q, want %q", claim.Harness, UnknownHarnessLabel)
		}
	})
}

// TestBackfillClaimsAreInferredOnly starts the resolver with an empty spawn
// index, feeds processes carrying markers, and asserts every claim is inferred
// and none is action-eligible. This is the cold-start cost of preferring
// witnessed facts.
func TestBackfillClaimsAreInferredOnly(t *testing.T) {
	r := NewResolver(newTestRegistry(t))
	idx := NewSpawnIndex()

	entry := ownerEntry(500, 1, 480, "node", "")
	entry.TTY, entry.TTYKnown = "", true
	cand := ownerCandidate(entry, "/usr/local/bin/node", "node ./indexer.js")

	claim := r.Resolve(idx, ResolveInput{
		Entry:     entry,
		ParentKey: NewProcKey(1, ownershipStart),
		Exe:       "/usr/local/bin/node",
		Chain:     []RootCandidate{cand},
		Env: map[string]string{
			"CLAUDE_CODE_SESSION_ID": "5f1c9a2e",
			"CLAUDE_PROJECT_DIR":     "/Users/dev/projects/example",
		},
		Witnessed: false,
	})

	if claim.Confidence != ConfidenceInferred {
		t.Fatalf("backfilled confidence = %q, want inferred", claim.Confidence)
	}
	if claim.SessionID != "5f1c9a2e" {
		t.Errorf("session = %q, want the marker value", claim.SessionID)
	}
	if claim.Repo != "/Users/dev/projects/example" {
		t.Errorf("repo = %q, want the marker value", claim.Repo)
	}
	if claim.Actionable() {
		t.Error("a backfilled claim must never be action-eligible")
	}
	if !claim.Attributed() {
		t.Error("a backfilled claim is still reportable")
	}
}

// TestMarkerCorroboratesWithoutRaisingTheTier asserts channel 2's first job. A
// marker naming the session the ancestry already named is recorded and shown,
// and it changes no tier because the tier was already the highest one.
func TestMarkerCorroboratesWithoutRaisingTheTier(t *testing.T) {
	r, idx, rootIn, childIn := claudeTree(t)
	rootIn.Env = map[string]string{"CLAUDE_CODE_SESSION_ID": "5f1c9a2e"}
	rootClaim := record(idx, r, rootIn)
	if rootClaim.SessionID != "5f1c9a2e" {
		t.Fatalf("root session = %q, want the marker value", rootClaim.SessionID)
	}

	childIn.Env = map[string]string{"CLAUDE_CODE_SESSION_ID": "5f1c9a2e"}
	claim := record(idx, r, childIn)

	if claim.Confidence != ConfidenceObserved {
		t.Fatalf("confidence = %q, want observed", claim.Confidence)
	}
	if !hasChan(claim.Channels, ChannelEnv) || !hasChan(claim.Channels, ChannelWatchedAncestry) {
		t.Errorf("channels = %v, want both watched_ancestry and env", claim.Channels)
	}
}

// TestForgedMarkerDisagreementCapsAtInferred covers the abuse case. Any process
// can set any variable, so a marker claiming a different session than the
// witnessed link keeps the witnessed answer and stops being action-eligible.
func TestForgedMarkerDisagreementCapsAtInferred(t *testing.T) {
	r, idx, rootIn, childIn := claudeTree(t)
	rootIn.Env = map[string]string{"CLAUDE_CODE_SESSION_ID": "5f1c9a2e"}
	rootClaim := record(idx, r, rootIn)

	childIn.Env = map[string]string{"CLAUDE_CODE_SESSION_ID": "forged-other-session"}
	claim := record(idx, r, childIn)

	if claim.SessionID != rootClaim.SessionID {
		t.Errorf("session = %q, want the witnessed %q; a forged marker cannot move a process", claim.SessionID, rootClaim.SessionID)
	}
	if claim.Confidence != ConfidenceInferred {
		t.Fatalf("confidence = %q, want inferred; a disagreement resolves to do-not-act", claim.Confidence)
	}
	if claim.Actionable() {
		t.Error("a disagreeing claim must never gate an action")
	}
}

// TestProcessGroupAloneIsInferred asserts channel 3 on its own. It seeds the
// generic descriptor and reports; it never reaches observed, because descendants
// leave the group and the leader's identity dies with the leader.
func TestProcessGroupAloneIsInferred(t *testing.T) {
	r := NewResolver(newTestRegistry(t))
	idx := NewSpawnIndex()

	rootEntry := ownerEntry(600, 70, 600, "someagent", "ttys002")
	rootCand := ownerCandidate(rootEntry, "/opt/vendor/someagent", "someagent")
	memberEntry := ownerEntry(601, 1, 600, "node", "ttys002")
	memberCand := ownerCandidate(memberEntry, "/usr/local/bin/node", "node ./server.js")

	claim := r.Resolve(idx, ResolveInput{
		Entry:     memberEntry,
		ParentKey: NewProcKey(1, ownershipStart),
		Exe:       "/usr/local/bin/node",
		Chain:     []RootCandidate{memberCand, rootCand},
		RootCwd:   "/Users/dev/projects/lab",
		Witnessed: false,
	})

	if claim.Confidence != ConfidenceInferred {
		t.Fatalf("confidence = %q, want inferred", claim.Confidence)
	}
	if !hasChan(claim.Channels, ChannelPGID) {
		t.Errorf("channels = %v, want pgid", claim.Channels)
	}
	if claim.Harness != UnknownHarnessLabel {
		t.Errorf("harness = %q, want %q", claim.Harness, UnknownHarnessLabel)
	}
}

// TestForeignProcessGroupResolvesToNone covers the process the design measured:
// parent PID 1, a process group different from its session's, and no marker at
// all. No channel resolves, so it stays unattributed rather than being guessed
// into the nearest session.
func TestForeignProcessGroupResolvesToNone(t *testing.T) {
	r := NewResolver(newTestRegistry(t))
	idx := NewSpawnIndex()

	rootEntry := ownerEntry(700, 80, 700, "someagent", "ttys003")
	rootCand := ownerCandidate(rootEntry, "/opt/vendor/someagent", "someagent")
	strayEntry := ownerEntry(701, 1, 999, "node", "ttys009")
	strayCand := ownerCandidate(strayEntry, "/usr/local/bin/node", "node ./server.js")

	claim := r.Resolve(idx, ResolveInput{
		Entry:     strayEntry,
		ParentKey: NewProcKey(1, ownershipStart),
		Exe:       "/usr/local/bin/node",
		Chain:     []RootCandidate{strayCand, rootCand},
		Witnessed: false,
	})

	if claim.Confidence != ConfidenceNone {
		t.Fatalf("confidence = %q, want none", claim.Confidence)
	}
	if claim.Attributed() {
		t.Error("an unresolved process must record as unattributed")
	}
}

// TestEnvironmentReadFailureLeavesTheWitnessedLinkIntact asserts the failure
// direction. Losing the environment costs a corroborating channel and never the
// claim, because the claim never rested on the environment.
func TestEnvironmentReadFailureLeavesTheWitnessedLinkIntact(t *testing.T) {
	r, idx, rootIn, childIn := claudeTree(t)
	record(idx, r, rootIn)

	childIn.Env = nil
	claim := record(idx, r, childIn)

	if claim.Confidence != ConfidenceObserved {
		t.Fatalf("confidence = %q, want observed with no environment read", claim.Confidence)
	}
	if hasChan(claim.Channels, ChannelEnv) {
		t.Errorf("channels = %v, want no env channel", claim.Channels)
	}
}

// TestUnverifiableStartTimeCapsAtInferred asserts R2 at the ownership layer. A
// process with no readable start time has no identity to prove, so a chain that
// appears to reach a root cannot be shown to reach it from this process.
func TestUnverifiableStartTimeCapsAtInferred(t *testing.T) {
	r, idx, rootIn, childIn := claudeTree(t)
	record(idx, r, rootIn)

	childIn.Entry.StartTimeKnown = false
	childIn.Entry.StartTime = time.Time{}
	claim := record(idx, r, childIn)

	if claim.Confidence == ConfidenceObserved {
		t.Fatal("a process with an unreadable start time must never reach observed")
	}
	if claim.Actionable() {
		t.Error("an unverifiable record can never gate an action")
	}
}

// TestPIDReuseDoesNotMatchTheRecordedLink asserts the identity rule. PID reuse is
// live pressure on this machine, not theory, so a lookup that cannot match both
// halves of the key returns no match rather than the previous process's owner.
func TestPIDReuseDoesNotMatchTheRecordedLink(t *testing.T) {
	r, idx, rootIn, childIn := claudeTree(t)
	record(idx, r, rootIn)
	record(idx, r, childIn)

	reused := childIn.Entry
	reused.StartTime = childIn.Entry.StartTime.Add(90 * time.Minute)

	if _, found := idx.Lookup(reused.Key()); found {
		t.Fatal("a reused identifier matched the previous process's link")
	}
	if !KeyMismatch(childIn.Entry.Key(), reused) {
		t.Error("KeyMismatch must report a reused identifier as a mismatch")
	}
	if KeyMismatch(childIn.Entry.Key(), childIn.Entry) {
		t.Error("KeyMismatch must not fire on the process that wrote the record")
	}

	grandchild := ResolveInput{
		Entry:     ownerEntry(250, reused.PID, 100, "node", "ttys000"),
		ParentKey: reused.Key(),
		Chain:     []RootCandidate{ownerCandidate(ownerEntry(250, reused.PID, 100, "node", "ttys000"), "", "node x.js")},
		Witnessed: true,
	}
	if claim := r.Resolve(idx, grandchild); claim.Confidence == ConfidenceObserved {
		t.Error("a child of a reused identifier must not inherit the old session as observed")
	}
}

// TestClaimUpgradeInferredToObserved starts a process at inferred, later supplies
// the missing parent record, and asserts an upgrade record is written naming the
// resolved chain. Coverage counts the process after the upgrade, which is why the
// direction is one way.
func TestClaimUpgradeInferredToObserved(t *testing.T) {
	r, idx, rootIn, childIn := claudeTree(t)

	childIn.Env = map[string]string{"CLAUDE_CODE_SESSION_ID": "5f1c9a2e"}
	before := r.Resolve(idx, childIn)
	if before.Confidence != ConfidenceInferred {
		t.Fatalf("confidence before the parent record = %q, want inferred", before.Confidence)
	}

	// The parent record arrives, which is what a journal replay recovers.
	rootIn.Env = map[string]string{"CLAUDE_CODE_SESSION_ID": "5f1c9a2e"}
	record(idx, r, rootIn)

	upgrade, ok := r.Upgrade(idx, before, childIn, ownershipStart.Add(time.Hour), UpgradeAncestryResolved)
	if !ok {
		t.Fatal("no upgrade record was produced after the chain resolved")
	}
	if upgrade.From != ConfidenceInferred || upgrade.To != ConfidenceObserved {
		t.Errorf("upgrade %q to %q, want inferred to observed", upgrade.From, upgrade.To)
	}
	if upgrade.Reason != UpgradeAncestryResolved {
		t.Errorf("reason = %q", upgrade.Reason)
	}
	if !upgrade.Evidence.RootKey.Equal(rootIn.Entry.Key()) {
		t.Errorf("evidence root key = %v, want the root", upgrade.Evidence.RootKey)
	}
	if upgrade.Evidence.LinkDepth != 1 {
		t.Errorf("evidence link depth = %d, want 1", upgrade.Evidence.LinkDepth)
	}

	// The birth record itself is untouched: an upgrade is a separate record, and
	// the claim the caller passed in is unchanged by the call.
	if before.Confidence != ConfidenceInferred {
		t.Error("Upgrade must not mutate the claim it was given")
	}
}

// TestClaimUpgradeNeverLowersAClaim asserts the one-way rule from both ends.
func TestClaimUpgradeNeverLowersAClaim(t *testing.T) {
	r, idx, rootIn, childIn := claudeTree(t)
	record(idx, r, rootIn)
	observed := record(idx, r, childIn)

	if _, ok := r.Upgrade(idx, observed, childIn, ownershipStart, UpgradeAncestryResolved); ok {
		t.Error("nothing raises a claim past observed")
	}

	// A chain that still does not resolve produces no record rather than a
	// downgrade.
	emptyIdx := NewSpawnIndex()
	inferred := OwnershipClaim{SessionID: "5f1c9a2e", Confidence: ConfidenceInferred}
	orphanIn := childIn
	orphanIn.Env = nil
	if _, ok := r.Upgrade(emptyIdx, inferred, orphanIn, ownershipStart, ""); ok {
		t.Error("an unresolved chain must produce no upgrade record")
	}
}

// TestAdapterAdditionUpgradesAnExistingChain covers the second upgrade cause. A
// chain of recorded links that ended nowhere now ends at a root, because a
// descriptor was added rather than because the processes changed.
func TestAdapterAdditionUpgradesAnExistingChain(t *testing.T) {
	base := newTestRegistry(t)
	r := NewResolver(base)
	idx := NewSpawnIndex()

	rootEntry := ownerEntry(800, 90, 800, "futureagent", "ttys006")
	rootCand := ownerCandidate(rootEntry, "/opt/vendor/futureagent", "futureagent")
	rootCand.HasTrackedChild = false // no tracked child yet, so the generic rule cannot fire

	memberEntry := ownerEntry(801, 800, 800, "node", "ttys006")
	memberCand := ownerCandidate(memberEntry, "/usr/local/bin/node", "node ./server.js")
	memberIn := ResolveInput{
		Entry:     memberEntry,
		ParentKey: rootEntry.Key(),
		Exe:       "/usr/local/bin/node",
		Chain:     []RootCandidate{memberCand, rootCand},
		Witnessed: true,
	}

	before := r.Resolve(idx, memberIn)
	if before.Confidence != ConfidenceNone {
		t.Fatalf("confidence before recognition = %q, want none", before.Confidence)
	}

	// The descriptor arrives. The processes did not change; the label did.
	rootCand.HasTrackedChild = true
	memberIn.Chain = []RootCandidate{memberCand, rootCand}
	rootIn := ResolveInput{
		Entry: rootEntry, ParentKey: NewProcKey(90, ownershipStart),
		Exe: "/opt/vendor/futureagent", Chain: []RootCandidate{rootCand}, Witnessed: true,
	}
	record(idx, r, rootIn)

	upgrade, ok := r.Upgrade(idx, before, memberIn, ownershipStart, UpgradeAdapterAddition)
	if !ok {
		t.Fatal("no upgrade record after the ancestor became recognizable")
	}
	if upgrade.Reason != UpgradeAdapterAddition {
		t.Errorf("reason = %q, want %q", upgrade.Reason, UpgradeAdapterAddition)
	}
	if upgrade.To != ConfidenceObserved {
		t.Errorf("upgraded to %q, want observed", upgrade.To)
	}
}

// TestLinkDepthBoundStopsAttribution asserts the ancestry walk is bounded. Past
// the bound the process is recorded as unattributed rather than joined to a
// distant session.
func TestLinkDepthBoundStopsAttribution(t *testing.T) {
	r := NewResolver(newTestRegistry(t))
	idx := NewSpawnIndex()

	parentKey := NewProcKey(900, ownershipStart)
	idx.Add(SpawnLink{
		Key:       parentKey,
		Root:      false,
		Witnessed: true,
		Claim: OwnershipClaim{
			SessionID:  "5f1c9a2e",
			Confidence: ConfidenceObserved,
			LinkDepth:  MaxLinkDepth,
		},
	})

	entry := ownerEntry(901, 900, 900, "node", "ttys007")
	claim := r.Resolve(idx, ResolveInput{
		Entry:     entry,
		ParentKey: parentKey,
		Chain:     []RootCandidate{ownerCandidate(entry, "", "node deep.js")},
		Witnessed: true,
	})

	if claim.Confidence != ConfidenceNone {
		t.Fatalf("confidence at depth %d = %q, want none", MaxLinkDepth+1, claim.Confidence)
	}
}

// TestSpawnIndexRefusesUnverifiableKeys asserts the index cannot hold a link it
// could never match again.
func TestSpawnIndexRefusesUnverifiableKeys(t *testing.T) {
	idx := NewSpawnIndex()
	if idx.Add(SpawnLink{Key: ProcKey{PID: 42}}) {
		t.Error("the index accepted a key with no start time")
	}
	if idx.Len() != 0 {
		t.Errorf("index length = %d, want 0", idx.Len())
	}
	if _, ok := idx.Lookup(ProcKey{PID: 42}); ok {
		t.Error("an unverifiable key matched")
	}
}

// TestDeriveSessionIDIsStableAndKeyed asserts two roots that reused one
// identifier derive different sessions rather than merging into one that never
// existed.
func TestDeriveSessionIDIsStableAndKeyed(t *testing.T) {
	first := NewProcKey(98888, ownershipStart)
	again := NewProcKey(98888, ownershipStart)
	reused := NewProcKey(98888, ownershipStart.Add(time.Hour))

	if DeriveSessionID(first) != DeriveSessionID(again) {
		t.Error("the same key derived two different session identifiers")
	}
	if DeriveSessionID(first) == DeriveSessionID(reused) {
		t.Error("a reused identifier derived the same session identifier")
	}
	if got := len(DeriveSessionID(first)); got != SessionIDLength {
		t.Errorf("session identifier length = %d, want %d", got, SessionIDLength)
	}
	if DeriveSessionID(ProcKey{}) != "" {
		t.Error("a zero key must derive no session identifier")
	}
}
