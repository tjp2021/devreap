package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/tjp2021/devreap/internal/hygiene"
)

var hygieneJSON bool

var hygieneCmd = &cobra.Command{
	Use:   "hygiene",
	Short: "Run system hygiene audit",
	Long: `Checks for common system rot: broken LaunchAgents, ghost Claude sessions,
dead crons, zombie dotdirs, sensitive files in git, Downloads buildup,
Claude debug/telemetry accumulation, and low disk space.

Exit 0 = clean, Exit 1 = issues found.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		checker, err := hygiene.New()
		if err != nil {
			return err
		}

		result := checker.RunAll()

		if hygieneJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			if err := enc.Encode(result); err != nil {
				return err
			}
			if len(result.Issues) > 0 {
				return hygiene.ErrIssuesFound
			}
			return nil
		}

		if len(result.Issues) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "All clean. No hygiene issues found.")
			return nil
		}

		for _, issue := range result.Issues {
			fmt.Fprintf(cmd.OutOrStdout(), "  ! [%s] %s\n", issue.Check, issue.Message)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "\nTotal: %d issue(s) found.\n", len(result.Issues))

		return hygiene.ErrIssuesFound
	},
}

func init() {
	hygieneCmd.Flags().BoolVar(&hygieneJSON, "json", false, "output as JSON")
	rootCmd.AddCommand(hygieneCmd)
}
