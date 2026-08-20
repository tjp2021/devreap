# Session attribution

devreap used to guess. It scored a process against a handful of circumstantial
signals and hoped the answer was right. On 2026-08-19 a verification pass
measured what that cost: of 733 kill actions in the daemon log, 719 fired with
no direct evidence that the process had lost its parent, and real working
servers were killed 461 times.

Session attribution replaces the guess with a record. When a process is born,
devreap writes down who started it. When that owner exits, devreap writes down
when. Nothing is inferred at read time, so the tool can answer a question it
could never answer before: process 3338 belongs to the agent session that
started at 09:14 in repository X, and that session ended 22 minutes ago.

Attribution is observe-only. It records, it reports, and it takes no action.

## The one safety property

Attribution can only ever take processes out of the kill-eligible set. It can
never put one in.

That's not a promise about intentions. It's the shape of the code. The kill
path asks one question first, on its own: does this process carry the strong
lifecycle signal that devreap already required? If the answer is no, the process
is reported and never killed, and nothing attribution knows can change that.
Every check after that one can only return false.

So the worst case of a wrong attribution is a missed orphan, which costs you
some memory. It's never a wrong kill, which costs you your work.

Two tests hold this down. One drives a gate that confirms every process on the
machine and asserts the eligible set never grows. The other runs the whole
fixture corpus through both paths and asserts the gated set is a subset of the
ungated one.

## How ownership is established

Three channels, in a fixed order of trust.

**Watched ancestry is the whole mechanism.** The watcher polls the process table
every second and compares it against the previous poll. When a process appears,
the watcher records the link between it and its parent. If that parent is
already part of a session, the child joins the same session. The link is a fact
the watcher saw with its own eyes.

This is why devreap works with any harness. Every agent tool spawns children
through the same system calls, so a spawn link looks identical whether the
harness is Claude Code, Codex, an editor extension, or something that ships next
year. No vendor has to cooperate. No environment variable has to exist. A
process attributed this way is marked `observed`, and it stays `observed` after
its entire ancestry exits, because a written record can't be erased by a death.

**Environment markers corroborate and backfill.** Some harnesses publish a
session identifier in the environment of everything they spawn. When one does,
and it names the session the ancestry already named, devreap records that as
supporting evidence. It changes no confidence tier, because the tier was already
the highest one.

Markers also cover the cases where no spawn link exists: the process was born
before the watcher started, or during a sleep, or it was born and orphaned
inside a single poll. In all three the watcher witnessed nothing, so a marker is
the only way to name an owner, and the claim is marked `inferred` rather than
`observed`.

**Process group and terminal corroborate.** They also seed the fallback rule for
a session root nobody recognizes. On their own they aren't enough: descendants
leave the group, and the group leader's identity dies with the leader. A real
server measured during the design pass had parent PID 1, a process group
different from its session's, and no marker at all.

## The confidence ladder

| Tier | What it rests on | What it can do |
|---|---|---|
| `observed` | A chain of spawn links the watcher recorded, reaching a session root | Everything, including the future kill gate |
| `inferred` | A marker or a process group alone | Reported and displayed, never acted on |
| `none` | No channel resolved an owner | Recorded as unattributed, never acted on at any age |

Disagreement resolves toward doing nothing. If a marker names one session and
the witnessed link names another, devreap keeps the witnessed answer, because
that's the fact, and drops the tier to `inferred`, because a disagreement is
never a reason to act. That's also what defeats the obvious abuse: a process
that sets a variable claiming membership in a session about to end can't
manufacture a spawn link nobody saw.

Cold start is the honest cost of preferring witnessed facts. On first install,
after a reboot, and after any store loss, every process already running is
`inferred` at best. It clears as new sessions begin.

Claims only ever go up. A birth record is written once and never modified, so an
upgrade is a separate record written alongside it. Two things trigger one: a
journal replay recovers a parent record that completes a broken chain, or a new
harness descriptor makes an existing ancestor recognizable as a session root.
Nothing lowers a claim. Only a key mismatch invalidates one.

