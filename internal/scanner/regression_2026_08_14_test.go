package scanner

import (
	"testing"
	"time"

	"github.com/tjp2021/devreap/internal/patterns"
)

// Regression corpus for the 2026-08-14 incident.
//
// On that day the daemon killed working MCP servers and dev servers on the
// maintainer's machine: a docs MCP server 93 times, a Google Workspace MCP
// server 47, a transcript MCP server 47, a browser-automation MCP server 29,
// a local MCP server 29, a mobile dev server 227, plus two supervised agent
// watchdogs. Every kill scored 0.70 from exceeded_duration + no_tty +
// parent_ide_dead, with ppid_is_init absent — the processes had not lost their
// parents at all.
//
// The cmdlines below are taken from the daemon log. Home directories and
// project names are anonymized; the shapes that drove the misclassification
// are preserved exactly.
var killedOn20260814 = []struct {
	name    string
	cmdline string
	args    string
}{
	{
		name:    "gws",
		cmdline: "node /opt/homebrew/bin/gws mcp --services drive,gmail,calendar,docs,sheets,tasks --helpers --workflows",
		args:    "/opt/homebrew/bin/gws mcp --services drive,gmail,calendar,docs,sheets,tasks --helpers --workflows",
	},
	{
		name:    "notion-mcp-server",
		cmdline: "node /Users/dev/.npm/_npx/2e5266eea15d0ccd/node_modules/.bin/notion-mcp-server",
		args:    "/Users/dev/.npm/_npx/2e5266eea15d0ccd/node_modules/.bin/notion-mcp-server",
	},
	{
		name:    "mcp-youtube-transcript",
		cmdline: "node /Users/dev/.npm/_npx/e7216dc061acbf6a/node_modules/.bin/mcp-youtube-transcript",
		args:    "/Users/dev/.npm/_npx/e7216dc061acbf6a/node_modules/.bin/mcp-youtube-transcript",
	},
	{
		name:    "playwright-mcp",
		cmdline: "node /Users/dev/.npm/_npx/9833c18b2d85bc59/node_modules/.bin/playwright-mcp",
		args:    "/Users/dev/.npm/_npx/9833c18b2d85bc59/node_modules/.bin/playwright-mcp",
	},
	{
		name:    "mcp-context-server",
		cmdline: "node /Users/dev/.npm/_npx/fe3332f3befcd1b0/node_modules/.bin/mcp-context-server",
		args:    "/Users/dev/.npm/_npx/fe3332f3befcd1b0/node_modules/.bin/mcp-context-server",
	},
	{
		name:    "expo",
		cmdline: "node /Users/dev/projects/mobile-app/node_modules/.bin/expo start --dev-client --port 8081 --host lan",
		args:    "/Users/dev/projects/mobile-app/node_modules/.bin/expo start --dev-client --port 8081 --host lan",
	},
}

// liveAgents are process lists in which an agent or IDE is clearly running, so
// parent_ide_dead must not fire.
var liveAgents = []struct {
	label string
	procs []ProcessInfo
}{
	{
		label: "terminal-launched claude",
		procs: []ProcessInfo{{Name: "claude", Cmdline: "claude"}},
	},
	{
		label: "homebrew claude",
		procs: []ProcessInfo{{Name: "node", Cmdline: "node /opt/homebrew/bin/claude"}},
	},
	{
		label: "codex",
		procs: []ProcessInfo{{Name: "codex", Cmdline: "codex"}},
	},
	{
		label: "codex via npm",
		procs: []ProcessInfo{{Name: "node", Cmdline: "node /Users/dev/.npm/lib/node_modules/@openai/codex/bin/codex.js"}},
	},
	{
		label: "cursor",
		procs: []ProcessInfo{{Name: "Cursor", Cmdline: "/Applications/Cursor.app/Contents/MacOS/Cursor"}},
	},
}

// With an agent alive, parent_ide_dead must not fire, so these processes stay
// well below the kill threshold.
func TestKilledProcessesScoreLowWhileAnAgentIsAlive(t *testing.T) {
	for _, tc := range killedOn20260814 {
		for _, agent := range liveAgents {
			t.Run(tc.name+"/"+agent.label, func(t *testing.T) {
				scorer := testScorer()
				scorer.ResetCache(agent.procs)

				proc := known(ProcessInfo{
					PID:        4242,
					PPID:       900, // live parent
					Name:       "node",
					Cmdline:    tc.cmdline,
					Args:       tc.args,
					CreateTime: time.Now().Add(-9 * time.Hour),
					HasTTY:     false,
				})

				pat := patterns.Pattern{Name: "node-mcp-server", MaxDuration: 4 * time.Hour}
				score, signals := scorer.Score(proc, pat)

				if _, fired := signals["parent_ide_dead"]; fired {
					t.Errorf("parent_ide_dead fired while %s was running", agent.label)
				}
				if score >= 0.6 {
					t.Errorf("score %.2f reached the kill threshold with an agent alive", score)
				}
				if HasStrongSignal(signals) {
					t.Error("no strong signal should be present for a process with a live parent")
				}
			})
		}
	}
}

