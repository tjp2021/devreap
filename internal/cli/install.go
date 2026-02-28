package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/tjp2021/devreap/internal/daemon"
)

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Install devreap as a macOS LaunchAgent",
	RunE: func(cmd *cobra.Command, args []string) error {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("finding executable path: %w", err)
		}

		if err := daemon.Install(exe, cfg.LogDir); err != nil {
			return fmt.Errorf("installing LaunchAgent: %w", err)
		}

		fmt.Printf("LaunchAgent installed at %s\n", daemon.PlistPath())
		fmt.Printf("Binary: %s\n", exe)
		fmt.Println("devreap will start automatically on login.")
		return nil
	},
}

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove devreap LaunchAgent",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := daemon.Uninstall(); err != nil {
			return fmt.Errorf("uninstalling LaunchAgent: %w", err)
		}

		fmt.Println("LaunchAgent removed.")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(installCmd)
	rootCmd.AddCommand(uninstallCmd)
}
