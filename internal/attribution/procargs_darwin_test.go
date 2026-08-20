//go:build darwin

package attribution

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestReadProcArgsReadsOwnProcess(t *testing.T) {
	got, err := ReadProcArgs(int32(os.Getpid()), nil)
	if err != nil {
		t.Fatalf("reading own arguments: %v", err)
	}
	if len(got.Args) == 0 {
		t.Fatal("no arguments returned for the test binary")
	}
	if got.Exe == "" {
		t.Error("no executable path returned for the test binary")
	}
	if got.Cmdline == "" || !strings.Contains(got.Cmdline, got.Args[0]) {
		t.Errorf("command line %q does not contain argv[0] %q", got.Cmdline, got.Args[0])
	}
	if got.Env == nil {
		t.Error("environment map is nil; it should be empty rather than absent")
	}
}

// TestReadProcArgsRefusesOtherUser asserts the same-user rule at the reader
// rather than at the caller. Process 1 belongs to root, and the test suite does
// not.
func TestReadProcArgsRefusesOtherUser(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root, so every process is same-user")
	}
	if _, err := ReadProcArgs(1, nil); !errors.Is(err, ErrNotSameUser) {
		t.Fatalf("got %v, want ErrNotSameUser", err)
	}
}

func TestReadProcArgsOwnedRefusesForeignUID(t *testing.T) {
	foreign := uint32(os.Getuid() + 1)
	if _, err := ReadProcArgsOwned(int32(os.Getpid()), foreign, nil); !errors.Is(err, ErrNotSameUser) {
		t.Fatalf("got %v, want ErrNotSameUser", err)
	}
}
