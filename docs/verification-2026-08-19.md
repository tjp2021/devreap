# devreap safety verification, 2026-08-19

Date of verification: 2026-08-19
Repo state at verification: `fb71689` (clean tree)
Running binary at verification: Homebrew `devreap` 0.1.0, from
`/opt/homebrew/Cellar/devreap/0.1.0/bin/devreap`, 4 commits behind the repo.

This file records what an external P0 review claimed, what was checked against
the code and the runtime logs, and which findings were fixed on 2026-08-19.

All 9 P0 claims were checked. All 9 are VERIFIED.

---

## Evidence sources

Two sources, both on the maintainer's machine.

1. The repo at commit `fb71689`.
2. The daemon log at `$HOME/.local/share/devreap/logs/devreap.log`, covering
   2026-02-27 to 2026-08-14.

Counts below come from the log. The commands are recorded so the numbers can be
reproduced.

---

## The 9 P0 claims

### P0-1. Kills by default. VERIFIED

`internal/config/defaults.go:15` set `DefaultDryRun = false`.
`internal/config/config.go:79-83` returns pure defaults when the config file is
missing. No config file existed at `$HOME/.config/devreap/config.yaml`, so the live
daemon ran in kill mode without anyone opting in.

Confirmed live before the fix: `devreap status` reported `Dry-run: false`.

### P0-2. `parent_ide_dead` is global, not ancestral. VERIFIED

`internal/scanner/scorer.go` computed one boolean per scan cycle by asking
whether any IDE process exists anywhere on the machine. The signal never
consulted the scored process's own ancestry. Closing a single editor therefore
made the signal fire for every matched process at once, including processes
started from a terminal that had nothing to do with that editor.

The signature list also had no entry for terminal-launched `claude` and no
entry for Codex. Only the npm-installed Claude Code paths were covered
(`/node_modules/.bin/claude`, `/@anthropic-ai/claude-code`). A Homebrew or bare
command launch was invisible, so the signal fired while the agent was running.

### P0-3. Weak signals are additive and can kill on their own. VERIFIED

Scoring summed every signal and compared the total to `kill_threshold` (0.6).
Nothing required a signal that actually indicates a lost parent.

Measured over the whole log:

```
grep -c '"action":"kill"' devreap.log                      -> 733
grep '"action":"kill"' devreap.log | grep -vc ppid_is_init -> 719
grep '"action":"kill"' devreap.log | grep -c  ppid_is_init -> 14
```

719 of 733 kill actions, 98.1%, fired with no `ppid_is_init` signal at all.
Restricting to kills that succeeded, 493 of 507 had no `ppid_is_init`.

The score distribution shows the same thing:

```
481 "score":0.7    (exceeded_duration + no_tty + parent_ide_dead)
226 "score":0.65   (weak combination)
  9 "score":0.60
  8 "score":0.85
  7 "score":0.75
  2 "score":0.80
```

The two dominant scores, 707 of 733 actions, are weak-signal combinations.

Note on figures: the review reported 716 of 733. The measured value here is
719 of 733. The difference comes from counting method. This file uses all log
lines with `"action":"kill"`, which includes both successful kills and failed
attempts.

### P0-4. `has_listener` adds 0.2 for holding a port. VERIFIED

`internal/scanner/scorer.go:116-119` added `HasListener` (default 0.2) whenever
the process held a listening socket. A server holding a port is doing its job.
The signal charged processes for working.

### P0-5. Unknown metadata failed toward killing. VERIFIED, three ways

gopsutil returns a zero value together with an error for processes it cannot
inspect. `internal/scanner/process.go:68-72` discarded every one of those
errors, so "could not read" became indistinguishable from a real answer.

- Zero `CreateTime`: `process.go:80-83` set `time.Time{}`, and `Age()` returned
  `time.Since(zero)`, roughly 2000 years. `exceeded_duration` fired on every
  scan for any process whose start time could not be read.
- `Terminal()` error: swallowed, leaving `HasTTY = false`, so `no_tty` fired.
- `Username()` error: swallowed, leaving `Username = ""`. The ownership guard at
  `scorer.go:86` only rejected a username that was both non-empty and
  different, so an unreadable owner skipped the guard entirely.

Every ambiguous case resolved toward killing.

### P0-6. Pre-kill identity check was name-only. VERIFIED

`internal/killer/killer.go:38-46` compared only the process name. Every
node-based MCP server on this machine is named `node`, so the check confirmed
almost nothing. A respawned server, or an unrelated process that inherited the
PID, passed it.

### P0-7. No reclassification between scan and kill. VERIFIED

`internal/daemon/daemon.go` scored candidates during the scan and killed them
later in the same cycle without re-reading the process. With a 30 second scan
interval, a kill could act on a snapshot up to a full interval old. Nothing
re-checked whether the conditions still held.

### P0-8. User blocklist replaced the built-in list. VERIFIED

`yaml.Unmarshal` overwrites a whole slice, so any config file containing a
`blocklist:` key discarded all 26 built-in protections, including `postgres`,
`sshd`, `WindowServer` and `launchd`.

The behavior was asserted as correct by the test suite.
`internal/config/config_test.go:361-362` asserted
`len(cfg.Blocklist) != 2` for a config supplying two entries, which encoded
the bug as the expected result. The README at the same time documented the
opposite, promising that user entries "are in addition to the built-in
protection list". The code was wrong, not the docs.

### P0-9. Real false positives on the maintainer's machine. VERIFIED

Successful kills by process class, whole log:

```
a mobile dev server              229
a docs MCP server                 98
a transcript MCP server           45
a Google Workspace MCP server     37
a local MCP server                29
a browser MCP server              23
a browser-automation MCP server    0  (29 kill attempts, all failed)
```

