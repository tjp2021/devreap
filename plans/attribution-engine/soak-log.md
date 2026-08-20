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

> Correction appended 2026-08-20: the restart expectation in the paragraph
> above is not supported. The cold start processes are 4 to 6 days old, so a
> restart inside a 7 day window is not a safe assumption. See "cold start
> caveat and the ratified two number rule" below, which supersedes it.

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

## 2026-08-20, cold start caveat and the ratified two number rule

Tim ratified this rule on 2026-08-20, after the window opened and before any
measurement data accumulated. The measurement is therefore pre-registered. No
part of it may be re-tuned once the numbers land.

### The cold start set is permanent, not transient

All 63 pattern-matched processes alive at window open were born before the
watcher. Their ages at the start reading run 4 to 6 days. None of them carries
an observed ownership claim, and none of them can gain one later.

Two facts hold that shut:

- No birth record exists for them. R7a raises a claim to `observed` only when a
  spawn link becomes provable, and a spawn link the watcher never saw cannot
  become provable afterwards.
- The claim upgrade path has no production caller in 0.3.0.
  `Resolver.Upgrade` (`internal/attribution/ownership.go:448`),
  `Engine.OnClaimUpgrade` (`internal/attribution/lifecycle.go:394`), and
  `Store.AppendClaimUpgrade` (`internal/attribution/store.go:235`) are reached
  from `_test.go` files only.

The `upgraded` counter in the heartbeat therefore reads 0 for the whole window.
That zero describes the wiring. It carries no information about the upgrade
mechanism, and it must never be read as "no upgrade was needed".

### The raw 90 percent criterion is unreachable while the cold start set lives

Coverage is `A / (A + N)`. `A` counts pattern-matched processes carrying an
observed claim. `N` counts the pattern-matched processes that do not. The cold
start set pins `N` at 63 until those processes exit.

Solving `A / (A + 63) >= 0.90` gives `A >= 567`. The machine would have to hold
567 observed pattern-matched processes at one moment to clear 90 percent while
the cold start set is still alive. Ordinary use does not reach that number, so
the raw reading measures drain rather than attribution quality.

### The two number rule, ratified

Cohort coverage decides the pass or the failure.

- Denominator: pattern-matched processes whose birth the watcher observed after
  the window opened at `2026-08-20T20:40:10Z`.
- Numerator: the subset of those carrying an `observed` ownership claim.
- Threshold: 90 percent or higher, read as the median of daily medians across
  the 7 days.

Raw coverage is reported beside it and decides nothing.

- Denominator: every pattern-matched process, cold start set included.
- It records how fast the cold start set drains across the window.
- It is evidence only. It never gates the exit criteria.

Every later entry records both numbers. Any pass claim cites the cohort number
and states the raw number next to it.

## DEFERRED until the window closes

Work named here is held on purpose. Changing the binary during measurement
would void the window, so each item waits for `2026-08-27T20:40:10Z`.

- Wire the claim upgrade path to a production caller.
  `Resolver.Upgrade` (`internal/attribution/ownership.go:448`),
  `Engine.OnClaimUpgrade` (`internal/attribution/lifecycle.go:394`), and
  `Store.AppendClaimUpgrade` (`internal/attribution/store.go:235`) are reached
  from tests only. R7a promises the behaviour and 0.3.0 does not deliver it.
  Tim deferred the fix on 2026-08-20. Tracked as issue #12, because a note at
  the foot of an append-only log does not fire by itself.
