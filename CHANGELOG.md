# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.3.0] - 2026-08-20

### Added

- Session attribution. devreap now records who started each process instead of inferring it from circumstantial signals. A watcher polls the process table each second, records the spawn link it observed, and writes a durable birth record with an ownership claim. Ownership is led by watched ancestry, which works for any harness because every agent tool spawns children through the same system calls, so a harness that publishes nothing and is missing from the descriptor table still reaches full confidence, labelled `unknown-harness`. Environment markers and process groups corroborate and backfill, and a claim resting on either alone is reportable and never gates an action.
- A per-process lifecycle state machine. Every tracked process holds one state and every transition is written with its trigger, its evidence, the confirmation counter, and the accumulated awake time. Windows count awake time only: sleep pauses them and never resets them, which is what makes overnight cleanup possible on a laptop that sleeps around 123 times a day.
- `devreap top` and `devreap top --json`, a read-only view of per-session process trees with resident memory totals, per-process state, and time since owner exit. The unattributed bucket is shown separately, so a coverage gap is visible rather than hidden.
- `devreap evidence <session>`, which exports one session's spawn tree, birth timings, owner exit event, and full transition history as a single JSON document to attach to an upstream bug report.
- Owner exit detection through kqueue `NOTE_EXIT` on recognised session roots, with poll absence as the fallback source.
- An append-only journal with periodic snapshots, its own rotation at mode 0600, and per-type retention floors. The 32 megabyte ceiling is enforced by compaction, so a week of records still inside their retention floor leaves the journal briefly over it with a `doctor` finding rather than dropping the measurement series.
- A redaction filter between every foreign-process read and every consumer. It keeps only the session identifier, the project directory, and the agent name out of an environment, and masks token-shaped and key-shaped command-line arguments.
- An `attribution` config section, and `lifecycle_grace`, the per-class awake-time budget between a recorded owner exit and orphan candidacy. A user map merges with the built-in table and never replaces it, per the P0-8 blocklist-replacement finding. A missing class and a zero value both mean never, and neither ever means immediately.
- Coverage and watcher health reporting in `devreap status` and `devreap doctor`, with staleness judged in awake time so an overnight sleep never reads as a dead watcher.
- `docs/attribution.md`, covering the ownership channels, the confidence ladder, the harness table, the rules a user descriptor file must pass, the lifecycle, the storage model, and every degraded path.

### Changed

- Attribution is on by default and observe-only. It records and reports, and it holds no kill path in its code.
- The kill path calls one gate function instead of the strong-signal check directly. The existing requirement is evaluated first and on its own, and every check after it can only return false, so attribution can never make a process eligible that the previous path would not already permit. Phase A leaves the gate off, which makes the call reduce exactly to the check it replaced.

### Notes

- Phase B, in which attribution may narrow the kill-eligible set, ships as inert structure behind `attribution.gate_kills`. It defaults false, it is a separate opt-in from the existing kill opt-in, and no install or upgrade sets it. A test asserts no shipped code path can reach the reclaim states.

## [0.2.2] - 2026-08-19

### Fixed

- A terminal-launched Claude Code session is now detected as a running IDE, so `parent_ide_dead` no longer fires while the agent is working ([#8](https://github.com/tjp2021/devreap/issues/8)). macOS reports the short process name as `claude.exe`, which the previous `claude` signature never matched, and a bare `claude` command line carries no install path for the other signatures to read.
- The native install under `~/.local/share/claude/` is recognised. It names the process after the version, so only the executable path identifies it.

### Added

- `ProcessInfo` carries the resolved executable path. It is empty when the path cannot be read, which is routine on macOS after a tool updates itself, and an empty path is never read as evidence for a kill.

## [0.2.1] - 2026-08-19

### Added

- A `hygiene` config section that names the machine-specific targets for `devreap hygiene`. It holds `git_repos` and `zombie_dotdirs`. A leading `~/` in a repository path is expanded.

### Changed

- The sensitive-file check scans the repositories listed in `hygiene.git_repos` instead of a hardcoded `~/YNG` path, and it names the repository each hit came from.
- The zombie-dotdir check reads its directory names from `hygiene.zombie_dotdirs` instead of a hardcoded list.
- Both checks skip themselves when their list is empty, so a fresh install audits nothing machine-specific until someone configures it.
- The built-in sensitive-file patterns are now generic. The personal credential filenames are gone.

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

[0.3.0]: https://github.com/tjp2021/devreap/releases/tag/v0.3.0
[0.2.2]: https://github.com/tjp2021/devreap/releases/tag/v0.2.2
[0.2.1]: https://github.com/tjp2021/devreap/releases/tag/v0.2.1
[0.2.0]: https://github.com/tjp2021/devreap/releases/tag/v0.2.0
[0.1.0]: https://github.com/tjp2021/devreap/releases/tag/v0.1.0
