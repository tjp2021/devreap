package attribution

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// Errors returned by the process-arguments reader. Callers distinguish them
// with errors.Is, because each one means something different about whether the
// process may be attributed at all.
var (
	// ErrNotSupported means the platform has no implementation. The reader is
	// darwin-only, and every other platform gets this rather than a silent
	// empty result.
	ErrNotSupported = errors.New("attribution: process argument reads are implemented on darwin only")

	// ErrNotSameUser means the target process belongs to another account. The
	// reader refuses it: devreap only ever inspects the account it runs in.
	ErrNotSameUser = errors.New("attribution: process belongs to another user")

	// ErrProcargsParse means the packed buffer did not parse. It is always an
	// error and never a partial result, because a half-parsed environment block
	// is exactly how a secret leaks into a log.
	ErrProcargsParse = errors.New("attribution: malformed process argument buffer")
)

// Bounds on the packed buffer. The kernel supplies this memory and another
// process's arguments shaped it, so every length parsed here is treated as
// untrusted input and checked against a bound before it is used.
const (
	// maxProcargsBuffer caps the whole buffer. The kernel's own limit is
	// kern.argmax, typically 1 MiB; this leaves headroom and still bounds the
	// allocation if that limit is ever raised.
	maxProcargsBuffer = 4 << 20

	// maxArgc caps the argument count the buffer claims to hold, which is the
	// one length in the format that a corrupt prefix can inflate directly.
	maxArgc = 4096

	// maxEnvEntries caps the environment block, which carries no count of its
	// own and ends only at an empty string.
	maxEnvEntries = 4096

	// maxProcargsString caps one packed string.
	maxProcargsString = 256 << 10
)

// ProcArgs is the redacted result of reading one process's argument and
// environment memory. Every field has already passed the redaction filter, so
// this type is safe to store, log, and display. The unredacted environment
// never exists outside the read that produced it.
type ProcArgs struct {
	PID int32

	// Exe is the executable path recorded in the argument block. It is empty
	// when the kernel supplied none, which means unknown rather than absent.
	Exe string

	// Args is the redacted argument vector, including argv[0].
	Args []string

	// Cmdline is Args joined for storage and display. It is not re-executable.
	Cmdline string

	// Env holds only the allowlisted variables. A variable absent here was
	// dropped by the filter, not by the process.
	Env map[string]string
}

// rawProcargs is the parsed but unredacted buffer. It never leaves the package
// and never reaches a consumer: redact is the only way out of it.
type rawProcargs struct {
	exe  string
	args []string
	env  []string
}

// redact converts a raw read into the only form callers may see.
func (raw rawProcargs) redact(pid int32, r *Redactor) *ProcArgs {
	if r == nil {
		r = defaultRedactor
	}
	args := r.Args(raw.args)
	return &ProcArgs{
		PID:     pid,
		Exe:     raw.exe,
		Args:    args,
		Cmdline: joinArgs(args),
		Env:     r.Env(raw.env),
	}
}

func joinArgs(args []string) string {
	if len(args) == 0 {
		return ""
	}
	var b bytes.Buffer
	for i, arg := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(arg)
	}
	return b.String()
}

// parseProcargs2 parses the packed KERN_PROCARGS2 buffer.
//
// The layout is an argument count, the executable path, NUL padding, that many
// NUL-terminated arguments, and then NUL-terminated environment entries until
// an empty string or the end of the buffer.
//
// Every failure returns ErrProcargsParse and no data. A truncated buffer, an
// unterminated string, and a corrupt count all take that path, because
// returning what was parsed so far would hand a caller a fragment of another
// process's memory with no way to know it was a fragment.
func parseProcargs2(buf []byte) (rawProcargs, error) {
	var raw rawProcargs

	if len(buf) > maxProcargsBuffer {
		return raw, fmt.Errorf("%w: %d bytes exceeds the %d byte bound", ErrProcargsParse, len(buf), maxProcargsBuffer)
	}
	if len(buf) < 4 {
		return raw, fmt.Errorf("%w: %d bytes is shorter than the argument count", ErrProcargsParse, len(buf))
	}

	// The count is written in host byte order by the kernel that wrote it.
	argc := binary.NativeEndian.Uint32(buf[:4])
	if argc > maxArgc {
		return raw, fmt.Errorf("%w: argument count %d exceeds the bound of %d", ErrProcargsParse, argc, maxArgc)
	}

	exe, pos, err := readPackedString(buf, 4)
	if err != nil {
		return raw, fmt.Errorf("executable path: %w", err)
	}
	raw.exe = exe

	// The kernel pads between the executable path and the first argument with
	// NUL bytes. Skipping them is not optional: argv starts after the padding.
	for pos < len(buf) && buf[pos] == 0 {
		pos++
	}

	raw.args = make([]string, 0, argc)
	for n := uint32(0); n < argc; n++ {
		arg, next, argErr := readPackedString(buf, pos)
		if argErr != nil {
			return rawProcargs{}, fmt.Errorf("argument %d of %d: %w", n, argc, argErr)
		}
		raw.args = append(raw.args, arg)
		pos = next
	}

	for pos < len(buf) {
		// An empty entry ends the environment block.
		if buf[pos] == 0 {
			break
		}
		if len(raw.env) >= maxEnvEntries {
			return rawProcargs{}, fmt.Errorf("%w: environment exceeds the bound of %d entries", ErrProcargsParse, maxEnvEntries)
		}
		entry, next, envErr := readPackedString(buf, pos)
		if envErr != nil {
			return rawProcargs{}, fmt.Errorf("environment entry %d: %w", len(raw.env), envErr)
		}
		raw.env = append(raw.env, entry)
		pos = next
	}

	return raw, nil
}

// readPackedString reads one NUL-terminated string and returns the offset just
// past its terminator. A string running to the end of the buffer without a
// terminator is a truncated read, not a shorter string.
func readPackedString(buf []byte, pos int) (string, int, error) {
	if pos < 0 || pos >= len(buf) {
		return "", 0, fmt.Errorf("%w: offset %d is past the end of a %d byte buffer", ErrProcargsParse, pos, len(buf))
	}
	end := bytes.IndexByte(buf[pos:], 0)
	if end < 0 {
		return "", 0, fmt.Errorf("%w: unterminated string at offset %d", ErrProcargsParse, pos)
	}
	if end > maxProcargsString {
		return "", 0, fmt.Errorf("%w: string at offset %d is %d bytes, over the %d byte bound", ErrProcargsParse, pos, end, maxProcargsString)
	}
	return string(buf[pos : pos+end]), pos + end + 1, nil
}
