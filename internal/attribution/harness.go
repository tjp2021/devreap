package attribution

import (
	_ "embed"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/tjp2021/devreap/internal/config"
)

//go:embed harnesses.yaml
var builtinHarnesses []byte

// UnknownHarnessLabel is what a session root nobody recognizes is called. A
// harness nobody has heard of yet is attributed on the day it ships, and it
// carries this label until someone adds a descriptor for it.
const UnknownHarnessLabel = "unknown-harness"

// GenericHarnessName is the compiled-in descriptor that catches everything the
// data file does not name. It is compiled in so a missing or broken data file
// can never disable recognition.
const GenericHarnessName = "generic-interactive"

// HarnessStatus records how a descriptor's recognition data was obtained.
type HarnessStatus string

const (
	StatusVerifiedLive   HarnessStatus = "verified-live"
	StatusDocSourced     HarnessStatus = "doc-sourced"
	StatusUnverified     HarnessStatus = "unverified"
	StatusByConstruction HarnessStatus = "by-construction"
)

// ParentKind describes the shape a session root is launched in.
type ParentKind string

const (
	ParentKindTerminal            ParentKind = "terminal"
	ParentKindAppBundle           ParentKind = "app_bundle"
	ParentKindEditorExtensionHost ParentKind = "editor_extension_host"
	ParentKindGeneric             ParentKind = "generic"
)

// RootRule recognizes a session root process.
//
// A rule matches when any populated field matches. RequireAll flips that to a
// conjunction, which is how a shape that no single field can discriminate is
// expressed: an agent binary named codex under an editor extensions directory
// is neither "anything named codex" nor "anything under extensions".
type RootRule struct {
	RequireAll   bool     `yaml:"require_all"`
	Names        []string `yaml:"names"`
	ExeContains  []string `yaml:"exe_contains"`
	ExecPaths    []string `yaml:"exec_paths"`
	PathContains []string `yaml:"path_contains"`
}

// HarnessMarkers names the environment variables a harness publishes. Every
// field is optional, and a harness that publishes nothing is attributed exactly
// as well as one that publishes everything. That is the point of the design.
type HarnessMarkers struct {
	SessionIDEnv string `yaml:"session_id_env"`
	RepoEnv      string `yaml:"repo_env"`
	AgentEnv     string `yaml:"agent_env"`
}

// Harness is one adapter descriptor.
type Harness struct {
	Name       string         `yaml:"name"`
	Display    string         `yaml:"display"`
	Status     HarnessStatus  `yaml:"status"`
	ParentKind ParentKind     `yaml:"parent_kind"`
	Root       RootRule       `yaml:"root"`
	Markers    HarnessMarkers `yaml:"markers"`
	RepoSource []string       `yaml:"repo_source"`
}

// Label is the name shown in reports. The generic descriptor labels its roots
// unknown-harness rather than by its own name.
func (h Harness) Label() string {
	if h.Name == "" || h.Name == GenericHarnessName {
		return UnknownHarnessLabel
	}
	return h.Name
}

