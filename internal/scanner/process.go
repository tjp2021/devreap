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
type ProcessInfo struct {
	PID        int32
	PPID       int32
	Name       string
	Exe        string
	Cmdline    string
	Args       string
	CreateTime time.Time
	HasTTY     bool
	Ports      []uint32
	Username   string
	MemRSS     uint64
}

// Age returns how long the process has been running.
func (p *ProcessInfo) Age() time.Duration {
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
	portMap := buildPortMap()

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
	exe, _ := p.Exe()
	cmdline, _ := p.Cmdline()
	createMs, _ := p.CreateTime()
	terminal, _ := p.Terminal()
	username, _ := p.Username()

	var memRSS uint64
	memInfo, err := p.MemoryInfo()
	if err == nil && memInfo != nil {
		memRSS = memInfo.RSS
	}

	createTime := time.UnixMilli(createMs)
	if createMs == 0 {
		createTime = time.Time{}
	}

	args := ""
	parts := strings.Fields(cmdline)
	if len(parts) > 1 {
		args = strings.Join(parts[1:], " ")
	}

	return &ProcessInfo{
		PID:        p.Pid,
		PPID:       ppid,
		Name:       name,
		Exe:        exe,
		Cmdline:    cmdline,
		Args:       args,
		CreateTime: createTime,
		HasTTY:     terminal != "",
		Ports:      portMap[p.Pid],
		Username:   username,
		MemRSS:     memRSS,
	}
}

func buildPortMap() map[int32][]uint32 {
	portMap := make(map[int32][]uint32)

	connections, err := net.Connections("tcp")
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
