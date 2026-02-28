package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMCPFile_ValidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude.json")

	config := `{
		"mcpServers": {
			"memory": {
				"command": "npx",
				"args": ["-y", "@modelcontextprotocol/server-memory"]
			},
			"filesystem": {
				"command": "node",
				"args": ["dist/index.js", "/Users/test"]
			}
		}
	}`
	if err := os.WriteFile(path, []byte(config), 0644); err != nil {
		t.Fatal(err)
	}

	servers, warn := loadMCPFile(path)
	if warn != "" {
		t.Errorf("unexpected warning: %s", warn)
	}
	if len(servers) != 2 {
		t.Errorf("expected 2 servers, got %d", len(servers))
	}
}

func TestLoadMCPFile_MissingFile(t *testing.T) {
	servers, warn := loadMCPFile("/nonexistent/mcp.json")
	if warn != "" {
		t.Error("missing file should not produce a warning")
	}
	if len(servers) != 0 {
		t.Error("missing file should return empty servers")
	}
}

func TestLoadMCPFile_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.json")

	if err := os.WriteFile(path, []byte("not json{{{"), 0644); err != nil {
		t.Fatal(err)
	}

	servers, warn := loadMCPFile(path)
	if warn == "" {
		t.Error("expected warning for invalid JSON")
	}
	if len(servers) != 0 {
		t.Error("invalid JSON should return empty servers")
	}
}

func TestLoadMCPFile_NoMCPServersKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	if err := os.WriteFile(path, []byte(`{"editor.fontSize": 14}`), 0644); err != nil {
		t.Fatal(err)
	}

	servers, warn := loadMCPFile(path)
	if warn != "" {
		t.Error("no mcpServers key should not produce a warning")
	}
	if len(servers) != 0 {
		t.Error("expected 0 servers when mcpServers key is absent")
	}
}

func TestLoadMCPFile_InvalidMCPServersFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad-mcp.json")

	// mcpServers is an array instead of an object
	if err := os.WriteFile(path, []byte(`{"mcpServers": ["not", "valid"]}`), 0644); err != nil {
		t.Fatal(err)
	}

	servers, warn := loadMCPFile(path)
	if warn == "" {
		t.Error("expected warning for invalid mcpServers format")
	}
	if len(servers) != 0 {
		t.Error("invalid format should return empty servers")
	}
}

func TestLoadMCPFile_EmptyCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty-cmd.json")

	config := `{"mcpServers": {"test": {"command": "", "args": []}}}`
	if err := os.WriteFile(path, []byte(config), 0644); err != nil {
		t.Fatal(err)
	}

	servers, _ := loadMCPFile(path)
	if len(servers) != 0 {
		t.Error("servers with empty command should be filtered out")
	}
}

func TestCountMCPOrphans_NoServers(t *testing.T) {
	procs := []ProcessInfo{{PID: 1, Name: "node"}}
	count := CountMCPOrphans(procs, nil)
	if count != 0 {
		t.Errorf("expected 0 orphans with no servers, got %d", count)
	}
}

func TestCountMCPOrphans_WithIDERunning(t *testing.T) {
	procs := []ProcessInfo{
		{PID: 100, Name: "node", Cmdline: "node mcp-server-memory"},
		{PID: 200, Name: "Cursor", Cmdline: "/Applications/Cursor.app/Contents/MacOS/Cursor"},
	}
	servers := []MCPServer{{Command: "node", Args: []string{"mcp-server-memory"}}}

	count := CountMCPOrphans(procs, servers)
	if count != 0 {
		t.Errorf("expected 0 orphans with IDE running, got %d", count)
	}
}

func TestCountMCPOrphans_WithoutIDE(t *testing.T) {
	procs := []ProcessInfo{
		{PID: 100, Name: "node", Cmdline: "node mcp-server-memory"},
		{PID: 200, Name: "Finder", Cmdline: "/System/Library/CoreServices/Finder.app/Contents/MacOS/Finder"},
	}
	servers := []MCPServer{{Command: "node", Args: []string{"mcp-server-memory"}}}

	count := CountMCPOrphans(procs, servers)
	if count != 1 {
		t.Errorf("expected 1 orphan without IDE, got %d", count)
	}
}

func TestCountMCPOrphans_MultipleInstances(t *testing.T) {
	procs := []ProcessInfo{
		{PID: 100, Name: "node", Cmdline: "node mcp-server-memory"},
		{PID: 101, Name: "node", Cmdline: "node mcp-server-memory"},
		{PID: 102, Name: "node", Cmdline: "node mcp-server-memory"},
		{PID: 200, Name: "Finder", Cmdline: "Finder"},
	}
	servers := []MCPServer{{Command: "node", Args: []string{"mcp-server-memory"}}}

	count := CountMCPOrphans(procs, servers)
	if count != 3 {
		t.Errorf("expected 3 orphans, got %d", count)
	}
}

func TestCountRunningInstances(t *testing.T) {
	procs := []ProcessInfo{
		{PID: 100, Name: "node", Cmdline: "node /path/to/mcp-server"},
		{PID: 101, Name: "python3", Cmdline: "python3 mcp-server.py"},
		{PID: 102, Name: "node", Cmdline: "node /path/to/mcp-server"},
		{PID: 103, Name: "node", Cmdline: "node something-else"},
	}

	server := MCPServer{Command: "node", Args: []string{"/path/to/mcp-server"}}
	count := countRunningInstances(procs, server)
	if count != 2 {
		t.Errorf("expected 2 running instances, got %d", count)
	}
}

func TestCountRunningInstances_NoArgs(t *testing.T) {
	procs := []ProcessInfo{
		{PID: 100, Name: "node", Cmdline: "node anything"},
	}

	server := MCPServer{Command: "node", Args: nil}
	count := countRunningInstances(procs, server)
	if count != 1 {
		t.Errorf("expected 1 running instance (no args filter), got %d", count)
	}
}
