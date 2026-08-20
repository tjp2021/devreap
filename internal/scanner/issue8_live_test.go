package scanner

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tjp2021/devreap/internal/patterns"
)

// Live coverage for issue #8: a terminal-launched Claude Code session was not
// recognised as an IDE, so parent_ide_dead fired while the agent was running.
//
// The unit fixtures alone could not catch this. They described a process named
// "claude", and macOS reports "claude.exe" from p_comm, so the fixtures agreed
// with each other and disagreed with the machine. These tests therefore read
// the real process table.
//
// They skip when no terminal-launched session is running, which keeps CI and
// other machines green. On a machine that has one, they fail unless the
// session is detected.

// terminalLaunchedClaude returns the live processes that were started as a bare
// "claude" command, with every other IDE process excluded. Isolating them
// matters: an editor or a second agent elsewhere on the machine would satisfy
// the detector on its own and hide the bug.
func terminalLaunchedClaude(t *testing.T) []ProcessInfo {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	procs, err := EnumerateProcesses(ctx)
	if err != nil {
		t.Skipf("could not enumerate processes: %v", err)
	}

	var found []ProcessInfo
	for _, p := range procs {
		fields := strings.Fields(p.Cmdline)
		if len(fields) == 0 || fields[0] != "claude" {
			continue
		}
		// Keep only sessions that no other signature would rescue, so a pass
		// proves the Claude signatures did the work.
		if checkIDERunningFromList([]ProcessInfo{stripClaudeEvidence(p)}) {
			continue
		}
		found = append(found, p)
	}

	if len(found) == 0 {
		t.Skip("no terminal-launched claude session is running on this machine")
	}
	return found
}

// stripClaudeEvidence blanks the fields the Claude signatures read, leaving the
// process matchable only by an unrelated signature.
func stripClaudeEvidence(p ProcessInfo) ProcessInfo {
	p.Name = "stripped"
	p.Exe = ""
	p.Cmdline = ""
	return p
}

func TestIssue8TerminalClaudeIsDetectedLive(t *testing.T) {
	procs := terminalLaunchedClaude(t)

	for _, p := range procs {
		t.Logf("live session: pid=%d name=%q cmdline=%q exe=%q", p.PID, p.Name, p.Cmdline, p.Exe)
	}

	if !checkIDERunningFromList(procs) {
		t.Fatalf("a terminal-launched Claude Code session was not detected as an IDE (issue #8); %d live session(s) examined", len(procs))
	}
}

// The signal, not just the detector: with only those sessions on the machine,
// parent_ide_dead must stay silent.
func TestIssue8ParentIDEDeadStaysSilentLive(t *testing.T) {
	procs := terminalLaunchedClaude(t)

	scorer := testScorer()
	scorer.ResetCache(procs)

	target := known(ProcessInfo{
		PID:        4242,
		PPID:       900, // live parent
		Name:       "node",
		Cmdline:    "node /opt/homebrew/bin/gws mcp --services drive,gmail",
		Args:       "/opt/homebrew/bin/gws mcp --services drive,gmail",
		CreateTime: time.Now().Add(-9 * time.Hour),
	})

	pat := patterns.Pattern{Name: "node-mcp-server", MaxDuration: 4 * time.Hour}
	score, signals := scorer.Score(target, pat)

	if _, fired := signals["parent_ide_dead"]; fired {
		t.Errorf("parent_ide_dead fired while %d terminal-launched Claude Code session(s) were running", len(procs))
	}
	if score >= 0.6 {
		t.Errorf("score %.2f reached the kill threshold with a live agent on the machine", score)
	}
}
