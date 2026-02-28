package killer

import (
	"fmt"
	"time"

	"github.com/shirou/gopsutil/v4/process"

	"github.com/tjp2021/devreap/internal/patterns"
)

// KillResult reports the outcome of a kill attempt.
type KillResult struct {
	PID     int32
	Name    string
	Signal  string
	Success bool
	Error   string
}

// Kill sends signals to a process according to the pattern's signal strategy.
// It verifies process identity before killing to prevent PID reuse attacks.
func Kill(pid int32, expectedName string, strategy patterns.SignalStrategy, gracePeriod time.Duration, blocklist []string) KillResult {
	result := KillResult{PID: pid, Name: expectedName}

	if err := IsSafe(pid, expectedName, blocklist); err != nil {
		result.Error = err.Error()
		return result
	}

	p, err := process.NewProcess(pid)
	if err != nil {
		result.Error = fmt.Sprintf("process %d not found: %v", pid, err)
		return result
	}

	// Verify process identity — protect against PID reuse between scan and kill
	currentName, err := p.Name()
	if err != nil {
		result.Error = fmt.Sprintf("cannot verify process %d identity: %v", pid, err)
		return result
	}
	if currentName != expectedName {
		result.Error = fmt.Sprintf("PID %d is now %q, expected %q (PID reuse detected)", pid, currentName, expectedName)
		return result
	}

	signals := SignalSequence(strategy)
	for i, sig := range signals {
		result.Signal = sig.String()

		if err := p.SendSignal(sig); err != nil {
			// Process might already be dead
			if !isRunning(p) {
				result.Success = true
				return result
			}
			result.Error = fmt.Sprintf("sending %s: %v", sig, err)
			continue
		}

		// Wait for process to die (except for last signal)
		if i < len(signals)-1 {
			if waitForDeath(p, gracePeriod) {
				result.Success = true
				return result
			}
		} else {
			// After final signal, brief wait
			time.Sleep(500 * time.Millisecond)
			if !isRunning(p) {
				result.Success = true
				return result
			}
		}
	}

	if !isRunning(p) {
		result.Success = true
	} else {
		result.Error = "process still running after all signals"
	}

	return result
}

// KillByPID kills a process by PID with default strategy.
// Includes ownership verification — refuses to kill processes owned by other users.
func KillByPID(pid int32, blocklist []string, gracePeriod time.Duration) KillResult {
	p, err := process.NewProcess(pid)
	if err != nil {
		return KillResult{PID: pid, Error: fmt.Sprintf("process not found: %v", err)}
	}

	name, _ := p.Name()
	username, _ := p.Username()

	if err := IsSafeWithOwnership(pid, name, username, blocklist); err != nil {
		return KillResult{PID: pid, Name: name, Error: err.Error()}
	}

	return Kill(pid, name, patterns.SignalDefault, gracePeriod, blocklist)
}

func waitForDeath(p *process.Process, timeout time.Duration) bool {
	deadline := time.After(timeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			return false
		case <-ticker.C:
			if !isRunning(p) {
				return true
			}
		}
	}
}

func isRunning(p *process.Process) bool {
	running, err := p.IsRunning()
	if err != nil {
		return false
	}
	return running
}
