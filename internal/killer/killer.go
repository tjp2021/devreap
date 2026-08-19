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

// Identity is the snapshot of a process taken at scan time. Every field is
// re-read and compared immediately before signalling, so a process that was
// replaced, restarted, or re-execed between the scan and the kill is left
// alone. A name alone is not an identity: node processes share a name, and a
// respawned server reuses both the name and often the PID.
type Identity struct {
	PID     int32
	Name    string
	Cmdline string
	// CreateTime is the process start time observed at scan time. A zero value
	// means the start time was never established, and the identity therefore
	// cannot be verified — such a process is not killed.
	CreateTime time.Time
}

// Kill sends signals to a process according to the pattern's signal strategy.
// It re-reads the process and aborts unless PID, name, start time and full
// cmdline all still match the scan-time snapshot.
func Kill(id Identity, strategy patterns.SignalStrategy, gracePeriod time.Duration, blocklist []string) KillResult {
	result := KillResult{PID: id.PID, Name: id.Name}

	if err := IsSafe(id.PID, id.Name, blocklist); err != nil {
		result.Error = err.Error()
		return result
	}

	p, err := process.NewProcess(id.PID)
	if err != nil {
		result.Error = fmt.Sprintf("process %d not found: %v", id.PID, err)
		return result
	}

	if err := VerifyIdentity(p, id); err != nil {
		result.Error = err.Error()
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

// VerifyIdentity re-reads the live process and reports an error unless it is
// still the same process the scan saw. Any field that cannot be read is a
// verification failure, never a pass: an unverifiable process is not killed.
func VerifyIdentity(p *process.Process, id Identity) error {
	if p.Pid != id.PID {
		return fmt.Errorf("PID mismatch: verifying %d against snapshot of %d", p.Pid, id.PID)
	}

	currentName, err := p.Name()
	if err != nil {
		return fmt.Errorf("cannot verify process %d name: %v", id.PID, err)
	}
	if currentName != id.Name {
		return fmt.Errorf("PID %d is now %q, expected %q (PID reuse detected)", id.PID, currentName, id.Name)
	}

	currentCmdline, err := p.Cmdline()
	if err != nil {
		return fmt.Errorf("cannot verify process %d cmdline: %v", id.PID, err)
	}
	if currentCmdline != id.Cmdline {
		return fmt.Errorf("PID %d cmdline changed since scan (process was replaced)", id.PID)
	}

	// A snapshot without a start time cannot be verified at all.
	if id.CreateTime.IsZero() {
		return fmt.Errorf("PID %d has no recorded start time, cannot verify identity", id.PID)
	}
	createMs, err := p.CreateTime()
	if err != nil {
		return fmt.Errorf("cannot verify process %d start time: %v", id.PID, err)
	}
	if !time.UnixMilli(createMs).Equal(id.CreateTime) {
		return fmt.Errorf("PID %d start time changed since scan (process was restarted)", id.PID)
	}

	return nil
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

	// Snapshot the process as it is right now, so Kill verifies against a
	// consistent identity rather than re-deriving one field at a time.
	cmdline, err := p.Cmdline()
	if err != nil {
		return KillResult{PID: pid, Name: name, Error: fmt.Sprintf("cannot read process %d cmdline: %v", pid, err)}
	}
	createMs, err := p.CreateTime()
	if err != nil || createMs <= 0 {
		return KillResult{PID: pid, Name: name, Error: fmt.Sprintf("cannot read process %d start time", pid)}
	}

	id := Identity{
		PID:        pid,
		Name:       name,
		Cmdline:    cmdline,
		CreateTime: time.UnixMilli(createMs),
	}

	return Kill(id, patterns.SignalDefault, gracePeriod, blocklist)
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
