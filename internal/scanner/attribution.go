package scanner

import "time"

// AttributionGate is the restriction session attribution exposes to the kill
// path. It is deliberately narrow: it answers one question about one process and
// can express nothing except "leave this one alone".
//
// The scanner does not import the attribution package. The gate is an interface
// so the dependency runs one way, and so a test can drive both answers without
// a watcher.
type AttributionGate interface {
	// Trusted reports whether attribution data is current. A stale or stopped
	// watcher answers false, and every process is then refused rather than
	// waved through.
	Trusted() bool

	// ConfirmedOrphan reports whether the lifecycle engine independently reached
	// CONFIRMED_ORPHAN for this exact process, meaning all five conditions of R6
	// held on the latest observation.
	ConfirmedOrphan(pid int32, startTime time.Time) bool
}

// KillEligible reports whether a candidate may be killed.
//
// The shape of this function is the safety property, not a detail of it.
// The existing requirement is evaluated first and on its own: without a strong
// lifecycle signal the answer is false and nothing below can change it. Every
// later check can only return false, so the gated set is a subset of the ungated
// set by construction rather than by inspection.
//
// Attribution can never make a process eligible that the existing path would not
// already permit. The worst case of a wrong attribution is therefore a missed
// orphan, which costs the user some memory, rather than a wrong kill, which
// costs the user their work.
func KillEligible(candidate OrphanCandidate, gate AttributionGate, gateEnabled bool) bool {
	// The existing gate, unchanged. Weak signals alone describe a working
	// machine rather than an orphan.
	if !HasStrongSignal(candidate.Signals) {
		return false
	}

	// Phase A stops here, and so does any configuration that has not set the
	// separate phase B opt-in by hand.
	if !gateEnabled || gate == nil {
		return true
	}

	// Untrusted data refuses every process until the watcher recovers.
	if !gate.Trusted() {
		return false
	}

	// An unreadable start time has no identity to match, so it can never be
	// confirmed and is left alone.
	if !candidate.Process.CreateTimeKnown {
		return false
	}

	return gate.ConfirmedOrphan(candidate.Process.PID, candidate.Process.CreateTime)
}

// EligibleCandidates returns the candidates the kill path may act on. It exists
// so a test can compare the gated and ungated sets directly over one corpus.
func EligibleCandidates(candidates []OrphanCandidate, gate AttributionGate, gateEnabled bool) []OrphanCandidate {
	var out []OrphanCandidate
	for _, candidate := range candidates {
		if KillEligible(candidate, gate, gateEnabled) {
			out = append(out, candidate)
		}
	}
	return out
}
