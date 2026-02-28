package scanner

import (
	"context"

	"github.com/tjp2021/devreap/internal/config"
	"github.com/tjp2021/devreap/internal/patterns"
)

// Scanner enumerates running processes, matches them against patterns,
// and scores them for orphan likelihood.
type Scanner struct {
	registry  *patterns.Registry
	scorer    *Scorer
	threshold float64
	allowlist []string
}

// New creates a Scanner with the given pattern registry and config.
func New(registry *patterns.Registry, cfg *config.Config) *Scanner {
	return &Scanner{
		registry:  registry,
		scorer:    NewScorer(cfg.Weights),
		threshold: cfg.KillThreshold,
		allowlist: cfg.Allowlist,
	}
}

// ScanResult holds the output of a single scan cycle.
type ScanResult struct {
	TotalProcesses int
	Matched        int
	Orphans        []OrphanCandidate
	AllMatches     []OrphanCandidate // all pattern matches (including below-threshold), populated when verbose
	Processes      []ProcessInfo     // full snapshot for reuse by callers
}

// Scan enumerates processes and scores them for orphan likelihood.
// The context allows the caller to cancel/timeout the scan.
func (s *Scanner) Scan(ctx context.Context) (*ScanResult, error) {
	procs, err := EnumerateProcesses(ctx)
	if err != nil {
		return nil, err
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Pass process list to scorer to avoid double enumeration
	s.scorer.ResetCache(procs)

	// Single pass: get all matches (includes scoring)
	allMatches := FindAllMatches(procs, s.registry, s.scorer, s.allowlist)

	// Filter by threshold and sort parents-before-children
	orphans := FilterAndSortOrphans(allMatches, s.threshold)

	return &ScanResult{
		TotalProcesses: len(procs),
		Matched:        len(allMatches),
		Orphans:        orphans,
		AllMatches:     allMatches,
		Processes:      procs,
	}, nil
}
