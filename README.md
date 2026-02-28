# devreap

[![CI](https://github.com/tjp2021/devreap/actions/workflows/ci.yaml/badge.svg)](https://github.com/tjp2021/devreap/actions/workflows/ci.yaml)
[![Go Report Card](https://goreportcard.com/badge/github.com/tjp2021/devreap)](https://goreportcard.com/report/github.com/tjp2021/devreap)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](https://opensource.org/licenses/MIT)
[![Go Reference](https://pkg.go.dev/badge/github.com/tjp2021/devreap.svg)](https://pkg.go.dev/github.com/tjp2021/devreap)

Automatically detect and kill orphaned developer processes.

MCP servers, dev servers, headless browsers, and ffmpeg instances that survive after their parent IDE or terminal crashes. Uses multi-signal scoring to avoid false positives. Zero config to start.

## Why This Exists

When your IDE crashes or you force-quit a terminal, child processes survive as orphans. This isn't a minor annoyance — it's a systemic problem affecting every developer who uses AI coding agents:

- **[189 chrome processes, 27GB RAM in 10 hours](https://github.com/anthropics/claude-code/issues/15861)** — a single Claude Code session spawning `--claude-in-chrome-mcp` processes at 4/minute with no cleanup
- **[641 chroma-mcp processes in 5 minutes](https://github.com/thedotmack/claude-mem/issues/1063)** — nearly crashing WSL2, consuming 64GB virtual memory
- **[40+ orphaned MCP servers running for days](https://github.com/anthropics/claude-code/issues/1935)** — still open, filed June 2025
- **[30+ processes after 3 days of Cursor use](https://forum.cursor.com/t/mcp-server-process-leak/151615)** — 3-5GB RAM leaked
- **[11GB pagefile expansion on Windows](https://github.com/anthropics/claude-code/issues/29413)** — from 27 leaked Claude processes

**84% of developers now use AI tools** (Stack Overflow 2025). Each AI coding session spawns 3-10 background processes. None reliably clean up on crash or force-quit.

macOS makes this worse: there's no `PR_SET_PDEATHSIG` (Linux's mechanism to auto-kill children when parents die), no kernel-level safety net. When your IDE dies on macOS, orphans survive indefinitely.

`kill-port` gets **1.16M weekly npm downloads** — that's a million developers manually killing port-squatting processes every week. Both are reactive (you find the problem, you kill it). Nothing proactively detects and cleans up orphans.

devreap does.

## Install

### Homebrew (macOS)

```bash
brew install tjp2021/devreap/devreap
```

### Pre-built binaries

Download from [GitHub Releases](https://github.com/tjp2021/devreap/releases).

### From source

```bash
go install github.com/tjp2021/devreap/cmd/devreap@latest
```

## Quick Start

**Recommended setup — two commands:**

```bash
# 1. Install as a LaunchAgent so it runs automatically on login
devreap install

# 2. Start it now (without waiting for next login)
devreap start
```

That's it. devreap runs in the background, scans every 30 seconds, and kills orphans automatically. You'll get a macOS notification when something gets killed.

**Just want to see what's orphaned without running a daemon:**

```bash
devreap scan       # show orphan candidates
devreap scan -v    # show all matched processes, including safe ones (useful for debugging)
```

**Check it's running:**

```bash
devreap status
devreap doctor     # full diagnostics — checks config, patterns, process enumeration
```

## What It Looks Like

Running `devreap scan` shows matched processes and their scores:

```
$ devreap scan

Scanned 712 processes, 4 matched patterns

Orphan candidates (2):

PID    NAME  PATTERN          SCORE  AGE     STATUS  SIGNALS
---    ----  -------          -----  ---     ------  -------
3338   node  node-mcp-server  0.70   26h44m  ORPHAN  ppid_is_init, parent_ide_dead
7099   node  node-mcp-server  0.65   18h33m  ORPHAN  ppid_is_init, exceeded_duration, no_tty
```

The **SIGNALS** column shows which signals contributed to the score — what specifically made devreap flag this process. `ppid_is_init` means the parent died. `parent_ide_dead` means no IDE is running. `exceeded_duration` means it's been running longer than this type of process should. If you think something was killed incorrectly, the signals tell you exactly why it was flagged.

Use `devreap scan -v` to see all matched processes, including ones below the kill threshold — useful when tuning or debugging false positives.

When the daemon kills something, it logs the full reason:

```
$ devreap logs

14:02:31 INFO killed orphan pid=3338 process=node pattern=node-mcp-server score=0.70 signals=[ppid_is_init=0.40,parent_ide_dead=0.30]
14:02:31 INFO killed orphan pid=7099 process=node pattern=node-mcp-server score=0.65 signals=[ppid_is_init=0.40,exceeded_duration=0.25]
```

## How It Works

### Multi-Signal Orphan Scoring

devreap doesn't use binary "is orphan" / "isn't orphan" detection. A process that matches a known pattern gets scored across multiple signals. Each signal has a weight — the score is the sum of weights for signals that fire. If the total reaches the kill threshold (default 0.6), the process is killed.

| Signal | Weight | When it fires |
|--------|--------|---------------|
| `ppid_is_init` | 0.40 | Parent process died — process was reparented to launchd (PPID = 1) |
| `parent_ide_dead` | 0.30 | No IDE is running anywhere on the machine |
| `exceeded_duration` | 0.25 | Process has been running longer than its pattern's max duration |
| `has_listener` | 0.20 | Process is bound to a listening TCP port |
| `no_tty` | 0.15 | Process has no controlling terminal |

**Examples:**
- MCP server, PPID=1, no Cursor running → 0.40 + 0.30 = **0.70** → killed
- MCP server, PPID=1, Cursor IS running → 0.40 only = **0.40** → safe
- Dev server, PPID=1, running 48 hours → 0.40 + 0.25 = **0.65** → killed
- Your Postgres, running as `_postgres` user → **0.00** → ignored (devreap only scores your own processes)

### IDE Detection

devreap uses **path-based signatures**, not substring matching. `CursorUIViewService` (Apple's text cursor macOS service) won't trigger a false positive — only `/Applications/Cursor.app/Contents/MacOS/Cursor` counts.

Detected IDEs: VS Code, Cursor, Claude Code CLI, Windsurf, Zed, IntelliJ IDEA, WebStorm, GoLand, PyCharm, PhpStorm, RustRover, Xcode.

### MCP Config Cross-Referencing

devreap reads your IDE's MCP configuration files:
- `~/.claude.json` (Claude Code)
- `~/.cursor/mcp.json` (Cursor)
- `~/.vscode/mcp.json` (VS Code)

It knows which MCP servers *should* be running. If those servers are running but no IDE is active, they're flagged as orphans. `devreap doctor` will warn you if any of these files exist but can't be parsed.

### How Killing Works

When a process hits the threshold, devreap sends signals in sequence and waits between each:

1. **First signal** (SIGTERM by default, SIGINT for ffmpeg) — asks the process to exit cleanly
2. **Wait** (grace period, default 5 seconds)
3. **SIGTERM** (if first signal was SIGINT)
4. **Wait** (grace period)
5. **SIGKILL** — force kill if still running

ffmpeg gets SIGINT first because that's the signal that makes it write the MP4 file headers correctly. SIGKILL on ffmpeg produces a corrupted file.

### Safety

devreap will **never** kill:
- PID 1 (launchd/init) — hardcoded
- Its own process — hardcoded
- Its parent process — hardcoded
- Any process owned by a different user
- Anything on the blocklist (postgres, redis, nginx, sshd, and 20+ other system processes by default)

Before sending any signal, devreap re-verifies the process name matches what was scanned. If the PID was reused by a different process in between, it aborts.

## Commands

```
devreap scan                   # One-shot scan, print orphan candidates
devreap scan --json            # Machine-readable JSON output
devreap scan -v                # Show all pattern matches (including safe ones)
devreap start                  # Start background daemon
devreap stop                   # Stop daemon
devreap status                 # Daemon status + current config
devreap install                # Install macOS LaunchAgent (auto-start on login)
devreap uninstall              # Remove LaunchAgent
devreap kill <pid>             # Manually kill a process gracefully
devreap kill --port 3000       # Kill whatever is listening on a port
devreap logs                   # View recent daemon log entries (last 50)
devreap logs -n 100            # Show last N entries
devreap logs --level error     # Filter by severity (debug, info, warn, error)
devreap logs --json            # Raw JSON lines — pipe to jq for filtering
devreap doctor                 # Diagnostics: config, patterns, permissions, MCP configs
devreap patterns               # List all 18 built-in patterns with durations and signals
devreap version                # Print version, commit, and build date
```

## Configuration

devreap works out of the box with no config file. Create `~/.config/devreap/config.yaml` only if you need to change something.

```yaml
scan_interval: 30s       # How often to scan. Min: 1s. Max: 24h.
kill_threshold: 0.6      # Minimum score to kill a process. Range: 0.1 - 1.0.
                         # Lower = more aggressive. Higher = more conservative.
grace_period: 5s         # How long to wait between signals (SIGTERM → wait → SIGKILL).
                         # Min: 1s. Give processes time to clean up before force-killing.
dry_run: false           # If true, logs what would be killed but doesn't kill anything.
                         # Useful for testing — run `devreap logs` to see what it caught.

notify:
  enabled: true          # macOS notifications when the daemon kills something.

# Signal weights — how much each signal contributes to the orphan score.
# A process is killed when its total score >= kill_threshold.
# Higher weight = that signal matters more. Each must be 0.0 - 1.0.
#
# When to tune weights:
#   Getting false positives on MCP servers while your IDE is open?
#     → Lower parent_ide_dead (e.g. 0.1)
#   Running intentional background servers with no TTY?
#     → Lower no_tty (e.g. 0.05)
#   Want more aggressive cleanup of long-running processes?
#     → Raise exceeded_duration (e.g. 0.4)
#   Want to rely almost entirely on PPID?
#     → Raise ppid_is_init (e.g. 0.7)
#
# You only need to specify the weights you want to change.
# Unspecified weights keep their defaults.
weights:
  ppid_is_init: 0.4       # Parent process died (PPID = 1)
  parent_ide_dead: 0.3    # No IDE running on this machine
  exceeded_duration: 0.25 # Running longer than pattern's max_duration
  has_listener: 0.2       # Bound to a TCP listening port
  no_tty: 0.15            # No controlling terminal

# Processes to never kill, by name. Case-insensitive.
# These are in addition to the built-in protection list (postgres, redis, nginx, sshd, etc.)
blocklist:
  - my-database
  - my-background-worker

# Processes to skip even if they score above the threshold.
# Use this for servers you intentionally run persistently in the background.
# Matches against process name and command line. Case-insensitive.
allowlist:
  - my-persistent-mcp-server

# Paths to additional YAML pattern files to load alongside the built-ins.
extra_patterns:
  - ~/.config/devreap/my-patterns.yaml
```

## Built-in Patterns

18 patterns across 4 categories. The **Max Duration** is how long a process of that type is allowed to run before the `exceeded_duration` signal fires — it's not a hard kill timer, it contributes 0.25 to the score.

| Category | Patterns | Max Duration | Signal |
|----------|----------|-------------|--------|
| **MCP servers** | node, python, npx, uvx, docker | 4h | SIGTERM |
| **Dev servers** | Next.js, Vite, Webpack, Expo, CRA, esbuild | 24h | SIGTERM |
| **Headless browsers** | Chrome (headless + remote debugging), Firefox | 2-4h | SIGTERM |
| **Media tools** | ffmpeg, ffprobe, sox, ImageMagick | 30m-2h | SIGINT/SIGTERM |

Run `devreap patterns` for the full list with all fields.

## Troubleshooting

**Something got killed that shouldn't have been**

Run `devreap logs --json | tail -20` to see the last kills with full signal breakdown. The `signals` field shows exactly what triggered it. Then either:
- Add it to the `allowlist` in your config to permanently protect it
- Lower the relevant weight if that signal fires too aggressively for your setup
- Raise `kill_threshold` (e.g. to `0.7`) to require stronger evidence before killing

**devreap isn't catching orphans I know exist**

Run `devreap scan -v` — this shows all processes matching a pattern, even ones below the threshold. Look at their scores and which signals are firing. If a process has score 0.40 and you want it caught, either lower `kill_threshold` or raise the weight of a signal that's firing.

**I want to test what it would kill before letting it run for real**

Set `dry_run: true` in your config, then run `devreap start`. It will log everything it *would* kill to `devreap logs` without actually killing anything. Review the logs and adjust config, then set `dry_run: false`.

**A process isn't matching any pattern**

Run `devreap patterns` to see what's covered. If your process isn't there, you can add a custom pattern — see the CONTRIBUTING guide.

**devreap doctor shows a warning**

Run `devreap doctor` — it checks config validity, pattern loading, process enumeration, MCP config parsing, and LaunchAgent status. Warnings include an explanation of what to do.

## Architecture

Single static binary. No runtime dependencies. Cross-compiles to macOS (arm64/amd64) and Linux.

```
cmd/devreap/main.go     → entry point
internal/
  scanner/              → process enumeration, orphan scoring, MCP cross-referencing
  patterns/             → embedded YAML pattern library, regex matching
  killer/               → signal delivery, PID reuse protection, safety checks
  daemon/               → scan loop, LaunchAgent install/uninstall
  config/               → YAML config loading with validation
  logger/               → structured JSON logging with rotation
  notify/               → macOS notifications
  cli/                  → all commands
```

## License

MIT
