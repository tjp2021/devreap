# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.2.0] - 2026-08-19

### Changed

- **Default is now observe-only.** A fresh install scores and reports orphans but does not kill them. Set `dry_run: false` in `~/.config/devreap/config.yaml` to enable killing.
- A user blocklist now adds to the built-in blocklist instead of replacing it, so a custom entry can no longer drop the built-in protections.
- A listening port is no longer treated as evidence that a process is an orphan. A server holding a port is often alive and wanted.
- The scanner requires a strong lifecycle signal before it marks a process killable.
- Unreadable process metadata now counts as unknown instead of as evidence for a kill.
- The killer re-verifies full process identity and re-checks every kill condition immediately before it signals.
- The MCP pattern token is anchored, which stops incidental substring matches, and terminal-launched agents are detected.
- The broken-LaunchAgent hygiene check now inspects every user LaunchAgent instead of a fixed list of label prefixes.

### Added

- `devreap hygiene` system audit covering broken LaunchAgents, ghost sessions, dead crons, zombie dotdirs, sensitive tracked files, Downloads buildup, Claude debug and telemetry buildup, and low disk space.
- Dependabot configuration.

### Fixed

- Port scans now cancel on timeout instead of running past their deadline.

## [0.1.0] - 2026-02-28

### Added

- Multi-signal orphan scoring engine with five weighted signals: `ppid_is_init`, `parent_ide_dead`, `exceeded_duration`, `has_listener`, `no_tty`
- 18 built-in YAML process patterns across four categories: MCP servers, dev servers, headless browsers, and media tools
- Pattern matching via embedded YAML files with `go:embed` (zero external dependencies at runtime)
- MCP config cross-referencing — reads `~/.claude.json`, `~/.cursor/mcp.json`, `~/.vscode/mcp.json` to detect expected MCP servers
- IDE detection using path-based signatures for 12 IDEs (VS Code, Cursor, Claude Code CLI, Windsurf, Zed, IntelliJ family, Xcode)
- Background daemon with configurable scan interval (default 30s)
- macOS LaunchAgent install/uninstall (`devreap install`, `devreap uninstall`)
- Graceful multi-step signal delivery with PID reuse protection (SIGTERM -> wait -> SIGKILL; SIGINT first for ffmpeg)
- Safety checks: never kills PID 1, own process, parent process, other users' processes, or blocklisted system processes
- macOS notifications when the daemon kills an orphan
- Structured JSON logging with rotation
- CLI commands: `scan`, `start`, `stop`, `status`, `install`, `uninstall`, `kill`, `kill --port`, `logs`, `doctor`, `patterns`, `version`
- `--json` output for `scan` and `logs` commands
- `doctor` command for full diagnostics (config, patterns, permissions, MCP configs, LaunchAgent status)
- User-configurable YAML config at `~/.config/devreap/config.yaml` (signal weights, thresholds, blocklist, allowlist, extra patterns)
- Dry-run mode for testing without killing
- CI pipeline: golangci-lint, race-detector tests on macOS + Linux, build verification
- GoReleaser-based release workflow with Homebrew tap support
- MIT license

[0.2.0]: https://github.com/tjp2021/devreap/releases/tag/v0.2.0
[0.1.0]: https://github.com/tjp2021/devreap/releases/tag/v0.1.0