## Process identity

A process is identified by the pair of its identifier and its start time, never
by the identifier alone.

This isn't defensive theatre. On the machine this was designed on, PID 99978 and
PID 117 started one second apart, because the counter passed its maximum and
wrapped back to low numbers inside the same second. Comparing bare identifiers
would have called a brand-new process an old one.

Every lookup fails closed. If either start time can't be read, the comparison
returns no match rather than a guess, and a process whose start time is unknown
can never gate an action.

## Harness recognition

A harness adapter names a session root and says what identity it exposes. The
descriptors live in a data file, so adding a harness is a data change rather
than a release.

| Harness | Root recognition | Session marker | Repo source | Status |
|---|---|---|---|---|
| `claude-code-cli` | Named `claude` or `claude.exe`, or an executable under the native install directory, the npm package, or a `bin/claude` path | `CLAUDE_CODE_SESSION_ID` | Marker, else root cwd | verified-live |
| `codex-cli` | Vendor binary under the npm package platform directory | None in the environment | Root cwd | verified-live |
| `codex-editor-extension` | Agent binary under the editor's user extensions directory | None observed | Root cwd | verified-live |
| `chatgpt-desktop` | Agent binary inside the application bundle resources | None observed | Root cwd | verified-live |
| `vscode-copilot-agent` | Agent process under the editor's plugin helper | Unverified | Root cwd | unverified |
| `cursor` | Application bundle main process, then its plugin helper | Unverified | Root cwd | unverified |
| `windsurf` | Application bundle main process | Unverified | Root cwd | unverified |
| `jetbrains-ai` | A JetBrains application bundle main process | Unverified | Root cwd | unverified |
| `claude-desktop` | Application bundle main process | Unverified | Root cwd | unverified |
| `opencode` | Command-line binary by name | Unverified | Root cwd | unverified |
| `pi` | Command-line binary by name | Unverified | Root cwd | unverified |
| `generic-interactive` | Fallback, see below | None | Root cwd | by construction |

`status` records how the recognition data was obtained. The four `verified-live`
rows were walked on a real machine during the design pass. The `unverified` rows
carry shapes taken from the editor signature list already in this repository,
and their marker columns say unverified rather than guessing. Verifying one is
mechanical: run the harness once, read the environment of a child it spawns,
fill in the column.

Here's the part that matters: an `unverified` row still attributes correctly.
Recognition supplies the label and the session boundary. The ownership link
comes from the spawn link instead.
A harness missing from the table entirely gets full `observed` attribution
through the spawn link, labelled `unknown-harness`.

The generic fallback catches everything else. A session root under it is the
nearest ancestor that satisfies three conditions at once: it leads its own
process group or is an application bundle main process, it isn't a login shell
or a terminal emulator or `launchd`, and it has spawned at least one child of a
tracked class. Its session identifier is derived from its own process key. It's
compiled into the binary, so a missing or broken data file can't disable
recognition.

When one recognized root sits above another, the nearest one wins. An agent
binary running under an editor's extension host belongs to the extension-host
session rather than to the editor bundle that contains it.

### Adding your own descriptors

Point `attribution.adapter_file` at a YAML file shaped like the built-in table:

```yaml
harnesses:
  - name: my-harness
    display: "My harness"
    status: unverified
    parent_kind: terminal
    root:
      names: ["myharness"]
      exe_contains: ["/opt/myharness/"]
    markers:
      session_id_env: "MYHARNESS_SESSION_ID"
      repo_env: "MYHARNESS_PROJECT_DIR"
    repo_source: ["marker", "root_cwd"]
```

A rule matches when any populated field matches. Set `require_all: true` to
require all of them, which is how you express a shape that no single field can
pin down, like a binary named `codex` that only counts when it lives under an
editor extensions directory.

