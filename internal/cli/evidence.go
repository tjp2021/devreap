package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tjp2021/devreap/internal/attribution"
)

var evidenceCmd = &cobra.Command{
	Use:   "evidence <session>",
	Short: "Export one session's spawn tree, timings, and transitions as JSON",
	Long: `evidence exports everything devreap recorded about one session as a single
JSON document: the spawn tree with per-process keys and link depths, the birth
timings, the owner exit event, and every lifecycle transition with its trigger
and its evidence.

This is the artifact to attach to an upstream bug report when a harness leaks
processes. It runs the same redaction the rest of the tool runs, so no command
line or environment value reaches it that would not reach any other output.

Run devreap top to list the session identifiers.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !cfg.Attribution.Enabled {
			return fmt.Errorf("attribution is off; set attribution.enabled in %s to record process ownership", cfgPath)
		}

		now := time.Now()
		view, err := attribution.LoadView(cfg.Attribution.StoreDir, liveRSS(), now)
		if err != nil {
			return fmt.Errorf("reading the attribution store: %w", err)
		}

		evidence, found := view.Evidence(args[0], now)
		if !found {
			known := view.SessionIDs()
			if len(known) == 0 {
				return fmt.Errorf("no session %q is recorded, and no sessions are recorded yet", args[0])
			}
			return fmt.Errorf("no session %q is recorded; known sessions are %s", args[0], strings.Join(known, ", "))
		}

		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(evidence)
	},
}

func init() {
	rootCmd.AddCommand(evidenceCmd)
}
