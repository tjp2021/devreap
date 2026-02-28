package scanner

import (
	"sort"
	"strings"

	"github.com/tjp2021/devreap/internal/patterns"
)

// OrphanCandidate is a process that matched a pattern and was scored for orphan likelihood.
type OrphanCandidate struct {
	Process ProcessInfo
	Pattern patterns.Pattern
	Score   float64
	Signals map[string]float64 // signal name → contribution
}

// Reasons returns the names of signals that contributed to this candidate's score.
func (o *OrphanCandidate) Reasons() []string {
	var reasons []string
	for name, score := range o.Signals {
		if score > 0 {
			reasons = append(reasons, name)
		}
	}
	return reasons
}

// FindAllMatches returns all processes that match a pattern, scored but not threshold-filtered.
func FindAllMatches(procs []ProcessInfo, registry *patterns.Registry, scorer *Scorer, allowlist []string) []OrphanCandidate {
	var candidates []OrphanCandidate

	for _, proc := range procs {
		if isAllowlisted(proc, allowlist) {
			continue
		}

		match := registry.Match(proc.Name, proc.Args)
		if match == nil {
			continue
		}

		score, signals := scorer.Score(proc, match.Pattern)
		candidates = append(candidates, OrphanCandidate{
			Process: proc,
			Pattern: match.Pattern,
			Score:   score,
			Signals: signals,
		})
	}
	return candidates
}

// FindOrphans is a convenience function that finds all matches and filters by threshold.
func FindOrphans(procs []ProcessInfo, registry *patterns.Registry, scorer *Scorer, threshold float64, allowlist []string) []OrphanCandidate {
	return FilterAndSortOrphans(FindAllMatches(procs, registry, scorer, allowlist), threshold)
}

// FilterAndSortOrphans filters matches by threshold and sorts parents-before-children.
func FilterAndSortOrphans(all []OrphanCandidate, threshold float64) []OrphanCandidate {
	var candidates []OrphanCandidate
	for _, c := range all {
		if c.Score >= threshold {
			candidates = append(candidates, c)
		}
	}

	// Sort: parents before children (lower PID first within same tree).
	// This ensures we kill the parent process first, which is cleaner
	// than killing children and leaving orphaned parents.
	// Use SliceStable for deterministic ordering when scores are equal.
	sort.SliceStable(candidates, func(i, j int) bool {
		// If j is a child of i, i should come first
		if candidates[j].Process.PPID == candidates[i].Process.PID {
			return true
		}
		// If i is a child of j, j should come first
		if candidates[i].Process.PPID == candidates[j].Process.PID {
			return false
		}
		// Otherwise sort by score descending (kill worst offenders first)
		return candidates[i].Score > candidates[j].Score
	})

	return candidates
}

func isAllowlisted(proc ProcessInfo, allowlist []string) bool {
	for _, pattern := range allowlist {
		if pattern == "" {
			continue
		}
		nameLower := strings.ToLower(proc.Name)
		patternLower := strings.ToLower(pattern)
		if nameLower == patternLower {
			return true
		}
		// Also check if cmdline contains the allowlist entry
		if strings.Contains(strings.ToLower(proc.Cmdline), patternLower) {
			return true
		}
	}
	return false
}
