package attribution

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// SessionIDLength is how many hexadecimal characters a derived session
// identifier carries. It is long enough that two roots on one machine do not
// collide, and short enough to read in a table.
const SessionIDLength = 8

// DeriveSessionID builds a stable identifier for a session from its root's
// process key. The key already holds the start time, so two roots that reused
// one process identifier derive different identifiers rather than merging into
// a session that never existed.
//
// A harness that publishes its own session identifier overrides this, because a
// vendor identifier is more useful in a bug report than a hash. The derived form
// is what every other harness gets, which is what keeps recognition optional.
func DeriveSessionID(rootKey ProcKey) string {
	if rootKey.Zero() {
		return ""
	}
	sum := sha256.Sum256([]byte(rootKey.IndexKey()))
	return hex.EncodeToString(sum[:])[:SessionIDLength]
}

// SpawnLink is one recorded parent-to-child link together with what the child's
// ownership resolved to. The watcher adds one per process it tracks, and the
// resolver reads the parent's entry to decide whether a chain of recorded links
// reaches a session root.
type SpawnLink struct {
	Key       ProcKey
	ParentKey ProcKey
	Claim     OwnershipClaim

	// Root reports whether this process is itself a session root. A chain of
	// recorded links that ends here has reached its terminus.
	Root bool

	// Witnessed reports whether the watcher saw this process appear between two
	// polls. A process that already existed when the watcher started was
	// witnessed by nobody, so no spawn link of its own was ever recorded.
	Witnessed bool

	// PGID and TTY are kept for the corroborating channel, which compares a
	// member against its root rather than reading either process again.
	PGID     int32
	TTY      string
	TTYKnown bool
}

// SpawnIndex holds the spawn links the watcher has recorded. It is the memory
// that makes watched ancestry work: a link recorded once stays true after the
// whole ancestry exits, because the parent's death cannot erase a fact already
// written down.
//
// The index is not safe for concurrent use. The watcher owns one and reads it
// on its own goroutine.
type SpawnIndex struct {
	links map[string]SpawnLink
}

// NewSpawnIndex returns an empty index.
func NewSpawnIndex() *SpawnIndex {
	return &SpawnIndex{links: make(map[string]SpawnLink)}
}

// Add records a link. A link whose key cannot be verified is refused, because a
// process whose start time is unknown has no identity to record.
func (i *SpawnIndex) Add(link SpawnLink) bool {
	if !link.Key.Verifiable() {
		return false
	}
	i.links[link.Key.IndexKey()] = link
	return true
}

// Lookup finds a recorded link by identity, and fails closed. An unverifiable
// key never matches, and a reused process identifier never matches the record of
// the process that held the number before it, because the key carries the start
// time.
func (i *SpawnIndex) Lookup(key ProcKey) (SpawnLink, bool) {
	if !key.Verifiable() {
		return SpawnLink{}, false
	}
	link, ok := i.links[key.IndexKey()]
	return link, ok
}

// Remove drops a link, which the watcher does when a process exits and its
// record has been written.
func (i *SpawnIndex) Remove(key ProcKey) {
	delete(i.links, key.IndexKey())
}

// Len reports how many links the index holds.
func (i *SpawnIndex) Len() int { return len(i.links) }

// Keys returns every recorded index key, for a caller that needs to walk the
// whole index. The order is not defined.
func (i *SpawnIndex) Keys() []string {
	out := make([]string, 0, len(i.links))
	for key := range i.links {
		out = append(out, key)
	}
	return out
}

// ResolveInput is everything ownership resolution needs about one process. The
// caller has already collected every field, and every field that came from a
// foreign process has already passed the redaction filter.
//
// ParentKey carries the parent's start time read live during the same capture,
// so it names the parent that actually existed at that moment rather than
// whatever later holds the number.
type ResolveInput struct {
	// Entry is the process itself, from the bulk snapshot.
	Entry ProcEntry

	// ParentKey identifies the parent at the instant of capture.
	ParentKey ProcKey

	// Exe and Cmdline are used for root recognition only. Cmdline is redacted.
	Exe     string
	Cmdline string

	// Env holds the allowlisted environment variables and nothing else. A
	// variable absent here was dropped by the redaction filter.
	Env map[string]string

	// Chain is the process followed by its ancestors, nearest first, which is
	// the form root recognition walks.
	Chain []RootCandidate

	// RootCwd is the working directory of the resolved root, read once at birth.
	// It is the repository source for every harness that publishes no marker.
	RootCwd string

	// Witnessed reports whether the watcher saw this process appear. A process
	// that already existed when the watcher started is backfilled instead, and a
	// backfilled claim is inferred at best.
	Witnessed bool
}

