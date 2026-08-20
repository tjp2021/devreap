package attribution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var harnessTestStart = time.Date(2026, 8, 19, 8, 20, 54, 0, time.UTC)

func candidate(pid, ppid, pgid int32, name, exe, cmdline string) RootCandidate {
	return RootCandidate{
		Entry: ProcEntry{
			PID:            pid,
			PPID:           ppid,
			PGID:           pgid,
			Name:           name,
			StartTime:      harnessTestStart,
			StartTimeKnown: true,
			UID:            501,
		},
		Exe:     exe,
		Cmdline: cmdline,
	}
}

func newTestRegistry(t *testing.T) *HarnessRegistry {
	t.Helper()
	r, err := NewHarnessRegistry()
	if err != nil {
		t.Fatalf("loading built-in descriptors: %v", err)
	}
	return r
}

// TestBuiltinAdapterTableLoadsClean asserts the shipped table parses, holds the
// documented descriptors, and does not itself trip the rules that police user
// files. Built-in descriptors are trusted by construction; this test proves the
// trust is not hiding a malformed table.
func TestBuiltinAdapterTableLoadsClean(t *testing.T) {
	r := newTestRegistry(t)

	if got, want := r.Count(), 11; got != want {
		t.Errorf("loaded %d built-in descriptors, want %d", got, want)
	}
	if findings := r.Findings(); len(findings) != 0 {
		t.Errorf("built-in load raised findings: %+v", findings)
	}

	wantNames := []string{
		"claude-code-cli", "codex-editor-extension", "chatgpt-desktop", "codex-cli",
		"vscode-copilot-agent", "cursor", "windsurf", "jetbrains-ai",
		"claude-desktop", "opencode", "pi",
	}
	for _, name := range wantNames {
		h, ok := r.Lookup(name)
		if !ok {
			t.Errorf("descriptor %q is missing from the built-in table", name)
			continue
		}
		if h.Display == "" {
			t.Errorf("descriptor %q has no display name", name)
		}
		switch h.Status {
		case StatusVerifiedLive, StatusDocSourced, StatusUnverified:
		default:
			t.Errorf("descriptor %q has status %q, which is not one of the three recorded values", name, h.Status)
		}
		switch h.ParentKind {
		case ParentKindTerminal, ParentKindAppBundle, ParentKindEditorExtensionHost, ParentKindGeneric:
		default:
			t.Errorf("descriptor %q has parent kind %q", name, h.ParentKind)
		}
		if err := validateUserDescriptor(h); err != nil {
			t.Errorf("built-in descriptor %q would fail the user-file rules: %v", name, err)
		}
	}

	verified := 0
	for _, h := range r.All() {
		if h.Status == StatusVerifiedLive {
			verified++
		}
	}
	if verified != 4 {
		t.Errorf("%d descriptors are verified-live, want the 4 shapes walked during design", verified)
	}

	generic := r.Generic()
	if generic.Name != GenericHarnessName || generic.Label() != UnknownHarnessLabel {
		t.Errorf("generic descriptor: name %q label %q", generic.Name, generic.Label())
	}
	for _, h := range r.All() {
		if h.Name == GenericHarnessName {
			t.Error("the generic descriptor must be compiled in, not loaded from the data file")
		}
	}
}

