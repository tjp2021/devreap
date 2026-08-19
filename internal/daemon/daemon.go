package daemon

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	gopsProcess "github.com/shirou/gopsutil/v4/process"

	"github.com/tjp2021/devreap/internal/config"
	"github.com/tjp2021/devreap/internal/killer"
	"github.com/tjp2021/devreap/internal/logger"
	"github.com/tjp2021/devreap/internal/notify"
	"github.com/tjp2021/devreap/internal/scanner"
)

// ScannerI is the interface the daemon uses to scan for orphans.
// Extracted to enable testing with mock scanners.
type ScannerI interface {
	Scan(ctx context.Context) (*scanner.ScanResult, error)
}

type Daemon struct {
	cfg      *config.Config
	scanner  ScannerI
	log      *logger.Logger
	notifier notify.Notifier
	stopOnce sync.Once
	stopCh   chan struct{}

	// recheckStrongSignal re-reads a live process to confirm the kill
	// conditions still hold. Injectable so tests can drive both outcomes;
	// production always uses scanner.RecheckStrongSignal.
	recheckStrongSignal func(pid int32) (bool, error)
}

func New(cfg *config.Config, s ScannerI, log *logger.Logger, notifier notify.Notifier) *Daemon {
	return &Daemon{
		cfg:                 cfg,
		scanner:             s,
		log:                 log,
		notifier:            notifier,
		stopCh:              make(chan struct{}),
		recheckStrongSignal: scanner.RecheckStrongSignal,
	}
}

func (d *Daemon) Run() error {
	d.log.Info("daemon starting", logger.Entry{
		Action: "start",
	})

	if err := d.writePID(); err != nil {
		return fmt.Errorf("writing PID file: %w", err)
	}
	defer d.removePID()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	ticker := time.NewTicker(d.cfg.ScanInterval)
	defer ticker.Stop()

	// Run initial scan immediately
	d.scanAndKill()

	for {
		select {
		case <-ticker.C:
			d.scanAndKill()
		case sig := <-sigCh:
			d.log.Info(fmt.Sprintf("daemon stopping (signal: %s)", sig), logger.Entry{Action: "stop"})
			return nil
		case <-d.stopCh:
			d.log.Info("daemon stopping (requested)", logger.Entry{Action: "stop"})
			return nil
		}
	}
}

// Stop signals the daemon to shut down. Safe to call multiple times.
func (d *Daemon) Stop() {
	d.stopOnce.Do(func() {
		close(d.stopCh)
	})
}