// Resolver turns collected process fields into an ownership claim. It reads no
// process itself and holds no state beyond the harness registry, so the same
// input always produces the same claim.
type Resolver struct {
	registry *HarnessRegistry
}

// NewResolver returns a resolver backed by a harness registry. A nil registry is
// allowed and degrades recognition to nothing, which costs labels rather than
// attribution.
func NewResolver(registry *HarnessRegistry) *Resolver {
	return &Resolver{registry: registry}
}

// Attributed reports whether a claim names an owner at all.
func (c OwnershipClaim) Attributed() bool {
	return c.Confidence != ConfidenceNone && c.SessionID != ""
}

// Actionable reports whether a claim may ever contribute to an action. Only an
// observed claim can, and that gate belongs to phase B.
func (c OwnershipClaim) Actionable() bool {
	return c.Confidence == ConfidenceObserved && c.SessionID != ""
}

// Resolve runs the three ownership channels over one process and returns the
// resulting claim.
//
// Channel 1 is watched ancestry, and it is the whole mechanism. A process whose
// birth the watcher recorded, on a chain of recorded links reaching a session
// root, is observed regardless of vendor. The link is a fact the watcher saw, so
// it needs no cooperation from the harness, no environment variable, and no
// hook.
//
// Channel 2 is the inherited environment. It corroborates a claim the ancestry
// already made, and it backfills a process the watcher never saw born. A claim
// resting on a marker alone is inferred, because any process can set any
// variable.
//
// Channel 3 is the process group with its controlling terminal. It corroborates,
// and it seeds the generic descriptor. Alone it is insufficient, because
// descendants leave the group and the group leader's identity dies with the
// leader.
//
// Every disagreement resolves toward "do not act". Two channels naming different
// sessions keeps the witnessed answer and caps the tier at inferred, so the
// claim is still reported and can no longer gate anything.
func (r *Resolver) Resolve(index *SpawnIndex, in ResolveInput) OwnershipClaim {
	claim := OwnershipClaim{Confidence: ConfidenceNone, Channels: []Channel{}}

	rootMatch, hasRoot := r.resolveRoot(in)
	ancestry, hasAncestry := r.resolveAncestry(index, in, rootMatch, hasRoot)
	marker, hasMarker := r.resolveMarker(in, rootMatch, hasRoot)

	switch {
	case hasAncestry:
		claim = ancestry
		claim.Channels = []Channel{ChannelWatchedAncestry}
		if hasMarker {
			claim.Channels = append(claim.Channels, ChannelEnv)
			if marker.SessionID != claim.SessionID {
				// Two channels name different sessions. The witnessed link is the
				// fact and stays, but a disagreement is never a reason to act.
				claim.Confidence = ConfidenceInferred
			} else if claim.Repo == "" {
				claim.Repo = marker.Repo
			}
		}

	case hasMarker:
		// Backfill. The watcher witnessed nothing, so a marker is the only way to
		// name an owner and the claim can never rise above inferred here.
		claim = marker
		claim.Confidence = ConfidenceInferred
		claim.Channels = []Channel{ChannelEnv}

	case hasRoot:
		// Group and terminal alone. It seeds the generic descriptor and reports,
		// and it is never action-eligible.
		if !groupCorroborates(in.Entry, rootMatch.Root.Entry) {
			return claim
		}
		claim = r.claimFromRoot(rootMatch, in)
		claim.Confidence = ConfidenceInferred
		claim.Channels = []Channel{ChannelPGID}

	default:
		return claim
	}

	if hasRoot && groupCorroborates(in.Entry, rootMatch.Root.Entry) && !hasChannel(claim.Channels, ChannelPGID) {
		claim.Channels = append(claim.Channels, ChannelPGID)
	}

	// A process whose own start time could not be read has no identity to prove,
	// so a chain that appears to reach a root cannot be shown to reach it from
	// this process. The claim is reportable and never action-eligible.
	if !in.Entry.Key().Verifiable() && claim.Confidence == ConfidenceObserved {
		claim.Confidence = ConfidenceInferred
	}

	// The ancestry walk is bounded. Beyond the bound attribution stops rather
	// than joining a process to a distant session.
	if claim.LinkDepth > MaxLinkDepth {
		return OwnershipClaim{Confidence: ConfidenceNone, Channels: []Channel{}}
	}
	return claim
}

