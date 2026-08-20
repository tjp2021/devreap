//go:build !darwin

package attribution

import "fmt"

// ReadProcArgs is not implemented off darwin. It returns ErrNotSupported rather
// than an empty result, so a caller can never mistake "this platform has no
// reader" for "this process has no arguments".
func ReadProcArgs(pid int32, _ *Redactor) (*ProcArgs, error) {
	return nil, fmt.Errorf("%w: pid %d", ErrNotSupported, pid)
}

// ReadProcArgsOwned is not implemented off darwin.
func ReadProcArgsOwned(pid int32, _ uint32, _ *Redactor) (*ProcArgs, error) {
	return nil, fmt.Errorf("%w: pid %d", ErrNotSupported, pid)
}
