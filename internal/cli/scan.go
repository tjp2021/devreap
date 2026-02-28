package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/tjp2021/devreap/internal/patterns"
	"github.com/tjp2021/devreap/internal/scanner"
)

var (
	scanJSON    bool
	scanVerbose bool
)

var scanCmd = &cobra.Command{
	Use:   "scan",
	Short: "One-shot scan for orphaned processes",
	Long:  `Scans all running processes, matches against known patterns, and scores them for orphan likelihood.`,
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

		s := scanner.New(registry, cfg)
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		result, err := s.Scan(ctx)
		if err != nil {
			return fmt.Errorf("scan failed: %w", err)
		}

		// MCP cross-reference using the same process snapshot
		mcpResult := scanner.LoadMCPConfigs()
		mcpOrphans := scanner.CountMCPOrphans(result.Processes, mcpResult.Servers)

		if scanJSON {
			return printScanJSON(result, mcpOrphans)
		}

		return printScanTable(result, mcpOrphans)
	},
}

func init() {
	scanCmd.Flags().BoolVar(&scanJSON, "json", false, "output as JSON")
	scanCmd.Flags().BoolVarP(&scanVerbose, "verbose", "v", false, "show all matched processes, not just orphan candidates")
	rootCmd.AddCommand(scanCmd)
}

type scanOutput struct {
	TotalProcesses int            `json:"total_processes"`
	Matched        int            `json:"matched"`
	OrphanCount    int            `json:"orphan_count"`
	MCPOrphans     int            `json:"mcp_orphans"`
	Orphans        []orphanOutput `json:"orphans"`
	AllMatches     []orphanOutput `json:"all_matches,omitempty"`
}

type orphanOutput struct {
	PID     int32              `json:"pid"`
	Name    string             `json:"name"`
	Pattern string             `json:"pattern"`
	Score   float64            `json:"score"`
	Age     string             `json:"age"`
	Signals map[string]float64 `json:"signals"`
}

func toOrphanOutput(o scanner.OrphanCandidate) orphanOutput {
	return orphanOutput{
		PID:     o.Process.PID,
		Name:    o.Process.Name,
		Pattern: o.Pattern.Name,
		Score:   o.Score,
		Age:     o.Process.Age().Truncate(time.Second).String(),
		Signals: o.Signals,
	}
}

func printScanJSON(result *scanner.ScanResult, mcpOrphans int) error {
	out := scanOutput{
		TotalProcesses: result.TotalProcesses,
		Matched:        result.Matched,
		OrphanCount:    len(result.Orphans),
		MCPOrphans:     mcpOrphans,
	}

	for _, o := range result.Orphans {
		out.Orphans = append(out.Orphans, toOrphanOutput(o))
	}

	if scanVerbose {
		for _, o := range result.AllMatches {
			out.AllMatches = append(out.AllMatches, toOrphanOutput(o))
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func printScanTable(result *scanner.ScanResult, mcpOrphans int) error {
	fmt.Printf("Scanned %d processes, %d matched patterns\n", result.TotalProcesses, result.Matched)

	if mcpOrphans > 0 {
		fmt.Printf("MCP config cross-reference: %d likely orphaned MCP server(s)\n", mcpOrphans)
	}

	if scanVerbose && len(result.AllMatches) > 0 {
		fmt.Printf("\nAll pattern-matched processes (%d):\n\n", len(result.AllMatches))
		printOrphanTable(result.AllMatches, true)
	}

	if len(result.Orphans) == 0 {
		fmt.Println("\nNo orphan candidates found. System is clean.")
		return nil
	}

	fmt.Printf("\nFound %d orphan candidate(s) (threshold: %.1f):\n\n", len(result.Orphans), cfg.KillThreshold)
	printOrphanTable(result.Orphans, false)

	return nil
}

func printOrphanTable(entries []scanner.OrphanCandidate, showStatus bool) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if showStatus {
		fmt.Fprintln(w, "PID\tNAME\tPATTERN\tSCORE\tAGE\tSTATUS\tSIGNALS")
		fmt.Fprintln(w, "---\t----\t-------\t-----\t---\t------\t-------")
	} else {
		fmt.Fprintln(w, "PID\tNAME\tPATTERN\tSCORE\tAGE\tSIGNALS")
		fmt.Fprintln(w, "---\t----\t-------\t-----\t---\t-------")
	}

	for _, o := range entries {
		reasons := make([]string, 0, len(o.Signals))
		for name := range o.Signals {
			reasons = append(reasons, name)
		}

		if showStatus {
			status := "safe"
			if o.Score >= cfg.KillThreshold {
				status = "KILL"
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%.2f\t%s\t%s\t%s\n",
				o.Process.PID,
				o.Process.Name,
				o.Pattern.Name,
				o.Score,
				o.Process.Age().Truncate(time.Second),
				status,
				strings.Join(reasons, ", "),
			)
		} else {
			fmt.Fprintf(w, "%d\t%s\t%s\t%.2f\t%s\t%s\n",
				o.Process.PID,
				o.Process.Name,
				o.Pattern.Name,
				o.Score,
				o.Process.Age().Truncate(time.Second),
				strings.Join(reasons, ", "),
			)
		}
	}

	w.Flush()
}
