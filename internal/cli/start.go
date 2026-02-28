package cli

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/tjp2021/devreap/internal/daemon"
	"github.com/tjp2021/devreap/internal/logger"
	"github.com/tjp2021/devreap/internal/notify"
	"github.com/tjp2021/devreap/internal/patterns"
	"github.com/tjp2021/devreap/internal/scanner"
)

var foreground bool

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the devreap daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		if daemon.IsRunning(cfg.PidFile) {
			return fmt.Errorf("daemon is already running (PID file: %s)", cfg.PidFile)
		}

		if foreground {
			return runForeground()
		}

		return runBackground()
	},
}

func init() {
	startCmd.Flags().BoolVar(&foreground, "foreground", false, "run in foreground (used by LaunchAgent)")
	rootCmd.AddCommand(startCmd)
}

func runForeground() error {
	registry, err := patterns.NewRegistry()
	if err != nil {
		return fmt.Errorf("loading patterns: %w", err)
	}

	if len(cfg.Patterns) > 0 {
		if err := registry.LoadExtra(cfg.Patterns); err != nil {
			return fmt.Errorf("loading extra patterns: %w", err)
		}
	}

	log, err := logger.New(cfg.LogDir, cfg.MaxLogSize, cfg.MaxLogFiles)
	if err != nil {
		return fmt.Errorf("creating logger: %w", err)
	}
	defer log.Close()

	var notifier notify.Notifier
	if cfg.Notify.Enabled {
		notifier = notify.NewMacOS()
	} else {
		notifier = &notify.Noop{}
	}

	s := scanner.New(registry, cfg)
	d := daemon.New(cfg, s, log, notifier)

	return d.Run()
}

func runBackground() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding executable: %w", err)
	}

	cmdArgs := []string{"start", "--foreground"}
	if cfgPath != "" {
		cmdArgs = append(cmdArgs, "--config", cfgPath)
	}

	proc := exec.Command(exe, cmdArgs...)
	proc.Stdout = nil
	proc.Stderr = nil
	proc.Stdin = nil

	if err := proc.Start(); err != nil {
		return fmt.Errorf("starting daemon: %w", err)
	}

	// Wait briefly and verify the child is still alive
	// (catches immediate crashes like permission errors, bad config, etc.)
	childPid := proc.Process.Pid
	time.Sleep(500 * time.Millisecond)

	if !daemon.IsRunning(cfg.PidFile) {
		// Child may have died — check if process is still alive at all
		p, _ := os.FindProcess(childPid)
		if p != nil {
			if err := p.Signal(syscall.Signal(0)); err != nil {
				return fmt.Errorf("daemon exited immediately after start (PID %d) — check logs: %s/launchd-stderr.log", childPid, cfg.LogDir)
			}
		}
	}

	fmt.Printf("Daemon started (PID %d)\n", childPid)
	return nil
}
