package killer

import (
	"os"
	"os/user"
	"testing"
)

func TestIsSafe(t *testing.T) {
	blocklist := []string{"postgres", "redis-server", "nginx", "sshd"}

	tests := []struct {
		name      string
		pid       int32
		procName  string
		wantError bool
	}{
		{"PID 0 is unsafe", 0, "anything", true},
		{"PID 1 is unsafe", 1, "launchd", true},
		{"own PID is unsafe", int32(os.Getpid()), "self", true},
		{"parent PID is unsafe", int32(os.Getppid()), "parent", true},
		{"blocklisted postgres", 9999, "postgres", true},
		{"blocklisted redis", 9999, "redis-server", true},
		{"blocklisted nginx", 9999, "nginx", true},
		{"blocklisted sshd", 9999, "sshd", true},
		{"blocklist case insensitive", 9999, "Postgres", true},
		{"normal process is safe", 9999, "node", false},
		{"ffmpeg is safe", 9999, "ffmpeg", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := IsSafe(tt.pid, tt.procName, blocklist)
			if tt.wantError && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantError && err != nil {
				t.Errorf("expected no error, got: %v", err)
			}
		})
	}
}

func TestIsSafeWithOwnership(t *testing.T) {
	blocklist := []string{"postgres"}
	currentUser, err := user.Current()
	if err != nil {
		t.Skip("cannot determine current user")
	}

	tests := []struct {
		name        string
		pid         int32
		procName    string
		processUser string
		wantError   bool
	}{
		{"same user is safe", 9999, "node", currentUser.Username, false},
		{"different user is unsafe", 9999, "node", "_www", true},
		{"empty user is safe (unknown)", 9999, "node", "", false},
		{"blocklisted still blocked", 9999, "postgres", currentUser.Username, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := IsSafeWithOwnership(tt.pid, tt.procName, tt.processUser, blocklist)
			if tt.wantError && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantError && err != nil {
				t.Errorf("expected no error, got: %v", err)
			}
		})
	}
}
