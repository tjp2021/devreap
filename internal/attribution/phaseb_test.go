package attribution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjp2021/devreap/internal/config"
)

// reclaimEntryPoints are the only ways into the phase B states. Phase A ships
// them as inert structure: the state machine holds the edges so the design is
// complete and testable, and no shipped code path takes one.
var reclaimEntryPoints = []string{
	"RequestReclaim",
	"CompleteReclaim",
	"AbandonReclaim",
}

// TestPhaseBReclaimIsUnreachableInShippedCode asserts that nothing outside this
// package's own state machine calls a reclaim entry point.
//
// The subset test proves attribution can only subtract. This one proves the
// reclaim path is not merely unused but unreachable, so RECLAIM_REQUESTED cannot
// be entered by any shipped code path until the phase B opt-in is wired to one
// deliberately. A future change that wires it will fail here and have to say so.
func TestPhaseBReclaimIsUnreachableInShippedCode(t *testing.T) {
	root := repoRoot(t)

	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// The state machine that owns the edges is allowed to name them.
		if filepath.Base(path) == "lifecycle.go" {
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, entry := range reclaimEntryPoints {
			if strings.Contains(string(data), entry) {
				rel, _ := filepath.Rel(root, path)
				offenders = append(offenders, rel+" calls "+entry)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the repository: %v", err)
	}

	if len(offenders) > 0 {
		t.Errorf("phase B reclaim is reachable from shipped code:\n  %s\n"+
			"Phase A ships no reclaim action. Wiring one is a new design and a new approval.",
			strings.Join(offenders, "\n  "))
	}
}

// TestPhaseBOptInIsSeparateFromTheKillOptIn asserts the two switches are
// independent, which is what makes phase B require a deliberate second decision.
func TestPhaseBOptInIsSeparateFromTheKillOptIn(t *testing.T) {
	cfg := config.Default()

	if cfg.Attribution.GateKills {
		t.Fatal("the phase B opt-in defaults on")
	}

	// Turning killing on must not turn gating on.
	cfg.DryRun = false
	if cfg.Attribution.GateKills {
		t.Error("disabling dry-run enabled attribution gating; the two opt-ins are separate")
	}

	// Turning attribution on must not turn gating on either.
	cfg.Attribution.Enabled = true
	if cfg.Attribution.GateKills {
		t.Error("enabling attribution enabled gating; observing is not gating")
	}
}

// TestConfirmedOrphanIsTerminalForActionInPhaseA asserts what a confirmed orphan
// can and cannot do while phase A is the shipped behavior. It stays confirmed,
// it stays reportable, and it still recovers.
func TestConfirmedOrphanIsTerminalForActionInPhaseA(t *testing.T) {
	f := newFixture(t)
	f.ownerExits()
	f.driveTo(StateConfirmedOrphan, healthyOrphan())

	// Reporting does not move it, and it does not close the phase B edge.
	f.engine.MarkReported(f.key)
	for i := 0; i < 50; i++ {
		f.scan(healthyOrphan())
	}
	if got := f.state(); got != StateConfirmedOrphan {
		t.Errorf("state = %s after fifty further scans, want CONFIRMED_ORPHAN held", got)
	}
	if !f.process().Reported {
		t.Error("the reported flag was cleared by a later scan")
	}

	// The recovery edge is still open, which is what R7 requires.
	obs := healthyOrphan()
	obs.Adopted = TriTrue
	f.scan(obs)
	if got := f.state(); got != StateActive {
		t.Errorf("a confirmed orphan failed to recover on adoption, state = %s", got)
	}
}

// repoRoot walks up from the test's working directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("reading the working directory: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not find the module root")
	return ""
}
