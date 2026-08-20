//go:build !darwin

package attribution

import "fmt"

// NewKqueueExitWatch is not implemented off darwin. The caller falls back to
// poll absence, which is the same fallback a failed attach uses.
func NewKqueueExitWatch() (ExitWatch, error) {
	return nil, fmt.Errorf("%w: kqueue exit watches", ErrNotSupported)
}
