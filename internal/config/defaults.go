package config

import "time"

const (
	DefaultScanInterval  = 30 * time.Second
	DefaultKillThreshold = 0.6
	DefaultGracePeriod   = 5 * time.Second
	DefaultLogDir        = "~/.local/share/devreap/logs"
	DefaultConfigPath    = "~/.config/devreap/config.yaml"
	DefaultMaxLogSize    = 10 * 1024 * 1024 // 10MB
	DefaultMaxLogFiles   = 5
	DefaultPidFile       = "~/.local/share/devreap/daemon.pid"
	DefaultNotifyEnabled = true
	// DefaultDryRun is true so a fresh install observes and reports without
	// killing anything. Killing is opt-in: the user sets dry_run: false only
	// after reviewing what devreap would have killed on their machine.
	DefaultDryRun = true
)

// Attribution defaults.
const (
	// DefaultAttributionEnabled turns the watcher on by default. It is safe to
	// default on because attribution is observe-only and can never widen the set
	// of processes devreap is willing to kill.
	DefaultAttributionEnabled = false

	// DefaultAttributionPoll is the process table poll cadence. One second keeps
	// the birth race small at roughly 1.5 per cent of one core.
	DefaultAttributionPoll = time.Second

	DefaultAttributionStoreDir    = "~/.local/share/devreap/attribution"
	DefaultAttributionAdapterFile = "~/.config/devreap/harnesses.yaml"

	// DefaultGateKills is false and stays false until the user sets it by hand.
	// Phase B gating is a separate opt-in from the existing kill opt-in, and no
	// install or upgrade may set it.
	DefaultGateKills = false
)

// DefaultAttribution returns the built-in attribution settings.
func DefaultAttribution() AttributionConfig {
	return AttributionConfig{
		Enabled:      DefaultAttributionEnabled,
		PollInterval: DefaultAttributionPoll,
		StoreDir:     DefaultAttributionStoreDir,
		AdapterFile:  DefaultAttributionAdapterFile,
		GateKills:    DefaultGateKills,
	}
}

// Process classes the lifecycle window is keyed by. The first four are pattern
// categories; the last two are the conditions that have no window at all.
const (
	ClassHeadlessBrowser = "headless-browser"
	ClassMCP             = "mcp"
	ClassDevServer       = "dev-server"
	ClassMedia           = "media"
	ClassUnclassified    = "unclassified"
	ClassUnattributed    = "unattributed"
)

// LifecycleGraceNever is the window of a class that may never be acted on. A
// missing class means never, and so does a zero value. Neither ever means
// "immediately": absence of a window is absence of permission to act, and no
// value skips the wait entirely.
const LifecycleGraceNever time.Duration = 0

// DefaultConfirmationCount is the number of confirming scans required on top of
// the class window, which at the 30 second scan interval adds at least 90
// seconds of awake time.
const DefaultConfirmationCount = 3

// DefaultLifecycleGrace returns the built-in per-class window table.
//
// The windows are awake-time budgets between a recorded owner exit and
// candidacy, not wall-clock deadlines. A headless browser is cheap to restart
// and expensive to keep. An MCP server reconnects quickly, and longer idles are
// common. A running dev server is doing its job and a developer often returns. A
// long encode looks idle from outside. Absence of knowledge is not evidence of
// abandonment, and no owner means no owner death at any age.
func DefaultLifecycleGrace() map[string]time.Duration {
	return map[string]time.Duration{
		ClassHeadlessBrowser: 2 * time.Minute,
		ClassMCP:             5 * time.Minute,
		ClassDevServer:       30 * time.Minute,
		ClassMedia:           30 * time.Minute,
		ClassUnclassified:    LifecycleGraceNever,
		ClassUnattributed:    LifecycleGraceNever,
	}
}

// NeverActionableClasses are the two classes that stay ineligible however long
// they run, so a window for either could not take effect.
var NeverActionableClasses = []string{ClassUnclassified, ClassUnattributed}

var DefaultBlocklist = []string{
	"postgres", "postgresql", "redis-server", "redis", "nginx",
	"sshd", "ssh-agent", "cupsd", "coreaudiod", "WindowServer",
	"loginwindow", "Finder", "Dock", "SystemUIServer",
	"launchd", "kernel_task", "mds", "mds_stores",
	"spotlight", "fseventsd", "diskarbitrationd",
	"configd", "airportd", "bluetoothd",
}
