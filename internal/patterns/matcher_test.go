package patterns

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCommandName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/usr/local/bin/node", "node"},
		{"node", "node"},
		{"/opt/homebrew/bin/ffmpeg", "ffmpeg"},
		{"", ""},
		{"python3.11", "python3.11"},
	}

	for _, tt := range tests {
		got := CommandName(tt.input)
		if got != tt.want {
			t.Errorf("CommandName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestCommandArgs(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"node server.js --port 3000", "server.js --port 3000"},
		{"node", ""},
		{"", ""},
		{"python3 -m mcp.server", "-m mcp.server"},
	}

	for _, tt := range tests {
		got := CommandArgs(tt.input)
		if got != tt.want {
			t.Errorf("CommandArgs(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestMatchPattern(t *testing.T) {
	tests := []struct {
		name    string
		pattern Pattern
		cmdName string
		cmdArgs string
		want    bool
	}{
		{
			name:    "node MCP server matches",
			pattern: Pattern{Command: "^node$", Args: "(mcp|modelcontextprotocol)"},
			cmdName: "node",
			cmdArgs: "/Users/tim/.npm/mcp-server-fetch/index.js",
			want:    true,
		},
		{
			name:    "python MCP server matches",
			pattern: Pattern{Command: "^python[0-9.]*$", Args: "(mcp|model.context)"},
			cmdName: "python3.11",
			cmdArgs: "-m mcp.server",
			want:    true,
		},
		{
			name:    "node without MCP args doesn't match MCP pattern",
			pattern: Pattern{Command: "^node$", Args: "(mcp|modelcontextprotocol)"},
			cmdName: "node",
			cmdArgs: "server.js",
			want:    false,
		},
		{
			name:    "ffmpeg matches without args pattern",
			pattern: Pattern{Command: "^ffmpeg$"},
			cmdName: "ffmpeg",
			cmdArgs: "-i input.mp4 output.mp4",
			want:    true,
		},
		{
			name:    "wrong process name doesn't match",
			pattern: Pattern{Command: "^node$"},
			cmdName: "python3",
			cmdArgs: "",
			want:    false,
		},
		{
			name:    "chrome headless matches",
			pattern: Pattern{Command: "^(Google Chrome|chrome|chromium)$", Args: "--headless"},
			cmdName: "chrome",
			cmdArgs: "--headless --disable-gpu",
			want:    true,
		},
		{
			name:    "next dev matches",
			pattern: Pattern{Command: "^node$", Args: "next dev"},
			cmdName: "node",
			cmdArgs: "/Users/tim/project/node_modules/.bin/next dev",
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cp, err := getCompiled(tt.pattern)
			if err != nil {
				t.Fatalf("getCompiled error: %v", err)
			}
			got := matchCompiled(cp, tt.cmdName, tt.cmdArgs)
			if got != tt.want {
				t.Errorf("matchCompiled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRegistryMatch(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry() error: %v", err)
	}

	// should match node MCP server
	result := registry.Match("node", "/path/to/mcp-server/index.js")
	if result == nil {
		t.Error("expected match for node MCP server, got nil")
	} else if result.Pattern.Category != "mcp" {
		t.Errorf("expected category 'mcp', got %q", result.Pattern.Category)
	}

	// should match ffmpeg
	result = registry.Match("ffmpeg", "-i input.mp4 output.mp4")
	if result == nil {
		t.Error("expected match for ffmpeg, got nil")
	} else if result.Pattern.Name != "ffmpeg" {
		t.Errorf("expected pattern name 'ffmpeg', got %q", result.Pattern.Name)
	}

	// should not match random process
	result = registry.Match("Finder", "")
	if result != nil {
		t.Errorf("expected no match for Finder, got %+v", result)
	}
}

func TestRegistryCount(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry() error: %v", err)
	}

	count := registry.Count()
	if count < 10 {
		t.Errorf("expected at least 10 patterns, got %d", count)
	}
}

func TestRegexCaching(t *testing.T) {
	p := Pattern{Command: "^node$", Args: "mcp"}

	// First call should compile
	cp1, err := getCompiled(p)
	if err != nil {
		t.Fatalf("first getCompiled error: %v", err)
	}

	// Second call should return cached
	cp2, err := getCompiled(p)
	if err != nil {
		t.Fatalf("second getCompiled error: %v", err)
	}

	// Should be same pointer (from cache)
	if cp1 != cp2 {
		t.Error("expected cached compiled pattern to be reused")
	}
}

func TestInvalidRegexSkipped(t *testing.T) {
	registry := &Registry{
		patterns: []Pattern{
			{Command: "[invalid regex", Args: ""},
			{Command: "^ffmpeg$", Args: ""},
		},
	}

	// Should skip invalid regex and still match ffmpeg
	result := registry.Match("ffmpeg", "")
	if result == nil {
		t.Error("expected match for ffmpeg despite invalid first pattern")
	}
}

func TestLoadExtra(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry error: %v", err)
	}
	baseCt := registry.Count()

	// Create a temp extra pattern file
	dir := t.TempDir()
	path := filepath.Join(dir, "extra.yaml")
	content := `patterns:
  - name: custom-server
    category: custom
    command: "^my-server$"
    max_duration: 1h
    signal: sigterm
    description: "custom test server"
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := registry.LoadExtra([]string{path}); err != nil {
		t.Fatalf("LoadExtra error: %v", err)
	}

	if registry.Count() != baseCt+1 {
		t.Errorf("expected %d patterns after LoadExtra, got %d", baseCt+1, registry.Count())
	}

	// Should match the new pattern
	result := registry.Match("my-server", "")
	if result == nil {
		t.Error("expected match for custom-server pattern")
	}
}

func TestLoadExtra_InvalidPath(t *testing.T) {
	registry, _ := NewRegistry()
	err := registry.LoadExtra([]string{"/nonexistent/patterns.yaml"})
	if err == nil {
		t.Error("expected error for nonexistent extra pattern file")
	}
}

func TestLoadExtra_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	os.WriteFile(path, []byte("not: valid: yaml: [[["), 0644)

	registry, _ := NewRegistry()
	err := registry.LoadExtra([]string{path})
	if err == nil {
		t.Error("expected error for invalid YAML in extra pattern file")
	}
}

func TestRegistryAll(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry error: %v", err)
	}

	all := registry.All()
	if len(all) != registry.Count() {
		t.Errorf("All() length %d != Count() %d", len(all), registry.Count())
	}

	// Each pattern should have a name and command
	for _, p := range all {
		if p.Name == "" {
			t.Error("pattern has empty name")
		}
		if p.Command == "" {
			t.Error("pattern has empty command")
		}
	}
}

func TestMatchNoArgs(t *testing.T) {
	// Pattern with no args regex should match any args
	p := Pattern{Command: "^ffmpeg$", Args: ""}
	cp, err := getCompiled(p)
	if err != nil {
		t.Fatalf("getCompiled error: %v", err)
	}
	if !matchCompiled(cp, "ffmpeg", "some args here") {
		t.Error("pattern with empty Args should match any args")
	}
	if !matchCompiled(cp, "ffmpeg", "") {
		t.Error("pattern with empty Args should match empty args")
	}
}

func TestNoMatchOnName(t *testing.T) {
	// Command regex must match
	p := Pattern{Command: "^node$", Args: "mcp"}
	cp, err := getCompiled(p)
	if err != nil {
		t.Fatal(err)
	}
	if matchCompiled(cp, "python3", "mcp-server") {
		t.Error("should not match when command name doesn't match")
	}
}

func TestNoMatchOnArgs(t *testing.T) {
	// When args pattern is set, args must match too
	p := Pattern{Command: "^node$", Args: "mcp"}
	cp, err := getCompiled(p)
	if err != nil {
		t.Fatal(err)
	}
	if matchCompiled(cp, "node", "server.js") {
		t.Error("should not match when args don't match")
	}
}
