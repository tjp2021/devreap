package notify

import (
	"fmt"
	"os/exec"
	"strings"
)

type MacOS struct{}

func NewMacOS() *MacOS {
	return &MacOS{}
}

func (m *MacOS) Notify(title, message string) error {
	// Escape for AppleScript string literals: backslashes and quotes
	escTitle := escapeAppleScript(title)
	escMsg := escapeAppleScript(message)

	script := fmt.Sprintf(
		`display notification "%s" with title "%s"`,
		escMsg, escTitle,
	)

	cmd := exec.Command("osascript", "-e", script)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// escapeAppleScript escapes a string for use inside AppleScript double quotes.
func escapeAppleScript(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
