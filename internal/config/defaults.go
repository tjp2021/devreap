package config

import "time"

const (
	DefaultScanInterval   = 30 * time.Second
	DefaultKillThreshold  = 0.6
	DefaultGracePeriod    = 5 * time.Second
	DefaultLogDir         = "~/.local/share/devreap/logs"
	DefaultConfigPath     = "~/.config/devreap/config.yaml"
	DefaultMaxLogSize     = 10 * 1024 * 1024 // 10MB
	DefaultMaxLogFiles    = 5
	DefaultPidFile        = "~/.local/share/devreap/daemon.pid"
	DefaultNotifyEnabled  = true
	DefaultDryRun         = false
)

var DefaultBlocklist = []string{
	"postgres", "postgresql", "redis-server", "redis", "nginx",
	"sshd", "ssh-agent", "cupsd", "coreaudiod", "WindowServer",
	"loginwindow", "Finder", "Dock", "SystemUIServer",
	"launchd", "kernel_task", "mds", "mds_stores",
	"spotlight", "fseventsd", "diskarbitrationd",
	"configd", "airportd", "bluetoothd",
}