Your file is configuration, not authority, so devreap validates it at load and
refuses three shapes:

1. **A root rule naming a shell, a terminal emulator, a multiplexer, `launchd`,
   process 1, or a blocklisted binary.** Naming your terminal as a session root
   would make every command you run in it look like a session member, and
   closing the window would look like a session ending.
2. **A root rule naming a process supervisor**, including `pm2`, `supervisord`,
   `foreman`, and that class generally. A supervisor's children are supervised,
   not abandoned, and devreap already treats supervision as a reason to leave a
   process alone.
3. **A rule with nothing that discriminates**, meaning one that matches on a
   bare name shared with a system binary, or on a path fragment too short to
   pick anything out. A rule matching everything named `node` matches most of
   your machine.

A rejected descriptor is skipped with a `doctor` finding and the rest of the
file still loads. The built-in descriptors ship with the binary and are reviewed
in this repository, so these rules apply to your file only.

What the validation buys you is bounded, and it's worth saying plainly.
Attribution can never make a process eligible that devreap wouldn't already
permit. Inside that set, a wrong descriptor can still cause a wrong kill. The
blast radius is bounded. It isn't zero.

## Lifecycle

Every tracked process holds one state, and every change is written down with the
trigger and the evidence that caused it.

| State | Meaning |
|---|---|
| `ACTIVE` | The owner is alive, or a live parent holds the process |
| `OWNER_GONE` | An owner exit is recorded and the process is still running |
| `GRACE_PERIOD` | The class window is running down |
| `ORPHAN_CANDIDATE` | The window is spent and confirmations are accumulating |
| `CONFIRMED_ORPHAN` | Every condition holds. Reported, and only ever acted on in a future phase |
| `UNATTRIBUTED` | No ownership channel resolved. Never acted on, at any age |
| `EXITED` | The process is gone |

Three more states exist for the future reclaim path and are unreachable today.

A process reaches `CONFIRMED_ORPHAN` only when all five of these hold at once:
its owner exit is recorded from a trusted source, its awake-time budget for its
class is spent, the confirmation count is reached, its confidence is `observed`,
and the strong lifecycle signal is present on a live read.

Recovery is always available. Every state except the two terminal ones has an
edge back to `ACTIVE`, taken when the process gains a live parent, when its
session root turns up alive, or when a claim upgrade attaches it to a live
session. Adoption has one precise meaning: the current parent is neither 1 nor
absent, and it resolves to a live process whose identifier and start time are
both readable. An unreadable parent isn't adoption, because unknown never counts
as evidence in either direction.

There's a difference between "false" and "couldn't read it", and devreap keeps
them apart. A required condition observed to be false resets the counter and the
budget. A condition that couldn't be read holds both counters in place. That way a
transient read error can't restart a window a real observation already earned.

## Windows count awake time

Every lifecycle window is an awake-time budget rather than a wall-clock deadline. Sleep
pauses it. Sleep never resets it and never invalidates it.

The measured behaviour of a laptop forces this. Averaged over 7 days of the
power log on the machine this was designed on, it entered sleep 123 times a day.
Of 880 dark-wake bursts, the median lasted 11 seconds and the ninetieth
percentile lasted 50 seconds. Only about 4 per cent ran longer than 5 minutes.

So a rule requiring an uninterrupted window would almost never finish a 5 minute
window overnight, and would essentially never finish a 30 minute one. A rule
that reset on any gap is worse: it returns the budget to zero roughly every
minute all night. Either choice makes overnight cleanup useless, and overnight
is exactly when leaked processes pile up.

Confirmation counters work the same way. A sleep between the second and third
confirming scan leaves the counter at two rather than clearing it.

Both values are written into every transition record, so they survive a daemon
restart, a crash, and a reboot. A restart costs you the time actually spent
down rather than the progress already earned.

