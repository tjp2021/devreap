package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tjp2021/devreap/internal/attribution"
	"github.com/tjp2021/devreap/internal/config"
)

var topJSON bool

// sessionColumn is the width of the session identifier column. A derived
// identifier is 8 characters, and a vendor-supplied one can be a full 36
// character identifier that would otherwise push every later column out of line.
const sessionColumn = 12

// shortSession fits an identifier in the table without hiding which session it
// is. The full identifier stays in the JSON output and is what devreap evidence
// takes, and the tree below the table prints it in full.
func shortSession(id string) string {
	if len(id) <= sessionColumn {
		return id
	}
	return id[:sessionColumn-1] + "…"
}

var topCmd = &cobra.Command{
	Use:   "top",
	Short: "Show per-session process trees, memory totals, and owner exit times",
	Long: `top renders what devreap has recorded about who started each process.

It groups processes by the session that spawned them, totals resident memory per
session, and shows how long ago each owner exited. The unattributed bucket is
shown separately, so a coverage gap is visible rather than hidden.

The view is read-only. It performs no action.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if !cfg.Attribution.Enabled {
			return fmt.Errorf("attribution is off; set attribution.enabled in %s to record process ownership", cfgPath)
		}

		view, err := attribution.LoadView(cfg.Attribution.StoreDir, liveRSS(), time.Now())
		if err != nil {
			return fmt.Errorf("reading the attribution store: %w", err)
		}

		if topJSON {
			return json.NewEncoder(os.Stdout).Encode(view)
		}
		printTop(view, time.Now())
		return nil
	},
}

func init() {
	topCmd.Flags().BoolVar(&topJSON, "json", false, "emit the view as machine-readable JSON")
	rootCmd.AddCommand(topCmd)
}

func printTop(view *attribution.View, now time.Time) {
	if len(view.Sessions) == 0 && len(view.Unattributed) == 0 {
		fmt.Println("No attributed processes recorded yet.")
		fmt.Println("The watcher records a session when one starts, so this fills in as you work.")
		return
	}

	fmt.Printf("%-*s %-16s %-22s %-16s %6s %8s\n", sessionColumn, "SESSION", "HARNESS", "REPO", "OWNER", "PROCS", "RSS")
	for _, session := range view.Sessions {
		fmt.Printf("%-*s %-16s %-22s %-16s %6d %8s\n",
			sessionColumn, shortSession(session.SessionID),
			truncate(session.Harness, 16),
			truncate(shortenHome(session.Repo), 22),
			ownerColumn(session, now),
			len(session.Processes),
			humanRSS(session.RSSBytes),
		)
	}
	if len(view.Unattributed) > 0 {
		fmt.Printf("%-*s %-16s %-22s %-16s %6d %8s\n",
			sessionColumn, "--", "--", "--", "unattributed", len(view.Unattributed), humanRSS(view.UnattributedRSS()))
	}

	for _, session := range view.Sessions {
		if len(session.Processes) == 0 {
			continue
		}
		fmt.Printf("\n  %s  %s\n", session.SessionID, ownerColumn(session, now))
		for _, process := range session.Processes {
			fmt.Printf("    %-36s pid %-7d %6s  %s\n",
				truncate(displayCmdline(process), 36),
				process.Key.PID,
				humanRSS(process.RSSBytes),
				stateDetail(process),
			)
		}
	}

	fmt.Printf("\nCoverage %.0f%% (%d of %d pattern-matched processes observed). This view takes no action.\n",
		view.Coverage*100, view.Attributed, view.Tracked)
}

// ownerColumn says whether a session's owner is still running, and how long ago
// it exited when it is not.
func ownerColumn(session attribution.SessionView, now time.Time) string {
	ago, exited := session.OwnerExitedAgo(now)
	if !exited {
		return "alive"
	}
	return "exited " + humanAgo(ago) + " ago"
}

// stateDetail renders the lifecycle state with the progress that explains it.
// A candidate shows its confirmations, and a process inside its window shows how
// much awake time is left.
func stateDetail(process attribution.ProcessView) string {
	switch process.State {
	case attribution.StateOrphanCandidate:
		return fmt.Sprintf("%s %d/%d", process.State, process.Confirmations, confirmationTarget())
	case attribution.StateGracePeriod:
		window, actionable := cfg.LifecycleWindow(windowClass(process))
		if !actionable {
			return fmt.Sprintf("%s (never, %s)", process.State, windowClass(process))
		}
		left := window - time.Duration(process.AwakeMillis)*time.Millisecond
		if left < 0 {
			left = 0
		}
		return fmt.Sprintf("%s %s left", process.State, humanAgo(left))
	case attribution.StateConfirmedOrphan:
		if process.Reported {
			return string(process.State) + " (reported)"
		}
		return string(process.State)
	default:
		return string(process.State)
	}
}

// windowClass names the class the window is keyed by. An unattributed process is
// unattributed whatever its pattern says, because no owner means no owner death
// at any age.
func windowClass(process attribution.ProcessView) string {
	if process.Confidence != attribution.ConfidenceObserved || process.SessionID == "" {
		return "unattributed"
	}
	if process.Class == "" {
		return "unclassified"
	}
	return process.Class
}

// confirmationTarget is the confirming scan count a candidate has to reach. It
// is the same value the engine runs on.
func confirmationTarget() int { return config.DefaultConfirmationCount }

func displayCmdline(process attribution.ProcessView) string {
	if process.Cmdline != "" {
		return process.Cmdline
	}
	if process.Name != "" {
		return process.Name
	}
	return "(unknown)"
}

// shortenHome renders a path under the home directory with a tilde, which is how
// a developer reads their own paths.
func shortenHome(path string) string {
	if path == "" {
		return "--"
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+"/") {
		return "~" + path[len(home):]
	}
	return path
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

func humanRSS(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%dB", bytes)
	}
	value, exp := float64(bytes)/unit, 0
	for value >= unit && exp < 3 {
		value /= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", value, "KMGT"[exp])
}

// humanAgo renders a duration the way a person says it.
func humanAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
