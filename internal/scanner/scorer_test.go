package scanner

import (
	"os/user"
	"testing"
	"time"

	"github.com/tjp2021/devreap/internal/config"
	"github.com/tjp2021/devreap/internal/patterns"
)

func testScorer() *Scorer {
	return NewScorer(config.Default().Weights)
}

// known marks a fixture's metadata as successfully read, and owns it by the
// current user unless the fixture set an owner deliberately. Scoring requires
// positively established ownership and positively read metadata, so fixtures
// that are not about missing metadata have to say so.
func known(p ProcessInfo) ProcessInfo {
	p.CreateTimeKnown = !p.CreateTime.IsZero()
	p.TTYKnown = true
	p.UsernameKnown = true
	if p.Username == "" {
		if u, err := user.Current(); err == nil {
			p.Username = u.Username
		}
	}
	return p
}

func TestScorerPPIDIsInit(t *testing.T) {
	scorer := testScorer()
	scorer.ResetCache(nil)

	proc := ProcessInfo{
		PID:        1234,
		PPID:       1,
		Name:       "node",
		CreateTime: time.Now().Add(-1 * time.Hour),
		HasTTY:     true,
	}

	pat := patterns.Pattern{Name: "test", MaxDuration: 24 * time.Hour}
	score, signals := scorer.Score(known(proc), pat)

	if signals["ppid_is_init"] != 0.4 {
		t.Errorf("expected ppid_is_init signal = 0.4, got %f", signals["ppid_is_init"])
	}

	if score < 0.4 {
		t.Errorf("expected score >= 0.4 with PPID=1, got %f", score)
	}
}

func TestScorerNoTTY(t *testing.T) {
	scorer := testScorer()
	scorer.ResetCache(nil)

	proc := ProcessInfo{
		PID:        1234,
		PPID:       5678,
		Name:       "node",
		CreateTime: time.Now().Add(-1 * time.Hour),
		HasTTY:     false,
	}

	pat := patterns.Pattern{Name: "test", MaxDuration: 24 * time.Hour}
	_, signals := scorer.Score(known(proc), pat)

	if signals["no_tty"] != 0.15 {
		t.Errorf("expected no_tty signal = 0.15, got %f", signals["no_tty"])
	}
}

func TestScorerExceededDuration(t *testing.T) {
	scorer := testScorer()
	scorer.ResetCache(nil)

	proc := ProcessInfo{
		PID:        1234,
		PPID:       5678,
		Name:       "ffmpeg",
		CreateTime: time.Now().Add(-5 * time.Hour),
		HasTTY:     true,
	}

	pat := patterns.Pattern{Name: "ffmpeg", MaxDuration: 2 * time.Hour}
	_, signals := scorer.Score(known(proc), pat)

	if signals["exceeded_duration"] != 0.25 {
		t.Errorf("expected exceeded_duration signal = 0.25, got %f", signals["exceeded_duration"])
	}
}

func TestScorerNotExceededDuration(t *testing.T) {
	scorer := testScorer()
	scorer.ResetCache(nil)

	proc := ProcessInfo{
		PID:        1234,
		PPID:       5678,
		Name:       "ffmpeg",
		CreateTime: time.Now().Add(-1 * time.Hour),
		HasTTY:     true,
	}

	pat := patterns.Pattern{Name: "ffmpeg", MaxDuration: 2 * time.Hour}
	_, signals := scorer.Score(known(proc), pat)

	if _, ok := signals["exceeded_duration"]; ok {
		t.Error("expected no exceeded_duration signal for process within time limit")
	}
}

