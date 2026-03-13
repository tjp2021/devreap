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
	{pathContains: "/.local/share/claude/"},

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

	// Skip processes not owned by current user
	if s.currentUser != "" && proc.Username != "" && proc.Username != s.currentUser {
		return 0, signals
	}

	// PPID is 1 (launchd) — parent died
	if proc.PPID == 1 {
		signals["ppid_is_init"] = s.weights.PPIDIsInit
		total += s.weights.PPIDIsInit
	}

	// No controlling terminal
	if !proc.HasTTY {
		signals["no_tty"] = s.weights.NoTTY
		total += s.weights.NoTTY
	}

	// No IDE running
	if !s.isIDEAlive() {
		signals["parent_ide_dead"] = s.weights.ParentIDEDead
		total += s.weights.ParentIDEDead
	}

	// Exceeded max duration
	if pat.MaxDuration > 0 && proc.Age() > pat.MaxDuration {
		signals["exceeded_duration"] = s.weights.ExceededDur
		total += s.weights.ExceededDur
	}

	// Listening on port — bound to a network port (potential orphaned server)
	// Independent of TTY status; the no_tty signal already covers terminal absence
	if len(proc.Ports) > 0 {
		signals["has_listener"] = s.weights.HasListener
		total += s.weights.HasListener
	}

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
			if sig.pathContains != "" && (strings.Contains(p.Cmdline, sig.pathContains) || strings.Contains(p.Exe, sig.pathContains)) {
				return true
			}
			if sig.exactName != "" && p.Name == sig.exactName {
				return true
			}
		}
	}
	return false
}
