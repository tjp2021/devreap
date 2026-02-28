package patterns

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// compiledPattern holds a pattern with pre-compiled regexes.
type compiledPattern struct {
	Pattern
	cmdRegex  *regexp.Regexp
	argsRegex *regexp.Regexp // nil if no args pattern
}

// patternCache caches compiled regexes by pattern command+args strings.
// Thread-safe for concurrent reads after initial population.
var (
	patternCacheMu sync.RWMutex
	patternCache   = make(map[string]*compiledPattern)
)

func cacheKey(p Pattern) string {
	return p.Command + "\x00" + p.Args
}

func getCompiled(p Pattern) (*compiledPattern, error) {
	key := cacheKey(p)

	patternCacheMu.RLock()
	cp, ok := patternCache[key]
	patternCacheMu.RUnlock()
	if ok {
		return cp, nil
	}

	// Compile and cache
	cmdRegex, err := regexp.Compile(p.Command)
	if err != nil {
		return nil, fmt.Errorf("compiling command regex %q: %w", p.Command, err)
	}

	var argsRegex *regexp.Regexp
	if p.Args != "" {
		argsRegex, err = regexp.Compile(p.Args)
		if err != nil {
			return nil, fmt.Errorf("compiling args regex %q: %w", p.Args, err)
		}
	}

	cp = &compiledPattern{
		Pattern:   p,
		cmdRegex:  cmdRegex,
		argsRegex: argsRegex,
	}

	patternCacheMu.Lock()
	patternCache[key] = cp
	patternCacheMu.Unlock()

	return cp, nil
}

// MatchResult holds the pattern that matched a process.
type MatchResult struct {
	Pattern Pattern
}

// Match returns the first pattern that matches the given command name and args, or nil.
func (r *Registry) Match(cmdName string, cmdArgs string) *MatchResult {
	for _, p := range r.patterns {
		cp, err := getCompiled(p)
		if err != nil {
			continue // skip patterns with invalid regex
		}
		if matchCompiled(cp, cmdName, cmdArgs) {
			return &MatchResult{Pattern: p}
		}
	}
	return nil
}

func matchCompiled(cp *compiledPattern, cmdName string, cmdArgs string) bool {
	if !cp.cmdRegex.MatchString(cmdName) {
		return false
	}
	if cp.argsRegex != nil {
		if !cp.argsRegex.MatchString(cmdArgs) {
			return false
		}
	}
	return true
}

// CommandName extracts the base command name from a full path or command line.
func CommandName(cmdline string) string {
	parts := strings.Fields(cmdline)
	if len(parts) == 0 {
		return ""
	}
	cmd := parts[0]
	idx := strings.LastIndex(cmd, "/")
	if idx >= 0 {
		cmd = cmd[idx+1:]
	}
	return cmd
}

// CommandArgs returns everything after the first field in a command line.
func CommandArgs(cmdline string) string {
	parts := strings.Fields(cmdline)
	if len(parts) <= 1 {
		return ""
	}
	return strings.Join(parts[1:], " ")
}
