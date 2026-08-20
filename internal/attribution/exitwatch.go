package attribution

import "time"

// ExitNotice is one owner exit the kernel reported. It carries the instant the
// notification arrived, which is what makes a kqueue exit exact rather than
// bounded by the poll interval.
type ExitNotice struct {
	PID int32
	At  time.Time
}

// ExitWatch registers per-process exit notifications for session roots.
//
// kqueue EVFILT_PROC attaches per process identifier and needs the process to
// exist first, which is the wrong ordering for catching a birth and the right
// one for catching a death: the identifier is known in advance and there are
// about nine roots on a machine.
//
// Every implementation is best effort. Poll absence is the fallback source, so a
// watch that could not attach costs exactness rather than detection.
type ExitWatch interface {
	// Watch registers a notification for a process. It returns false when the
	// watch could not attach, which usually means the process already exited.
	Watch(pid int32) bool
	// Unwatch drops a registration.
	Unwatch(pid int32)
	// Drain returns the notices that arrived since the last call.
	Drain() []ExitNotice
	// Close releases every registration.
	Close() error
}

// noopExitWatch is used when kqueue is unavailable. Poll absence then supplies
// every exit event, which is slower and just as safe.
type noopExitWatch struct{}

func (noopExitWatch) Watch(int32) bool    { return false }
func (noopExitWatch) Unwatch(int32)       {}
func (noopExitWatch) Drain() []ExitNotice { return nil }
func (noopExitWatch) Close() error        { return nil }

// NewNoopExitWatch returns an exit watch that never reports anything.
func NewNoopExitWatch() ExitWatch { return noopExitWatch{} }
