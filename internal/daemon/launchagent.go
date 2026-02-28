package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"text/template"
)

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.devreap.daemon</string>
    <key>ProgramArguments</key>
    <array>
        <string>{{.BinaryPath}}</string>
        <string>start</string>
        <string>--foreground</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>{{.LogDir}}/launchd-stdout.log</string>
    <key>StandardErrorPath</key>
    <string>{{.LogDir}}/launchd-stderr.log</string>
    <key>ProcessType</key>
    <string>Background</string>
    <key>ThrottleInterval</key>
    <integer>10</integer>
</dict>
</plist>
`

const plistLabel = "com.devreap.daemon"

type PlistData struct {
	BinaryPath string
	LogDir     string
}

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", plistLabel+".plist")
}

func Install(binaryPath string, logDir string) error {
	// Validate binary exists and is executable
	info, err := os.Stat(binaryPath)
	if err != nil {
		return fmt.Errorf("binary not found at %s: %w", binaryPath, err)
	}
	if info.Mode()&0111 == 0 {
		return fmt.Errorf("binary at %s is not executable", binaryPath)
	}

	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("creating log dir: %w", err)
	}

	tmpl, err := template.New("plist").Parse(plistTemplate)
	if err != nil {
		return fmt.Errorf("parsing plist template: %w", err)
	}

	path := plistPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating LaunchAgents dir: %w", err)
	}

	// Unload existing if present (ignore errors)
	if IsInstalled() {
		unloadAgent()
	}

	// Write plist atomically (temp + rename)
	tmpPath := path + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("creating plist: %w", err)
	}

	data := PlistData{
		BinaryPath: binaryPath,
		LogDir:     logDir,
	}

	if err := tmpl.Execute(f, data); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("writing plist: %w", err)
	}
	f.Close()

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("installing plist: %w", err)
	}

	// Load the agent using bootstrap (modern API)
	if err := loadAgent(path); err != nil {
		return err
	}

	return nil
}

func Uninstall() error {
	path := plistPath()

	// Unload first
	unloadAgent()

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing plist: %w", err)
	}

	return nil
}

func IsInstalled() bool {
	_, err := os.Stat(plistPath())
	return err == nil
}

func PlistPath() string {
	return plistPath()
}

// loadAgent uses launchctl bootstrap (modern) with fallback to load (legacy).
func loadAgent(path string) error {
	uid := strconv.Itoa(os.Getuid())
	target := "gui/" + uid

	cmd := exec.Command("launchctl", "bootstrap", target, path)
	if output, err := cmd.CombinedOutput(); err != nil {
		// Fallback to legacy load for older macOS
		cmd = exec.Command("launchctl", "load", path)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("launchctl load: %w: %s", err, output)
		}
		_ = output
	}
	return nil
}

// unloadAgent uses launchctl bootout (modern) with fallback to unload (legacy).
func unloadAgent() {
	uid := strconv.Itoa(os.Getuid())
	target := "gui/" + uid + "/" + plistLabel

	cmd := exec.Command("launchctl", "bootout", target)
	if _, err := cmd.CombinedOutput(); err != nil {
		// Fallback to legacy unload
		cmd = exec.Command("launchctl", "unload", plistPath())
		cmd.CombinedOutput()
	}
}
