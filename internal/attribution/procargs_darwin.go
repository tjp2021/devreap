//go:build darwin

package attribution

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// SnapshotProcesses takes the whole process table in one KERN_PROC_ALL call.
//
// The returned kinfo_proc array carries the identifier, the parent identifier,
// the process group, the controlling terminal device, the start time, and the
// owner for every process at once. Reading those fields through per-process
// library calls would multiply syscalls by the process count for no benefit,
// and the design's poll budget rests on this being one call.
func SnapshotProcesses(takenAt time.Time) (*Snapshot, error) {
	kprocs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, fmt.Errorf("enumerating processes: %w", err)
	}

	entries := make([]ProcEntry, 0, len(kprocs))
	for i := range kprocs {
		if kprocs[i].Proc.P_pid == 0 {
			continue
		}
		entries = append(entries, entryFromKinfo(&kprocs[i]))
	}
	return NewSnapshot(takenAt, entries), nil
}

func entryFromKinfo(kp *unix.KinfoProc) ProcEntry {
	entry := ProcEntry{
		PID:  kp.Proc.P_pid,
		PPID: kp.Eproc.Ppid,
		PGID: kp.Eproc.Pgid,
		Name: nulTerminated(kp.Proc.P_comm[:]),
		UID:  kp.Eproc.Ucred.Uid,
	}

	// A start time of zero means the kernel reported none. Unknown is not a
	// start time, so the entry stays unverifiable rather than claiming the
	// epoch.
	if sec := kp.Proc.P_starttime.Sec; sec > 0 {
		entry.StartTime = NormalizeTime(time.Unix(sec, int64(kp.Proc.P_starttime.Usec)*1000))
		entry.StartTimeKnown = true
	}

	entry.TTY, entry.TTYKnown = terminalName(kp.Eproc.Tdev)
	return entry
}

func nulTerminated(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		return string(b[:i])
	}
	return string(b)
}

var (
	terminalOnce sync.Once
	terminalMap  map[uint64]string
)

// terminalName resolves a controlling terminal device number to its name.
//
// NODEV means the process has no controlling terminal, which is a known answer.
// A device the map does not hold is unknown, and unknown is never evidence in
// either direction, so the caller sees TTYKnown false rather than an empty name
// that reads as "no terminal".
func terminalName(dev int32) (string, bool) {
	const nodev = -1
	if dev == nodev {
		return "", true
	}
	terminalOnce.Do(buildTerminalMap)
	name, ok := terminalMap[uint64(uint32(dev))]
	return name, ok
}

func buildTerminalMap() {
	terminalMap = make(map[uint64]string)
	paths, err := filepath.Glob("/dev/tty*")
	if err != nil {
		return
	}
	for _, path := range paths {
		var st unix.Stat_t
		if statErr := unix.Stat(path, &st); statErr != nil {
			continue
		}
		terminalMap[uint64(uint32(st.Rdev))] = strings.TrimPrefix(path, "/dev/")
	}
}

// ReadProcArgs reads one process's argument vector, executable path, and
// environment through KERN_PROCARGS2, and returns them redacted.
//
// This is the most sensitive read in devreap: it reaches into another process's
// argument and environment memory. Four rules bound it, and all four are
// enforced here rather than by the caller.
//
// It runs same-user only. The owner is read from the kernel, not supplied by
// the caller, and a process owned by another account is refused outright.
//
// It never writes to a foreign process.
//
// It treats the packed buffer as untrusted input and bounds every length it
// parses.
//
// It returns an error rather than a partial result, and it returns nothing that
// has not passed the redaction filter.
//
// The process library cannot do this read. gopsutil v4.26.1 selects the shared
// BSD implementation on darwin, whose EnvironWithContext returns
// ErrNotImplementedError for every process without exception, which is why this
// reader exists.
func ReadProcArgs(pid int32, r *Redactor) (*ProcArgs, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", int(pid))
	if err != nil {
		return nil, fmt.Errorf("reading process %d: %w", pid, err)
	}
	return ReadProcArgsOwned(pid, kp.Eproc.Ucred.Uid, r)
}

// ReadProcArgsOwned is ReadProcArgs for a caller that already holds the owner
// from the same kernel snapshot it found the process in, which saves one sysctl
// per new process on the watcher's path. The same-user check still happens
// here, against a value the kernel supplied.
func ReadProcArgsOwned(pid int32, ownerUID uint32, r *Redactor) (*ProcArgs, error) {
	if int(ownerUID) != os.Getuid() {
		return nil, fmt.Errorf("%w: pid %d", ErrNotSameUser, pid)
	}

	buf, err := unix.SysctlRaw("kern.procargs2", int(pid))
	if err != nil {
		return nil, fmt.Errorf("reading arguments of process %d: %w", pid, err)
	}

	raw, err := parseProcargs2(buf)
	if err != nil {
		return nil, fmt.Errorf("process %d: %w", pid, err)
	}

	// raw holds the full environment. It goes out of scope with this call, and
	// the only value returned is the redacted one.
	return raw.redact(pid, r), nil
}