Gaps are detected by comparing wall time against the monotonic clock, which
stops while the machine sleeps. When they disagree by more than one poll, the
difference is a gap, and the watcher re-enumerates on wake before crediting any
awake time.

## Per-class windows

`lifecycle_grace` is the awake-time budget between a recorded owner exit and
candidacy, keyed by class.

| Class | Window | Why |
|---|---|---|
| `headless-browser` | 2 minutes | Cheap to restart, expensive to keep, never legitimately idle for long |
| `mcp` | 5 minutes | Session restarts reconnect quickly, and longer idles are common |
| `dev-server` | 30 minutes | A running server is doing its job, and you often come back |
| `media` | 30 minutes | A long encode looks idle from outside |
| unclassified | never | Absence of knowledge isn't evidence of abandonment |
| unattributed | never | No owner means no owner death, at any age |

On top of the window, a process needs 3 confirming scans, which at the default
30 second scan interval adds at least another 90 seconds of awake time.

### Merge semantics

Your `lifecycle_grace` map merges with the table above. It never replaces it.

This repository has already paid for the other choice once. A `yaml.Unmarshal`
into a slice overwrote the whole value, so any config containing a `blocklist`
key silently discarded all 26 built-in protections, including the database, the
shell, and the window server entries. The test suite asserted that replacement
as correct while the README promised the opposite. A map with per-class entries
is the same hazard in a new place, so the rule was fixed before the code existed
and a test asserts it, naming that finding as its reason.

Writing `{mcp: 10m}` sets the MCP window to 10 minutes and leaves every other
class exactly where it was. You can't delete a class from the table, and an
unknown class name is a load error rather than a silent addition.

A missing class means never. So does a zero value. Neither ever means
"immediately": absence of a window is absence of permission to act. If you want
a class handled quickly, set a small positive duration. There's no value that
skips the wait.

Two settings sound alike and mean different things. `grace_period` is the
existing key and keeps its meaning, the wait between `SIGTERM` and escalation.
`lifecycle_grace` is the new per-class awake-time budget. The state is still
called `GRACE_PERIOD`, because it names a position in the state machine rather
than a setting.

## What gets recorded, and what never does

Reading another process's environment is the sharpest hazard this feature
introduces, so the rule is strict. The watcher reads the environment in memory,
keeps only the session identifier, the project directory, and the agent name,
and discards everything else before the record leaves the function. The full
environment is never written to the journal, never shown by `top`, and never
logged.

Every foreign-process read passes through one redaction filter before any
consumer sees it. The filter drops every environment value outside the
allowlist, refuses to allowlist any variable whose name announces a secret, and
masks token-shaped and key-shaped command-line arguments along with credentials
embedded in URLs. Its tests are the gate on the reader shipping at all.

The store lives in its own directory at mode 0700, with every file at 0600,
because a birth record holds command lines and repository paths even after
redaction.

## Storage

An append-only journal of newline-delimited JSON, plus a snapshot every 5
minutes, plus an in-memory index rebuilt at start.

There's one writer, one reader, and a fixed set of three queries, so an embedded
database would have added a dependency for value nothing uses at this scale. An
append-only file is crash-safe by construction: a torn final line is discarded
at load and every record before it stands. What would reverse this decision is a
second writer process or ad-hoc historical queries.

The journal rotates at 4 megabyte segments and keeps 8 of them, which is a 32
megabyte ceiling against a measured budget of about 3 megabytes a day.

Retention is set per record type. Birth records for live processes are never
evicted. Birth records for exited processes are kept 7 days. Owner exits,
transitions, claim upgrades, and heartbeats keep an 8 day floor, which gives the
7 day measurement window a day of margin.

Compaction enforces the ceiling and follows a fixed eviction order: exited-process
births first, oldest first, then owner exits past their floor, then heartbeats,
then the audit records. A record belonging to a live process is never evicted.

