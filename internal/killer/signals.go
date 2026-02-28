package killer

import (
	"syscall"

	"github.com/tjp2021/devreap/internal/patterns"
)

// SignalSequence returns the ordered list of signals to send for a given strategy.
func SignalSequence(strategy patterns.SignalStrategy) []syscall.Signal {
	switch strategy {
	case patterns.SignalINT:
		return []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL}
	case patterns.SignalTERM, patterns.SignalDefault, "":
		return []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}
	default:
		return []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}
	}
}