// TestAdapterValidationRejectsShellAndSupervisorRoots drives the three
// load-time rules over a user file, and asserts the rest of the file still
// loads after each rejection.
func TestAdapterValidationRejectsShellAndSupervisorRoots(t *testing.T) {
	file := `
harnesses:
  - name: shell-root
    root:
      names: ["zsh"]
  - name: terminal-root
    root:
      names: ["iTerm2"]
  - name: multiplexer-root
    root:
      names: ["tmux"]
  - name: init-root
    root:
      names: ["launchd"]
  - name: blocklisted-root
    root:
      names: ["postgres"]
  - name: supervisor-root
    root:
      names: ["pm2"]
  - name: supervisor-by-path
    root:
      exec_paths: ["/opt/homebrew/bin/supervisord"]
  - name: bare-runtime-root
    root:
      names: ["node"]
  - name: alternation-hiding-a-bare-name
    root:
      names: ["node"]
      exe_contains: ["/my-harness/"]
  - name: empty-root
    root: {}
  - name: short-fragment-root
    root:
      exe_contains: ["/"]
  - name: good-root
    display: "A harness with a discriminating rule"
    status: unverified
    parent_kind: terminal
    root:
      names: ["myagent"]
      exe_contains: ["/opt/my-harness/bin/"]
    markers:
      session_id_env: "MY_HARNESS_SESSION_ID"
`
	path := filepath.Join(t.TempDir(), "harnesses.yaml")
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatalf("writing user file: %v", err)
	}

	r := newTestRegistry(t)
	builtinCount := r.Count()
	r.LoadUserFile(path)

	if got, want := r.Count(), builtinCount+1; got != want {
		t.Errorf("registry holds %d descriptors, want %d: only the good one should load", got, want)
	}
	if _, ok := r.Lookup("good-root"); !ok {
		t.Error("the valid descriptor after the rejections did not load")
	}

	rejected := []string{
		"shell-root", "terminal-root", "multiplexer-root", "init-root",
		"blocklisted-root", "supervisor-root", "supervisor-by-path",
		"bare-runtime-root", "alternation-hiding-a-bare-name", "empty-root",
		"short-fragment-root",
	}
	for _, name := range rejected {
		if _, ok := r.Lookup(name); ok {
			t.Errorf("descriptor %q was accepted and should have been rejected", name)
		}
	}

	findings := r.Findings()
	if len(findings) != len(rejected) {
		t.Errorf("got %d findings, want one per rejection (%d): %+v", len(findings), len(rejected), findings)
	}
	for _, f := range findings {
		if f.Kind != FindingAdapterDescriptorRejected {
			t.Errorf("finding kind %q, want %q", f.Kind, FindingAdapterDescriptorRejected)
		}
		if f.Detail == "" {
			t.Error("a rejection finding carries no detail")
		}
	}
}

func TestAdapterFileMissingOrCorruptKeepsBuiltins(t *testing.T) {
	dir := t.TempDir()

	r := newTestRegistry(t)
	builtinCount := r.Count()
	r.LoadUserFile(filepath.Join(dir, "absent.yaml"))
	if r.Count() != builtinCount {
		t.Errorf("a missing file changed the registry: %d descriptors", r.Count())
	}
	if findings := r.Findings(); len(findings) != 0 {
		t.Errorf("a missing file raised findings: %+v", findings)
	}

	corrupt := filepath.Join(dir, "corrupt.yaml")
	if err := os.WriteFile(corrupt, []byte("harnesses: [ this is not: valid: yaml"), 0o600); err != nil {
		t.Fatalf("writing corrupt file: %v", err)
	}
	r.LoadUserFile(corrupt)
	if r.Count() != builtinCount {
		t.Errorf("a corrupt file changed the registry: %d descriptors", r.Count())
	}
	findings := r.Findings()
	if len(findings) != 1 || findings[0].Kind != FindingAdapterFileUnreadable {
		t.Errorf("findings: %+v, want one unreadable-file finding", findings)
	}
}

