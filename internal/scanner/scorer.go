package scanner

import (
	"fmt"
	"os/user"
	"strings"

	"github.com/shirou/gopsutil/v4/process"

	"github.com/tjp2021/devreap/internal/config"
	"github.com/tjp2021/devreap/internal/patterns"
)

// ideSignatures matches IDE processes by executable path or exact binary name.
// Uses path-based matching to avoid false positives from macOS system processes
// (e.g., CursorUIViewService is Apple's text cursor service, NOT the Cursor IDE).
type ideSignature struct {
	pathContains string // match against cmdline/exe path (most reliable)
	exactName    string // fallback: exact process name match (no substring!)
	// execPath matches only when a whole command-line argument is this exact
	// executable path, or when the resolved executable is exactly that path.
	// Needed for short bin paths: pathContains "/usr/local/bin/claude" would
	// also match "/usr/local/bin/claude-helper".
	execPath string
	// exeContains matches a fragment of the resolved executable path. A
	// terminal-launched agent has a bare command line ("claude"), so its
	// install location is only visible through the executable path.
	exeContains string
}

var ideSignatures = []ideSignature{
	// VS Code
	{pathContains: "/Visual Studio Code.app/"},
	{pathContains: "/Code.app/Contents/MacOS/Electron"},
	{exactName: "Code Helper (Plugin)"},

	// Cursor
	{pathContains: "/Cursor.app/Contents/MacOS/Cursor"},
	{exactName: "Cursor Helper (Plugin)"},

	// Claude Code CLI — match the actual binary, not anything with "claude" in it
	{pathContains: "/node_modules/.bin/claude"},
	{pathContains: "/@anthropic-ai/claude-code"},
	// Terminal-launched claude. The previous signatures only covered the
	// npm-installed paths, so a Homebrew or bare-command launch left
	// parent_ide_dead firing while the agent was sitting right there.
	{exactName: "claude"},
	// macOS reports the short process name from p_comm, which for the packaged
	// CLI is "claude.exe" even when the command line is a bare "claude". The
	// "claude" name above never matched a real macOS session, so every
	// terminal-launched agent stayed invisible.
	{exactName: "claude.exe"},
	{execPath: "/opt/homebrew/bin/claude"},
	{execPath: "/usr/local/bin/claude"},
	// The native installer keeps versioned binaries under the user's data
	// directory and names the process after the version, so neither the name
	// nor the bare command line identifies it. Only the executable path does.
	{exeContains: "/.local/share/claude/"},
	{exeContains: "/@anthropic-ai/claude-code"},
	{exeContains: "/node_modules/.bin/claude"},

	// Codex CLI
	{exactName: "codex"},
	{pathContains: "/@openai/codex/"},
	{execPath: "/opt/homebrew/bin/codex"},
	{execPath: "/usr/local/bin/codex"},

	// Windsurf
	{pathContains: "/Windsurf.app/"},

	// Zed
	{pathContains: "/Zed.app/"},
	{exactName: "zed"},

	// JetBrains IDEs
	{pathContains: "/IntelliJ IDEA.app/"},
	{pathContains: "/WebStorm.app/"},
	{pathContains: "/GoLand.app/"},
	{pathContains: "/PyCharm.app/"},
	{pathContains: "/PhpStorm.app/"},
	{pathContains: "/RustRover.app/"},

	// Xcode
	{pathContains: "/Xcode.app/Contents/MacOS/Xcode"},
}

// strongSignals are the signals that are direct evidence a process lost its
// parent, rather than circumstantial evidence about the machine as a whole.
//
// Only ppid_is_init qualifies today. The others are weak:
//
//   - parent_ide_dead is global, not ancestral. It asks whether any IDE is
//     running anywhere on the machine, so closing one editor makes it fire for
//     every matched process, including ones launched from a terminal.
//   - no_tty is normal for any process started by a launcher or a daemon.
//   - exceeded_duration is a statement about age, not about abandonment. A
//     long-lived server is doing its job.
//
// A process is only killable when at least one strong signal is present. Weak
// signals can raise a process for reporting, but on their own they describe a
// working machine, not an orphan.
var strongSignals = []string{"ppid_is_init"}

// HasStrongSignal reports whether a scored signal set contains direct evidence
// that the process lost its parent.
func HasStrongSignal(signals map[string]float64) bool {
	for _, name := range strongSignals {
		if signals[name] > 0 {
			return true
		}
	}
	return false
}

// Scorer computes orphan likelihood scores for processes using weighted signals.
type Scorer struct {
	weights     config.WeightConfig
	ideAlive    *bool         // cached per scan cycle
	procs       []ProcessInfo // reuse enumerated processes
	currentUser string
}

