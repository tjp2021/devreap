package hygiene

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tjp2021/devreap/internal/config"
)

// ErrIssuesFound indicates that the audit completed successfully and found
// actionable hygiene issues. Callers can use it to return a non-zero status
// without terminating the process from inside the library.
var ErrIssuesFound = errors.New("hygiene issues found")

// Issue represents a single hygiene problem found.
type Issue struct {
	Check   string `json:"check"`
	Message string `json:"message"`
}

// Result holds all hygiene check results.
type Result struct {
	Issues []Issue `json:"issues"`
}

// Checker runs system hygiene audits.
type Checker struct {
	homeDir string
	cfg     config.HygieneConfig
}

// New creates a Checker rooted at the user's home directory. The config
// supplies the targets for the checks that depend on one machine's layout.
func New(cfg config.HygieneConfig) (*Checker, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}
	return &Checker{homeDir: home, cfg: cfg}, nil
}

// RunAll executes every hygiene check and returns the combined result.
func (c *Checker) RunAll() *Result {
	r := &Result{}
	c.checkBrokenLaunchAgents(r)
	c.checkGhostSessions(r)
	c.checkDeadCrons(r)
	c.checkZombieDotdirs(r)
	c.checkSensitiveFiles(r)
	c.checkDownloadsBuildup(r)
	c.checkClaudeAccumulation(r)
	c.checkDiskSpace(r)
	return r
}

func (c *Checker) addIssue(r *Result, check, message string) {
	r.Issues = append(r.Issues, Issue{Check: check, Message: message})
}

// 1. Broken LaunchAgents — target executable doesn't exist.
//
// Every user LaunchAgent is checked. The label is not filtered, because a
// broken agent is worth reporting whoever installed it.
func (c *Checker) checkBrokenLaunchAgents(r *Result) {
	agentDir := filepath.Join(c.homeDir, "Library", "LaunchAgents")

	entries, err := os.ReadDir(agentDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".plist") {
			continue
		}

		plistPath := filepath.Join(agentDir, entry.Name())
		target := plistExecutable(plistPath)
		if target == "" || !filepath.IsAbs(target) {
			continue
		}
		if _, err := os.Stat(target); os.IsNotExist(err) {
			c.addIssue(r, "BROKEN_LAUNCHAGENT",
				fmt.Sprintf("%s -> %s (missing)", entry.Name(), target))
		}
	}
}

func plistExecutable(plistPath string) string {
	for _, key := range []string{"ProgramArguments.0", "Program"} {
		out, err := exec.Command("plutil", "-extract", key, "raw", "-o", "-", plistPath).Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
	}
	return ""
}

// 2. Ghost Claude sessions — recent session records whose recorded cwd no
// longer exists. The cwd is read from JSONL instead of decoding Claude's
// ambiguous dash-encoded project-directory name.
func (c *Checker) checkGhostSessions(r *Result) {
	projectsDir := filepath.Join(c.homeDir, ".claude", "projects")
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	missing := make(map[string]int)

	_ = filepath.WalkDir(projectsDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().Before(cutoff) {
			return nil
		}
		cwd := sessionCWD(path)
		if cwd == "" || filepath.Clean(cwd) == filepath.Clean(c.homeDir) {
			return nil
		}
		if _, err := os.Stat(cwd); os.IsNotExist(err) {
			missing[cwd]++
		}
		return nil
	})

	for cwd, count := range missing {
		c.addIssue(r, "GHOST_SESSION",
			fmt.Sprintf("%d recent session(s) reference missing cwd %s", count, cwd))
	}
}

func sessionCWD(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	reader := bufio.NewReader(io.LimitReader(f, 1<<20))
	for i := 0; i < 100; i++ {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			var event struct {
				CWD string `json:"cwd"`
			}
			if json.Unmarshal(line, &event) == nil && filepath.IsAbs(event.CWD) {
				return filepath.Clean(event.CWD)
			}
		}
		if err != nil {
			break
		}
	}
	return ""
}

// 3. Dead crons — crontab entries pointing to missing executables.
func (c *Checker) checkDeadCrons(r *Result) {
	out, err := exec.Command("crontab", "-l").Output()
	if err != nil {
		return // no crontab
	}

	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Skip lines that don't look like cron entries (need at least 6 fields)
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		cmd := cronExecutable(fields[5:])
		if cmd == "" || isShellBuiltin(cmd) {
			continue
		}
		if strings.HasPrefix(cmd, "~/") {
			cmd = filepath.Join(c.homeDir, strings.TrimPrefix(cmd, "~/"))
		}
		var err error
		if filepath.IsAbs(cmd) {
			_, err = os.Stat(cmd)
		} else {
			_, err = exec.LookPath(cmd)
		}
		if err != nil {
			c.addIssue(r, "DEAD_CRON",
				fmt.Sprintf("%s doesn't exist — line: %s", cmd, line))
		}
	}
}

func cronExecutable(fields []string) string {
	for _, field := range fields {
		if isEnvAssignment(field) {
			continue
		}
		return strings.Trim(field, `"'`)
	}
	return ""
}