// IsSessionRoot reports whether this process is itself a session root, which is
// the terminus a chain of recorded links has to reach.
func (r *Resolver) IsSessionRoot(in ResolveInput) bool {
	match, ok := r.resolveRoot(in)
	return ok && match.Depth == 0
}

// ResolveLink resolves ownership and returns the link to record alongside the
// claim. The watcher writes one of these per tracked process, and every later
// resolution reads them back as the recorded ancestry.
func (r *Resolver) ResolveLink(index *SpawnIndex, in ResolveInput) (SpawnLink, OwnershipClaim) {
	claim := r.Resolve(index, in)
	link := SpawnLink{
		Key:       in.Entry.Key(),
		ParentKey: in.ParentKey,
		Claim:     claim,
		Root:      r.IsSessionRoot(in),
		Witnessed: in.Witnessed,
		PGID:      in.Entry.PGID,
		TTY:       in.Entry.TTY,
		TTYKnown:  in.Entry.TTYKnown,
	}
	return link, claim
}

func (r *Resolver) resolveRoot(in ResolveInput) (RootMatch, bool) {
	if r.registry == nil || len(in.Chain) == 0 {
		return RootMatch{}, false
	}
	return r.registry.ResolveRoot(in.Chain)
}

// resolveAncestry runs channel 1.
//
// Two shapes reach a session root. The process is itself a recognized root, in
// which case the chain has length zero and the process opens its own session.
// Or the process's parent holds a recorded link, in which case the child joins
// the parent's session one link further out.
//
// A parent that is itself a root terminates the chain even when the parent's own
// claim is inferred, because the link from child to parent was witnessed and the
// root is the terminus rather than another link. That is what lets a session
// started before the watcher attribute the children it spawns afterwards.
//
// A parent that is neither a root nor observed passes its own tier down, because
// the chain above it was never witnessed.
func (r *Resolver) resolveAncestry(index *SpawnIndex, in ResolveInput, rootMatch RootMatch, hasRoot bool) (OwnershipClaim, bool) {
	if !in.Witnessed {
		return OwnershipClaim{}, false
	}

	if hasRoot && rootMatch.Depth == 0 {
		claim := r.claimFromRoot(rootMatch, in)
		claim.Confidence = ConfidenceObserved
		return claim, true
	}

	if index == nil {
		return OwnershipClaim{}, false
	}
	parent, known := index.Lookup(in.ParentKey)
	if !known || !parent.Claim.Attributed() {
		return OwnershipClaim{}, false
	}

	claim := parent.Claim
	claim.LinkDepth = parent.Claim.LinkDepth + 1
	claim.Channels = nil
	switch {
	case parent.Root:
		claim.Confidence = ConfidenceObserved
	case parent.Claim.Confidence == ConfidenceObserved:
		claim.Confidence = ConfidenceObserved
	default:
		claim.Confidence = ConfidenceInferred
	}
	return claim, true
}

// resolveMarker runs channel 2. It reads the session identifier and the
// repository from the environment variables the loaded descriptors name, and
// nothing else: the redaction filter has already dropped every other value.
func (r *Resolver) resolveMarker(in ResolveInput, rootMatch RootMatch, hasRoot bool) (OwnershipClaim, bool) {
	if len(in.Env) == 0 || r.registry == nil {
		return OwnershipClaim{}, false
	}

	// The recognized harness is asked first, so a process carrying two vendors'
	// variables is read as belonging to the harness that actually started it.
	descriptors := make([]Harness, 0, r.registry.Count()+1)
	if hasRoot && !rootMatch.Generic {
		descriptors = append(descriptors, rootMatch.Harness)
	}
	descriptors = append(descriptors, r.registry.All()...)

	for _, h := range descriptors {
		if h.Markers.SessionIDEnv == "" {
			continue
		}
		sessionID := in.Env[h.Markers.SessionIDEnv]
		if sessionID == "" {
			continue
		}
		claim := OwnershipClaim{
			SessionID:  sessionID,
			Harness:    h.Label(),
			Confidence: ConfidenceInferred,
			Channels:   []Channel{ChannelEnv},
		}
		if h.Markers.RepoEnv != "" {
			claim.Repo = in.Env[h.Markers.RepoEnv]
		}
		if hasRoot {
			claim.RootKey = rootMatch.Root.Entry.Key()
			claim.LinkDepth = rootMatch.Depth
			if claim.Repo == "" {
				claim.Repo = in.RootCwd
			}
		}
		return claim, true
	}
	return OwnershipClaim{}, false
}

