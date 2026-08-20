package scanner

import (
	"math/rand"
	"testing"
	"time"

	"github.com/tjp2021/devreap/internal/patterns"
)

// fixtureGate answers from a fixed set, so a test drives both answers without a
// watcher.
type fixtureGate struct {
	trusted   bool
	confirmed map[int32]time.Time
	asked     int
}

func (g *fixtureGate) Trusted() bool { return g.trusted }

func (g *fixtureGate) ConfirmedOrphan(pid int32, startTime time.Time) bool {
	g.asked++
	start, known := g.confirmed[pid]
	if !known {
		return false
	}
	// Identity is the pair, so a reused identifier does not match.
	return start.Equal(startTime)
}

// corpus builds a spread of candidates: some with the strong signal, some
// without, some with unreadable start times, across every pattern class.
func corpus(t *testing.T) []OrphanCandidate {
	t.Helper()
	seed := rand.New(rand.NewSource(20260820))
	base := time.Date(2026, 8, 20, 5, 0, 0, 0, time.UTC)
	classes := []string{"mcp", "dev-server", "headless-browser", "media"}

	var out []OrphanCandidate
	for i := 0; i < 200; i++ {
		pid := int32(1000 + i)
		signals := map[string]float64{}
		if i%3 != 0 {
			signals["ppid_is_init"] = 0.40
		}
		if i%2 == 0 {
			signals["no_tty"] = 0.15
		}
		if i%5 == 0 {
			signals["parent_ide_dead"] = 0.30
		}
		if i%7 == 0 {
			signals["exceeded_duration"] = 0.25
		}

		process := ProcessInfo{
			PID:             pid,
			PPID:            1,
			Name:            "node",
			Cmdline:         "node ./server.js",
			CreateTime:      base.Add(time.Duration(i) * time.Second),
			CreateTimeKnown: i%11 != 0,
			UsernameKnown:   true,
			Username:        "dev",
		}
		if !process.CreateTimeKnown {
			process.CreateTime = time.Time{}
		}

		out = append(out, OrphanCandidate{
			Process:      process,
			Pattern:      patterns.Pattern{Name: "p", Category: classes[seed.Intn(len(classes))]},
			Score:        0.55 + float64(i%5)/10,
			Signals:      signals,
			KillEligible: HasStrongSignal(signals),
		})
	}
	return out
}

// TestWatcherDownMatchesTodaysEligibleSet is the degradation test. With no
// watcher, no gate, or the gate switched off, the eligible set equals today's
// eligible set exactly.
func TestWatcherDownMatchesTodaysEligibleSet(t *testing.T) {
	all := corpus(t)

	today := map[int32]bool{}
	for _, candidate := range all {
		if HasStrongSignal(candidate.Signals) {
			today[candidate.Process.PID] = true
		}
	}
	if len(today) == 0 {
		t.Fatal("the corpus produced no eligible candidates to compare")
	}

	cases := map[string]struct {
		gate    AttributionGate
		enabled bool
	}{
		"no gate at all":                {nil, false},
		"gate present but switched off": {&fixtureGate{trusted: true}, false},
		"gate switched off while stale": {&fixtureGate{trusted: false}, false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := map[int32]bool{}
			for _, candidate := range EligibleCandidates(all, tc.gate, tc.enabled) {
				got[candidate.Process.PID] = true
			}
			if len(got) != len(today) {
				t.Fatalf("eligible set holds %d, want today's %d", len(got), len(today))
			}
			for pid := range today {
				if !got[pid] {
					t.Errorf("process %d is eligible today and was dropped with the watcher down", pid)
				}
			}
			if gate, ok := tc.gate.(*fixtureGate); ok && gate.asked != 0 {
				t.Errorf("the gate was consulted %d times while switched off", gate.asked)
			}
		})
	}
}

// TestPhaseBEligibilityIsSubset asserts the property the whole design rests on,
// across the whole fixture corpus: attribution can only subtract.
func TestPhaseBEligibilityIsSubset(t *testing.T) {
	all := corpus(t)

	ungated := map[int32]bool{}
	for _, candidate := range EligibleCandidates(all, nil, false) {
		ungated[candidate.Process.PID] = true
	}

	// Build a gate that confirms a mixture: some candidates that are eligible
	// today, some that are not, and one whose start time does not match.
	gate := &fixtureGate{trusted: true, confirmed: map[int32]time.Time{}}
	for i, candidate := range all {
		switch i % 4 {
		case 0:
			gate.confirmed[candidate.Process.PID] = candidate.Process.CreateTime
		case 1:
			// A confirmation for a process that never earned a strong signal. It
			// must not become eligible.
			gate.confirmed[candidate.Process.PID] = candidate.Process.CreateTime
		case 2:
			// A reused identifier: the pair does not match.
			gate.confirmed[candidate.Process.PID] = candidate.Process.CreateTime.Add(time.Hour)
		}
	}

	gated := map[int32]bool{}
	for _, candidate := range EligibleCandidates(all, gate, true) {
		gated[candidate.Process.PID] = true
	}

	for pid := range gated {
		if !ungated[pid] {
			t.Errorf("process %d is kill-eligible with attribution and was not without it", pid)
		}
	}
	if len(gated) >= len(ungated) {
		t.Errorf("gated set holds %d and ungated holds %d, so the gate subtracted nothing", len(gated), len(ungated))
	}
	if len(gated) == 0 {
		t.Error("the gate refused everything, so the subset assertion proves nothing")
	}
}

// TestAttributionNeverAddsEligibility drives the adversarial case directly: a
// gate that confirms every process on the machine.
func TestAttributionNeverAddsEligibility(t *testing.T) {
	all := corpus(t)

	confirmEverything := &fixtureGate{trusted: true, confirmed: map[int32]time.Time{}}
	for _, candidate := range all {
		confirmEverything.confirmed[candidate.Process.PID] = candidate.Process.CreateTime
	}

	ungated := EligibleCandidates(all, nil, false)
	gated := EligibleCandidates(all, confirmEverything, true)

	if len(gated) > len(ungated) {
		t.Fatalf("a gate confirming everything produced %d eligible, more than today's %d", len(gated), len(ungated))
	}
	for _, candidate := range gated {
		if !HasStrongSignal(candidate.Signals) {
			t.Errorf("process %d became eligible with no strong lifecycle signal", candidate.Process.PID)
		}
	}
}

// TestUntrustedGateRefusesEveryProcess asserts the failure direction.
func TestUntrustedGateRefusesEveryProcess(t *testing.T) {
	all := corpus(t)
	stale := &fixtureGate{trusted: false, confirmed: map[int32]time.Time{}}
	for _, candidate := range all {
		stale.confirmed[candidate.Process.PID] = candidate.Process.CreateTime
	}

	if got := EligibleCandidates(all, stale, true); len(got) != 0 {
		t.Errorf("a stale watcher left %d processes eligible, want none", len(got))
	}
}

// TestUnreadableStartTimeIsNeverConfirmed asserts a process with no identity to
// match is left alone rather than matched by identifier alone.
func TestUnreadableStartTimeIsNeverConfirmed(t *testing.T) {
	candidate := OrphanCandidate{
		Process: ProcessInfo{PID: 4242, CreateTimeKnown: false},
		Signals: map[string]float64{"ppid_is_init": 0.40},
	}
	gate := &fixtureGate{trusted: true, confirmed: map[int32]time.Time{4242: {}}}

	if KillEligible(candidate, gate, true) {
		t.Error("a process with an unreadable start time was confirmed")
	}
	if !KillEligible(candidate, nil, false) {
		t.Error("the ungated path changed for a process with an unreadable start time")
	}
}
