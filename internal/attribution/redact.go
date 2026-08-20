// Package attribution records who started a process and tracks that process
// through its lifecycle. It is observe-only: nothing in this package signals a
// process, and nothing here may widen the set of processes devreap is willing
// to kill.
package attribution

import (
	"regexp"
	"strings"
)

// Redacted replaces any value the filter refuses to pass through.
const Redacted = "[REDACTED]"

// DefaultMaxCmdlineDisplay is the length a command line is truncated to before
// it is displayed. Records keep the redacted command line in full.
const DefaultMaxCmdlineDisplay = 200

// defaultEnvAllowlist names the only environment variables whose values may
// leave a foreign-process read. Each one is a session identifier, a project
// directory, or an agent name, which is the whole set the design permits. The
// harness registry widens this list with the marker names of the descriptors it
// loads, and it can never widen it past the secret-name guard below.
var defaultEnvAllowlist = []string{
	"CLAUDE_CODE_SESSION_ID",
	"CLAUDE_PROJECT_DIR",
	"CLAUDECODE",
	"CLAUDE_CODE_ENTRYPOINT",
	"AI_AGENT",
}

// secretNameRE matches a variable or flag name that names a secret. A name
// matching this is never allowlisted, even when a caller asks for it, because
// an allowlist entry is easier to add by accident than a leak is to undo.
var secretNameRE = regexp.MustCompile(`(?i)(token|secret|password|passwd|passphrase|api[-_]?key|apikey|access[-_]?key|private[-_]?key|credential|bearer|cookie|signature)`)

// secretFlagRE matches a whole command-line flag or key whose name announces a
// secret, so the value attached to it is masked whatever shape it has.
var secretFlagRE = regexp.MustCompile(`(?i)^-{0,2}[a-z0-9_.-]*(token|secret|password|passwd|passphrase|api[-_]?key|apikey|access[-_]?key|private[-_]?key|credential|bearer)[a-z0-9_.-]*$`)

// tokenValueREs match values that are token-shaped on their own, with no flag
// name to announce them. A long run of hex is included deliberately: masking a
// commit hash costs a reader some context, and not masking a key costs the user
// a secret.
var tokenValueREs = []*regexp.Regexp{
	regexp.MustCompile(`^sk-[A-Za-z0-9_-]{16,}$`),
	regexp.MustCompile(`^(?:ghp|gho|ghu|ghs|ghr|github_pat)_[A-Za-z0-9_]{16,}$`),
	regexp.MustCompile(`^xox[abeoprs]-[A-Za-z0-9-]{10,}$`),
	regexp.MustCompile(`^AKIA[0-9A-Z]{16}$`),
	regexp.MustCompile(`^eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{4,}$`),
	regexp.MustCompile(`^[A-Fa-f0-9]{32,}$`),
}

