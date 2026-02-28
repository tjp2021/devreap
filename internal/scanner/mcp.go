package scanner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MCPConfig represents the MCP server configuration from IDE config files.
type MCPConfig struct {
	Servers map[string]MCPServer `json:"mcpServers"`
}

// MCPServer represents a single MCP server entry from an IDE config file.
type MCPServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// MCPLoadResult contains MCP servers and any warnings from parsing config files.
type MCPLoadResult struct {
	Servers  []MCPServer
	Warnings []string
}

// LoadMCPConfigs reads MCP server configurations from known IDE config paths.
func LoadMCPConfigs() MCPLoadResult {
	result := MCPLoadResult{}
	home, err := os.UserHomeDir()
	if err != nil {
		return result
	}

	paths := []string{
		filepath.Join(home, ".claude.json"),
		filepath.Join(home, ".cursor", "mcp.json"),
		filepath.Join(home, ".vscode", "mcp.json"),
		filepath.Join(home, "Library", "Application Support", "Code", "User", "settings.json"),
	}

	for _, path := range paths {
		servers, warn := loadMCPFile(path)
		result.Servers = append(result.Servers, servers...)
		if warn != "" {
			result.Warnings = append(result.Warnings, warn)
		}
	}

	return result
}

// loadMCPFile reads and parses a single MCP config file.
// Returns servers found and an optional warning if the file exists but can't be parsed.
func loadMCPFile(path string) ([]MCPServer, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		// File doesn't exist — not a warning
		return nil, ""
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Sprintf("%s: invalid JSON: %v", path, err)
	}

	serversRaw, ok := raw["mcpServers"]
	if !ok {
		// File exists but has no mcpServers key — not an error, just no MCP config
		return nil, ""
	}

	var serverMap map[string]MCPServer
	if err := json.Unmarshal(serversRaw, &serverMap); err != nil {
		return nil, fmt.Sprintf("%s: invalid mcpServers format: %v", path, err)
	}

	var servers []MCPServer
	for _, s := range serverMap {
		if s.Command != "" {
			servers = append(servers, s)
		}
	}
	return servers, ""
}

// CountMCPOrphans compares configured MCP servers against running processes
// and returns the number of excess instances (running but no IDE parent).
func CountMCPOrphans(procs []ProcessInfo, mcpServers []MCPServer) int {
	if len(mcpServers) == 0 {
		return 0
	}

	ideAlive := checkIDERunningFromList(procs)

	orphans := 0
	for _, server := range mcpServers {
		running := countRunningInstances(procs, server)
		if running > 0 && !ideAlive {
			orphans += running
		}
	}
	return orphans
}

func countRunningInstances(procs []ProcessInfo, server MCPServer) int {
	count := 0
	serverCmd := filepath.Base(server.Command)
	argsStr := strings.Join(server.Args, " ")

	for _, p := range procs {
		if strings.Contains(p.Name, serverCmd) || strings.Contains(p.Cmdline, server.Command) {
			if argsStr == "" || strings.Contains(p.Cmdline, argsStr) {
				count++
			}
		}
	}
	return count
}
