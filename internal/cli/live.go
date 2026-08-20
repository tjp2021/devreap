package cli

import (
	"context"

	"github.com/tjp2021/devreap/internal/attribution"
	"github.com/tjp2021/devreap/internal/scanner"
)

// liveRSS returns a lookup that reconciles the store against the machine as it
// is now.
//
// Resident memory is a live number and the journal is a record of the past, so
// the read-only surfaces enumerate once and match by the pair of identifier and
// start time. A record whose process is gone reads as not alive rather than as a
// process using zero memory.
func liveRSS() attribution.LiveLookup {
	procs, err := scanner.EnumerateProcesses(context.Background())
	if err != nil {
		// Without a live read nothing can be reconciled. Reporting every record
		// as gone is wrong, so the lookup reports memory it does not know as
		// zero and leaves the record visible.
		return func(attribution.ProcKey) (uint64, bool) { return 0, true }
	}

	type liveProc struct {
		rss   uint64
		start attribution.ProcKey
	}
	byPID := make(map[int32]liveProc, len(procs))
	for _, proc := range procs {
		entry := liveProc{rss: proc.MemRSS}
		if proc.CreateTimeKnown {
			entry.start = attribution.NewProcKey(proc.PID, proc.CreateTime)
		}
		byPID[proc.PID] = entry
	}

	return func(key attribution.ProcKey) (uint64, bool) {
		live, present := byPID[key.PID]
		if !present {
			return 0, false
		}
		// Identity is the pair. A reused identifier is a different process, and
		// reporting its memory against the old record would be a lie.
		if !live.start.Equal(key) {
			return 0, false
		}
		return live.rss, true
	}
}
