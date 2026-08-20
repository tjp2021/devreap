package attribution

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

// buildProcargs2 assembles a buffer in the KERN_PROCARGS2 layout: the argument
// count, the executable path, NUL padding, argc NUL-terminated arguments, then
// NUL-terminated environment entries closed by an empty one.
func buildProcargs2(argc uint32, exe string, args, env []string) []byte {
	var b bytes.Buffer
	var count [4]byte
	binary.NativeEndian.PutUint32(count[:], argc)
	b.Write(count[:])
	b.WriteString(exe)
	b.WriteByte(0)
	b.WriteByte(0) // the kernel pads between the path and argv
	for _, arg := range args {
		b.WriteString(arg)
		b.WriteByte(0)
	}
	for _, entry := range env {
		b.WriteString(entry)
		b.WriteByte(0)
	}
	b.WriteByte(0)
	return b.Bytes()
}

func sampleProcargs2() []byte {
	return buildProcargs2(4,
		"/opt/homebrew/bin/node",
		[]string{"node", "./server.js", "--port", "7333"},
		[]string{
			"CLAUDE_CODE_SESSION_ID=5f1c9a2e",
			"CLAUDE_PROJECT_DIR=/home/dev/projects/example",
			"PATH=/usr/bin:/bin",
			"SLACK_BOT_TOKEN=xoxb-2417-99120345678-Zm9vYmFyYmF6cXV4",
		})
}

func TestProcargsParsesCapturedBuffer(t *testing.T) {
	raw, err := parseProcargs2(sampleProcargs2())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if raw.exe != "/opt/homebrew/bin/node" {
		t.Errorf("exe: got %q", raw.exe)
	}
	wantArgs := []string{"node", "./server.js", "--port", "7333"}
	if len(raw.args) != len(wantArgs) {
		t.Fatalf("args: got %v, want %v", raw.args, wantArgs)
	}
	for i := range wantArgs {
		if raw.args[i] != wantArgs[i] {
			t.Errorf("arg %d: got %q, want %q", i, raw.args[i], wantArgs[i])
		}
	}
	if len(raw.env) != 4 {
		t.Errorf("env: got %d entries, want 4: %v", len(raw.env), raw.env)
	}
}

// TestProcargsTruncatedBufferErrors asserts a clean error rather than a partial
// result. A half-parsed environment block is how a secret reaches a log.
func TestProcargsTruncatedBufferErrors(t *testing.T) {
	full := sampleProcargs2()
	const exe = "/opt/homebrew/bin/node"
	argvStart := 4 + len(exe) + 1 + 1

	tests := []struct {
		name string
		buf  []byte
	}{
		{"empty buffer", nil},
		{"shorter than the argument count", full[:3]},
		{"cut inside the executable path", full[:8]},
		{"cut inside the first argument", full[:argvStart+2]},
		{"cut inside an environment entry", full[:len(full)-12]},
		{"cut at the very end of the last entry", full[:len(full)-2]},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := parseProcargs2(tt.buf)
			if err == nil {
				t.Fatalf("parsed a truncated buffer instead of failing: %+v", raw)
			}
			if !errors.Is(err, ErrProcargsParse) {
				t.Errorf("error %v does not wrap ErrProcargsParse", err)
			}
			if raw.exe != "" || raw.args != nil || raw.env != nil {
				t.Errorf("a failed parse returned data: %+v", raw)
			}
		})
	}
}

func TestProcargsCorruptLengthPrefixErrors(t *testing.T) {
	tests := []struct {
		name string
		buf  []byte
	}{
		{
			name: "argument count over the bound",
			buf:  buildProcargs2(0xFFFFFFF0, "/bin/sh", []string{"sh"}, nil),
		},
		{
			name: "argument count larger than the buffer holds",
			buf:  buildProcargs2(64, "/bin/sh", []string{"sh", "-c", "true"}, nil),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := parseProcargs2(tt.buf)
			if err == nil {
				t.Fatalf("parsed a corrupt count instead of failing: %+v", raw)
			}
			if !errors.Is(err, ErrProcargsParse) {
				t.Errorf("error %v does not wrap ErrProcargsParse", err)
			}
			if raw.args != nil || raw.env != nil {
				t.Errorf("a failed parse returned data: %+v", raw)
			}
		})
	}
}

func TestProcargsRejectsOversizeBuffer(t *testing.T) {
	_, err := parseProcargs2(make([]byte, maxProcargsBuffer+1))
	if !errors.Is(err, ErrProcargsParse) {
		t.Fatalf("got %v, want ErrProcargsParse", err)
	}
}

func TestProcargsBoundsEnvironmentEntries(t *testing.T) {
	env := make([]string, maxEnvEntries+1)
	for i := range env {
		env[i] = "VAR=value"
	}
	_, err := parseProcargs2(buildProcargs2(1, "/bin/sh", []string{"sh"}, env))
	if !errors.Is(err, ErrProcargsParse) {
		t.Fatalf("got %v, want ErrProcargsParse", err)
	}
}

func TestProcargsHandlesEmptyEnvironment(t *testing.T) {
	raw, err := parseProcargs2(buildProcargs2(1, "/bin/sh", []string{"sh"}, nil))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(raw.env) != 0 {
		t.Errorf("env: got %v, want none", raw.env)
	}
}

// TestProcargsResultIsRedactedBeforeItLeaves asserts R12 at the one place it
// matters most: the reader hands its result to the filter, so no caller can
// obtain the raw environment.
func TestProcargsResultIsRedactedBeforeItLeaves(t *testing.T) {
	const secret = "xoxb-2417-99120345678-Zm9vYmFyYmF6cXV4"

	raw, err := parseProcargs2(buildProcargs2(3,
		"/opt/homebrew/bin/node",
		[]string{"node", "./bot.js", "--slack-token=" + secret},
		[]string{
			"CLAUDE_CODE_SESSION_ID=5f1c9a2e",
			"SLACK_BOT_TOKEN=" + secret,
			"PATH=/usr/bin:/bin",
		}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	got := raw.redact(98925, nil)

	if got.PID != 98925 {
		t.Errorf("pid: got %d", got.PID)
	}
	if got.Env["CLAUDE_CODE_SESSION_ID"] != "5f1c9a2e" {
		t.Errorf("session id was lost: %v", got.Env)
	}
	if len(got.Env) != 1 {
		t.Errorf("env: got %v, want only the allowlisted session id", got.Env)
	}
	if strings.Contains(got.Cmdline, secret) {
		t.Errorf("secret survived into the command line: %s", got.Cmdline)
	}
	if !strings.Contains(got.Cmdline, "./bot.js") {
		t.Errorf("command line lost a harmless argument: %s", got.Cmdline)
	}
	if got.Exe != "/opt/homebrew/bin/node" {
		t.Errorf("exe: got %q", got.Exe)
	}
}
