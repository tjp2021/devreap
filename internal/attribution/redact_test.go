package attribution

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSecretRedaction is the gate the design puts on the process-arguments
// reader shipping at all: no value outside the allowlist reaches a record, and
// a token-shaped value never appears in any output.
func TestSecretRedaction(t *testing.T) {
	const messagingToken = "xoxb-2417-99120345678-Zm9vYmFyYmF6cXV4"
	const openAIKey = "sk-proj-abcdefghijklmnopqrstuvwxyz012345"

	env := []string{
		"CLAUDE_CODE_SESSION_ID=5f1c9a2e",
		"CLAUDE_PROJECT_DIR=/home/dev/projects/example",
		"AI_AGENT=claude-code",
		"SLACK_BOT_TOKEN=" + messagingToken,
		"OPENAI_API_KEY=" + openAIKey,
		"AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY",
		"PATH=/usr/bin:/bin",
		"HOME=/home/dev",
	}

	kept := RedactEnv(env)

	if got := kept["CLAUDE_CODE_SESSION_ID"]; got != "5f1c9a2e" {
		t.Errorf("session id: got %q, want the value kept", got)
	}
	if got := kept["CLAUDE_PROJECT_DIR"]; got != "/home/dev/projects/example" {
		t.Errorf("project dir: got %q, want the value kept", got)
	}
	if got := kept["AI_AGENT"]; got != "claude-code" {
		t.Errorf("agent name: got %q, want the value kept", got)
	}
	if len(kept) != 3 {
		t.Errorf("kept %d variables, want exactly the 3 allowlisted ones: %v", len(kept), kept)
	}

	args := RedactArgs([]string{
		"node", "./server.js", "--port", "7333",
		"--api-key", openAIKey,
		"--slack-token=" + messagingToken,
		"https://x-access-token:" + messagingToken + "@github.com/example/repo.git",
	})

	// Serializing is what a record does, so assert against the serialized form
	// rather than against the slice.
	blob, err := json.Marshal(map[string]any{"env": kept, "args": args})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, secret := range []string{messagingToken, openAIKey, "wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY"} {
		if strings.Contains(string(blob), secret) {
			t.Errorf("secret %q reached the record: %s", secret, blob)
		}
	}
	if !strings.Contains(string(blob), "7333") {
		t.Errorf("redaction removed a harmless argument: %s", blob)
	}
}

func TestEnvAllowlistDropsEverythingElse(t *testing.T) {
	kept := RedactEnv([]string{
		"CLAUDECODE=1",
		"NODE_OPTIONS=--max-old-space-size=8192",
		"DATABASE_URL=postgres://user:hunter2@localhost/app",
		"malformed-entry-without-separator",
	})
	if len(kept) != 1 {
		t.Fatalf("kept %v, want only CLAUDECODE", kept)
	}
	if kept["CLAUDECODE"] != "1" {
		t.Errorf("CLAUDECODE: got %q, want 1", kept["CLAUDECODE"])
	}
}

func TestEnvAllowlistRefusesSecretShapedNames(t *testing.T) {
	r := NewRedactor("SLACK_BOT_TOKEN", "MY_HARNESS_SESSION_ID")

	if r.EnvNameAllowed("SLACK_BOT_TOKEN") {
		t.Error("a secret-shaped name was allowlisted on request")
	}
	if !r.EnvNameAllowed("MY_HARNESS_SESSION_ID") {
		t.Error("a harness marker name was refused")
	}
	if got := r.Env([]string{"SLACK_BOT_TOKEN=xoxb-2417-99120345678-Zm9vYmFy"}); len(got) != 0 {
		t.Errorf("kept %v, want nothing", got)
	}
}

func TestRedactArgsMasksFlagValuePairs(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "separate value argument",
			in:   []string{"--token", "abc123", "--port", "7333"},
			want: []string{"--token", Redacted, "--port", "7333"},
		},
		{
			name: "joined value argument",
			in:   []string{"--password=hunter2"},
			want: []string{"--password=" + Redacted},
		},
		{
			name: "environment style assignment",
			in:   []string{"API_KEY=short"},
			want: []string{"API_KEY=" + Redacted},
		},
		{
			name: "harmless arguments untouched",
			in:   []string{"node", "./server.js", "--port", "7333", "--dir=/home/dev/app"},
			want: []string{"node", "./server.js", "--port", "7333", "--dir=/home/dev/app"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactArgs(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("arg %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestRedactArgsMasksTokenShapedValues(t *testing.T) {
	tokenShaped := []string{
		"sk-proj-abcdefghijklmnopqrstuvwxyz012345",
		"ghp_16C7e42F292c6912E7710c838347Ae178B4a",
		"xoxb-2417-99120345678-Zm9vYmFyYmF6cXV4",
		"AKIAIOSFODNN7EXAMPLE",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N",
		"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
		"Ax7Kq2mZ9pR4tY6uI8oP0aS3dF5gH7jK9lZ1xC3v",
	}
	for _, value := range tokenShaped {
		got := RedactArgs([]string{value})
		if got[0] != Redacted {
			t.Errorf("token-shaped %q survived redaction as %q", value, got[0])
		}
	}

	harmless := []string{
		"./server.js",
		"/Users/dev/projects/example/node_modules/.bin/next",
		"--max-old-space-size=8192",
		"7333",
		"development",
	}
	for _, value := range harmless {
		got := RedactArgs([]string{value})
		if got[0] != value {
			t.Errorf("harmless %q was redacted to %q", value, got[0])
		}
	}
}

func TestRedactArgsMasksURLCredentials(t *testing.T) {
	got := RedactArgs([]string{"https://user:hunter2@github.com/example/repo.git"})
	want := "https://" + Redacted + "@github.com/example/repo.git"
	if got[0] != want {
		t.Errorf("got %q, want %q", got[0], want)
	}
}

func TestRedactCmdlineStringHandlesJoinedInput(t *testing.T) {
	got := RedactCmdlineString("node ./server.js --token abc123 --port 7333")
	want := "node ./server.js --token " + Redacted + " --port 7333"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if RedactCmdlineString("") != "" {
		t.Error("empty command line should stay empty")
	}
}

func TestTruncateForDisplay(t *testing.T) {
	tests := []struct {
		in     string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"exactly-10", 10, "exactly-10"},
		{"far too long for the view", 10, "far too..."},
		{"anything", 0, "anything"},
		{"abcdef", 2, "ab"},
	}
	for _, tt := range tests {
		if got := TruncateForDisplay(tt.in, tt.maxLen); got != tt.want {
			t.Errorf("TruncateForDisplay(%q, %d): got %q, want %q", tt.in, tt.maxLen, got, tt.want)
		}
	}
}

func TestRedactArgsPreservesNilAndEmpty(t *testing.T) {
	if got := RedactArgs(nil); got != nil {
		t.Errorf("nil input returned %v, want nil", got)
	}
	if got := RedactCmdline([]string{}); got != "" {
		t.Errorf("empty input returned %q, want empty", got)
	}
}
