package cli

import (
	"fmt"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/tjp2021/devreap/internal/attribution"
	"github.com/tjp2021/devreap/internal/daemon"
	"github.com/tjp2021/devreap/internal/patterns"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon status and configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		running := daemon.IsRunning(cfg.PidFile)
		pid := 0
		if running {
			pid, _ = daemon.ReadPID(cfg.PidFile)
		}

		registry, err := patterns.NewRegistry()
		if err != nil {
			return err
		}

		installed := daemon.IsInstalled()

		fmt.Println("devreap status")
		fmt.Println("──────────────")

		if running {
			fmt.Printf("Daemon:       running (PID %d)\n", pid)
		} else {
			fmt.Println("Daemon:       stopped")
		}

		if installed {
			fmt.Printf("LaunchAgent:  installed (%s)\n", daemon.PlistPath())
		} else {
			fmt.Println("LaunchAgent:  not installed")
		}

		if cfg.DryRun {
			fmt.Println("Mode:         observe-only (dry-run) — nothing is killed")
		} else {
			fmt.Println("Mode:         ACTIVE — matching processes will be killed")
		}

		fmt.Printf("Patterns:     %d loaded\n", registry.Count())
		fmt.Printf("Threshold:    %.2f\n", cfg.KillThreshold)
		fmt.Printf("Interval:     %s\n", cfg.ScanInterval)
		fmt.Printf("Dry-run:      %v\n", cfg.DryRun)
		fmt.Printf("Notify:       %v\n", cfg.Notify.Enabled)
		fmt.Printf("Log dir:      %s\n", cfg.LogDir)
		fmt.Printf("Config:       %s\n", cfgPath)

		printAttributionStatus()
		return nil
	},
}

// printAttributionStatus prints coverage and the count of processes in each
// lifecycle state. It reads the store rather than the daemon, because this runs
// in a different process.
func printAttributionStatus() {
	fmt.Println()
	if !cfg.Attribution.Enabled {
		fmt.Println("Attribution:  off")
		return
	}

	view, err := attribution.LoadView(cfg.Attribution.StoreDir, liveRSS(), time.Now())
	if err != nil {
		fmt.Printf("Attribution:  unreadable (%v)\n", err)
		return
	}

	fmt.Println("Attribution:  on (observe-only)")
	if cfg.Attribution.GateKills {
		fmt.Println("Kill gating:  ON — attribution removes processes from the kill set")
	} else {
		fmt.Println("Kill gating:  off — attribution reports and never gates")
	}
	fmt.Printf("Coverage:     %.0f%% (%d of %d pattern-matched processes observed)\n",
		view.Coverage*100, view.Attributed, view.Tracked)
	fmt.Printf("Sessions:     %d\n", len(view.Sessions))
	fmt.Printf("Unattributed: %d\n", len(view.Unattributed))

	if len(view.StateCounts) > 0 {
		states := make([]string, 0, len(view.StateCounts))
		for state := range view.StateCounts {
			states = append(states, string(state))
		}
		sort.Strings(states)
		fmt.Println("States:")
		for _, state := range states {
			fmt.Printf("  %-18s %d\n", state, view.StateCounts[attribution.LifecycleState(state)])
		}
	}
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
