package cli

import (
	"fmt"
	"os"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/tjp2021/devreap/internal/daemon"
)

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the devreap daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		if !daemon.IsRunning(cfg.PidFile) {
			return fmt.Errorf("daemon not running")
		}

		pid, err := daemon.ReadPID(cfg.PidFile)
		if err != nil {
			return fmt.Errorf("reading PID file: %w", err)
		}

		proc, err := os.FindProcess(pid)
		if err != nil {
			return fmt.Errorf("process %d not found: %w", pid, err)
		}

		if err := proc.Signal(syscall.SIGTERM); err != nil {
			return fmt.Errorf("sending SIGTERM to PID %d: %w", pid, err)
		}

		fmt.Printf("Sent SIGTERM to daemon (PID %d)\n", pid)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(stopCmd)
}
