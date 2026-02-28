package scanner

import (
	"testing"
	"time"

	"github.com/tjp2021/devreap/internal/config"
	"github.com/tjp2021/devreap/internal/patterns"
)

func defaultWeights() config.WeightConfig {
	return config.Default().Weights
}

func TestAllowlistSkipsProcess(t *testing.T) {
	proc := ProcessInfo{
		PID:     1234,
		Name:    "node",
		Cmdline: "node /path/to/my-special-server.js",
	}

	// Process name match
	if !isAllowlisted(proc, []string{"node"}) {
		t.Error("expected process to be allowlisted by name")
	}

	// Cmdline match
	if !isAllowlisted(proc, []string{"my-special-server"}) {
		t.Error("expected process to be allowlisted by cmdline match")
	}

	// No match
	if isAllowlisted(proc, []string{"python", "ffmpeg"}) {
		t.Error("expected process to NOT be allowlisted")
	}

	// Empty allowlist
	if isAllowlisted(proc, nil) {
		t.Error("expected empty allowlist to not match anything")
	}
}

func TestAllowlistEmptyStringDoesNotMatchAll(t *testing.T) {
	proc := ProcessInfo{
		PID:     1234,
		Name:    "node",
		Cmdline: "node server.js",
	}

	// Empty string in allowlist should NOT match everything
	if isAllowlisted(proc, []string{""}) {
		t.Error("empty string in allowlist should NOT match any process")
	}

	// Mix of empty and valid entries — only valid should match
	if !isAllowlisted(proc, []string{"", "node"}) {
		t.Error("valid entry after empty string should still match")
	}
}

func TestAllowlistCaseInsensitive(t *testing.T) {
	proc := ProcessInfo{
		PID:     1234,
		Name:    "Node",
		Cmdline: "Node server.js",
	}

	if !isAllowlisted(proc, []string{"node"}) {
		t.Error("allowlist should be case insensitive")
	}
}

func TestFindOrphansRespectsAllowlist(t *testing.T) {
	procs := []ProcessInfo{
		{
			PID:        1234,
			PPID:       1,
			Name:       "node",
			Args:       "mcp-server-test",
			Cmdline:    "node mcp-server-test",
			CreateTime: time.Now().Add(-10 * time.Hour),
			HasTTY:     false,
		},
	}

	// Use a real registry (which has patterns loaded)
	registry, err := patterns.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry error: %v", err)
	}

	scorer := NewScorer(defaultWeights())
	scorer.ResetCache([]ProcessInfo{
		{Name: "Finder", Cmdline: "/System/Library/CoreServices/Finder.app/Contents/MacOS/Finder"},
	}) // no IDE — list has processes but none are IDEs

	// With allowlist matching the cmdline, should get 0 orphans
	orphans := FindOrphans(procs, registry, scorer, 0.6, []string{"mcp-server-test"})
	if len(orphans) != 0 {
		t.Errorf("expected 0 orphans with allowlist, got %d", len(orphans))
	}
}

func TestFindAllMatches_EmptyProcessList(t *testing.T) {
	registry, err := patterns.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry error: %v", err)
	}
	scorer := NewScorer(defaultWeights())
	scorer.ResetCache(nil)

	matches := FindAllMatches(nil, registry, scorer, nil)
	if len(matches) != 0 {
		t.Errorf("expected 0 matches for empty process list, got %d", len(matches))
	}
}

func TestFilterAndSortOrphans_ParentBeforeChild(t *testing.T) {
	all := []OrphanCandidate{
		{
			Process: ProcessInfo{PID: 200, PPID: 100},
			Score:   0.8,
		},
		{
			Process: ProcessInfo{PID: 100, PPID: 1},
			Score:   0.7,
		},
	}

	sorted := FilterAndSortOrphans(all, 0.6)
	if len(sorted) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(sorted))
	}

	// Parent (PID 100) should come before child (PID 200)
	if sorted[0].Process.PID != 100 {
		t.Errorf("expected parent PID 100 first, got PID %d", sorted[0].Process.PID)
	}
	if sorted[1].Process.PID != 200 {
		t.Errorf("expected child PID 200 second, got PID %d", sorted[1].Process.PID)
	}
}

func TestFilterAndSortOrphans_ThresholdFiltering(t *testing.T) {
	all := []OrphanCandidate{
		{Process: ProcessInfo{PID: 1}, Score: 0.3},
		{Process: ProcessInfo{PID: 2}, Score: 0.7},
		{Process: ProcessInfo{PID: 3}, Score: 0.6},
		{Process: ProcessInfo{PID: 4}, Score: 0.59},
	}

	filtered := FilterAndSortOrphans(all, 0.6)
	if len(filtered) != 2 {
		t.Errorf("expected 2 candidates above threshold 0.6, got %d", len(filtered))
	}
}