// TestNearestRootWinsForNestedRoots builds the nesting pair the shipped table
// already contains: an editor bundle root above an extension-host root. The
// descendant belongs to the closer root.
func TestNearestRootWinsForNestedRoots(t *testing.T) {
	r := newTestRegistry(t)

	chain := []RootCandidate{
		candidate(99010, 98925, 98888, "node", "/opt/homebrew/bin/node", "node ./server.js --port 7333"),
		candidate(98925, 98900, 98888, "codex",
			"/home/dev/.vscode/extensions/openai.chatgpt-26.818.22352-darwin-arm64/bin/macos-aarch64/codex",
			"codex --stdio"),
		candidate(98900, 98800, 98800, "Code Helper (Plugin)",
			"/Applications/Visual Studio Code.app/Contents/Frameworks/Code Helper (Plugin).app/Contents/MacOS/Code Helper (Plugin)",
			"/Applications/Visual Studio Code.app/Contents/Frameworks/Code Helper (Plugin).app/Contents/MacOS/Code Helper (Plugin) --type=utility"),
		candidate(98800, 1, 98800, "Electron",
			"/Applications/Visual Studio Code.app/Contents/MacOS/Electron",
			"/Applications/Visual Studio Code.app/Contents/MacOS/Electron"),
	}

	match, ok := r.ResolveRoot(chain)
	if !ok {
		t.Fatal("no root resolved for a chain holding two recognized roots")
	}
	if match.Label != "codex-editor-extension" {
		t.Errorf("label: got %q, want the nearer root codex-editor-extension", match.Label)
	}
	if match.Root.Entry.PID != 98925 {
		t.Errorf("root: got pid %d, want the extension host agent 98925", match.Root.Entry.PID)
	}
	if match.Depth != 1 {
		t.Errorf("depth: got %d, want 1", match.Depth)
	}
	if match.Generic {
		t.Error("a named descriptor matched, so the match must not be generic")
	}
}

// TestHarnessRecognitionVerifiedShapes drives the four shapes observed live
// during the design pass, and asserts the root, the session boundary, and the
// label for each.
func TestHarnessRecognitionVerifiedShapes(t *testing.T) {
	r := newTestRegistry(t)

	tests := []struct {
		name      string
		chain     []RootCandidate
		wantLabel string
		wantRoot  int32
		wantDepth int
	}{
		{
			name: "terminal harness is its own process group leader",
			chain: []RootCandidate{
				candidate(99010, 98888, 98888, "node", "/opt/homebrew/bin/node", "node ./server.js"),
				candidate(98888, 98800, 98888, "claude.exe", "/opt/homebrew/bin/claude", "claude"),
				candidate(98800, 1, 98800, "zsh", "/bin/zsh", "-zsh"),
			},
			wantLabel: "claude-code-cli",
			wantRoot:  98888,
			wantDepth: 1,
		},
		{
			name: "native install is recognized by executable path alone",
			chain: []RootCandidate{
				candidate(98888, 98800, 98888, "claude-2.1.4",
					"/home/dev/.local/share/claude/versions/2.1.4/claude", "claude"),
			},
			wantLabel: "claude-code-cli",
			wantRoot:  98888,
			wantDepth: 0,
		},
		{
			name: "editor extension chain three levels deep",
			chain: []RootCandidate{
				candidate(99010, 98925, 98888, "node", "/opt/homebrew/bin/node", "node ./indexer.js"),
				candidate(98925, 98900, 98888, "codex",
					"/home/dev/.cursor/extensions/openai.chatgpt-26.818.22352-darwin-arm64/bin/macos-aarch64/codex",
					"codex --stdio"),
				candidate(98900, 98800, 98800, "Code Helper (Plugin)",
					"/Applications/Visual Studio Code.app/Contents/Frameworks/Code Helper (Plugin).app/Contents/MacOS/Code Helper (Plugin)",
					"Code Helper (Plugin) --type=utility"),
			},
			wantLabel: "codex-editor-extension",
			wantRoot:  98925,
			wantDepth: 1,
		},
		{
			name: "application bundle root with an embedded runtime child",
			chain: []RootCandidate{
				candidate(99010, 98925, 1, "node", "/opt/homebrew/bin/node", "node ./worker.js"),
				candidate(98925, 98900, 98900, "codex",
					"/Applications/ChatGPT.app/Contents/Resources/codex",
					"codex app-server"),
			},
			wantLabel: "chatgpt-desktop",
			wantRoot:  98925,
			wantDepth: 1,
		},
		{
			name: "wrapper and vendor binary pair",
			chain: []RootCandidate{
				candidate(98925, 98888, 98888, "codex",
					"/opt/homebrew/lib/node_modules/@openai/codex/vendor/aarch64-apple-darwin/codex/codex",
					"codex --resume"),
				candidate(98888, 98800, 98888, "node", "/opt/homebrew/bin/node", "node /opt/homebrew/bin/codex --resume"),
			},
			wantLabel: "codex-cli",
			wantRoot:  98925,
			wantDepth: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			match, ok := r.ResolveRoot(tt.chain)
			if !ok {
				t.Fatal("no root resolved")
			}
			if match.Label != tt.wantLabel {
				t.Errorf("label: got %q, want %q", match.Label, tt.wantLabel)
			}
			if match.Root.Entry.PID != tt.wantRoot {
				t.Errorf("root: got pid %d, want %d", match.Root.Entry.PID, tt.wantRoot)
			}
			if match.Depth != tt.wantDepth {
				t.Errorf("depth: got %d, want %d", match.Depth, tt.wantDepth)
			}
		})
	}
}

