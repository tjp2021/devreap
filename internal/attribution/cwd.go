package attribution

import (
	"github.com/shirou/gopsutil/v4/process"
)

// ReadProcessCwd reads a process's working directory, which is the repository
// source for every harness that publishes no marker.
//
// Unlike the environment call, this one the process library can do on darwin: it
// goes through proc_pidinfo and works for processes the effective user can
// reach. A refusal or a failure returns false, and unknown is never treated as a
// repository named "".
func ReadProcessCwd(pid int32) (string, bool) {
	p, err := process.NewProcess(pid)
	if err != nil {
		return "", false
	}
	cwd, err := p.Cwd()
	if err != nil || cwd == "" {
		return "", false
	}
	return cwd, true
}
