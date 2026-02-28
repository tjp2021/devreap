package cli

import (
	"fmt"

	"github.com/spf13/cobra"

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

		fmt.Printf("Patterns:     %d loaded\n", registry.Count())
		fmt.Printf("Threshold:    %.2f\n", cfg.KillThreshold)
		fmt.Printf("Interval:     %s\n", cfg.ScanInterval)
		fmt.Printf("Dry-run:      %v\n", cfg.DryRun)
		fmt.Printf("Notify:       %v\n", cfg.Notify.Enabled)
		fmt.Printf("Log dir:      %s\n", cfg.LogDir)
		fmt.Printf("Config:       %s\n", cfgPath)

		return nil
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