The ceiling is enforced by compaction rather than absolute, and that distinction
is deliberate. If eviction would have to touch a record still inside its
retention floor, devreap evicts nothing further and raises a `doctor` finding
naming the floor that held. Silently dropping the measurement series would
invalidate the coverage number rather than merely shrink the file, so the
journal goes briefly over the ceiling instead, and the condition clears by
itself once that floor passes.

Compaction runs on a background pass rather than on the append that noticed the
store was full. Rewriting the whole journal inside a one second poll would blow
the performance budget the watcher is built on.

## When things break

Every failure resolves toward leaving processes alone.

**The watcher isn't running.** Every process reads as unattributed and the
scanner behaves exactly as it does today. This is safe by construction, because
attribution can only subtract.

**A poll panics.** Every poll body runs under a recover, so a malformed argument
buffer kills the poll rather than the daemon. Three consecutive panicking polls
stop the watcher, mark the data untrusted, and leave the scanner running.

**The watcher goes quiet.** Three heartbeat intervals of awake time with no
heartbeat marks the data untrusted. Untrusted data freezes the lifecycle: no
state advances toward candidacy and every accumulator pauses, exactly as it
pauses across sleep, because time nobody observed can't be evidence that a
process stayed orphaned. Staleness is measured in awake time everywhere, so an
overnight sleep never reads as a dead watcher.

**The store is corrupt or truncated.** The loader keeps the valid prefix and
discards the unparseable tail. A store with an unrecognized schema version is
ignored entirely rather than guessed at.

**The snapshot and the journal disagree after a crash.** The more restrictive
answer wins. If the snapshot says `ACTIVE` and the journal says
`ORPHAN_CANDIDATE`, devreap resumes at `GRACE_PERIOD` with the recorded budget,
so progress survives and eligibility doesn't. A member whose session root has no
record after recovery is demoted to `UNATTRIBUTED` rather than joined to a
guessed session.

**A PID is reused.** Identity is the pair, so the stored record doesn't match.
The record is invalidated and the new process starts unattributed.

**The environment can't be read.** The claim drops a tier rather than being
guessed at.

**The adapter file is broken.** The built-in descriptors stay in force and
`doctor` reports it. The generic fallback is compiled in, so recognition never
depends on that file parsing.

## Reading the output

```
devreap top
```

Sessions with their harness, repository, owner status, process count, and
resident memory total, then a tree per session. The unattributed bucket is shown
separately, so a coverage gap is visible rather than hidden.

```
devreap top --json
```

The same view, machine-readable.

```
devreap evidence <session>
```

One session as a single JSON document: the spawn tree with keys and link depths,
the birth timings, the owner exit event, and every transition with its trigger.
This is the artifact to attach to a bug report when a harness leaks processes.

```
devreap status
```

Coverage and a count of processes in each lifecycle state.

```
devreap doctor
```

Watcher liveness, heartbeat age in awake time, store size against the ceiling,
snapshot age, schema version, file permissions, and coverage.

## Cost

The steady-state poll is one bulk system call plus a key comparison, measured at
13 to 15 milliseconds for about 800 processes. Full metadata collection, which
is the expensive part, runs only for processes that are new in that poll, and at
an idle birth rate of about 2 new processes per 30 seconds that work is idle most
seconds.

That comes to roughly 15 milliseconds per second, about 1.5 per cent of one
core, with brief spikes when a session starts and spawns a burst of children.
The poll interval is configurable upward, and the measured poll duration is
reported in the heartbeat, so the number is verified rather than assumed.

The exit watches cost effectively nothing: one per session root, and about nine
roots exist on a busy machine. Memory holds one entry per live process plus the
session index, which is kilobytes. Disk grows by roughly 3 megabytes a day.

## Turning it off

```yaml
attribution:
  enabled: false
```

That stops the watcher and leaves the scanner untouched. Deleting the store
directory removes all recorded data with no effect on scanning. Downgrading the
binary is safe, because nothing else reads the store.
