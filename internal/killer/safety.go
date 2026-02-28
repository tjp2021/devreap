package killer

import (
	"fmt"
	"os"
	"os/user"
	"strings"
)

// IsSafe returns nil if it's safe to kill the given PID, or an error explaining why not.
func IsSafe(pid int32, name string, blocklist []string) error {
	// Never kill PID 1 (launchd/init)
	if pid <= 1 {
		return fmt.Errorf("refusing to kill PID %d (system process)", pid)
	}

	// Never kill our own process
	if pid == int32(os.Getpid()) {
		return fmt.Errorf("refusing to kill own process (PID %d)", pid)
	}

	// Never kill parent process
	if pid == int32(os.Getppid()) {
		return fmt.Errorf("refusing to kill parent process (PID %d)", pid)
	}

	// Check blocklist
	nameLower := strings.ToLower(name)
	for _, blocked := range blocklist {
		if strings.ToLower(blocked) == nameLower {
			return fmt.Errorf("process %q is on the blocklist", name)
		}
	}

	return nil
}

// IsSafeWithOwnership additionally checks that the process belongs to the current user.
func IsSafeWithOwnership(pid int32, name string, processUser string, blocklist []string) error {
	if err := IsSafe(pid, name, blocklist); err != nil {
		return err
	}

	// Verify process belongs to current user
	if processUser != "" {
		currentUser, err := user.Current()
		if err == nil && currentUser.Username != processUser {
			return fmt.Errorf("process %q (PID %d) belongs to user %q, not current user %q",
				name, pid, processUser, currentUser.Username)
		}
	}

	return nil
}
