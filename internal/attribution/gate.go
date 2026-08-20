package attribution

import "time"

// Gate adapts the lifecycle engine to the restriction the kill path consults.
//
// It answers one question about one process, and it can express nothing except
// "leave this one alone". Attribution can never make a process eligible that the
// existing path would not already permit, so this type has no method that could
// widen anything.
type Gate struct {
	watcher *Watcher
}

// NewGate returns the restriction backed by a running watcher.
func NewGate(w *Watcher) *Gate { return &Gate{watcher: w} }

// Trusted reports whether attribution data is current. A stale or stopped
// watcher answers false, and the kill path then refuses every process rather
// than waving them through.
func (g *Gate) Trusted() bool {
	if g == nil || g.watcher == nil {
		return false
	}
	return g.watcher.Healthy()
}

// ConfirmedOrphan reports whether the engine independently reached
// CONFIRMED_ORPHAN for this exact process, with all five conditions of R6
// holding on the latest observation.
//
// Identity is the pair of identifier and start time, so a reused identifier
// never matches the record of the process that held the number before it.
func (g *Gate) ConfirmedOrphan(pid int32, startTime time.Time) bool {
	if g == nil || g.watcher == nil || startTime.IsZero() {
		return false
	}
	return g.watcher.Engine().Eligible(NewProcKey(pid, startTime))
}