func TestFilterAndSortOrphans_ScoreDescending(t *testing.T) {
	// Two unrelated orphans (neither is parent of the other) — should sort by score descending
	// Use PIDs that don't match each other's PPID to avoid triggering parent-child sort
	all := []OrphanCandidate{
		{Process: ProcessInfo{PID: 5000, PPID: 1}, Score: 0.65},
		{Process: ProcessInfo{PID: 6000, PPID: 1}, Score: 0.9},
	}

	sorted := FilterAndSortOrphans(all, 0.6)
	if sorted[0].Score < sorted[1].Score {
		t.Error("expected higher score first for unrelated orphans")
	}
}

func TestReasons(t *testing.T) {
	oc := OrphanCandidate{
		Signals: map[string]float64{
			"ppid_is_init":    0.4,
			"no_tty":          0.15,
			"parent_ide_dead": 0.3,
		},
	}

	reasons := oc.Reasons()
	if len(reasons) != 3 {
		t.Errorf("expected 3 reasons, got %d", len(reasons))
	}

	// Zero-value signals should be excluded
	oc2 := OrphanCandidate{
		Signals: map[string]float64{
			"ppid_is_init": 0.4,
			"no_tty":       0.0,
		},
	}
	reasons2 := oc2.Reasons()
	if len(reasons2) != 1 {
		t.Errorf("expected 1 reason (0-value excluded), got %d", len(reasons2))
	}
}

func TestIDEDetection_EmptyProcessList(t *testing.T) {
	// Empty list should assume IDE is alive (conservative)
	if !checkIDERunningFromList(nil) {
		t.Error("empty process list should assume IDE is alive (conservative)")
	}
	if !checkIDERunningFromList([]ProcessInfo{}) {
		t.Error("empty process list should assume IDE is alive (conservative)")
	}
}

func TestIDEDetection_AllSignatures(t *testing.T) {
	// Test each IDE signature is detected
	tests := []struct {
		name    string
		cmdline string
		procName string
	}{
		{"VS Code", "/Applications/Visual Studio Code.app/Contents/MacOS/Electron", "Electron"},
		{"Cursor", "/Applications/Cursor.app/Contents/MacOS/Cursor", "Cursor"},
		{"Claude Code", "/opt/homebrew/lib/node_modules/@anthropic-ai/claude-code/cli.js", "node"},
		{"Windsurf", "/Applications/Windsurf.app/Contents/MacOS/Windsurf", "Windsurf"},
		{"Zed (path)", "/Applications/Zed.app/Contents/MacOS/zed", "zed-app"},
		{"Zed (name)", "/usr/local/bin/zed", "zed"},
		{"IntelliJ", "/Applications/IntelliJ IDEA.app/Contents/MacOS/idea", "idea"},
		{"WebStorm", "/Applications/WebStorm.app/Contents/MacOS/webstorm", "webstorm"},
		{"GoLand", "/Applications/GoLand.app/Contents/MacOS/goland", "goland"},
		{"PyCharm", "/Applications/PyCharm.app/Contents/MacOS/pycharm", "pycharm"},
		{"Xcode", "/Applications/Xcode.app/Contents/MacOS/Xcode", "Xcode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			procs := []ProcessInfo{{Name: tt.procName, Cmdline: tt.cmdline}}
			if !checkIDERunningFromList(procs) {
				t.Errorf("%s should be detected as an IDE", tt.name)
			}
		})
	}
}

func TestScorerResetCache(t *testing.T) {
	scorer := testScorer()

	// First scan: no IDE
	scorer.ResetCache([]ProcessInfo{
		{Name: "Finder", Cmdline: "/System/Library/CoreServices/Finder.app/Contents/MacOS/Finder"},
	})
	proc := ProcessInfo{PID: 1, PPID: 1, Name: "node", CreateTime: time.Now()}
	pat := patterns.Pattern{Name: "test", MaxDuration: 24 * time.Hour}
	score1, _ := scorer.Score(proc, pat)

	// Second scan: IDE is running
	scorer.ResetCache([]ProcessInfo{
		{Name: "Cursor", Cmdline: "/Applications/Cursor.app/Contents/MacOS/Cursor"},
	})
	score2, _ := scorer.Score(proc, pat)

	// Score should be different because IDE state changed
	if score1 == score2 {
		t.Error("score should differ between scans with different IDE states")
	}
	if score2 >= score1 {
		t.Error("score should be lower when IDE is running")
	}
}
