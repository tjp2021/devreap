package cli

import (
	"context"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/tjp2021/devreap/internal/killer"
	"github.com/tjp2021/devreap/internal/scanner"
)

var killPort uint32

var killCmd = &cobra.Command{
	Use:   "kill [pid]",
	Short: "Gracefully kill a process by PID or port",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if killPort > 0 {
			return killByPort(killPort)
		}

		if len(args) == 0 {
			return fmt.Errorf("provide a PID or use --port")
		}

		pid, err := strconv.ParseInt(args[0], 10, 32)
		if err != nil {
			return fmt.Errorf("invalid PID: %w", err)
		}

		result := killer.KillByPID(int32(pid), cfg.Blocklist, cfg.GracePeriod)
		if result.Success {
			fmt.Printf("Killed PID %d (%s) with %s\n", result.PID, result.Name, result.Signal)
		} else {
			return fmt.Errorf("failed to kill PID %d: %s", result.PID, result.Error)
		}

		return nil
	},
}

func init() {
	killCmd.Flags().Uint32Var(&killPort, "port", 0, "kill process listening on this port")
	rootCmd.AddCommand(killCmd)
}

func killByPort(port uint32) error {
	procs, err := scanner.EnumerateProcesses(context.Background())
	if err != nil {
		return fmt.Errorf("enumerating processes: %w", err)
	}

	for _, p := range procs {
		for _, pPort := range p.Ports {
			if pPort == port {
				result := killer.KillByPID(p.PID, cfg.Blocklist, cfg.GracePeriod)
				if result.Success {
					fmt.Printf("Killed PID %d (%s) listening on port %d\n", p.PID, p.Name, port)
				} else {
					return fmt.Errorf("failed to kill PID %d: %s", p.PID, result.Error)
				}
				return nil
			}
		}
	}

	return fmt.Errorf("no process found listening on port %d", port)
}