// NewScorer creates a Scorer with the given signal weights.
func NewScorer(weights config.WeightConfig) *Scorer {
	username := ""
	if u, err := user.Current(); err == nil {
		username = u.Username
	}
	return &Scorer{
		weights:     weights,
		currentUser: username,
	}
}

// ResetCache should be called at the start of each scan cycle.
// Pass the already-enumerated process list to avoid double enumeration.
func (s *Scorer) ResetCache(procs []ProcessInfo) {
	s.ideAlive = nil
	s.procs = procs
}

// Score computes an orphan likelihood score (0.0-1.0) for a process against a pattern.
// Returns the total score and a breakdown of individual signal contributions.
func (s *Scorer) Score(proc ProcessInfo, pat patterns.Pattern) (float64, map[string]float64) {
	signals := make(map[string]float64)
	total := 0.0

	// Ownership must be positively established. An unreadable owner used to
	// fall through this guard and leave the process eligible for killing;
	// now it disqualifies the process outright.
	if s.currentUser != "" {
		if !proc.UsernameKnown || proc.Username != s.currentUser {
			return 0, signals
		}
	}

	// PPID is 1 (launchd) — parent died
	if proc.PPID == 1 {
		signals["ppid_is_init"] = s.weights.PPIDIsInit
		total += s.weights.PPIDIsInit
	}

	// No controlling terminal. Only fires when the terminal was actually
	// read; a failed lookup is not evidence of a missing terminal.
	if proc.TTYKnown && !proc.HasTTY {
		signals["no_tty"] = s.weights.NoTTY
		total += s.weights.NoTTY
	}

	// No IDE running
	if !s.isIDEAlive() {
		signals["parent_ide_dead"] = s.weights.ParentIDEDead
		total += s.weights.ParentIDEDead
	}

	// Exceeded max duration. An unknown start time used to read as a zero
	// timestamp, making every such process appear decades old and firing
	// this signal unconditionally.
	if proc.CreateTimeKnown && pat.MaxDuration > 0 && proc.Age() > pat.MaxDuration {
		signals["exceeded_duration"] = s.weights.ExceededDur
		total += s.weights.ExceededDur
	}

	// Deliberately absent: has_listener.
	//
	// Holding an open listening socket is evidence a process is doing its job,
	// not evidence it was abandoned. Scoring it as positive kill evidence
	// pushed healthy MCP servers and dev servers over the threshold. Ports are
	// still collected on ProcessInfo and shown in reports; they just no longer
	// argue for termination.

	// Cap at 1.0
	if total > 1.0 {
		total = 1.0
	}

	return total, signals
}

func (s *Scorer) isIDEAlive() bool {
	if s.ideAlive != nil {
		return *s.ideAlive
	}

	alive := checkIDERunningFromList(s.procs)
	s.ideAlive = &alive
	return alive
}

// checkIDERunningFromList uses the already-enumerated process list
// and path-based matching to accurately detect IDE processes.
func checkIDERunningFromList(procs []ProcessInfo) bool {
	if len(procs) == 0 {
		return true // assume alive if no process list (conservative)
	}

	for _, p := range procs {
		for _, sig := range ideSignatures {
			if sig.pathContains != "" && strings.Contains(p.Cmdline, sig.pathContains) {
				return true
			}
			if sig.exactName != "" && p.Name == sig.exactName {
				return true
			}
			if sig.execPath != "" && (p.Exe == sig.execPath || hasExecArg(p.Cmdline, sig.execPath)) {
				return true
			}
			// An empty Exe means the path could not be read, so it must not
			// match a fragment and claim an IDE that is not there.
			if sig.exeContains != "" && p.Exe != "" && strings.Contains(p.Exe, sig.exeContains) {
				return true
			}
		}
	}
	return false
}

// hasExecArg reports whether any whole argument of the command line is exactly
// the given executable path. Comparing whole arguments keeps
// "/usr/local/bin/claude-helper" from matching "/usr/local/bin/claude".
func hasExecArg(cmdline, execPath string) bool {
	for _, field := range strings.Fields(cmdline) {
		if field == execPath {
			return true
		}
	}
	return false
}

// RecheckStrongSignal re-reads the live process and reports whether the strong
// lifecycle signal still holds. A scan snapshot can be tens of seconds old by
// the time a kill is attempted, and the kill decision must rest on current
// facts, not stale ones.
//
// An error means the condition could not be confirmed, and callers must treat
// that as "do not kill" rather than as a pass.
func RecheckStrongSignal(pid int32) (bool, error) {
	p, err := process.NewProcess(pid)
	if err != nil {
		return false, fmt.Errorf("re-reading process %d: %w", pid, err)
	}

	ppid, err := p.Ppid()
	if err != nil {
		return false, fmt.Errorf("re-reading parent of process %d: %w", pid, err)
	}

	// ppid_is_init: the parent died and the process was reparented to launchd.
	return ppid == 1, nil
}
