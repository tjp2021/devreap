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

macOS makes this worse: there's no `PR_SET_PDEATHSIG` (Linux's mechanism to auto-kill children when parents die), no `PR_SET_CHILD_SUBREAPER`, no kernel-level safety net. When your IDE dies on macOS, orphans survive indefinitely.

`kill-port` gets **1.16M weekly npm downloads** — that's a million developers manually killing port-squatting processes every week. `fkill-cli` has 6,900 GitHub stars. Both are reactive (you find the problem, you kill it). Nothing proactively detects and cleans up orphans.

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

```bash
# See what's orphaned right now
devreap scan

# Start the daemon (scans every 30s, kills orphans automatically)
devreap start

# Install as LaunchAgent (auto-start on login)
devreap install

# Check status
devreap status

# Run diagnostics
devreap doctor
```

## What It Looks Like

```
$ devreap scan

Scanned 712 processes, 4 matched patterns

Orphan candidates (2):

PID    NAME  PATTERN          SCORE  AGE     STATUS  SIGNALS
---    ----  -------          -----  ---     ------  -------
3338   node  node-mcp-server  0.70   26h44m  ORPHAN  ppid_is_init, parent_ide_dead
7099   node  node-mcp-server  0.65   18h33m  ORPHAN  ppid_is_init, exceeded_duration, no_tty
```

```
$ devreap logs --level info

14:02:31 INFO daemon starting
14:02:31 INFO found 2 orphan candidates
14:02:31 INFO killed orphan pid=3338 process=node pattern=node-mcp-server score=0.70 signals=[ppid_is_init=0.40,parent_ide_dead=0.30]
14:02:31 INFO killed orphan pid=7099 process=node pattern=node-mcp-server score=0.65 signals=[ppid_is_init=0.40,exceeded_duration=0.25]
```

## How It Works

### Multi-Signal Orphan Scoring

devreap doesn't use binary "is orphan" / "isn't orphan" detection. A process that matches a known pattern gets scored across multiple signals:

| Signal | Weight | Description |
|--------|--------|-------------|
| `ppid_is_init` | 0.40 | PPID is 1 (parent died, reparented to launchd) |
| `parent_ide_dead` | 0.30 | No IDE process running (VS Code, Cursor, Claude Code, Zed, JetBrains) |
| `exceeded_duration` | 0.25 | Running longer than the pattern's max duration |
| `has_listener` | 0.20 | Bound to a listening port (potential orphaned server) |
| `no_tty` | 0.15 | No controlling terminal attached |

**Default threshold: 0.6** — a process needs multiple signals to be flagged. This eliminates false positives.

**Examples:**
- MCP server, PPID=1, no Cursor running → **0.70** → killed
- MCP server, PPID=1, Cursor IS running → **0.40** → safe
- Dev server, PPID=1, running 48 hours → **0.65** → killed
- Your Postgres, running as `_postgres` user → **0.00** → ignored (wrong user)

### IDE Detection