func (d *Daemon) scanAndKill() {
	// Enforce scan timeout to prevent gopsutil hangs from blocking the daemon
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resultCh := make(chan *scanner.ScanResult, 1)
	errCh := make(chan error, 1)

	go func() {
		result, err := d.scanner.Scan(ctx)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	var result *scanner.ScanResult
	select {
	case result = <-resultCh:
		// success
	case err := <-errCh:
		d.log.Error("scan failed", logger.Entry{Error: err.Error()})
		return
	case <-ctx.Done():
		d.log.Error("scan timed out after 30s", logger.Entry{Error: ctx.Err().Error(), Action: "scan"})
		return
	}

	if len(result.Orphans) == 0 {
		d.log.Debug("scan clean", logger.Entry{
			Action: "scan",
		})
		return
	}

	d.log.Info(fmt.Sprintf("found %d orphan candidates", len(result.Orphans)), logger.Entry{
		Action: "scan",
	})

	var killed []string
	for _, orphan := range result.Orphans {
		// Weak signals alone describe a working machine, not an orphan. A
		// candidate can clear the score threshold on circumstantial evidence;
		// without a strong lifecycle signal it is reported, never killed.
		//
		// This is derived from the signal data rather than read from the
		// candidate's KillEligible field, so the gate cannot be bypassed by a
		// caller that builds a candidate without setting the field.
		if !scanner.HasStrongSignal(orphan.Signals) {
			d.log.Info("suspicious, not killable (no strong signal)", logger.Entry{
				PID:     orphan.Process.PID,
				Process: orphan.Process.Name,
				Cmdline: orphan.Process.Cmdline,
				Pattern: orphan.Pattern.Name,
				Score:   orphan.Score,
				Signals: orphan.Signals,
				Action:  "report",
			})
			continue
		}

		if d.cfg.DryRun {
			d.log.Info("would kill (dry-run)", logger.Entry{
				PID:     orphan.Process.PID,
				Process: orphan.Process.Name,
				Cmdline: orphan.Process.Cmdline,
				Pattern: orphan.Pattern.Name,
				Score:   orphan.Score,
				Signals: orphan.Signals,
				Action:  "dry-run",
			})
			continue
		}

		gracePeriod := d.cfg.GracePeriod
		if orphan.Pattern.GracePeriod > 0 {
			gracePeriod = orphan.Pattern.GracePeriod
		}

		// The snapshot behind this decision can be a full scan interval old.
		// Re-check the strong signal against the live process so a stale
		// snapshot cannot drive a kill. A process whose parent came back, or
		// whose condition can no longer be confirmed, is left alone.
		stillOrphaned, err := d.recheckStrongSignal(orphan.Process.PID)
		if err != nil || !stillOrphaned {
			reason := "conditions no longer hold on fresh data"
			if err != nil {
				reason = err.Error()
			}
			d.log.Info("skipped kill after re-check", logger.Entry{
				PID:     orphan.Process.PID,
				Process: orphan.Process.Name,
				Cmdline: orphan.Process.Cmdline,
				Pattern: orphan.Pattern.Name,
				Score:   orphan.Score,
				Signals: orphan.Signals,
				Error:   reason,
				Action:  "recheck",
			})
			continue
		}

		kr := killer.Kill(
			killer.Identity{
				PID:        orphan.Process.PID,
				Name:       orphan.Process.Name,
				Cmdline:    orphan.Process.Cmdline,
				CreateTime: orphan.Process.CreateTime,
			},
			orphan.Pattern.Signal,
			gracePeriod,
			d.cfg.Blocklist,
		)

		if kr.Success {
			d.log.Info("killed orphan", logger.Entry{
				PID:     orphan.Process.PID,
				Process: orphan.Process.Name,
				Cmdline: orphan.Process.Cmdline,
				Pattern: orphan.Pattern.Name,
				Score:   orphan.Score,
				Signals: orphan.Signals,
				Action:  "kill",
			})
			killed = append(killed, fmt.Sprintf("%s (PID %d)", orphan.Process.Name, orphan.Process.PID))
		} else {
			d.log.Error("kill failed", logger.Entry{
				PID:     orphan.Process.PID,
				Process: orphan.Process.Name,
				Cmdline: orphan.Process.Cmdline,
				Pattern: orphan.Pattern.Name,
				Score:   orphan.Score,
				Signals: orphan.Signals,
				Error:   kr.Error,
				Action:  "kill",
			})
		}
	}

	// Batch notification: one notification per scan cycle, not per kill
	if len(killed) > 0 {
		msg := fmt.Sprintf("Killed %d orphan(s): %s", len(killed), strings.Join(killed, ", "))
		d.notifier.Notify("devreap", msg)
	}
}

func (d *Daemon) writePID() error {
	dir := filepath.Dir(d.cfg.PidFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Atomic write: write to temp file, then rename
	tmpFile := d.cfg.PidFile + ".tmp"
	if err := os.WriteFile(tmpFile, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		return err
	}
	return os.Rename(tmpFile, d.cfg.PidFile)
}

func (d *Daemon) removePID() {
	os.Remove(d.cfg.PidFile)
}

// ReadPID reads the daemon PID from the PID file.
func ReadPID(pidFile string) (int, error) {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("invalid PID file: %w", err)
	}
	return pid, nil
}

// IsRunning checks if the daemon process is currently running.
// It verifies both that the PID exists AND that it's actually a devreap process,
// protecting against stale PID files after PID reuse.
func IsRunning(pidFile string) bool {
	pid, err := ReadPID(pidFile)
	if err != nil {
		return false
	}
	return isDevreapProcess(pid)
}

// isDevreapProcess checks if the given PID is alive and is a devreap process.
func isDevreapProcess(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, FindProcess always succeeds. Send signal 0 to check alive.
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	// Verify it's actually devreap by checking the process name via gopsutil
	goproc, err := gopsProcess.NewProcess(int32(pid))
	if err != nil {
		return false
	}
	name, err := goproc.Name()
	if err != nil {
		return false
	}
	return name == "devreap"
}
