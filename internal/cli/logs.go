package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tjp2021/devreap/internal/logger"
)

var (
	logsLines int
	logsLevel string
	logsJSON  bool
)

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "View daemon log entries",
	Long:  `Shows recent log entries from the devreap daemon. Use --level to filter by severity.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		logFile := filepath.Join(cfg.LogDir, "devreap.log")

		f, err := os.Open(logFile)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Println("No log file found. Has the daemon run yet?")
				return nil
			}
			return fmt.Errorf("reading log: %w", err)
		}
		defer f.Close()

		// Read all lines with scanner (bounded by line, not slurping entire file)
		var lines []string
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // up to 1MB per line
		for sc.Scan() {
			lines = append(lines, sc.Text())
			// Keep only the last N+buffer lines to bound memory
			if logsLines > 0 && len(lines) > logsLines*2 {
				lines = lines[len(lines)-logsLines:]
			}
		}

		if len(lines) == 0 {
			fmt.Println("Log file is empty.")
			return nil
		}

		minLevel := logger.ParseLevel(logsLevel)

		// Take last N lines
		start := 0
		if logsLines > 0 && logsLines < len(lines) {
			start = len(lines) - logsLines
		}

		for _, line := range lines[start:] {
			var entry logger.Entry
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				fmt.Println(line)
				continue
			}

			entryLevel := logger.ParseLevel(entry.Level)
			if entryLevel < minLevel {
				continue
			}

			if logsJSON {
				fmt.Println(line)
			} else {
				printLogEntry(entry)
			}
		}

		return nil
	},
}

func init() {
	logsCmd.Flags().IntVarP(&logsLines, "lines", "n", 50, "number of recent log lines to show")
	logsCmd.Flags().StringVar(&logsLevel, "level", "info", "minimum log level (debug, info, warn, error)")
	logsCmd.Flags().BoolVar(&logsJSON, "json", false, "output raw JSON log lines (for piping to jq)")
	rootCmd.AddCommand(logsCmd)
}

func printLogEntry(e logger.Entry) {
	ts := e.Time
	if t, err := time.Parse(time.RFC3339, e.Time); err == nil {
		ts = t.Local().Format("15:04:05")
	}

	level := strings.ToUpper(e.Level)
	switch e.Level {
	case "error":
		level = "ERR "
	case "warn":
		level = "WARN"
	case "info":
		level = "INFO"
	case "debug":
		level = "DBG "
	}

	line := fmt.Sprintf("%s %s %s", ts, level, e.Message)

	if e.PID > 0 {
		line += fmt.Sprintf(" pid=%d", e.PID)
	}
	if e.Process != "" {
		line += fmt.Sprintf(" process=%s", e.Process)
	}
	if e.Pattern != "" {
		line += fmt.Sprintf(" pattern=%s", e.Pattern)
	}
	if e.Score > 0 {
		line += fmt.Sprintf(" score=%.2f", e.Score)
	}
	if len(e.Signals) > 0 {
		reasons := make([]string, 0, len(e.Signals))
		for name, weight := range e.Signals {
			reasons = append(reasons, fmt.Sprintf("%s=%.2f", name, weight))
		}
		line += fmt.Sprintf(" signals=[%s]", strings.Join(reasons, ","))
	}
	if e.Cmdline != "" {
		// Truncate long command lines for readability
		cmdline := e.Cmdline
		if len(cmdline) > 80 {
			cmdline = cmdline[:77] + "..."
		}
		line += fmt.Sprintf(" cmd=%q", cmdline)
	}
	if e.Error != "" {
		line += fmt.Sprintf(" error=%s", e.Error)
	}

	fmt.Println(line)
}