// The harder case: no agent is running anywhere, so every weak signal fires and
// the score clears the threshold exactly as it did on 2026-08-14. The
// strong-signal gate must still make these processes unkillable.
func TestKilledProcessesAreNotKillEligibleOnWeakSignalsAlone(t *testing.T) {
	for _, tc := range killedOn20260814 {
		t.Run(tc.name, func(t *testing.T) {
			scorer := testScorer()
			// Nothing that looks like an IDE or agent.
			scorer.ResetCache([]ProcessInfo{
				{Name: "Finder", Cmdline: "/System/Library/CoreServices/Finder.app/Contents/MacOS/Finder"},
			})

			proc := known(ProcessInfo{
				PID:        4242,
				PPID:       900, // live parent — never reparented to launchd
				Name:       "node",
				Cmdline:    tc.cmdline,
				Args:       tc.args,
				CreateTime: time.Now().Add(-9 * time.Hour),
				HasTTY:     false,
			})

			pat := patterns.Pattern{Name: "node-mcp-server", MaxDuration: 4 * time.Hour}
			score, signals := scorer.Score(proc, pat)

			// Reproduce the incident conditions, so this test fails loudly if
			// the fixture stops representing them.
			if score < 0.6 {
				t.Fatalf("fixture no longer reproduces the incident: score %.2f is below threshold", score)
			}
			if signals["ppid_is_init"] != 0 {
				t.Fatal("fixture is wrong: this process has a live parent")
			}

			// The gate.
			if HasStrongSignal(signals) {
				t.Errorf("%s must not be kill-eligible on weak signals alone", tc.name)
			}
		})
	}
}

// The agent processes killed on 2026-08-14 were python watchdogs supervising
// MCP servers under a live agent gateway.
func TestSupervisedWatchdogsAreNotKillEligible(t *testing.T) {
	cmdlines := []string{
		"/Users/dev/.agent-runtime/venv/bin/python /Users/dev/.agent-runtime/tools/mcp_stdio_watchdog.py --ppid 900 -- /opt/homebrew/bin/uv run --project /Users/dev/tools/transcribe-mcp python /Users/dev/tools/transcribe-mcp/server.py",
		"/Users/dev/.agent-runtime/venv/bin/python /Users/dev/.agent-runtime/tools/mcp_stdio_watchdog.py --ppid 900 -- /opt/homebrew/Cellar/node/25.5.0/bin/npx -y @notionhq/notion-mcp-server",
	}

	for _, cmdline := range cmdlines {
		scorer := testScorer()
		scorer.ResetCache([]ProcessInfo{
			{Name: "Finder", Cmdline: "/System/Library/CoreServices/Finder.app/Contents/MacOS/Finder"},
		})

		proc := known(ProcessInfo{
			PID:        4242,
			PPID:       900, // supervised by a live agent gateway
			Name:       "python",
			Cmdline:    cmdline,
			CreateTime: time.Now().Add(-30 * time.Hour),
			HasTTY:     false,
		})

		pat := patterns.Pattern{Name: "python-mcp-server", MaxDuration: 4 * time.Hour}
		_, signals := scorer.Score(proc, pat)

		if HasStrongSignal(signals) {
			t.Error("a supervised watchdog must not be kill-eligible")
		}
	}
}

// "mcp" must match as a token or path segment, not as any substring. The
// unanchored pattern matched anything containing the three letters.
func TestMCPPatternMatchesTokensNotSubstrings(t *testing.T) {
	registry, err := patterns.NewRegistry()
	if err != nil {
		t.Fatalf("loading patterns: %v", err)
	}

	shouldMatch := []string{
		"/Users/dev/.npm/_npx/abc/node_modules/.bin/mcp-youtube-transcript",
		"/opt/homebrew/bin/gws mcp --services drive,gmail",
		"/Users/dev/server/mcp_server.py",
		"/Users/dev/.npm/_npx/abc/node_modules/.bin/notion-mcp-server",
	}
	for _, args := range shouldMatch {
		if registry.Match("node", args) == nil {
			t.Errorf("expected a pattern match for %q", args)
		}
	}

	// "mcp" buried inside a longer word is not an MCP server.
	shouldNotMatch := []string{
		"/Users/dev/projects/mcpanel/server.js",
		"/Users/dev/tools/dumpcp/index.js",
		"/Users/dev/src/gimcpx/main.js",
	}
	for _, args := range shouldNotMatch {
		if m := registry.Match("node", args); m != nil {
			t.Errorf("%q should not match pattern %q — 'mcp' is a substring, not a token", args, m.Pattern.Name)
		}
	}
}

// The agent signatures added after the incident.
func TestAgentSignaturesDetected(t *testing.T) {
	cases := []struct {
		label string
		procs []ProcessInfo
		want  bool
	}{
		{"bare claude", []ProcessInfo{{Name: "claude", Cmdline: "claude"}}, true},
		{"homebrew claude", []ProcessInfo{{Name: "node", Cmdline: "node /opt/homebrew/bin/claude"}}, true},
		{"usr local claude", []ProcessInfo{{Name: "node", Cmdline: "node /usr/local/bin/claude"}}, true},
		{"bare codex", []ProcessInfo{{Name: "codex", Cmdline: "codex"}}, true},
		{"npm codex", []ProcessInfo{{Name: "node", Cmdline: "node /Users/dev/.npm/lib/node_modules/@openai/codex/bin/codex.js"}}, true},
		{"homebrew codex", []ProcessInfo{{Name: "node", Cmdline: "node /opt/homebrew/bin/codex"}}, true},
		// Still must not fire on unrelated processes that merely contain the word.
		{"claude-helper", []ProcessInfo{{Name: "claude-helper", Cmdline: "/usr/local/bin/claude-helper"}}, false},
		{"codex-notes", []ProcessInfo{{Name: "codex-notes", Cmdline: "/usr/local/bin/codex-notes"}}, false},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			if got := checkIDERunningFromList(tc.procs); got != tc.want {
				t.Errorf("checkIDERunningFromList = %v, want %v", got, tc.want)
			}
		})
	}
}
