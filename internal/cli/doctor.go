package cli

import (
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/tjp2021/devreap/internal/daemon"
	"github.com/tjp2021/devreap/internal/patterns"
	"github.com/tjp2021/devreap/internal/scanner"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Run diagnostics and check configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("devreap doctor")
		fmt.Println("══════════════")
		allGood := true

		// 1. OS check
		fmt.Printf("\n[OS] %s/%s\n", runtime.GOOS, runtime.GOARCH)
		if runtime.GOOS != "darwin" {
			warn("LaunchAgent only works on macOS")
		}

		// 2. Config
		fmt.Printf("\n[Config] %s\n", cfgPath)
		if _, err := os.Stat(cfg.LogDir); os.IsNotExist(err) {
			warn("Log directory does not exist: %s (will be created on start)", cfg.LogDir)
		} else {
			pass("Log directory exists: %s", cfg.LogDir)
		}

		// 3. Patterns
		registry, err := patterns.NewRegistry()
		if err != nil {
			fail("Failed to load patterns: %v", err)
			allGood = false
		} else {
			pass("Loaded %d patterns", registry.Count())
		}

		// 4. Process enumeration
		procs, err := scanner.EnumerateProcesses(cmd.Context())
		if err != nil {
			fail("Cannot enumerate processes: %v", err)
			allGood = false
		} else {
			pass("Process enumeration works (%d processes)", len(procs))
		}

		// 5. MCP configs
		mcpResult := scanner.LoadMCPConfigs()
		if len(mcpResult.Servers) > 0 {
			pass("Found %d MCP server configs", len(mcpResult.Servers))
		} else {
			info("No MCP server configs found (this is normal if you don't use MCP)")
		}
		for _, w := range mcpResult.Warnings {
			warn("MCP config: %s", w)
		}

		// 6. LaunchAgent
		if daemon.IsInstalled() {
			pass("LaunchAgent installed at %s", daemon.PlistPath())
		} else {
			info("LaunchAgent not installed (run 'devreap install' to set up)")
		}

		// 7. Daemon status
		if daemon.IsRunning(cfg.PidFile) {
			pid, _ := daemon.ReadPID(cfg.PidFile)
			pass("Daemon running (PID %d)", pid)
		} else {
			info("Daemon not running")
		}

		fmt.Println()
		if allGood {
			fmt.Println("All checks passed.")
		} else {
			fmt.Println("Some checks failed. See above for details.")
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}

func pass(format string, args ...interface{}) {
	fmt.Printf("  ✓ %s\n", fmt.Sprintf(format, args...))
}

func warn(format string, args ...interface{}) {
	fmt.Printf("  ! %s\n", fmt.Sprintf(format, args...))
}

func fail(format string, args ...interface{}) {
	fmt.Printf("  ✗ %s\n", fmt.Sprintf(format, args...))
}

func info(format string, args ...interface{}) {
	fmt.Printf("  - %s\n", fmt.Sprintf(format, args...))
}