Two supervised agent processes were also killed. Both were
`mcp_stdio_watchdog.py` processes supervising MCP servers under a live agent
gateway, meaning they had a live parent at the time.

Kills span 2026-02-27 to 2026-08-14. The final burst on 2026-08-14 accounts for
74 kill actions, 35 of them successful. That burst includes a kill and respawn
thrash loop: the daemon killed a server, the server was restarted by its
supervisor, and the daemon killed it again on the next scan.

Sample log line, verbatim, showing the weak-signal shape:

```json
{"time":"2026-08-14T17:38:21Z","level":"info","msg":"killed orphan",
 "pid":89867,"process":"node",
 "cmdline":"npm exec @sinco-lab/mcp-youtube-transcript ...",
 "pattern":"node-mcp-server","score":0.7,
 "signals":{"exceeded_duration":0.25,"no_tty":0.15,"parent_ide_dead":0.3},
 "action":"kill"}
```

No `ppid_is_init`. The process had not lost its parent.

---

## Immediate mitigation, 2026-08-19

Before any code changed, the live daemon was forced into observe-only mode.

1. Created `$HOME/.config/devreap/config.yaml` containing `dry_run: true` and
   nothing else. No `blocklist` key was written, because the replacement bug
   in P0-8 was still live in the installed 0.1.0 binary and any user blocklist
   would have dropped the built-in protections.
2. Restarted the daemon once with
   `launchctl kickstart -k gui/$UID/com.devreap.daemon`.

After the restart the daemon reports `Dry-run: true`, with all other settings
still at defaults (18 patterns, threshold 0.60, interval 30s).

---

## Fixes applied, 2026-08-19

Bug tier. Fixed in this pass, one commit each.

1. **Default to observe-only.** `DefaultDryRun = true`. Killing is opt-in.
   `devreap status` prints an explicit Mode line. README documents observe
   first.
2. **Blocklist merges.** User entries are added to the built-in list and
   deduplicated. Explicit replacement moved behind a new
   `replace_builtin_blocklist` option. The test that asserted replacement now
   asserts merge.
3. **`has_listener` removed from scoring.** Ports are still collected and
   reported. The config key still parses so existing files load, and is
   documented as ignored.
4. **Unknown metadata is treated as unknown.** `ProcessInfo` carries
   `CreateTimeKnown`, `TTYKnown` and `UsernameKnown`. `exceeded_duration` and
   `no_tty` require a successful read. A process whose owner cannot be
   established is excluded from kill eligibility. `Age()` returns 0 rather than
   a multi-decade duration when the start time is unknown.
5. **Strong-signal gate.** A process is killable only when a strong lifecycle
   signal fired, meaning `ppid_is_init` today. Weak signals still score and
   still surface the process for reporting, but can no longer terminate it. The
   daemon derives the gate from the signal data rather than a struct field, so
   a caller cannot bypass it. `devreap scan` marks these candidates `watch`
   instead of `KILL`.
6. **Full identity and re-verification before kill.** `killer.Kill` takes an
   `Identity` snapshot and aborts unless PID, name, full cmdline and start time
   all still match. An unreadable field is a verification failure, not a pass.
   A snapshot with no recorded start time is unverifiable and is never killed.
   The daemon also re-reads the live process immediately before signalling and
   abandons the kill unless the strong signal still holds. An error during the
   re-check means do not kill.
7. **Pattern and signature fixes.** The `mcp` token is anchored to a word or
   path-segment boundary, so `mcp-youtube-transcript` and `gws mcp` still match while
   `mcpanel` and `dumpcp` no longer do. Added agent signatures for
   terminal-launched `claude` and for Codex, matched on whole command-line
   arguments so `/usr/local/bin/claude` does not also match
   `/usr/local/bin/claude-helper`.

A regression corpus was added at
`internal/scanner/regression_2026_08_14_test.go`, built from the cmdlines
devreap actually killed. Home directories and project names are anonymized;
the shapes that drove the misclassification are preserved. Each is asserted
non-killable both while an agent is alive and, through the strong-signal gate,
when every weak signal fires.

Verification after the fixes: `go vet ./...` clean, `go test -race ./...` green
across all 9 packages, 173 tests passing, 0 failing.

---

## Deliberately deferred

The review also proposed a redesign. That work is **not** adopted here, and not
because it is wrong. Adopting a large redesign wholesale off a single review
would over-index on one reviewer's model of the problem. The bug tier above
stands on its own: each item is a defect with evidence in the log.

Deferred, each needing its own plan before any code changes:

- Session attribution, tying a process to the session that started it.
- True ancestry tracking, replacing the global `parent_ide_dead` heuristic.
- An explicit process state machine.
- Multi-scan confirmation before action.
- Confidence classes rather than a single summed score.
- A fixture corpus captured from real machines.
- A TUI.

Nothing in that list is scheduled. Nothing in it is authorized.

---

## Open item

The repo is fixed. The running binary is not.

`/opt/homebrew/bin/devreap` is still Homebrew 0.1.0 and now sits well behind
the repo. The `dry_run: true` config file is what is protecting the machine
right now, not the code changes. That protection is real but it is a
configuration, not a fixed binary: anyone who removes or edits that file
returns the machine to kill-by-default behavior on the old code.

Two ways to close the gap, neither performed:

1. `brew upgrade` once a release carrying these fixes is published to the tap.
2. Build from the repo and install over the Homebrew binary.

Option 2 makes the Homebrew-managed install diverge from the formula. Neither
was done in this pass.