func isEnvAssignment(field string) bool {
	name, _, ok := strings.Cut(field, "=")
	if !ok || name == "" {
		return false
	}
	for i, r := range name {
		isLower := r >= 'a' && r <= 'z'
		isUpper := r >= 'A' && r <= 'Z'
		isDigit := i > 0 && r >= '0' && r <= '9'
		if !isLower && !isUpper && r != '_' && !isDigit {
			return false
		}
	}
	return true
}

func isShellBuiltin(command string) bool {
	switch command {
	case "cd", "source", ".", "export", "eval":
		return true
	default:
		return false
	}
}

// 4. Zombie dotdirs — directories the user deleted that came back.
//
// Which directories those are is specific to one machine, so the list comes
// from hygiene.zombie_dotdirs in the config. An empty list skips the check.
func (c *Checker) checkZombieDotdirs(r *Result) {
	for _, name := range c.cfg.ZombieDotdirs {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		dir := filepath.Join(c.homeDir, name)
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		// Get size
		out, err := exec.Command("du", "-sh", dir).Output()
		size := "unknown"
		if err == nil {
			parts := strings.Fields(string(out))
			if len(parts) > 0 {
				size = parts[0]
			}
		}
		c.addIssue(r, "ZOMBIE_DOTDIR",
			fmt.Sprintf("%s is back (%s) — should have been deleted", dir, size))
	}
}

// sensitiveFilePatterns match the base name of a tracked file that usually
// holds a credential or an exported conversation. They are generic on
// purpose: a name here must be suspicious in any repository.
var sensitiveFilePatterns = []string{
	"*_messages.csv",
	"*_messages.txt",
	"client_secret_*.json",
	"*.session",
}

// 5. Sensitive files tracked in git.
//
// Which repositories to scan is specific to one machine, so the list comes
// from hygiene.git_repos in the config. An empty list skips the check.
func (c *Checker) checkSensitiveFiles(r *Result) {
	for _, repo := range c.cfg.GitRepos {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			continue
		}
		if _, err := os.Stat(repo); err != nil {
			continue
		}
		c.checkRepoSensitiveFiles(r, repo)
	}
}

func (c *Checker) checkRepoSensitiveFiles(r *Result, repo string) {
	out, err := exec.Command("git", "-C", repo, "ls-files", "-z").Output()
	if err != nil {
		return
	}
	for _, tracked := range strings.Split(string(out), "\x00") {
		if tracked == "" {
			continue
		}
		base := filepath.Base(tracked)
		for _, pattern := range sensitiveFilePatterns {
			matched, matchErr := filepath.Match(pattern, base)
			if matchErr == nil && matched {
				c.addIssue(r, "SENSITIVE_FILE",
					fmt.Sprintf("git-tracked in %s: %s", repo, tracked))
				break
			}
		}
	}
}

// 6. Downloads buildup — more than 50 files
func (c *Checker) checkDownloadsBuildup(r *Result) {
	dlDir := filepath.Join(c.homeDir, "Downloads")
	entries, err := os.ReadDir(dlDir)
	if err != nil {
		return
	}

	count := len(entries)
	if count <= 50 {
		return
	}

	out, err := exec.Command("du", "-sh", dlDir).Output()
	size := "unknown"
	if err == nil {
		parts := strings.Fields(string(out))
		if len(parts) > 0 {
			size = parts[0]
		}
	}
	c.addIssue(r, "DOWNLOADS_BUILDUP",
		fmt.Sprintf("%d files (%s) — triage needed", count, size))
}

// 7. Claude debug/telemetry accumulation
func (c *Checker) checkClaudeAccumulation(r *Result) {
	// Debug logs (>100MB)
	debugDir := filepath.Join(c.homeDir, ".claude", "debug")
	if mb := dirSizeMB(debugDir); mb > 100 {
		c.addIssue(r, "CLAUDE_DEBUG",
			fmt.Sprintf("%dMB — purge with: find ~/.claude/debug/ -mtime +7 -delete", mb))
	}

	// Telemetry (>10MB)
	telemetryDir := filepath.Join(c.homeDir, ".claude", "telemetry")
	if mb := dirSizeMB(telemetryDir); mb > 10 {
		c.addIssue(r, "CLAUDE_TELEMETRY",
			fmt.Sprintf("%dMB — purge with: rm ~/.claude/telemetry/*.json", mb))
	}
}

// 8. Disk space warning (<15GB free)
func (c *Checker) checkDiskSpace(r *Result) {
	out, err := exec.Command("df", "-g", "/").Output()
	if err != nil {
		return
	}
	lines := strings.Split(string(out), "\n")
	if len(lines) < 2 {
		return
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 4 {
		return
	}
	avail, err := strconv.Atoi(fields[3])
	if err != nil {
		return
	}
	if avail < 15 {
		c.addIssue(r, "LOW_DISK",
			fmt.Sprintf("%dGB free — under 15GB threshold", avail))
	}
}

// dirSizeMB returns the size of a directory in megabytes, or 0 on error.
func dirSizeMB(path string) int {
	out, err := exec.Command("du", "-sm", path).Output()
	if err != nil {
		return 0
	}
	parts := strings.Fields(string(out))
	if len(parts) == 0 {
		return 0
	}
	mb, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0
	}
	return mb
}