// TestUnrecognizedRootFallsBackToGeneric is the harness-neutrality property at
// the registry level: a harness nobody has heard of is still given a session
// boundary, labelled unknown-harness.
func TestUnrecognizedRootFallsBackToGeneric(t *testing.T) {
	r := newTestRegistry(t)
	// No user file exists, so only the compiled-in descriptors are in play.
	r.LoadUserFile(filepath.Join(t.TempDir(), "absent.yaml"))

	root := candidate(98888, 98800, 98888, "brand-new-agent", "/opt/brand-new/bin/agent", "agent --serve")
	root.HasTrackedChild = true

	chain := []RootCandidate{
		candidate(99010, 98888, 98888, "node", "/opt/homebrew/bin/node", "node ./server.js"),
		root,
		candidate(98800, 1, 98800, "zsh", "/bin/zsh", "-zsh"),
	}

	match, ok := r.ResolveRoot(chain)
	if !ok {
		t.Fatal("an unrecognized harness produced no session root")
	}
	if !match.Generic {
		t.Error("the match should come from the compiled-in generic descriptor")
	}
	if match.Label != UnknownHarnessLabel {
		t.Errorf("label: got %q, want %q", match.Label, UnknownHarnessLabel)
	}
	if match.Root.Entry.PID != 98888 || match.Depth != 1 {
		t.Errorf("root: got pid %d at depth %d, want 98888 at depth 1", match.Root.Entry.PID, match.Depth)
	}
}

// TestNamedRootBeatsNearerGenericRoot fixes the precedence between the two
// passes. A positive recognition is a better answer than a shape test, at any
// depth, and the alternative would split a session at any group leader that
// happened to sit between a process and its harness.
func TestNamedRootBeatsNearerGenericRoot(t *testing.T) {
	r := newTestRegistry(t)

	wrapper := candidate(98950, 98888, 98950, "supervise-me", "/opt/tooling/bin/wrapper", "wrapper ./server.js")
	wrapper.HasTrackedChild = true

	chain := []RootCandidate{
		candidate(99010, 98950, 98950, "node", "/opt/homebrew/bin/node", "node ./server.js"),
		wrapper,
		candidate(98888, 98800, 98888, "claude.exe", "/opt/homebrew/bin/claude", "claude"),
	}

	match, ok := r.ResolveRoot(chain)
	if !ok {
		t.Fatal("no root resolved")
	}
	if match.Label != "claude-code-cli" || match.Root.Entry.PID != 98888 {
		t.Errorf("got %q at pid %d, want the named root claude-code-cli at 98888", match.Label, match.Root.Entry.PID)
	}
}

