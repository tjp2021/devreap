package scanner

import (
	"testing"
	"time"

	"github.com/tjp2021/devreap/internal/config"
	"github.com/tjp2021/devreap/internal/patterns"
)

func testScorer() *Scorer {
	return NewScorer(config.Default().Weights)
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
	score, signals := scorer.Score(proc, pat)

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
	_, signals := scorer.Score(proc, pat)

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
	_, signals := scorer.Score(proc, pat)

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
	_, signals := scorer.Score(proc, pat)

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
	score, signals := scorer.Score(proc, pat)

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
	score, _ := scorer.Score(proc, pat)

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
	score, _ := scorer.Score(proc, pat)

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

	// Actual Claude Code CLI (node_modules install) should be detected
	procs = []ProcessInfo{
		{
			Name:    "node",
			Cmdline: "/opt/homebrew/lib/node_modules/@anthropic-ai/claude-code/cli.js",
		},
	}
	if !checkIDERunningFromList(procs) {
		t.Error("Claude Code CLI (node_modules) should be detected as an IDE")
	}

	// Claude Code native binary (installed via ~/.local/share/claude/) should be detected via Exe path
	procs = []ProcessInfo{
		{
			Name:    "2.1.74",
			Exe:     "/Users/testuser/.local/share/claude/versions/2.1.74",
			Cmdline: "claude --dangerously-skip-permissions",
		},
	}
	if !checkIDERunningFromList(procs) {
		t.Error("Claude Code native binary should be detected as an IDE via Exe path")
	}
}

func TestScorerHasListenerIndependent(t *testing.T) {
	// has_listener should fire even without PPID=1
	scorer := testScorer()
	scorer.ResetCache([]ProcessInfo{
		{Name: "Cursor", Cmdline: "/Applications/Cursor.app/Contents/MacOS/Cursor"},
	})

	proc := ProcessInfo{
		PID:        1234,
		PPID:       5678, // NOT init
		Name:       "node",
		CreateTime: time.Now().Add(-1 * time.Hour),
		HasTTY:     true, // HAS a TTY — should still fire because it has ports
		Ports:      []uint32{3000},
	}

	pat := patterns.Pattern{Name: "test", MaxDuration: 24 * time.Hour}
	_, signals := scorer.Score(proc, pat)

	if signals["has_listener"] != 0.2 {
		t.Errorf("expected has_listener = 0.2 (should fire with ports regardless of TTY), got %f", signals["has_listener"])
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
	score, _ := scorer.Score(proc, pat)

	if score != 0 {
		t.Errorf("expected score 0 for other user's process, got %f", score)
	}
}
