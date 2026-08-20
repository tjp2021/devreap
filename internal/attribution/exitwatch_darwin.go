//go:build darwin

package attribution

import (
	"fmt"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// kqueueExitWatch turns a session root's exit into a recorded event within
// milliseconds, using EVFILT_PROC with NOTE_EXIT.
//
// NOTE_FORK reports that a fork happened and carries no child identifier, and
// NOTE_TRACK, the flag that would deliver the child, does not appear in this
// system's kqueue manual page and is reported to return ENOTSUP on modern
// Darwin. That is why this filter is used for the death half of the problem
// only, where the identifier is known in advance.
type kqueueExitWatch struct {
	kq int

	mu      sync.Mutex
	watched map[int32]struct{}
	notices []ExitNotice
	closed  bool

	stop chan struct{}
	done chan struct{}
	now  func() time.Time
}

// NewKqueueExitWatch opens a kqueue and starts the goroutine that waits on it.
//
// The goroutine blocks with a timeout rather than forever, so a close is
// observed promptly without needing to interrupt a blocked syscall.
func NewKqueueExitWatch() (ExitWatch, error) {
	kq, err := unix.Kqueue()
	if err != nil {
		return nil, fmt.Errorf("opening kqueue: %w", err)
	}
	w := &kqueueExitWatch{
		kq:      kq,
		watched: make(map[int32]struct{}),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		now:     time.Now,
	}
	go w.run()
	return w, nil
}

// waitTimeout bounds one blocking wait. It only decides how quickly a close is
// noticed; an exit still arrives the moment the kernel reports it.
const waitTimeout = 500 * time.Millisecond

func (w *kqueueExitWatch) run() {
	defer close(w.done)
	events := make([]unix.Kevent_t, 16)
	timeout := unix.NsecToTimespec(int64(waitTimeout))

	for {
		select {
		case <-w.stop:
			return
		default:
		}

		n, err := unix.Kevent(w.kq, nil, events, &timeout)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return
		}
		if n <= 0 {
			continue
		}

		at := w.now()
		w.mu.Lock()
		for i := 0; i < n; i++ {
			if events[i].Filter != unix.EVFILT_PROC {
				continue
			}
			pid := int32(events[i].Ident)
			// EV_CLEAR means the event fires once. Dropping the registration
			// here keeps a reused identifier from inheriting the watch.
			delete(w.watched, pid)
			w.notices = append(w.notices, ExitNotice{PID: pid, At: at})
		}
		w.mu.Unlock()
	}
}

// Watch attaches a NOTE_EXIT watch.
//
// The attach race is real and is handled here rather than by the caller. A
// process can exit between the decision to watch it and the attach, in which
// case the kernel refuses the registration and this returns false. The caller
// then falls back to poll absence, which is the safety net for exactly this
// case.
func (w *kqueueExitWatch) Watch(pid int32) bool {
	if pid <= 1 {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return false
	}
	if _, already := w.watched[pid]; already {
		return true
	}

	event := unix.Kevent_t{
		Ident:  uint64(pid),
		Filter: unix.EVFILT_PROC,
		Flags:  unix.EV_ADD | unix.EV_ENABLE | unix.EV_CLEAR,
		Fflags: unix.NOTE_EXIT,
	}
	if _, err := unix.Kevent(w.kq, []unix.Kevent_t{event}, nil, nil); err != nil {
		return false
	}
	w.watched[pid] = struct{}{}
	return true
}

// Unwatch drops a registration, which the caller does when a root's identity no
// longer matches the process holding its identifier.
func (w *kqueueExitWatch) Unwatch(pid int32) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, watching := w.watched[pid]; !watching {
		return
	}
	delete(w.watched, pid)
	event := unix.Kevent_t{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_DELETE}
	// A delete that fails means the registration is already gone, which is the
	// outcome the caller wanted.
	_, _ = unix.Kevent(w.kq, []unix.Kevent_t{event}, nil, nil)
}

// Drain returns the notices that arrived since the last call.
func (w *kqueueExitWatch) Drain() []ExitNotice {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := w.notices
	w.notices = nil
	return out
}

// Close stops the waiting goroutine and releases every registration.
func (w *kqueueExitWatch) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	w.mu.Unlock()

	close(w.stop)
	<-w.done
	return unix.Close(w.kq)
}