func TestScorerOrphanExample(t *testing.T) {
	// Simulate: MCP server with PPID=1 and no IDE running
	scorer := testScorer()
	// Process list with non-IDE processes only = IDE is dead
	scorer.ResetCache([]ProcessInfo{
		{Name: "Finder", Cmdline: "/System/Library/CoreServices/Finder.app/Contents/MacOS/Finder"},
	})

	proc := ProcessInfo{
		PID:        1234,
		PPID:       1,
		Name:       "node",
		CreateTime: time.Now().Add(-2 * time.Hour),
		HasTTY:     false,
	}

	pat := patterns.Pattern{Name: "mcp-server", MaxDuration: 4 * time.Hour}
	score, signals := scorer.Score(known(proc), pat)

	// PPID=1 (0.4) + no TTY (0.15) + no IDE (0.3) = 0.85
	expectedMin := 0.85
	if score < expectedMin {
		t.Errorf("expected score >= %.2f for full orphan, got %.2f (signals: %v)", expectedMin, score, signals)
	}
}

func TestScorerSafeProcess(t *testing.T) {
	// Simulate: MCP server with real parent and IDE running
	scorer := testScorer()
	// Provide a fake IDE process in the list
	scorer.ResetCache([]ProcessInfo{
		{Name: "Cursor", Cmdline: "/Applications/Cursor.app/Contents/MacOS/Cursor"},
	})

	proc := ProcessInfo{
		PID:        1234,
		PPID:       5678,
		Name:       "node",
		CreateTime: time.Now().Add(-1 * time.Hour),
		HasTTY:     true,
	}

	pat := patterns.Pattern{Name: "mcp-server", MaxDuration: 4 * time.Hour}
	score, _ := scorer.Score(known(proc), pat)

	if score != 0 {
		t.Errorf("expected score 0 for safe process, got %f", score)
	}
}

func TestScorerCap(t *testing.T) {
	scorer := testScorer()
	scorer.ResetCache([]ProcessInfo{
		{Name: "Finder", Cmdline: "/System/Library/CoreServices/Finder.app/Contents/MacOS/Finder"},
	})

	proc := ProcessInfo{
		PID:        1234,
		PPID:       1,
		Name:       "node",
		CreateTime: time.Now().Add(-100 * time.Hour),
		HasTTY:     false,
		Ports:      []uint32{3000},
	}

	pat := patterns.Pattern{Name: "test", MaxDuration: 1 * time.Hour}
	score, _ := scorer.Score(known(proc), pat)

	if score > 1.0 {
		t.Errorf("score should be capped at 1.0, got %f", score)
	}
}

func TestIDEDetectionExactPath(t *testing.T) {
	// CursorUIViewService should NOT be detected as an IDE
	procs := []ProcessInfo{
		{
			Name:    "CursorUIViewService",
			Cmdline: "/System/Library/PrivateFrameworks/TextInputUIMacHelper.framework/Versions/A/XPCServices/CursorUIViewService.xpc/Contents/MacOS/CursorUIViewService",
		},
	}
	if checkIDERunningFromList(procs) {
		t.Error("CursorUIViewService should NOT be detected as an IDE")
	}

	// Actual Cursor IDE SHOULD be detected
	procs = []ProcessInfo{
		{
			Name:    "Cursor",
			Cmdline: "/Applications/Cursor.app/Contents/MacOS/Cursor",
		},
	}
	if !checkIDERunningFromList(procs) {
		t.Error("Cursor IDE should be detected as an IDE")
	}
}

func TestIDEDetectionElectronFalsePositive(t *testing.T) {
	// Antigravity Electron app should NOT be detected as an IDE
	procs := []ProcessInfo{
		{
			Name:    "Electron",
			Cmdline: "/Applications/Antigravity.app/Contents/MacOS/Electron",
		},
	}
	if checkIDERunningFromList(procs) {
		t.Error("Antigravity Electron app should NOT be detected as an IDE")
	}

	// VS Code Electron SHOULD be detected
	procs = []ProcessInfo{
		{
			Name:    "Electron",
			Cmdline: "/Applications/Visual Studio Code.app/Contents/MacOS/Electron",
		},
	}
	if !checkIDERunningFromList(procs) {
		t.Error("VS Code Electron should be detected as an IDE")
	}
}

