# Attribution engine soak log

Append-only. Each entry records one observation of the proving run against the
exit criteria in `design.yaml`.

## 2026-08-20, proving run starts

The 7 day measurement window opens now.

- Start: `2026-08-20T20:40:10Z`
- Window closes: `2026-08-27T20:40:10Z`
- Binary: devreap 0.3.0, commit `07344a13a00f40225496dbc83c051894cb72a9f3`,
  installed from the Homebrew tap at `/opt/homebrew/Cellar/devreap/0.3.0/bin/devreap`
- Daemon: `com.devreap.daemon`, PID 98329
- Mode: observe-only. `dry_run` is true and `attribution.gate_kills` is off, so
  attribution records and reports and never gates a kill.
- Store: `~/.local/share/devreap/attribution`, directory at 0700 and journal at 0600

The watcher came up clean. The first heartbeat landed 60 seconds after start with
`polls=61`, `poll_duration_us=9300`, and `sleep_gap_ms=0`, so the measured poll
sits well inside the performance budget the design claims.

Ownership resolution works on real processes. A process born after the watcher
started resolves through `watched_ancestry` at `observed` confidence and carries
the correct session root. Processes that were already running when the watcher
started resolve through the weaker channels or not at all, which is the
documented cold-start behaviour.

Coverage reads 0 percent of 63 pattern-matched processes at start, and `doctor`
raises it as a finding. That number is expected here rather than a fault. Every
pattern-matched process alive at start predates the store, so none of them can
carry an observed spawn link. Coverage climbs as those processes restart across
the window, and the criterion is the median of daily medians over 7 days rather
than any single reading.

### What this window has to produce

- Coverage at 90 percent or higher of pattern-matched processes, counted after
  claim upgrades, taken as the median of daily medians
- Watcher uptime at 99 percent or higher of awake time, with sleep gaps
  subtracted from the expected heartbeat count
- The journal under the 32 megabyte ceiling, with any transient exceedance
  carrying a `doctor` finding that names the floor that held and clears itself
- Poll duration inside the stated budget across the series
- At least one real session end with a recorded owner exit, a correct tree in
  `top`, and a state progression matching its class window
- Coverage across two harness families, one publishing no session marker, with
  at least one session in `top` labelled `unknown-harness`
- No change to kill behaviour, proven by comparing the eligible set with the
  watcher running and stopped
