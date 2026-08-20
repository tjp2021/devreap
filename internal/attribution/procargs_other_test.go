//go:build !darwin

package attribution

import (
	"errors"
	"os"
	"testing"
)

// TestReadProcArgsUnsupportedOffDarwin asserts the stub reports the platform
// gap rather than returning an empty result that reads as "no arguments".
func TestReadProcArgsUnsupportedOffDarwin(t *testing.T) {
	got, err := ReadProcArgs(int32(os.Getpid()), nil)
	if !errors.Is(err, ErrNotSupported) {
		t.Fatalf("got %v, want ErrNotSupported", err)
	}
	if got != nil {
		t.Errorf("got %+v, want no result", got)
	}

	if _, err := ReadProcArgsOwned(int32(os.Getpid()), 0, nil); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("owned read: got %v, want ErrNotSupported", err)
	}
}