func TestIDEDetectionClaude(t *testing.T) {
	// Random process with "claude" in its name should NOT be detected
	procs := []ProcessInfo{
		{
			Name:    "claude-helper",
			Cmdline: "/usr/local/bin/claude-helper",
		},
	}
	if checkIDERunningFromList(procs) {
		t.Error("Random claude-helper should NOT be detected as an IDE")
	}

	// Actual Claude Code CLI should be detected
	procs = []ProcessInfo{
		{
			Name:    "node",
			Cmdline: "/opt/homebrew/lib/node_modules/@anthropic-ai/claude-code/cli.js",
		},
	}
	if !checkIDERunningFromList(procs) {
		t.Error("Claude Code CLI should be detected as an IDE")
	}
}

// A listening socket is evidence a process is serving, not evidence it was
// abandoned. It must never contribute to the orphan score.
func TestScorerIgnoresListeningPorts(t *testing.T) {
	scorer := testScorer()
	scorer.ResetCache([]ProcessInfo{
		{Name: "Cursor", Cmdline: "/Applications/Cursor.app/Contents/MacOS/Cursor"},
	})

	proc := ProcessInfo{
		PID:        1234,
		PPID:       5678, // NOT init
		Name:       "node",
		CreateTime: time.Now().Add(-1 * time.Hour),
		HasTTY:     true,
		Ports:      []uint32{3000},
	}

	pat := patterns.Pattern{Name: "test", MaxDuration: 24 * time.Hour}
	score, signals := scorer.Score(known(proc), pat)

	if _, present := signals["has_listener"]; present {
		t.Errorf("has_listener must not be scored, got %f", signals["has_listener"])
	}
	if score != 0 {
		t.Errorf("a healthy port-bound process with a TTY and a live IDE should score 0, got %f", score)
	}
	// Port data is still available to callers for reporting.
	if len(proc.Ports) != 1 {
		t.Error("port data should still be present on ProcessInfo")
	}
}

func TestScorerSkipsOtherUsers(t *testing.T) {
	scorer := testScorer()
	scorer.ResetCache([]ProcessInfo{
		{Name: "Finder", Cmdline: "/System/Library/CoreServices/Finder.app/Contents/MacOS/Finder"},
	})

	proc := ProcessInfo{
		PID:        1234,
		PPID:       1,
		Name:       "node",
		Username:   "_www", // different user
		CreateTime: time.Now().Add(-10 * time.Hour),
		HasTTY:     false,
	}

	pat := patterns.Pattern{Name: "test", MaxDuration: 1 * time.Hour}
	score, _ := scorer.Score(known(proc), pat)

	if score != 0 {
		t.Errorf("expected score 0 for other user's process, got %f", score)
	}
}

// Unknown metadata must never be read as incriminating metadata. gopsutil
// returns a zero value plus an error for processes it cannot inspect, and the
// scorer used to treat those zero values as facts.

// A process whose start time could not be read used to appear to have started
// at the Unix epoch, so exceeded_duration fired on every single scan.
func TestUnknownStartTimeDoesNotFireExceededDuration(t *testing.T) {
	scorer := testScorer()
	scorer.ResetCache(nil)

	proc := known(ProcessInfo{
		PID:  1234,
		PPID: 5678,
		Name: "node",
	})
	proc.CreateTime = time.Time{}
	proc.CreateTimeKnown = false

	pat := patterns.Pattern{Name: "test", MaxDuration: 1 * time.Hour}
	_, signals := scorer.Score(proc, pat)

	if _, fired := signals["exceeded_duration"]; fired {
		t.Error("exceeded_duration must not fire when the start time is unknown")
	}
	if proc.Age() != 0 {
		t.Errorf("Age() must be 0 when the start time is unknown, got %v", proc.Age())
	}
}

// A failed terminal lookup is not the same as having no terminal.
func TestUnknownTTYDoesNotFireNoTTY(t *testing.T) {
	scorer := testScorer()
	scorer.ResetCache(nil)

	proc := known(ProcessInfo{
		PID:        1234,
		PPID:       5678,
		Name:       "node",
		CreateTime: time.Now().Add(-1 * time.Hour),
	})
	proc.HasTTY = false
	proc.TTYKnown = false

	pat := patterns.Pattern{Name: "test", MaxDuration: 24 * time.Hour}
	_, signals := scorer.Score(proc, pat)

	if _, fired := signals["no_tty"]; fired {
		t.Error("no_tty must not fire when the terminal could not be read")
	}
}

