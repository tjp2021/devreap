package cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/tjp2021/devreap/internal/patterns"
)

var patternsCmd = &cobra.Command{
	Use:   "patterns",
	Short: "List all loaded patterns",
	RunE: func(cmd *cobra.Command, args []string) error {
		registry, err := patterns.NewRegistry()
		if err != nil {
			return fmt.Errorf("loading patterns: %w", err)
		}

		if len(cfg.Patterns) > 0 {
			if err := registry.LoadExtra(cfg.Patterns); err != nil {
				return fmt.Errorf("loading extra patterns: %w", err)
			}
		}

		all := registry.All()
		fmt.Printf("Loaded %d patterns:\n\n", len(all))

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tCATEGORY\tCOMMAND\tMAX DUR\tSIGNAL\tDESCRIPTION")
		fmt.Fprintln(w, "----\t--------\t-------\t-------\t------\t-----------")

		for _, p := range all {
			signal := string(p.Signal)
			if signal == "" {
				signal = "default"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				p.Name, p.Category, p.Command, p.MaxDuration, signal, p.Description)
		}

		w.Flush()
		return nil
	},
}

func init() {
	rootCmd.AddCommand(patternsCmd)
}
