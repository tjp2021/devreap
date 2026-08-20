//go:build darwin

package attribution

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

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
