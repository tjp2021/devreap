package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/tjp2021/devreap/internal/config"
)

var (
	cfgPath string
	cfg     *config.Config
)

var rootCmd = &cobra.Command{
	Use:   "devreap",
	Short: "Automatically detect and kill orphaned developer processes",
	Long: `devreap monitors your system for orphaned developer processes — MCP servers,
dev servers, headless browsers, ffmpeg instances — that survive after their
parent IDE or terminal crashes.

Uses multi-signal scoring to avoid false positives. Zero config to start.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Skip config loading for commands that don't need it
		switch cmd.Name() {
		case "version":
			return nil
		}
		var err error
		cfg, err = config.Load(cfgPath)
		if err != nil {
			return err
		}
		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgPath, "config", config.DefaultConfigPath, "config file path")
}

func Execute() error {
	err := rootCmd.Execute()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
	}
	return err
}
