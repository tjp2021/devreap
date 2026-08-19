# devreap

[![CI](https://github.com/tjp2021/devreap/actions/workflows/ci.yaml/badge.svg)](https://github.com/tjp2021/devreap/actions/workflows/ci.yaml)
[![Go Report Card](https://goreportcard.com/badge/github.com/tjp2021/devreap)](https://goreportcard.com/report/github.com/tjp2021/devreap)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](https://opensource.org/licenses/MIT)
[![Go Reference](https://pkg.go.dev/badge/github.com/tjp2021/devreap.svg)](https://pkg.go.dev/github.com/tjp2021/devreap)

Find and clean up orphaned developer processes on macOS.

MCP servers, dev servers, headless browsers, and ffmpeg instances survive when their parent IDE or terminal dies. devreap scores each one against multiple signals and reports what it finds. It observes first. You turn on cleanup after you agree with the reports.

## Why this exists

When your IDE crashes or you force-quit a terminal, child processes survive as orphans. This isn't a minor annoyance. It's a systemic problem for developers who use AI coding agents:

- **[189 chrome processes, 27GB RAM in 10 hours](https://github.com/anthropics/claude-code/issues/15861)**, a single Claude Code session spawning `--claude-in-chrome-mcp` processes at 4/minute with no cleanup
- **[641 chroma-mcp processes in 5 minutes](https://github.com/thedotmack/claude-mem/issues/1063)**, nearly crashing WSL2 and consuming 64GB virtual memory
- **[40+ orphaned MCP servers running for days](https://github.com/anthropics/claude-code/issues/1935)**, still open, filed June 2025
- **[30+ processes after 3 days of Cursor use](https://forum.cursor.com/t/mcp-server-process-leak/151615)**, 3-5GB RAM leaked
- **[11GB pagefile expansion on Windows](https://github.com/anthropics/claude-code/issues/29413)**, from 27 leaked Claude processes

**84% of developers now use AI tools** (Stack Overflow 2025). Each AI coding session spawns 3-10 background processes. None of them reliably clean up on crash or force-quit.

macOS makes this worse. There's no `PR_SET_PDEATHSIG`, the Linux mechanism that auto-kills children when parents die. No kernel safety net exists. When your IDE dies on macOS, orphans survive indefinitely.

`kill-port` gets **1.16M weekly npm downloads**. That's a million developers manually killing port-squatting processes every week, after they notice the problem. devreap looks for it continuously instead.

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

## Quick start

devreap starts in observe-only mode. A fresh install kills nothing. Work through these four steps and you decide when cleanup turns on.

**1. See what's on your machine right now.**

```bash
devreap scan
```

**2. Run it in the background.**

```bash
devreap install    # LaunchAgent, starts on login
devreap start      # start now, without waiting for a login
```

**3. Let it watch for a few days, then read the reports.**

```bash
devreap logs
devreap status     # confirms you're still in observe-only mode
```

Every entry says what devreap would've killed and which signals fired. Check each one against what you were actually running.

**4. Turn on cleanup once you agree with the reports.**

Create `~/.config/devreap/config.yaml`:

```yaml
dry_run: false
```

Restart the daemon and devreap starts acting. If a report ever looks wrong, set `dry_run: true` again and tune the config.

## What it looks like

```
$ devreap scan

Scanned 712 processes, 4 matched patterns

Orphan candidates (2):

PID    NAME  PATTERN          SCORE  AGE     STATUS  SIGNALS
---    ----  -------          -----  ---     ------  -------
3338   node  node-mcp-server  0.70   26h44m  KILL    ppid_is_init, parent_ide_dead
7099   node  node-mcp-server  0.70   18h33m  watch   exceeded_duration, no_tty, parent_ide_dead
```

Both processes score 0.70. Only the first one is killable.

PID 3338 has `ppid_is_init`, so its parent is genuinely gone. PID 7099 scores the same total from three weak signals, and its parent is still alive. devreap reports it and leaves it running.

The **SIGNALS** column shows what made devreap flag each process. Use `devreap scan -v` to see everything that matched a pattern, including processes below the threshold.

When the daemon acts, it logs the full reason:

```
$ devreap logs

14:02:31 INFO killed orphan pid=3338 process=node pattern=node-mcp-server score=0.70 signals=[ppid_is_init=0.40,parent_ide_dead=0.30]
```

## How classification works

### Scoring is necessary but not sufficient

A process that matches a known pattern gets scored across several signals. Each signal carries a weight, and the score is the sum of the weights that fire.

| Signal | Weight | When it fires | Strength |
|--------|--------|---------------|----------|
| `ppid_is_init` | 0.40 | Parent died, so the process was reparented to launchd (PPID = 1) | strong |
| `parent_ide_dead` | 0.30 | No IDE is running anywhere on the machine | weak |
| `exceeded_duration` | 0.25 | Process ran longer than its pattern's max duration | weak |
| `no_tty` | 0.15 | Process has no controlling terminal | weak |

Clearing the threshold gets a process reported. It doesn't get it killed.

### A strong signal is required to kill

Only `ppid_is_init` is direct evidence that a process lost its parent. The other three describe the state of your machine as a whole.

`parent_ide_dead` is global rather than ancestral. It asks whether any IDE runs anywhere, so closing one editor makes it fire for every matched process at once, including processes you launched from a terminal. `no_tty` is normal for anything a launcher or daemon started. `exceeded_duration` just means a long-lived server is doing its job.

So devreap splits the decision in two:

- **Strong signal present** and score above the threshold, the process shows as `KILL` and the daemon can act on it.
- **Weak signals only**, the process shows as `watch`. It appears in scans and logs, and devreap never kills it.

The daemon derives this gate from the signal data itself, so no caller can skip it.

### Unknown metadata protects the process

macOS won't always tell you about a process. Older devreap versions read a failure as an answer, and every ambiguous case resolved toward killing. A process whose start time couldn't be read looked like it had run for 2000 years.

Now devreap tracks whether each field was actually read. `exceeded_duration` and `no_tty` need a successful read before they fire. A process whose owner can't be established is excluded from kill eligibility completely. Unknown means unknown.

### Identity is re-verified before any kill

A scan snapshot can be a full scan interval old by the time a kill runs. PIDs get reused, and supervisors restart the servers they manage.

Before it sends a signal, devreap re-reads the live process and compares four fields against the snapshot: PID, name, full command line, and start time. Any mismatch aborts the kill. A field it can't read counts as a verification failure. A snapshot with no recorded start time can't be verified at all, so it's never killed.

The daemon also re-checks that the strong signal still holds immediately before signalling. An error during that re-check means it doesn't kill.

### IDE detection

devreap matches IDEs on whole path and argument boundaries. `CursorUIViewService`, an Apple text-cursor service, won't trigger a false positive. Only `/Applications/Cursor.app/Contents/MacOS/Cursor` counts.

Detected: VS Code, Cursor, Claude Code, Codex, Windsurf, Zed, IntelliJ IDEA, WebStorm, GoLand, PyCharm, PhpStorm, RustRover, Xcode. Coverage includes terminal launches and Homebrew installs alongside npm paths.

### MCP config cross-referencing

devreap reads your IDE's MCP configuration:

- `~/.claude.json` (Claude Code)
- `~/.cursor/mcp.json` (Cursor)
- `~/.vscode/mcp.json` (VS Code)

It learns which MCP servers should be running. `devreap doctor` warns you when one of these files exists but won't parse.

### How killing works

Once a process passes every gate, devreap escalates:

1. **First signal**, SIGTERM by default, SIGINT for ffmpeg
2. **Wait** for the grace period, 5 seconds by default
3. **SIGTERM**, if the first signal was SIGINT
4. **Wait** again
5. **SIGKILL**, if it's still running

ffmpeg gets SIGINT first because that signal makes it write MP4 headers correctly. SIGKILL on ffmpeg leaves a corrupted file.

## Safety

devreap optimizes for precision over recall. A missed orphan wastes some memory and annoys you. A killed active process can destroy hours of work. Those costs aren't symmetric, so devreap accepts missed orphans to avoid false kills.

Every default follows from that. Cleanup is opt-in. Weak evidence reports instead of acting. Unreadable metadata protects rather than condemns. Identity gets re-verified against live data before any signal goes out.

devreap will **never** kill:

- PID 1, hardcoded
- Its own process, hardcoded
- Its parent process, hardcoded
- Any process owned by a different user, or any process whose owner it can't read
- Anything on the blocklist, which covers postgres, redis, nginx, sshd, and 20+ other system processes by default

If devreap does kill something it shouldn't have, that's a bug worth reporting. The `signals` field in `devreap logs --json` shows exactly what drove the decision.

## Commands

```
devreap scan                   # One-shot scan, print orphan candidates
devreap scan --json            # Machine-readable JSON output
devreap scan -v                # Show all pattern matches, including safe ones
devreap start                  # Start background daemon
devreap stop                   # Stop daemon
devreap status                 # Daemon status, mode, and current config
devreap install                # Install macOS LaunchAgent (auto-start on login)
devreap uninstall              # Remove LaunchAgent
devreap kill <pid>             # Manually kill a process gracefully
devreap kill --port 3000       # Kill whatever is listening on a port
devreap logs                   # View recent daemon log entries (last 50)
devreap logs -n 100            # Show last N entries
devreap logs --level error     # Filter by severity (debug, info, warn, error)
devreap logs --json            # Raw JSON lines, pipe to jq for filtering
devreap doctor                 # Diagnostics: config, patterns, permissions, MCP configs
devreap patterns               # List all 18 built-in patterns
devreap version                # Print version, commit, and build date
```

## Configuration

devreap runs with no config file. Create `~/.config/devreap/config.yaml` only when you need to change something. Run `devreap status` to see which mode you're in.

```yaml
scan_interval: 30s       # How often to scan. Min: 1s. Max: 24h.
kill_threshold: 0.6      # Minimum score to report a process. Range: 0.1 - 1.0.
                         # A strong signal is still required before any kill.
grace_period: 5s         # Wait between signals (SIGTERM, wait, SIGKILL). Min: 1s.
dry_run: true            # DEFAULT. Logs what would be killed and kills nothing.
                         # Set false only after reviewing `devreap logs`.

notify:
  enabled: true          # macOS notifications when the daemon kills something.

# Signal weights. Each must be 0.0 - 1.0. Specify only what you want to change.
#
# When to tune:
#   False positives while your IDE is open?   Lower parent_ide_dead (e.g. 0.1)
#   Intentional background servers, no TTY?   Lower no_tty (e.g. 0.05)
#   Want long-running processes flagged?      Raise exceeded_duration (e.g. 0.4)
#   Want to rely almost entirely on PPID?     Raise ppid_is_init (e.g. 0.7)
weights:
  ppid_is_init: 0.4       # Parent died (PPID = 1). The only strong signal.
  parent_ide_dead: 0.3    # No IDE running on this machine.
  exceeded_duration: 0.25 # Running longer than the pattern's max_duration.
  no_tty: 0.15            # No controlling terminal.
  # has_listener was removed in 0.2.0. Holding a port is not evidence of
  # abandonment. The key still parses so old config files load, but it is
  # ignored. Ports are still collected and shown in scan output.

# Processes to never kill, by name. Case-insensitive.
# These are ADDED to the built-in protection list (postgres, redis, sshd, etc.)
blocklist:
  - my-database
  - my-background-worker

# Set true to make the blocklist above REPLACE the built-in list instead of
# adding to it. Leave it false unless you know exactly what you're giving up.
# Replacing drops protection for sshd, WindowServer, launchd, and the rest.
replace_builtin_blocklist: false

# Processes to skip even when they score above the threshold.
# Matches against process name and command line. Case-insensitive.
allowlist:
  - my-persistent-mcp-server

# Extra YAML pattern files to load alongside the built-ins.
extra_patterns:
  - ~/.config/devreap/my-patterns.yaml
```

## Built-in patterns

18 patterns across 4 categories. **Max Duration** is how long a process of that type may run before `exceeded_duration` fires. It's a scoring input worth 0.25, not a kill timer.

| Category | Patterns | Max Duration | Signal |
|----------|----------|-------------|--------|
| **MCP servers** | node, python, npx, uvx, docker | 4h | SIGTERM |
| **Dev servers** | Next.js, Vite, Webpack, Expo, CRA, esbuild | 24h | SIGTERM |
| **Headless browsers** | Chrome (headless + remote debugging), Firefox | 2-4h | SIGTERM |
| **Media tools** | ffmpeg, ffprobe, sox, ImageMagick | 30m-2h | SIGINT/SIGTERM |

Run `devreap patterns` for the full list.

## Troubleshooting

**Something got killed that shouldn't have been**

Run `devreap logs --json | tail -20`. The `signals` field shows what triggered it. Then either add it to the `allowlist`, lower the weight of the signal that fired too eagerly, or raise `kill_threshold` to 0.7.

**A process shows as `watch` and I want it gone**

`watch` means the process cleared the score threshold on weak signals alone, and its parent is still alive. devreap won't kill it by design. Use `devreap kill <pid>` when you're sure.

**devreap isn't catching orphans I know exist**

Run `devreap scan -v` to see everything that matched a pattern with its score. If a real orphan sits below the threshold, lower `kill_threshold` or raise the weight of a signal that's firing.

**I want to test what it would kill before letting it run for real**

That's the default. A fresh install logs everything it would kill without killing anything.

**A process isn't matching any pattern**

Run `devreap patterns` to see what's covered. You can add a custom pattern, described in the CONTRIBUTING guide.

**devreap doctor shows a warning**

`devreap doctor` checks config validity, pattern loading, process enumeration, MCP config parsing, and LaunchAgent status. Each warning explains what to do.

## Architecture

Single static binary. No runtime dependencies. Cross-compiles to macOS (arm64/amd64) and Linux.

```
cmd/devreap/main.go     → entry point
internal/
  scanner/              → process enumeration, orphan scoring, MCP cross-referencing
  patterns/             → embedded YAML pattern library, regex matching
  killer/               → signal delivery, identity verification, safety checks
  daemon/               → scan loop, LaunchAgent install/uninstall
  config/               → YAML config loading with validation
  logger/               → structured JSON logging with rotation
  notify/               → macOS notifications
  cli/                  → all commands
```

## License

MIT