// MarkerNames returns the environment variable names this descriptor reads.
func (h Harness) MarkerNames() []string {
	var names []string
	for _, name := range []string{h.Markers.SessionIDEnv, h.Markers.RepoEnv, h.Markers.AgentEnv} {
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

type harnessFile struct {
	Harnesses []Harness `yaml:"harnesses"`
}

// RootCandidate is one process on an ancestry chain, with the fields root
// recognition needs. The registry never reads a process itself: the watcher has
// already collected these, through the redaction filter.
type RootCandidate struct {
	Entry   ProcEntry
	Exe     string
	Cmdline string

	// HasTrackedChild reports whether this process has spawned at least one
	// child of a tracked class. Only the generic descriptor consults it, and it
	// is the condition that separates a session root from any other process
	// group leader on the machine.
	HasTrackedChild bool
}

// RootMatch is the outcome of root resolution.
type RootMatch struct {
	// Harness is the descriptor that recognized the root.
	Harness Harness
	// Root is the process the session belongs to.
	Root RootCandidate
	// Label is the harness name, or unknown-harness for a generic root.
	Label string
	// Generic reports whether the compiled-in fallback made the match.
	Generic bool
	// Depth is the number of links from the process to its root. Zero means the
	// process is its own session root.
	Depth int
}

// HarnessRegistry holds the built-in descriptors plus any user additions, and
// answers one question: which process on this chain is the session root.
//
// It is a labelling service and holds no other logic. A gap in it degrades a
// label rather than an attribution, because ownership comes from the spawn link
// the watcher recorded and not from anything in this file.
type HarnessRegistry struct {
	harnesses []Harness
	generic   Harness
	findings  []Finding
}

// NewHarnessRegistry returns a registry holding the built-in descriptors and the
// compiled-in generic fallback.
func NewHarnessRegistry() (*HarnessRegistry, error) {
	var file harnessFile
	if err := yaml.Unmarshal(builtinHarnesses, &file); err != nil {
		return nil, fmt.Errorf("parsing built-in harness descriptors: %w", err)
	}

	r := &HarnessRegistry{
		harnesses: file.Harnesses,
		generic:   genericDescriptor(),
	}
	for i := range r.harnesses {
		normalizeDescriptor(&r.harnesses[i])
	}
	return r, nil
}

// genericDescriptor is compiled in rather than loaded, so recognition survives a
// missing or broken data file.
func genericDescriptor() Harness {
	return Harness{
		Name:       GenericHarnessName,
		Display:    "Unrecognized interactive session root",
		Status:     StatusByConstruction,
		ParentKind: ParentKindGeneric,
		RepoSource: []string{"root_cwd"},
	}
}

func normalizeDescriptor(h *Harness) {
	if h.Status == "" {
		h.Status = StatusUnverified
	}
	if h.ParentKind == "" {
		h.ParentKind = ParentKindGeneric
	}
	if len(h.RepoSource) == 0 {
		h.RepoSource = []string{"root_cwd"}
	}
}

// LoadUserFile adds descriptors from a user-supplied data file.
//
// It never returns an error and never blocks startup, because the adapter file
// is user-writable configuration rather than authority. A missing file is
// normal. An unreadable or unparseable file leaves the built-in descriptors in
// force and raises a finding. A descriptor that fails validation is skipped with
// a finding, and the rest of the file still loads.
func (r *HarnessRegistry) LoadUserFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			r.findings = append(r.findings, Finding{
				Kind:   FindingAdapterFileUnreadable,
				Detail: fmt.Sprintf("reading %s: %v; built-in descriptors stay in force", path, err),
			})
		}
		return
	}

	var file harnessFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		r.findings = append(r.findings, Finding{
			Kind:   FindingAdapterFileUnreadable,
			Detail: fmt.Sprintf("parsing %s: %v; built-in descriptors stay in force", path, err),
		})
		return
	}

	for _, h := range file.Harnesses {
		normalizeDescriptor(&h)
		if err := validateUserDescriptor(h); err != nil {
			r.findings = append(r.findings, Finding{
				Kind:   FindingAdapterDescriptorRejected,
				Detail: fmt.Sprintf("descriptor %q in %s: %v", h.Name, path, err),
			})
			continue
		}
		r.harnesses = append(r.harnesses, h)
	}
}

// Findings returns the conditions doctor should report.
func (r *HarnessRegistry) Findings() []Finding {
	out := make([]Finding, len(r.findings))
	copy(out, r.findings)
	return out
}

// All returns every named descriptor, excluding the compiled-in fallback.
func (r *HarnessRegistry) All() []Harness {
	out := make([]Harness, len(r.harnesses))
	copy(out, r.harnesses)
	return out
}

// Count reports how many named descriptors are loaded.
func (r *HarnessRegistry) Count() int { return len(r.harnesses) }

// Generic returns the compiled-in fallback descriptor.
func (r *HarnessRegistry) Generic() Harness { return r.generic }

// Lookup finds a descriptor by name.
func (r *HarnessRegistry) Lookup(name string) (Harness, bool) {
	if name == GenericHarnessName {
		return r.generic, true
	}
	for _, h := range r.harnesses {
		if h.Name == name {
			return h, true
		}
	}
	return Harness{}, false
}