// urlCredentialRE matches the user information part of a URL, which is how a
// token reaches a command line without ever looking like a flag value.
var urlCredentialRE = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)([^/@\s]+)@`)

// Redactor is the single choke point between a foreign-process read and every
// consumer of that read. Nothing in this package returns an argument list, a
// command line, or an environment to a caller without passing it through here
// first, which is what R12 of the design requires.
//
// The zero value is not usable. Build one with NewRedactor.
type Redactor struct {
	allowedEnv map[string]struct{}
}

// NewRedactor returns a Redactor allowing the built-in environment variable
// names plus any extra names supplied. An extra name whose spelling names a
// secret is refused, so widening the allowlist cannot open a leak.
func NewRedactor(extraEnvNames ...string) *Redactor {
	r := &Redactor{allowedEnv: make(map[string]struct{}, len(defaultEnvAllowlist)+len(extraEnvNames))}
	for _, name := range defaultEnvAllowlist {
		r.allow(name)
	}
	for _, name := range extraEnvNames {
		r.allow(name)
	}
	return r
}

func (r *Redactor) allow(name string) {
	name = strings.TrimSpace(name)
	if name == "" || secretNameRE.MatchString(name) {
		return
	}
	r.allowedEnv[name] = struct{}{}
}

// defaultRedactor serves the package-level helpers. Callers that need the
// harness marker names in the allowlist build their own with NewRedactor.
var defaultRedactor = NewRedactor()

// EnvNameAllowed reports whether the value of this variable may be kept.
func (r *Redactor) EnvNameAllowed(name string) bool {
	if secretNameRE.MatchString(name) {
		return false
	}
	_, ok := r.allowedEnv[name]
	return ok
}

// AllowedEnvNames returns the allowlisted variable names in no fixed order.
func (r *Redactor) AllowedEnvNames() []string {
	names := make([]string, 0, len(r.allowedEnv))
	for name := range r.allowedEnv {
		names = append(names, name)
	}
	return names
}

// Env reduces a raw environment block to the allowlisted variables. Entries are
// "NAME=value" strings as the kernel supplies them. Every value outside the
// allowlist is dropped rather than masked, because a masked entry still records
// that the variable existed and this filter keeps nothing it does not need.
//
// Allowlisted values pass through unchanged. The secret-name guard already bars
// any name that announces a secret, and masking a session identifier that
// happened to look token-shaped would break attribution for no safety gain.
func (r *Redactor) Env(entries []string) map[string]string {
	out := make(map[string]string)
	for _, entry := range entries {
		name, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if !r.EnvNameAllowed(name) {
			continue
		}
		out[name] = value
	}
	return out
}

// Arg masks one command-line argument whose own shape announces a secret. It
// does not see the argument next to it, so callers wanting the "--flag value"
// form handled must use Args.
func (r *Redactor) Arg(arg string) string {
	if name, value, found := strings.Cut(arg, "="); found {
		if secretFlagRE.MatchString(name) {
			return name + "=" + Redacted
		}
		if isTokenShaped(value) {
			return name + "=" + Redacted
		}
		return maskURLCredentials(arg)
	}
	if isTokenShaped(arg) {
		return Redacted
	}
	return maskURLCredentials(arg)
}

// Args masks every argument that carries a secret, including the value that
// follows a secret-named flag written as two separate arguments.
func (r *Redactor) Args(args []string) []string {
	if args == nil {
		return nil
	}
	out := make([]string, 0, len(args))
	maskNext := false
	for _, arg := range args {
		if maskNext {
			out = append(out, Redacted)
			maskNext = false
			continue
		}
		if !strings.Contains(arg, "=") && secretFlagRE.MatchString(arg) {
			out = append(out, arg)
			maskNext = true
			continue
		}
		out = append(out, r.Arg(arg))
	}
	return out
}

// Cmdline joins an argument vector into the redacted command line that a record
// stores. It is a display and storage form, not something to re-execute.
func (r *Redactor) Cmdline(args []string) string {
	return strings.Join(r.Args(args), " ")
}

// CmdlineString redacts a command line that is already one string, which is the
// only form the existing scanner has. Splitting on whitespace loses quoting, so
// this is a best-effort pass for a value that was never structured.
func (r *Redactor) CmdlineString(cmdline string) string {
	if cmdline == "" {
		return ""
	}
	return strings.Join(r.Args(strings.Fields(cmdline)), " ")
}

// RedactEnv applies the package-level filter to a raw environment block.
func RedactEnv(entries []string) map[string]string { return defaultRedactor.Env(entries) }

// RedactArgs applies the package-level filter to an argument vector.
func RedactArgs(args []string) []string { return defaultRedactor.Args(args) }

// RedactCmdline applies the package-level filter and joins the result.
func RedactCmdline(args []string) string { return defaultRedactor.Cmdline(args) }

// RedactCmdlineString applies the package-level filter to a joined command line.
func RedactCmdlineString(cmdline string) string { return defaultRedactor.CmdlineString(cmdline) }

// TruncateForDisplay shortens a redacted string for a terminal view. It never
// substitutes for redaction, because a truncated secret is still a secret.
func TruncateForDisplay(s string, maxLen int) string {
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

func isTokenShaped(value string) bool {
	if len(value) < 16 {
		return false
	}
	for _, re := range tokenValueREs {
		if re.MatchString(value) {
			return true
		}
	}
	return isHighEntropyRun(value)
}

// isHighEntropyRun matches a long unbroken run of base64url characters holding
// all three character classes. A filesystem path never matches, because a path
// carries a separator or a dot, and both are excluded.
func isHighEntropyRun(value string) bool {
	const minRun = 40
	if len(value) < minRun {
		return false
	}
	var upper, lower, digit bool
	for _, c := range value {
		switch {
		case c >= 'A' && c <= 'Z':
			upper = true
		case c >= 'a' && c <= 'z':
			lower = true
		case c >= '0' && c <= '9':
			digit = true
		case c == '-' || c == '_':
		default:
			return false
		}
	}
	return upper && lower && digit
}

func maskURLCredentials(arg string) string {
	return urlCredentialRE.ReplaceAllString(arg, "${1}"+Redacted+"@")
}
