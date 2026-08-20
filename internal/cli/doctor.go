package cli

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/tjp2021/devreap/internal/attribution"
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

		// 8. Attribution
		if !checkAttribution() {
			allGood = false
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

// checkAttribution reports watcher liveness, last heartbeat age, store size,
// snapshot age, schema version, and coverage.
//
// Staleness is judged in awake time rather than wall-clock time, so an overnight
// sleep does not report a healthy watcher as dead. A store the tool cannot read
// is a failure line rather than silence.
func checkAttribution() bool {
	fmt.Printf("\n[Attribution] %s\n", cfg.Attribution.StoreDir)
	if !cfg.Attribution.Enabled {
		info("Attribution is off (set attribution.enabled to record process ownership)")
		return true
	}

	view, err := attribution.LoadView(cfg.Attribution.StoreDir, liveRSS(), time.Now())
	if err != nil {
		fail("Cannot read the attribution store: %v", err)
		return false
	}

	pass("Store schema version %d", view.SchemaVersion)
	if view.JournalBytes == 0 {
		info("The journal is empty (normal before the first run)")
	} else {
		pass("Journal holds %s across %d segment(s), under a %s ceiling",
			humanBytes(view.JournalBytes), view.Segments,
			humanBytes(attribution.DefaultSegmentSize*attribution.DefaultMaxSegments))
	}
	if !checkStorePermissions(cfg.Attribution.StoreDir) {
		return false
	}

	if view.SnapshotAt != nil {
		pass("Snapshot written %s ago", time.Since(*view.SnapshotAt).Truncate(time.Second))
	} else {
		info("No snapshot yet (written every 5 minutes and on clean shutdown)")
	}

	healthy := true
	if beat := view.LastHeartbeat; beat != nil {
		age := time.Since(beat.At).Truncate(time.Second)
		stale := attribution.StaleHeartbeatIntervals * attribution.DefaultHeartbeatInterval
		// The daemon has to be running for a heartbeat to be current at all.
		switch {
		case !daemon.IsRunning(cfg.PidFile):
			info("Last heartbeat %s ago (the daemon is not running)", age)
		case age > stale:
			fail("Last heartbeat %s ago, past %s of silence: attribution data is untrusted", age, stale)
			healthy = false
		default:
			pass("Last heartbeat %s ago, poll %dus", age, beat.PollDurationMicros)
		}
	} else if daemon.IsRunning(cfg.PidFile) {
		info("No heartbeat yet (the first one is written after a minute)")
	}

	if view.Tracked == 0 {
		info("No pattern-matched processes tracked yet, so coverage is not measurable")
	} else if view.Coverage < 0.90 {
		warn("Coverage %.0f%% (%d of %d), below the 90%% the feature must reach",
			view.Coverage*100, view.Attributed, view.Tracked)
	} else {
		pass("Coverage %.0f%% (%d of %d pattern-matched processes observed)",
			view.Coverage*100, view.Attributed, view.Tracked)
	}

	for _, finding := range view.Findings {
		warn("%s: %s", finding.Kind, finding.Detail)
	}
	return healthy
}

// checkStorePermissions asserts the store is owner-only. A birth record holds
// command lines and repository paths even after redaction.
func checkStorePermissions(dir string) bool {
	stat, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return true
		}
		fail("Cannot inspect the store directory: %v", err)
		return false
	}
	if mode := stat.Mode().Perm(); mode != attribution.StoreDirMode.Perm() {
		fail("Store directory mode is %#o, want %#o", mode, attribution.StoreDirMode.Perm())
		return false
	}
	pass("Store directory is owner-only (%#o)", attribution.StoreDirMode.Perm())
	return true
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 3 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMG"[exp])
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