// MarkerNames returns every environment variable name the loaded descriptors
// read, which is what widens the redaction allowlist. Nothing else may.
func (r *HarnessRegistry) MarkerNames() []string {
	seen := make(map[string]struct{})
	var out []string
	for _, h := range r.harnesses {
		for _, name := range h.MarkerNames() {
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	return out
}

// ResolveRoot finds the session root of a process. The chain starts with the
// process itself and continues through its ancestors, nearest first.
//
// Two rules decide the answer. A named descriptor beats the generic fallback
// anywhere on the chain, because a positive recognition is a better answer than
// a shape test at any depth. Among descriptors of the same kind, the nearest
// root wins: an agent binary under an extension host belongs to the
// extension-host session rather than to the editor bundle that contains it.
//
// Nesting is settled here rather than at load time, because whether one root
// sits above another depends on the process tree in front of you and not on the
// descriptor text.
func (r *HarnessRegistry) ResolveRoot(chain []RootCandidate) (RootMatch, bool) {
	if len(chain) > MaxLinkDepth+1 {
		chain = chain[:MaxLinkDepth+1]
	}

	for depth, candidate := range chain {
		for _, h := range r.harnesses {
			if h.Root.matches(candidate) {
				return RootMatch{Harness: h, Root: candidate, Label: h.Label(), Depth: depth}, true
			}
		}
	}

	for depth, candidate := range chain {
		if r.genericRootMatches(candidate) {
			return RootMatch{
				Harness: r.generic,
				Root:    candidate,
				Label:   UnknownHarnessLabel,
				Generic: true,
				Depth:   depth,
			}, true
		}
	}

	return RootMatch{}, false
}

// genericRootMatches applies the three conditions of the generic descriptor. All
// three must hold at once.
func (r *HarnessRegistry) genericRootMatches(c RootCandidate) bool {
	if !c.Entry.IsGroupLeader() && !isAppBundleMain(c) {
		return false
	}
	if isExcludedRootName(c.Entry.Name) {
		return false
	}
	return c.HasTrackedChild
}

func isAppBundleMain(c RootCandidate) bool {
	const bundleMain = ".app/Contents/MacOS/"
	return strings.Contains(c.Exe, bundleMain) || strings.Contains(c.Cmdline, bundleMain)
}

// matches reports whether this rule recognizes the candidate.
func (r RootRule) matches(c RootCandidate) bool {
	populated, matched := 0, 0

	if len(r.Names) > 0 {
		populated++
		if containsExact(r.Names, c.Entry.Name) {
			matched++
		}
	}
	if len(r.ExeContains) > 0 {
		populated++
		if c.Exe != "" && containsAnySubstring(r.ExeContains, c.Exe) {
			matched++
		}
	}
	if len(r.ExecPaths) > 0 {
		populated++
		if matchesExecPath(r.ExecPaths, c) {
			matched++
		}
	}
	if len(r.PathContains) > 0 {
		populated++
		if containsAnySubstring(r.PathContains, c.Cmdline) || (c.Exe != "" && containsAnySubstring(r.PathContains, c.Exe)) {
			matched++
		}
	}

	if populated == 0 {
		return false
	}
	if r.RequireAll {
		return matched == populated
	}
	return matched > 0
}

// matchesExecPath matches a whole executable path rather than a fragment of one,
// so a rule naming /usr/local/bin/claude does not also match a sibling binary
// whose name starts with the same letters.
func matchesExecPath(paths []string, c RootCandidate) bool {
	for _, path := range paths {
		if path == "" {
			continue
		}
		if c.Exe == path {
			return true
		}
		for _, field := range strings.Fields(c.Cmdline) {
			if field == path {
				return true
			}
		}
	}
	return false
}

func containsExact(list []string, value string) bool {
	for _, item := range list {
		if item != "" && item == value {
			return true
		}
	}
	return false
}

func containsAnySubstring(fragments []string, value string) bool {
	for _, fragment := range fragments {
		if fragment != "" && strings.Contains(value, fragment) {
			return true
		}
	}
	return false
}

// Names that may never be a session root. A shell, a terminal emulator, or a
// multiplexer is where sessions are launched from rather than a session itself,
// and naming one as a root would make every command run in that terminal look
// like a session member.
var (
	shellNames = []string{
		"sh", "bash", "zsh", "fish", "dash", "ksh", "csh", "tcsh", "ash", "login",
		"-sh", "-bash", "-zsh", "-fish",
	}
	terminalEmulatorNames = []string{
		"Terminal", "iTerm", "iTerm2", "Alacritty", "kitty", "WezTerm",
		"wezterm-gui", "Hyper", "Warp", "Ghostty", "Tabby", "rio", "foot",
		"konsole", "gnome-terminal", "xterm", "urxvt",
	}
	multiplexerNames = []string{
		"tmux", "tmux: server", "tmux: client", "screen", "zellij", "byobu",
		"dtach", "abduco",
	}
	// initNames covers process 1 under every name it goes by.
	initNames = []string{"launchd", "init", "systemd"}
	// supervisorNames name process supervisors and managers. A supervisor's
	// children are supervised rather than abandoned, and the scanner already
	// treats supervision as a reason to leave a process alone.
	supervisorNames = []string{
		"pm2", "pm2-runtime", "PM2", "supervisord", "supervisorctl", "foreman",
		"forever", "runit", "runsv", "runsvdir", "s6-svscan", "s6-supervise",
		"svscan", "daemontools", "monit", "circus", "circusd", "honcho",
		"overmind", "god", "immortal", "procman",
	}
	// nonDiscriminatingNames are shared with system binaries and language
	// runtimes, so a rule naming one and nothing else matches far more than the
	// harness it meant.
	nonDiscriminatingNames = []string{
		"node", "node.exe", "python", "python2", "python3", "ruby", "java",
		"deno", "bun", "perl", "php", "dotnet", "mono", "electron", "main",
		"server", "app", "npm", "npx", "yarn", "pnpm", "go", "cargo", "rustc",
		"env", "code", "helper", "bash", "sh", "zsh",
	}
)

func isExcludedRootName(name string) bool {
	for _, list := range [][]string{shellNames, terminalEmulatorNames, multiplexerNames, initNames} {
		if containsFold(list, name) {
			return true
		}
	}
	return containsFold(config.DefaultBlocklist, name)
}

func containsFold(list []string, value string) bool {
	if value == "" {
		return false
	}
	for _, item := range list {
		if strings.EqualFold(item, value) {
			return true
		}
	}
	return false
}

// validateUserDescriptor applies the load-time rules to a user-supplied
// descriptor.
//
// The rules apply to user descriptors only. Built-in descriptors ship with the
// binary and are reviewed in this repository, so they are trusted by
// construction. A rule that also policed the built-ins would reject the shipped
// table, because several of its entries are recognized by a bare name that no
// user file should be allowed to claim.
//
// The safety bound these rules serve is narrow and worth stating plainly.
// Attribution can never make a process eligible that the existing path would not
// already permit. Inside that set, a wrong entry can still cause a wrong kill,
// which is why naming a shell or a supervisor is refused rather than trusted.
func validateUserDescriptor(h Harness) error {
	if strings.TrimSpace(h.Name) == "" {
		return fmt.Errorf("descriptor has no name")
	}

	// Rule 1: a root rule may not name a shell, a terminal emulator, a
	// multiplexer, launchd, process 1, or anything on the built-in blocklist.
	for _, name := range h.Root.Names {
		if isExcludedRootName(name) {
			return fmt.Errorf("root rule names %q, which is a shell, a terminal, a multiplexer, process 1, or a blocklisted binary", name)
		}
	}
	for _, path := range h.Root.ExecPaths {
		if base := basename(path); isExcludedRootName(base) {
			return fmt.Errorf("root rule names executable %q, whose binary %q may never be a session root", path, base)
		}
	}

	// Rule 2: a root rule may not name a process supervisor or manager. A
	// supervisor's children are supervised, not abandoned.
	for _, name := range h.Root.Names {
		if isSupervisorName(name) {
			return fmt.Errorf("root rule names %q, which is a process supervisor", name)
		}
	}
	for _, path := range h.Root.ExecPaths {
		if base := basename(path); isSupervisorName(base) {
			return fmt.Errorf("root rule names executable %q, which is a process supervisor", path)
		}
	}

	// Rule 3: a root rule must discriminate. A rule with no populated field
	// matches nothing usable, and a rule holding only a bare name shared with a
	// system binary matches far more than it meant.
	if err := requireDiscriminatingRule(h.Root); err != nil {
		return err
	}

	return nil
}

// minPathFragment is the shortest path fragment that can discriminate. A
// fragment of one or two characters matches most paths on the machine.
const minPathFragment = 4

func requireDiscriminatingRule(rule RootRule) error {
	hasName := len(rule.Names) > 0
	hasPath := len(rule.ExeContains) > 0 || len(rule.ExecPaths) > 0 || len(rule.PathContains) > 0

	if !hasName && !hasPath {
		return fmt.Errorf("root rule has no recognition field, so it recognizes nothing")
	}

	for _, list := range [][]string{rule.ExeContains, rule.PathContains} {
		for _, fragment := range list {
			if len(strings.TrimSpace(fragment)) < minPathFragment {
				return fmt.Errorf("root rule matches on the path fragment %q, which is too short to discriminate", fragment)
			}
		}
	}

	// A conjunction is discriminated by its other fields, so a bare name is
	// allowed inside one. An alternation is not: each populated field matches on
	// its own, so a bare name shared with a system binary would match every
	// process carrying that name.
	if rule.RequireAll && hasPath {
		return nil
	}
	for _, name := range rule.Names {
		if containsFold(nonDiscriminatingNames, name) {
			return fmt.Errorf("root rule matches on the bare name %q, which is shared with a system binary; add an executable path or set require_all with a path fragment", name)
		}
	}
	return nil
}

func isSupervisorName(name string) bool {
	if containsFold(supervisorNames, name) {
		return true
	}
	// The design names a class rather than a list, so anything announcing
	// itself as a supervisor is refused too.
	lower := strings.ToLower(name)
	return strings.Contains(lower, "supervisor") || strings.Contains(lower, "supervise")
}

func basename(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}