// claimFromRoot builds the claim a session root itself carries. Its session
// identifier is the harness marker when one exists and its own process key
// otherwise, its repository is the marker or the root's working directory, and
// its link depth is the distance from the process asking.
func (r *Resolver) claimFromRoot(rootMatch RootMatch, in ResolveInput) OwnershipClaim {
	rootKey := rootMatch.Root.Entry.Key()
	claim := OwnershipClaim{
		SessionID: DeriveSessionID(rootKey),
		Harness:   rootMatch.Label,
		Repo:      in.RootCwd,
		RootKey:   rootKey,
		LinkDepth: rootMatch.Depth,
		Channels:  []Channel{},
	}

	// A root recognized at depth zero can read its own marker. Deeper roots are
	// a different process, whose environment this call never saw.
	if rootMatch.Depth == 0 && len(in.Env) > 0 {
		if name := rootMatch.Harness.Markers.SessionIDEnv; name != "" {
			if id := in.Env[name]; id != "" {
				claim.SessionID = id
			}
		}
		if name := rootMatch.Harness.Markers.RepoEnv; name != "" {
			if repo := in.Env[name]; repo != "" {
				claim.Repo = repo
			}
		}
	}
	return claim
}

// groupCorroborates reports whether a process shares its root's process group
// and controlling terminal. It only ever adds a channel to a claim another
// channel already made, or seeds the generic descriptor, because a group alone
// is insufficient evidence of ownership.
func groupCorroborates(member, root ProcEntry) bool {
	if member.PGID == 0 || member.PGID != root.PGID {
		return false
	}
	if !member.TTYKnown || !root.TTYKnown {
		return false
	}
	return member.TTY == root.TTY
}

func hasChannel(channels []Channel, want Channel) bool {
	for _, c := range channels {
		if c == want {
			return true
		}
	}
	return false
}

// Upgrade re-resolves a process whose claim is below observed and returns the
// upgrade record to write when a spawn link that already existed has become
// provable.
//
// The direction is one way. Nothing downgrades a claim here, and only a key
// mismatch invalidates one, which is why a mismatch returns no record rather
// than a lowered claim: the caller invalidates the whole record instead.
//
// Two events produce an upgrade. A journal replay recovers a parent birth record
// the snapshot did not hold, completing a chain that was previously broken. An
// adapter addition makes an existing ancestor recognizable as a session root, so
// a chain of recorded links that ended nowhere now ends at a root.
func (r *Resolver) Upgrade(index *SpawnIndex, current OwnershipClaim, in ResolveInput, at time.Time, reason string) (ClaimUpgradeRecord, bool) {
	if current.Confidence == ConfidenceObserved {
		return ClaimUpgradeRecord{}, false
	}
	key := in.Entry.Key()
	if !key.Verifiable() {
		return ClaimUpgradeRecord{}, false
	}

	next := r.Resolve(index, in)
	if next.Confidence != ConfidenceObserved {
		return ClaimUpgradeRecord{}, false
	}
	if reason == "" {
		reason = UpgradeAncestryResolved
	}

	return ClaimUpgradeRecord{
		At:     NormalizeTime(at),
		Key:    key,
		From:   current.Confidence,
		To:     next.Confidence,
		Reason: reason,
		Evidence: ClaimUpgradeEvidence{
			RootKey:   next.RootKey,
			LinkDepth: next.LinkDepth,
			SessionID: next.SessionID,
			Harness:   next.Harness,
		},
	}, true
}

// KeyMismatch reports whether a stored record still describes the live process
// holding its identifier. A reused identifier fails this, and the caller
// invalidates the record rather than attributing the new process to the old
// owner.
func KeyMismatch(stored ProcKey, live ProcEntry) bool {
	if stored.PID != live.PID {
		return true
	}
	return !stored.Equal(live.Key())
}