devreap uses **path-based signatures**, not substring matching. `CursorUIViewService` (Apple's text cursor macOS service) won't trigger a false positive — only `/Applications/Cursor.app/Contents/MacOS/Cursor` counts.

Detected IDEs: VS Code, Cursor, Claude Code CLI, Windsurf, Zed, IntelliJ IDEA, WebStorm, GoLand, PyCharm, PhpStorm, RustRover, Xcode.

### MCP Config Cross-Referencing

devreap reads your IDE's MCP configuration files:
- `~/.claude.json` (Claude Code)
- `~/.cursor/mcp.json` (Cursor)
- `~/.vscode/mcp.json` (VS Code)

It knows which MCP servers *should* be running. If servers are running but no IDE is active, they're flagged.

### Pattern-Aware Signal Strategy

Each process type gets the right shutdown signal:
- **ffmpeg** → `SIGINT` first (writes moov atom for clean MP4, then SIGTERM, then SIGKILL)
- **Node.js** → `SIGTERM` → `SIGKILL`
- **Chrome** → `SIGTERM` with extended grace period

### Safety

- **PID reuse protection** — verifies process name before sending signals
- **User isolation** — only kills your processes, never another user's
- **Blocklist** — postgres, redis, nginx, sshd, and 20+ system processes are protected
- **PID 1 / self / parent protection** — hardcoded, can't be overridden
- **Config validation** — rejects invalid thresholds, weights, and intervals at startup

## Commands

```
devreap scan                   # One-shot scan, print orphan candidates
devreap scan --json            # Machine-readable output
devreap scan -v                # Show all pattern matches (including safe ones)
devreap start                  # Start background daemon
devreap start --foreground     # Foreground mode (used by LaunchAgent)
devreap stop                   # Stop daemon
devreap status                 # Daemon status + config
devreap kill <pid>             # Manual graceful kill
devreap kill --port 3000       # Kill by port
devreap logs                   # View recent daemon log entries
devreap logs -n 100            # Show last 100 entries
devreap logs --level error     # Filter by severity
devreap logs --json            # Raw JSON (pipe to jq)
devreap install                # Install macOS LaunchAgent
devreap uninstall              # Remove LaunchAgent
devreap doctor                 # Run diagnostics
devreap patterns               # List all 18 built-in patterns
devreap version                # Print version
```

## Configuration

Optional. devreap works with zero config using sensible defaults.

Create `~/.config/devreap/config.yaml` to customize:

```yaml
scan_interval: 30s       # How often to scan (min: 1s, max: 24h)
kill_threshold: 0.6      # Score threshold to kill (0.1 - 1.0)
grace_period: 5s         # Time between signals
dry_run: false           # Log what would be killed without killing

notify:
  enabled: true          # macOS notifications on kill

# Tune signal weights (each 0.0 - 1.0)
# Higher weight = that signal contributes more to the orphan score.
# A process is killed when its total score >= kill_threshold (default 0.6).
#
# Common tuning scenarios:
#   Getting false positives on MCP servers while IDE is open?
#     → Lower parent_ide_dead (e.g. 0.1) so IDE presence matters less
#   Running headless servers with no TTY that aren't orphans?
#     → Lower no_tty (e.g. 0.05) so terminal absence matters less
#   Want more aggressive cleanup of long-running processes?
#     → Raise exceeded_duration (e.g. 0.4)
#   Want to rely almost entirely on PPID detection?
#     → Raise ppid_is_init (e.g. 0.7) and lower the others
weights:
  ppid_is_init: 0.4      # Parent process died (PPID reparented to launchd)
  parent_ide_dead: 0.3   # No IDE running on this machine
  exceeded_duration: 0.25 # Process running longer than pattern's max_duration
  has_listener: 0.2      # Process is bound to a listening port
  no_tty: 0.15           # No controlling terminal

# Note: setting one weight preserves all others at their defaults.
# You only need to specify the weights you want to change.

# Never kill these (in addition to built-in system process protection)
blocklist:
  - postgres
  - redis-server
  - nginx

# Always skip these even if they match a pattern and score above threshold.
# Use this for persistent servers you intentionally run in the background.
allowlist:
  - my-persistent-mcp-server

# Additional pattern files beyond built-ins
extra_patterns:
  - ~/.config/devreap/my-patterns.yaml
```

## Built-in Patterns

18 patterns across 4 categories:

| Category | Patterns | Max Duration | Signal |
|----------|----------|-------------|--------|
| **MCP servers** | node, python, npx, uvx, docker | 4h | SIGTERM |
| **Dev servers** | Next.js, Vite, Webpack, Expo, CRA, esbuild | 24h | SIGTERM |
| **Headless browsers** | Chrome (headless + remote debugging), Firefox | 2-4h | SIGTERM |
| **Media tools** | ffmpeg, ffprobe, sox, ImageMagick | 30m-2h | SIGINT/SIGTERM |

See all with `devreap patterns`.

## Architecture

```
cmd/devreap/main.go           → entry point
internal/
  cli/                         → 10 cobra commands
  scanner/
    process.go                 → gopsutil process enumeration + port mapping
    scorer.go                  → multi-signal scoring engine + IDE detection
    orphan.go                  → threshold filtering + parent-first kill ordering
    mcp.go                     → MCP config cross-referencing
  patterns/
    registry.go                → go:embed YAML pattern loading
    matcher.go                 → compiled regex caching (18 compiles, not 12,600)
  killer/
    killer.go                  → PID-reuse-safe signal delivery
    safety.go                  → blocklist + ownership checks
    signals.go                 → per-pattern signal sequences
  daemon/
    daemon.go                  → scan loop with 30s timeout, sync.Once stop
    launchagent.go             → macOS plist generation + bootstrap/bootout
  config/                      → YAML config with validation + partial merge
  logger/                      → structured JSON logging with rotation
  notify/                      → macOS notifications via osascript
```

Single static binary. No runtime dependencies. Cross-compiles to macOS (arm64/amd64) and Linux.

## License

MIT
