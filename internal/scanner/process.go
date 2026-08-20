package scanner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// ProcessInfo holds metadata about a running process, collected via gopsutil.
//
// The *Known fields record whether each lookup actually succeeded. gopsutil
// returns a zero value and an error for processes it cannot inspect, and a
// zero value is indistinguishable from a real answer. Treating "unknown" as
// a real answer made devreap kill processes it knew nothing about, so every
// consumer must check the corresponding *Known flag before drawing a
// conclusion from the value.
type ProcessInfo struct {
	PID     int32
	PPID    int32
	Name    string
	Cmdline string
	Args    string

	// Exe is the resolved path of the running executable. It is empty when the
	// path could not be read, which happens routinely on macOS: proc_pidpath
	// fails once the binary has been replaced on disk, so every process from a
	// tool that self-updates loses its path. Emptiness therefore means
	// "unknown", never "not an IDE", and Exe is only ever used to establish
	// that a process IS something. No kill decision may rest on it being empty.
	Exe string

	CreateTime time.Time
	// CreateTimeKnown is false when the start time could not be read.
	CreateTimeKnown bool

	HasTTY bool
	// TTYKnown is false when the terminal could not be read. Distinguishes
	// "no controlling terminal" from "could not determine".
	TTYKnown bool

	Ports    []uint32
	Username string
	// UsernameKnown is false when the owner could not be determined.
	UsernameKnown bool

	MemRSS uint64
}

// Age returns how long the process has been running.
// It returns 0 when the start time is unknown. Callers that need to
// distinguish "just started" from "unknown" must check CreateTimeKnown.
func (p *ProcessInfo) Age() time.Duration {
	if !p.CreateTimeKnown {
		return 0
	}
	return time.Since(p.CreateTime)
}

// String returns a human-readable summary of the process.
func (p *ProcessInfo) String() string {
	return fmt.Sprintf("PID=%d PPID=%d %s (age: %s)", p.PID, p.PPID, p.Name, p.Age().Truncate(time.Second))
}

// EnumerateProcesses returns metadata for all running processes.
// The context allows cancellation for scan timeouts.
func EnumerateProcesses(ctx context.Context) ([]ProcessInfo, error) {
	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing processes: %w", err)
	}

	// get listening ports once
	portMap := buildPortMap(ctx)

	var result []ProcessInfo
	for _, p := range procs {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		info := processToInfo(p, portMap)
		if info != nil {
			result = append(result, *info)
		}
	}

	return result, nil
}

func processToInfo(p *process.Process, portMap map[int32][]uint32) *ProcessInfo {
	name, err := p.Name()
	if err != nil {
		return nil
	}

	ppid, _ := p.Ppid()
	cmdline, _ := p.Cmdline()

	// A failed lookup leaves Exe empty. See the field comment: unknown is not
	// evidence, so the error is deliberately not propagated.
	exe, _ := p.Exe()

	// Each of these can fail. Record success separately so downstream code
	// can tell a real answer from a missing one.
	createMs, createErr := p.CreateTime()
	terminal, terminalErr := p.Terminal()
	username, usernameErr := p.Username()

	var memRSS uint64
	memInfo, err := p.MemoryInfo()
	if err == nil && memInfo != nil {
		memRSS = memInfo.RSS
	}

	createTime := time.Time{}
	createTimeKnown := createErr == nil && createMs > 0
	if createTimeKnown {
		createTime = time.UnixMilli(createMs)
	}

	args := ""
	parts := strings.Fields(cmdline)
	if len(parts) > 1 {
		args = strings.Join(parts[1:], " ")
	}

	return &ProcessInfo{
		PID:             p.Pid,
		PPID:            ppid,
		Name:            name,
		Cmdline:         cmdline,
		Args:            args,
		Exe:             exe,
		CreateTime:      createTime,
		CreateTimeKnown: createTimeKnown,
		HasTTY:          terminalErr == nil && terminal != "",
		TTYKnown:        terminalErr == nil,
		Ports:           portMap[p.Pid],
		Username:        username,
		UsernameKnown:   usernameErr == nil && username != "",
		MemRSS:          memRSS,
	}
}

func buildPortMap(ctx context.Context) map[int32][]uint32 {
	portMap := make(map[int32][]uint32)

	connections, err := net.ConnectionsWithContext(ctx, "tcp")
	if err != nil {
		return portMap
	}

	for _, conn := range connections {
		if conn.Status == "LISTEN" && conn.Pid > 0 {
			portMap[conn.Pid] = append(portMap[conn.Pid], conn.Laddr.Port)
		}
	}

	return portMap
}
