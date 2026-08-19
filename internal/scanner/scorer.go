package scanner

import (
	"os/user"
	"strings"

	"github.com/tjp2021/devreap/internal/config"
	"github.com/tjp2021/devreap/internal/patterns"
)

// ideSignatures matches IDE processes by executable path or exact binary name.
// Uses path-based matching to avoid false positives from macOS system processes
// (e.g., CursorUIViewService is Apple's text cursor service, NOT the Cursor IDE).
type ideSignature struct {
	pathContains string // match against cmdline/exe path (most reliable)
	exactName    string // fallback: exact process name match (no substring!)
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

// Scorer computes orphan likelihood scores for processes using weighted signals.
type Scorer struct {
	weights  config.WeightConfig
	ideAlive *bool // cached per scan cycle
	procs    []ProcessInfo // reuse enumerated processes
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
		}
	}
	return false
}
