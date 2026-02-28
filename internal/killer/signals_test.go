package killer

import (
	"syscall"
	"testing"

	"github.com/tjp2021/devreap/internal/patterns"
)

func TestSignalSequence(t *testing.T) {
	tests := []struct {
		strategy patterns.SignalStrategy
		first    syscall.Signal
		last     syscall.Signal
		length   int
	}{
		{patterns.SignalINT, syscall.SIGINT, syscall.SIGKILL, 3},
		{patterns.SignalTERM, syscall.SIGTERM, syscall.SIGKILL, 2},
		{patterns.SignalDefault, syscall.SIGTERM, syscall.SIGKILL, 2},
		{"", syscall.SIGTERM, syscall.SIGKILL, 2},
	}

	for _, tt := range tests {
		t.Run(string(tt.strategy), func(t *testing.T) {
			seq := SignalSequence(tt.strategy)
			if len(seq) != tt.length {
				t.Errorf("expected %d signals, got %d", tt.length, len(seq))
			}
			if seq[0] != tt.first {
				t.Errorf("expected first signal %v, got %v", tt.first, seq[0])
			}
			if seq[len(seq)-1] != tt.last {
				t.Errorf("expected last signal %v, got %v", tt.last, seq[len(seq)-1])
			}
		})
	}
}