func TestGenericRootRequiresAllThreeConditions(t *testing.T) {
	r := newTestRegistry(t)

	base := func() RootCandidate {
		c := candidate(98888, 98800, 98888, "brand-new-agent", "/opt/brand-new/bin/agent", "agent --serve")
		c.HasTrackedChild = true
		return c
	}

	tests := []struct {
		name  string
		build func() RootCandidate
		want  bool
	}{
		{
			name:  "group leader, not excluded, has a tracked child",
			build: base,
			want:  true,
		},
		{
			name: "not a group leader and not an application bundle",
			build: func() RootCandidate {
				c := base()
				c.Entry.PGID = 98800
				return c
			},
			want: false,
		},
		{
			name: "application bundle main process need not lead a group",
			build: func() RootCandidate {
				c := base()
				c.Entry.PGID = 98800
				c.Exe = "/Applications/Brand New.app/Contents/MacOS/Brand New"
				return c
			},
			want: true,
		},
		{
			name: "a login shell is never a session root",
			build: func() RootCandidate {
				c := base()
				c.Entry.Name = "zsh"
				return c
			},
			want: false,
		},
		{
			name: "a terminal emulator is never a session root",
			build: func() RootCandidate {
				c := base()
				c.Entry.Name = "Ghostty"
				return c
			},
			want: false,
		},
		{
			name: "a blocklisted binary is never a session root",
			build: func() RootCandidate {
				c := base()
				c.Entry.Name = "postgres"
				return c
			},
			want: false,
		},
		{
			name: "no tracked child means no session",
			build: func() RootCandidate {
				c := base()
				c.HasTrackedChild = false
				return c
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.genericRootMatches(tt.build()); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolveRootReturnsNothingWhenNoConditionHolds(t *testing.T) {
	r := newTestRegistry(t)
	chain := []RootCandidate{
		candidate(99010, 98800, 98800, "node", "/opt/homebrew/bin/node", "node ./server.js"),
		candidate(98800, 1, 98800, "zsh", "/bin/zsh", "-zsh"),
	}
	if match, ok := r.ResolveRoot(chain); ok {
		t.Errorf("resolved %+v, want no root: a shell is not a session", match)
	}
	if _, ok := r.ResolveRoot(nil); ok {
		t.Error("an empty chain resolved a root")
	}
}

func TestResolveRootBoundsTheChain(t *testing.T) {
	r := newTestRegistry(t)

	chain := make([]RootCandidate, 0, 64)
	for i := int32(0); i < 64; i++ {
		chain = append(chain, candidate(1000+i, 1001+i, 1000+i, "node", "/opt/homebrew/bin/node", "node ./worker.js"))
	}
	// The only recognizable root sits past the bound, so it must not be found.
	chain = append(chain, candidate(2000, 1, 2000, "claude.exe", "/opt/homebrew/bin/claude", "claude"))

	if match, ok := r.ResolveRoot(chain); ok {
		t.Errorf("resolved %+v past the %d link bound", match, MaxLinkDepth)
	}
}

func TestHarnessMarkerNamesFeedTheRedactionAllowlist(t *testing.T) {
	r := newTestRegistry(t)

	names := r.MarkerNames()
	if len(names) == 0 {
		t.Fatal("no marker names from the built-in table")
	}
	joined := strings.Join(names, ",")
	for _, want := range []string{"CLAUDE_CODE_SESSION_ID", "CLAUDE_PROJECT_DIR", "CLAUDECODE"} {
		if !strings.Contains(joined, want) {
			t.Errorf("marker names %v are missing %q", names, want)
		}
	}

	redactor := NewRedactor(names...)
	if !redactor.EnvNameAllowed("CLAUDE_CODE_SESSION_ID") {
		t.Error("a marker name from the registry was not allowlisted")
	}
	if redactor.EnvNameAllowed("SLACK_BOT_TOKEN") {
		t.Error("the allowlist widened past the secret-name guard")
	}
}