// An unreadable owner used to slip past the ownership guard entirely, because
// the guard only rejected a username that was both known and different.
func TestUnknownOwnerIsNotKillEligible(t *testing.T) {
	scorer := testScorer()
	scorer.ResetCache([]ProcessInfo{
		{Name: "Finder", Cmdline: "/System/Library/CoreServices/Finder.app/Contents/MacOS/Finder"},
	})

	// Every other signal is maximally incriminating.
	proc := ProcessInfo{
		PID:             1234,
		PPID:            1,
		Name:            "node",
		CreateTime:      time.Now().Add(-100 * time.Hour),
		CreateTimeKnown: true,
		HasTTY:          false,
		TTYKnown:        true,
		Username:        "",
		UsernameKnown:   false,
	}

	pat := patterns.Pattern{Name: "test", MaxDuration: 1 * time.Hour}
	score, _ := scorer.Score(proc, pat)

	if score != 0 {
		t.Errorf("a process with an unreadable owner must score 0, got %f", score)
	}
}

// The strong-signal gate. On 2026-08-14 this daemon killed live MCP servers
// and dev processes on weak signals alone: every kill in the log scored 0.70
// from exceeded_duration + no_tty + parent_ide_dead, with ppid_is_init absent.
// The processes had not lost their parents; the user had merely closed an
// editor somewhere on the machine.
func TestWeakSignalsAloneAreNotKillEligible(t *testing.T) {
	scorer := testScorer()
	// No IDE anywhere on the machine, so parent_ide_dead fires for everything.
	scorer.ResetCache([]ProcessInfo{
		{Name: "Finder", Cmdline: "/System/Library/CoreServices/Finder.app/Contents/MacOS/Finder"},
	})

	proc := known(ProcessInfo{
		PID:        1234,
		PPID:       5678, // real live parent — NOT reparented to launchd
		Name:       "node",
		Cmdline:    "node /Users/dev/.npm/_npx/abc/node_modules/.bin/mcp-youtube-transcript",
		CreateTime: time.Now().Add(-9 * time.Hour),
		HasTTY:     false,
	})

	pat := patterns.Pattern{Name: "node-mcp-server", MaxDuration: 4 * time.Hour}
	score, signals := scorer.Score(proc, pat)

	// The weak signals still fire, and still clear the 0.6 threshold. That is
	// exactly the shape of the 2026-08-14 kills.
	if score < 0.6 {
		t.Fatalf("expected the weak signals to still clear the threshold, got %f", score)
	}
	if signals["ppid_is_init"] != 0 {
		t.Fatal("fixture is wrong: ppid_is_init must not fire for a process with a live parent")
	}

	// But it must not be killable.
	if HasStrongSignal(signals) {
		t.Error("weak signals alone must not make a process kill-eligible")
	}
}

// The same process, once genuinely reparented to launchd, is killable.
func TestStrongSignalMakesCandidateKillEligible(t *testing.T) {
	scorer := testScorer()
	scorer.ResetCache([]ProcessInfo{
		{Name: "Finder", Cmdline: "/System/Library/CoreServices/Finder.app/Contents/MacOS/Finder"},
	})

	proc := known(ProcessInfo{
		PID:        1234,
		PPID:       1, // reparented to launchd — the parent really did die
		Name:       "node",
		CreateTime: time.Now().Add(-9 * time.Hour),
		HasTTY:     false,
	})

	pat := patterns.Pattern{Name: "node-mcp-server", MaxDuration: 4 * time.Hour}
	_, signals := scorer.Score(proc, pat)

	if !HasStrongSignal(signals) {
		t.Error("ppid_is_init must make a candidate kill-eligible")
	}
}
