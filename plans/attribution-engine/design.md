# System Design: Session attribution and process lifecycle engine

Plan ID: `attribution-engine`
Status: design, not authorized for implementation
Risk tier: 2

## Problem and desired outcome

devreap decides whether a process is abandoned by scoring circumstantial
signals. It has no record of who started the process, so "the owner died" is
inferred from machine-wide facts rather than observed. The verification pass on
2026-08-19 measured the cost of that inference: 719 of 733 kill actions in the
daemon log fired with no `ppid_is_init` signal at all, and real working servers
were killed 461 times.

The strong-signal gate stopped the bleeding. It did not add knowledge. A
process whose parent exited cleanly, and which now reports `PPID 1`, still
carries no evidence of which agent session started it, in which repository, or
when that session ended.

The outcome this design must produce is a durable ownership record written when
a process is born, and a per-process lifecycle that moves on observed events
over time. After it ships, devreap can answer "process 3338 belongs to the
agent session started at 09:14 in repository X, which exited 22 minutes ago"
from recorded facts. A read-only `devreap top` surface presents that answer as
per-session process trees with resident memory totals and owner exit times.

## Users, stakeholders, and operators

The user is a developer running several agent sessions at once on one machine.
They own every process involved, and they carry the entire cost of a wrong
kill.

The operator is the same person. There is no separate operations team, no
fleet, and no remote control plane. The daemon runs as a user LaunchAgent under
the developer's own account, and everything it touches belongs to that account.

The maintainer approves risk decisions for this repository, including whether
attribution may ever gate a kill. The affected third parties are the process
owners of anything devreap observes, which on a personal machine is the same
single user.

No external stakeholder consumes this data. Nothing leaves the machine.

## Current system

The daemon scans every 30 seconds. Each cycle enumerates all processes through
gopsutil, matches them against 18 built-in patterns across four classes, and
scores each match with weighted signals: `ppid_is_init` at 0.40,
`parent_ide_dead` at 0.30, `exceeded_duration` at 0.25, and `no_tty` at 0.15.
Candidates at or above 0.60 are reported.

Four properties of the current system matter here.

First, `parent_ide_dead` asks whether any agent or editor process exists
anywhere on the machine. It never consults the scored process's own ancestry,
so one closed editor makes the signal fire for every matched process at once.

Second, the only strong signal is `ppid_is_init`, which fires after the fact.
It reports that a parent is gone. It cannot report which parent.

Third, unknown metadata now reads as unknown rather than as evidence, and a
process whose owner, start time, or terminal cannot be read is excluded from
kill eligibility. That property is load-bearing and this design preserves it.

Fourth, the default is observe-only, kills are opt-in, and every kill
re-verifies PID, name, command line, and start time against a live read before
signalling.

There is no persistent state. Each scan starts from nothing, so no fact about a
process survives a cycle, a daemon restart, or a reboot.

## Scope and non-goals

In scope: a watcher that records process births with ownership, a durable store
for those records, a lifecycle state machine per tracked process, per-class
delays before a process may be called an orphan, multi-scan confirmation, and a
read-only `devreap top` presentation surface.

Out of scope, stated as commitments rather than omissions.

No new kill heuristics. This design adds no signal that argues for termination.
Attribution may only narrow the existing kill gate, never widen it. A process
that today is not kill-eligible must not become kill-eligible because
attribution data exists.

No change to kill behavior in phase A. The scanner, the scorer, the strong
signal gate, and the observe-only default stay exactly as they are.

Precision over recall. An orphan that devreap fails to attribute costs the user
some memory. A working process that devreap misattributes and kills costs the
user their work. The design accepts a large amount of the first to avoid the
second.

Harness neutrality is a design principle rather than a preference. devreap must
attribute processes from any agent harness, current or future. No attribution
feature may depend on one vendor's cooperation, one vendor's environment
variable, or one vendor's hook. The primary mechanism is a spawn link the
watcher saw with its own eyes, which every harness produces because every
harness uses the same system calls. Vendor markers are enrichment layered on
top of a mechanism that already works without them, and a harness with no
markers at all reaches full attribution confidence.

Personal janitor mode remains the product. This is a single-user tool on one
machine, with no daemon privilege escalation, no system extension, no network
listener, and no telemetry.

Not in scope: Linux and Windows support, cache reclamation, cross-machine
correlation, historical cost accounting beyond resident memory, and any
automatic action against a process whose owner cannot be established.

## Requirements

R1. The watcher records a birth for every process it observes appear, holding
process identity, parent identity, process group, controlling terminal, start
time, class, and the ownership claim with its confidence.

R2. Process identity is the pair of PID and start time. A record whose start
time could not be read is marked unverifiable and can never gate an action.

R3. Ownership is established from watched ancestry first. A process whose birth
the watcher recorded, on a chain of recorded links reaching a session root, is
`observed` regardless of vendor. Environment markers and process group
corroborate an observed claim, and they attribute processes born before the
watcher started. A claim resting on markers alone is `inferred`, which is
reportable and never action-eligible.

R3a. Session roots are recognized from a harness adapter data file. An
unrecognized root falls back to the generic descriptor and carries the label
`unknown-harness`. A missing adapter entry never blocks attribution, because
recognition supplies the label and the session boundary rather than the
ownership link itself.

R4. Owner death is recorded as an event with a timestamp and a source, not
computed at read time.

R5. Every tracked process holds one lifecycle state, and every transition is
written with its trigger and its evidence.

R6. A process may reach `CONFIRMED_ORPHAN` only when all of the following hold:
its owner exit is recorded, its awake-time accumulator has reached
`lifecycle_grace` for its class, the confirmation count has been reached, its
attribution confidence is `observed`, and the existing strong lifecycle signal
is present on a live read.

R7. Recovery is always available. Every state except `EXITED` and `RECLAIMED`
has an edge back to `ACTIVE`, taken when the process gains a live owner or when
a claim upgrade attaches it to a live session.

R7a. A birth record is immutable, and a later claim upgrade record may raise a
claim from `inferred` to `observed` when a spawn link becomes provable. Nothing
may lower a claim except a key mismatch, which invalidates the record outright.
Coverage is measured after upgrades are applied.

R7b. Lifecycle windows and confirmation counters count awake time only. Sleep
pauses them, and never resets or invalidates them. Both values persist in
transition records so they survive a restart.

R8. Unattributed and unclassified processes never expire out of their grace
window. They stay visible and stay ineligible forever.

R9. `devreap top` renders per-session process trees, per-session resident
memory totals, per-process state, and time since owner exit, and it performs no
action.

R10. When the watcher is absent, stale, or unhealthy, the scanner behaves
exactly as it does today.

R11. Evidence export is part of phase A. `devreap top --json` emits the view as
machine-readable output, and `devreap evidence <session>` emits one session's
spawn tree, timings, owner exit, and full transition history as a single JSON
document suitable for attaching to an upstream bug report.

R12. Every path that reads a foreign process's arguments or environment passes
its result through one redaction filter before any consumer sees it. No such
facility exists in the codebase today, so this design builds it.

Acceptance criteria are measurable and are listed in Exit criteria.

## Quality attributes

Safety first. The design fails toward leaving processes alone. Every unknown,
every read error, every gap in observation, and every disagreement between
channels resolves to "do not act".

Attribution coverage is the headline quality number. Coverage is the share of
pattern-matched processes carrying an `observed` ownership claim, counted after
claim upgrades. Below 90 per cent over a week of real use, the feature has not
earned its place.

Watcher cost stays near 1.5 per cent of one core at the default cadence. The
steady-state poll is one bulk `sysctl` call plus a key comparison, measured at
13 to 15 milliseconds for about 800 processes, and full metadata collection
runs only for processes that are new in that poll. The budget rests on the
diff-only path rather than on a whole-machine environment read, and the figures
are set out under Capacity.

Store size stays bounded by rotation with a hard ceiling, and the tool works
correctly with an empty store.

Restart recovery completes in under one second from a snapshot, and correctness
never depends on the store surviving.

## Constraints and assumptions

Verified constraints, each checked on macOS 15.7.3, Darwin 24.6.0.

The Endpoint Security framework requires an Apple-granted entitlement, an
application bundle with a provisioning profile, a user-approved system
extension, and full disk access. A Homebrew-distributed Go binary satisfies
none of that, so the framework is unavailable.

`kqueue` `EVFILT_PROC` attaches per process ID, and a process must already
exist before a watch can attach to it. `NOTE_FORK` reports that a fork
happened, and carries no child PID. `NOTE_TRACK`, the flag that would deliver
the child, does not appear anywhere in this system's `kqueue` manual page, and
current reports say it returns `ENOTSUP` on modern Darwin.

The environment block of another process is readable for the process classes
this tool tracks. Measured live: 40 of 40 `node` processes, 40 of 40 `python3`
processes, and 8 of 8 agent binaries exposed their environment. The reads that
failed were Apple platform binaries such as `cfprefsd`, `trustd`, `secd`, and
`WindowManager`, which are precisely the processes devreap must never touch.

The current process library cannot perform that read. In gopsutil v4.26.1, the
`darwin` build selects the shared BSD implementation, whose
`EnvironWithContext` returns `ErrNotImplementedError`, the error whose text is
"not implemented yet". Verified by reading the module source at
`process/process_bsd.go` lines 63 to 65, under the build constraint
`darwin || freebsd || openbsd`. Every environment read through that library
fails on this platform, without exception. The capability exists at the
operating system level, which is how the measurements above were taken, so this
design builds a small `KERN_PROCARGS2` reader rather than assuming a library
call that does not exist.

The same `sysctl` interface supplies the rest of the birth record. A single
`KERN_PROC_ALL` call returns a `kinfo_proc` array holding the process
identifier, parent identifier, process group, controlling terminal, start time,
and owner for every process at once. Reading those fields per process through
separate library calls would multiply syscalls by the process count for no
benefit.

Session markers exist for some harnesses and not for others, which is why they
cannot be the primary channel. One harness family measured live here exposed
`CLAUDE_CODE_SESSION_ID` on 96 of 755 processes, spanning 9 sessions and 7
project directories, alongside `CLAUDE_PROJECT_DIR`, `CLAUDECODE`,
`CLAUDE_CODE_ENTRYPOINT`, and `AI_AGENT`. A second harness family on the same
machine exposed no session identifier in the environment at all, publishing
only packaging variables, with its session identifier visible on the root
process argv when a session is resumed. Two vendors, two different answers, on
one machine, on one day.

Harness process shapes differ by launch mode, and all three shapes were walked
here. A terminal harness is a direct child of the shell and is its own process
group leader. An editor-hosted harness runs three levels down: the editor
bundle main process starts a plugin helper, and the plugin helper starts an
agent binary from the user's extensions directory. A desktop harness runs
inside an application bundle, and two of its embedded runtime children were
already reparented to PID 1 when observed.

PID reuse is live pressure, not theory. On a machine 6 days into one uptime,
PID 99978 and PID 117 started one second apart, so the counter passed its
maximum and reused low numbers inside the same second.

The machine sleeps constantly. The retained power log holds 1752 sleep and wake
transitions, with maintenance sleep recurring about every twelve minutes
overnight.

Process group inheritance is real but partial. Terminal-launched agent sessions
were process group leaders in every observed case, which is what the closest
competitor relies on. One observed server had parent PID 1, a process group
different from its session's, and no environment marker at all.

Assumptions still to validate. Environment variable names set by agent vendors
are observed behavior and not a published contract, so they can change without
notice, and the design treats that as expected rather than as a defect. Root
recognition for the harnesses not installed here is marked `unverified` in the
adapter table and needs one live run each to confirm. The birth rate sample of
2 new processes in 30 seconds was taken at idle and understates an active day.
A 1 second poll is assumed sufficient, and phase A measures the miss rate
directly.

## System context and dependencies

The system runs entirely inside one user account on one machine. Its trust
boundary is that account. Everything it reads is owned by the same user, and
everything it writes lives under the user's data directory.

Upstream dependencies are the macOS process interfaces already in use through
gopsutil, the `sysctl` calls that library does not expose, and `kqueue` for
owner exit notification. No new external service, no network call, and no
additional third-party module are required. The store carries its own small
rotation rather than reusing the logger's, for the permission and retention
reasons given under Storage.

Downstream consumers are the existing scanner, which may read attribution as an
additional restriction, and the CLI surfaces `top`, `scan`, `status`, and
`doctor`.

An agent-side session-end hook is an optional enrichment input. It supplies an
exact session end time when the user has configured one. It is never a
dependency, because it does not fire when a session is killed, and because it
requires user configuration the tool cannot assume.

## Components and responsibilities

```
   +--------------------------+
   |  harness adapter registry|
   |  data file, hot-loadable |
   |  root recognition rules  |
   |  generic fallback rule   |
   +------------+-------------+
                |  root labels only
                v
   +--------------------------+        +---------------------------+
   |  watcher loop            |        |  owner exit watches       |
   |  1s process table poll   |        |  kqueue EVFILT_PROC       |
   |  diff -> new PIDs        |------->|  NOTE_EXIT per session    |
   |  records spawn links     | attach |  root; ~10 on this host   |
   +------------+-------------+        +-------------+-------------+
                |  birth records                     |  exit events
                v                                    v
   +--------------------------------------------------------------+
   |  attribution store                                            |
   |  append-only journal + periodic snapshot + in-memory index    |
   |  keyed by (pid, start_time); rotation-bounded on disk         |
   +------------+--------------------------------+----------------+
                |  ownership lookups              ^  transitions
                v                                 |
   +--------------------------+        +----------+----------------+
   |  existing scanner        |        |  lifecycle engine         |
   |  patterns + scorer       |------->|  one state per process    |
   |  strong-signal gate      | scans  |  grace windows, confirms  |
   +------------+-------------+        +----------+----------------+
                |                                 |
                v                                 v
   +--------------------------------------------------------------+
   |  CLI surfaces: top (read-only), scan, status, doctor          |
   +--------------------------------------------------------------+
```

The process-arguments reader owns the platform read that the process library
cannot do. It wraps `KERN_PROC_ALL` for the bulk snapshot and `KERN_PROCARGS2`
for the argument and environment block of a single process, parses the packed
buffer, and returns the command line and the environment as slices. It is the
only component that touches foreign process memory, it never retains a full
environment beyond the call, and it hands every result to the redaction filter
before anything else sees it.

The redaction filter owns secret suppression, and it does not exist in the
codebase today. Nothing in the repository currently redacts a command line or
an environment value, so this design builds that facility rather than reusing
one. It applies the variable-name allowlist, drops every value outside it,
masks token-shaped and key-shaped command-line arguments, and is the single
choke point between the reader and every consumer. Its tests are the gate on
the reader shipping at all.

The harness adapter registry owns root recognition. It loads descriptors from a
data file, exposes one lookup that maps a process to a harness name or to the
generic descriptor, and holds no other logic. It is a labelling service, so a
gap in it degrades a label rather than an attribution. It validates every
descriptor at load and rejects the unsafe shapes listed under Threats.

The watcher loop owns birth capture. It polls the process table, diffs against
the previous snapshot, and for each new process reads parent, process group,
terminal, start time, executable path, command line, and environment. It
records the spawn link against its own index, resolves ownership, and hands a
birth record to the store. It never signals a process and holds no kill path in
its code.

The owner exit watches own death detection. The watcher registers one `kqueue`
`NOTE_EXIT` watch per identified session root, roughly nine on this machine
today, and turns a root exit into a recorded event within milliseconds. Poll
absence is the fallback source when a watch could not attach.

The attribution store owns durability. It appends records, maintains the
in-memory index, writes periodic compacted snapshots, enforces retention, and
rejects records it cannot parse. It answers three queries: ownership for a
process key, membership for a session, and lifecycle state for a process key.

The lifecycle engine owns state. It consumes birth records, owner exit events,
and scan results, applies the transition rules, and writes each transition with
its evidence. It computes eligibility and exposes it as a restriction, never as
a reason to act.

The existing scanner keeps its current responsibility unchanged. In phase B it
gains one additional input: a process is kill-eligible only if it is
kill-eligible today and the lifecycle engine also says `CONFIRMED_ORPHAN`.

The CLI surfaces own presentation only.

## State, control flow, and data flow

### Ownership record

The birth record is written once and never modified. It is the source of truth
for who started a process.

```json
{
  "v": 1,
  "type": "birth",
  "observed_at": "2026-08-20T05:24:11.412Z",
  "source": "poll",
  "key": { "pid": 98925, "start_time": "2026-08-19T08:20:54.113Z" },
  "parent_key": { "pid": 98888, "start_time": "2026-08-19T08:20:53.902Z" },
  "pgid": 98888,
  "tty": "ttys000",
  "name": "node",
  "exe": "$HOME/.local/bin/node",
  "cmdline": "node ./server.js --port 7333",
  "class": "mcp",
  "owner": {
    "session_id": "5f1c9a2e",
    "harness": "claude-code-cli",
    "repo": "$HOME/projects/example",
    "root_key": { "pid": 98888, "start_time": "2026-08-19T08:20:53.902Z" },
    "confidence": "observed",
    "channels": ["watched_ancestry", "env", "pgid"],
    "link_depth": 1
  },
  "unverifiable": []
}
```

`unverifiable` lists every field whose read failed. A record naming
`start_time` there is never usable for action. `cmdline` is redacted before it
is written, and the environment itself is never stored.

Ownership resolution runs one primary channel and two supporting ones.

Channel 1 is watched ancestry, and it is the whole mechanism. When the watcher
records a birth, it looks up the parent key in its own index. If the parent is
already a session member, the child joins that session and `link_depth`
increments. If the parent is itself a session root, the child joins at depth 1.
The link is a fact the watcher observed, so it needs no cooperation from the
harness, no environment variable, and no hook. Every harness spawns children
through the same system calls, so every harness is covered identically. A
process attributed this way is `observed`, and it stays `observed` after its
whole ancestry exits, because the record is durable and the parent's death
cannot erase it.

Channel 2 is the inherited environment, which now serves two narrower jobs.
First it corroborates: a marker naming the same session the ancestry chain
already named raises no confidence tier but is recorded in `channels` and shown
in reports. Second it backfills, and backfill covers every case where no spawn
link was recorded, for any reason. That is one condition with three causes: the
process was born before the watcher started, it was born during a sleep gap, or
it was born and orphaned inside a single poll interval. In all three the
watcher witnessed nothing, so a marker is the only way to name an owner, and
the claim is `inferred` rather than `observed`.

Channel 3 is process group with controlling terminal, used only to corroborate
and to seed the generic descriptor. Alone it is insufficient, because
descendants leave the group and because the group leader's identity dies with
the leader.

The confidence ladder has three rungs. `observed` requires a recorded spawn
link chain, and it is the only tier phase B may act on. `inferred` covers
marker-only and group-only claims, which are displayed and never acted on.
`none` means no channel resolved, and the process is recorded as unattributed.

Cold start is the honest cost of this ordering. On first install, after a
reboot, and after any store loss, every existing process is at best `inferred`.
The snapshot preserves `observed` claims across an ordinary daemon restart, so
the cold-start window is a real but rare condition rather than a routine one.

Claims can be upgraded, which recovers part of that cost. A birth record is
immutable, so an upgrade is a separate record rather than an edit.

```json
{
  "v": 1,
  "type": "claim_upgrade",
  "at": "2026-08-20T06:02:44.118Z",
  "key": { "pid": 98925, "start_time": "2026-08-19T08:20:54.113Z" },
  "from": "inferred",
  "to": "observed",
  "reason": "ancestry_chain_resolved",
  "evidence": { "root_key": { "pid": 98888, "start_time": "..." }, "link_depth": 2 }
}
```

An upgrade fires when a spawn link that already existed becomes provable. Two
cases produce that. A journal replay can recover a parent birth record the
snapshot did not hold, completing a chain that was previously broken. An
adapter addition can make an existing ancestor recognizable as a session root,
so a chain of recorded links that ended nowhere now ends at a root. The
direction is one-way: nothing downgrades a claim, and only a key mismatch
invalidates one. Coverage is measured after upgrades are applied, so the
reported number reflects what the engine actually knows.

Birth detection compares two snapshots by key rather than by process
identifier. A process is new when its `(pid, start_time)` pair is absent from
the previous snapshot, and gone when its pair is absent from the current one.
Comparing bare identifiers would call a reused identifier an old process, which
on this machine is not hypothetical. The parent's start time is read live
during the same capture, so the recorded `parent_key` names the parent that
actually existed at that moment rather than whatever later holds the number.

### Harness adapters

A harness adapter names a session root and says what identity it exposes. The
descriptors live in a data file loaded the same way the pattern registry is
loaded today, so adding a harness is a data change rather than a release.

```yaml
harnesses:
  - name: claude-code-cli
    display: "Claude Code, terminal and native install"
    status: verified-live
    parent_kind: terminal
    root:
      names: ["claude", "claude.exe"]
      exe_contains: ["/.local/share/claude/", "/@anthropic-ai/claude-code"]
      exec_paths: ["/opt/homebrew/bin/claude", "/usr/local/bin/claude"]
    markers:
      session_id_env: "CLAUDE_CODE_SESSION_ID"
      repo_env: "CLAUDE_PROJECT_DIR"
      agent_env: "CLAUDECODE"
    repo_source: ["marker", "root_cwd"]
```

Fields are the same for every entry. `status` records how the recognition data
was obtained, and it is one of `verified-live`, `doc-sourced`, or `unverified`.
`parent_kind` is `terminal`, `app_bundle`, `editor_extension_host`, or
`generic`. `markers` is optional and may be empty, which is the point of the
design.

| Harness | Root recognition | Session marker | Repo source | Status |
|---|---|---|---|---|
| `claude-code-cli` | Process named `claude` or `claude.exe`; executable under the native install directory, the npm package, or a `bin/claude` path | `CLAUDE_CODE_SESSION_ID`, with `CLAUDECODE` and `AI_AGENT` present | Marker variable, else root cwd | verified-live |
| `codex-cli` | Vendor binary under the npm package's platform directory, usually with a `node .../bin/codex` wrapper as its parent | None in the environment; a session identifier appears on the root's own argv when a session is resumed | Root cwd | verified-live |
| `codex-editor-extension` | Agent binary under the editor's user extensions directory, whose parent is the plugin helper process | None observed | Root cwd | verified-live |
| `chatgpt-desktop` | Agent binary inside the application bundle resources, parented by the bundle's embedded runtime | None observed | Root cwd | verified-live |
| `vscode-copilot-agent` | Agent process parented by the plugin helper process of the editor bundle | Unverified | Root cwd | unverified |
| `cursor` | Application bundle main process, then its plugin helper | Unverified | Root cwd | unverified |
| `windsurf` | Application bundle main process | Unverified | Root cwd | unverified |
| `jetbrains-ai` | One of the JetBrains application bundle main processes | Unverified | Root cwd | unverified |
| `opencode` | Command-line harness binary by name | Unverified | Root cwd | unverified |
| `pi` | Command-line harness binary by name | Unverified | Root cwd | unverified |
| `claude-desktop` | Application bundle main process, with helper subprocesses as children | Unverified | Root cwd | unverified |
| `generic-interactive` | Fallback, see below | None | Root cwd | by construction |

The four `verified-live` rows were observed on the maintainer's machine during
this design pass. Both Claude Code install shapes are present, the terminal
harness and the native install directory. The editor-extension chain was walked
directly: the editor bundle main process starts a plugin helper, and the plugin
helper starts an agent binary from the user's extensions directory. The desktop
case was observed with two of its embedded runtime children already reparented
to PID 1, which is the leak this tool exists to catch.

The rows marked `unverified` carry root shapes taken from the editor signature
list already in this repository, and their marker columns are recorded as
unverified rather than guessed. Verification is mechanical: run the harness
once, read the environment of a child it spawns, and fill the marker column.
Until then those harnesses still attribute correctly through watched ancestry,
which is the entire point of the inversion.

The generic descriptor catches everything else. A session root under it is the
nearest ancestor that satisfies three conditions at once: it is a process group
leader or an application bundle main process, it is not a login shell, a
terminal emulator, or `launchd`, and it has spawned at least one child of a
tracked class. Its session identifier is its own process key, its repository is
its working directory read once at birth, and its label is `unknown-harness`.
A harness nobody has heard of yet is therefore attributed on the day it ships.

### Owner exit record

```json
{
  "v": 1,
  "type": "owner_exit",
  "at": "2026-08-20T05:31:02.004Z",
  "session_id": "5f1c9a2e",
  "harness": "claude-code-cli",
  "root_key": { "pid": 98888, "start_time": "2026-08-19T08:20:53.902Z" },
  "source": "kqueue_note_exit",
  "members_alive": 11,
  "rss_alive_bytes": 4512345678
}
```

`source` is one of `kqueue_note_exit`, `poll_absent`, or `agent_hook`. Only
`kqueue_note_exit` and `poll_absent` are trusted for eligibility. The hook is
recorded for display and for earlier notification.

### Lifecycle states

| State | Meaning | Action eligible |
|---|---|---|
| `ACTIVE` | Owner alive, or a live parent holds the process | No |
| `OWNER_GONE` | Owner exit recorded, process still alive | No |
| `GRACE_PERIOD` | Waiting out `lifecycle_grace` for the class | No |
| `ORPHAN_CANDIDATE` | Window budget spent, confirmations accumulating | No |
| `CONFIRMED_ORPHAN` | All six conditions of R6 hold | Phase B only |
| `RECLAIM_REQUESTED` | Phase B, kill authorized and re-verifying | Phase B only |
| `RECLAIMED` | Phase B, process terminated | Terminal |
| `RECLAIM_FAILED` | Phase B, kill aborted or refused | No |
| `UNATTRIBUTED` | No ownership channel resolved | Never |
| `EXITED` | Process is gone | Terminal |

Reporting is a display flag on a record rather than a state. `CONFIRMED_ORPHAN`
carries a boolean marking whether the user has already seen it, so a confirmed
process stays in `CONFIRMED_ORPHAN` and remains reachable by the phase B edge.
An earlier draft made `REPORTED` a state, which stranded every confirmed
process in a dead end and made `RECLAIM_REQUESTED` unreachable.

Adoption is an event rather than a state, so a record never rests in an
`ADOPTED` state. Adoption returns the process directly to `ACTIVE`, and the
transition record names adoption as the trigger.

Adoption has one precise meaning. The process gains a live parent when its
current parent process identifier is neither 1 nor absent, and that parent
resolves to a live process whose PID and start time are both readable. An
unreadable parent is not adoption, because unknown never counts as evidence in
either direction. A process also returns to `ACTIVE` when its session root key
is observed alive again, or when a claim upgrade attaches it to a live session.

### Transitions

| From | To | Trigger |
|---|---|---|
| (birth) | `ACTIVE` | Birth record written with a live owner |
| (birth) | `UNATTRIBUTED` | Birth record written with confidence `none` |
| `ACTIVE` | `OWNER_GONE` | Owner exit event recorded |
| `OWNER_GONE` | `GRACE_PERIOD` | First scan after the exit event |
| `OWNER_GONE` | `ACTIVE` | Owner key observed alive and exit source was `poll_absent` |
| `GRACE_PERIOD` | `ORPHAN_CANDIDATE` | Awake-time accumulator reached `lifecycle_grace` for the class |
| `GRACE_PERIOD` | `ACTIVE` | Adoption |
| `ORPHAN_CANDIDATE` | `CONFIRMED_ORPHAN` | Confirmation count reached and R6 satisfied |
| `ORPHAN_CANDIDATE` | `GRACE_PERIOD` | A condition of R6 is observed false; counter and accumulator both reset |
| `ORPHAN_CANDIDATE` | `ACTIVE` | Adoption |
| `CONFIRMED_ORPHAN` | `ACTIVE` | Adoption |
| `CONFIRMED_ORPHAN` | `RECLAIM_REQUESTED` | Phase B only, with the user opt-in set |
| `RECLAIM_REQUESTED` | `RECLAIMED` | Live re-verification passed and the signal succeeded |
| `RECLAIM_REQUESTED` | `RECLAIM_FAILED` | Re-verification failed, or the signal failed |
| `RECLAIM_REQUESTED` | `ACTIVE` | Re-verification observed adoption; the kill is abandoned |
| `RECLAIM_FAILED` | `ORPHAN_CANDIDATE` | Backoff elapsed; the process re-earns confirmations before any retry |
| `RECLAIM_FAILED` | `ACTIVE` | Adoption |
| `UNATTRIBUTED` | `ACTIVE` | A claim upgrade resolved ownership to a live session |
| any | `EXITED` | The process key is no longer present |

Two rules govern this table. Every state except `EXITED` and `RECLAIMED` has an
edge back to `ACTIVE`, which is what R7 requires, and `RECLAIM_FAILED` returns
to candidacy rather than retrying a kill directly, so a failed attempt must
re-earn its confirmations. Retry backoff starts at one minute of awake time and
doubles to a one hour cap.

A confirmation only fails when a required condition is observed to be false. An
unreadable condition is neither a confirmation nor a failure: the counter holds
where it is, and nothing resets. This keeps a transient read error from
restarting a window that a real observation had already earned.

### Class windows

The per-class window is configured as `lifecycle_grace`, keyed by class.

| Class | `lifecycle_grace` | Rationale |
|---|---|---|
| `headless-browser` | 2 minutes | Cheap to restart, expensive to keep, never legitimately idle for long |
| `mcp` | 5 minutes | Session restarts reconnect quickly; longer idles are common |
| `dev-server` | 30 minutes | A running server is doing its job, and a developer often returns |
| `media` | 30 minutes | A long encode looks idle from outside |
| unclassified | never | Absence of knowledge is not evidence of abandonment |
| unattributed | never | No owner means no owner death, at any age |

Confirmation count defaults to 3 confirming scans, which at the 30 second scan
interval adds at least 90 seconds of awake time on top of the window.
Confirmations accumulate across sleep, and they need not be consecutive in wall
clock terms.

### Sleep semantics

Sleep pauses a lifecycle window. It never resets one and never invalidates one.

Every window is an awake-time accumulator rather than a wall-clock deadline.
Each scan adds the awake interval since the previous scan to the accumulator. A
sleep gap contributes zero to the accumulator, and the window resumes from
where it stopped on wake. Candidacy arrives when the accumulator reaches
`lifecycle_grace` for the class, however many sleeps happened along the way.
Confirmation counters behave the same way: a sleep gap between the second and
third confirming scan leaves the counter at two rather than clearing it.

The measured behavior of this machine forces that rule. Averaged over 7 full
days of the retained power log, the machine enters sleep 123 times a day. Of
880 dark-wake bursts in that log, the median awake burst lasts 11 seconds, the
ninetieth percentile lasts 50 seconds, and 95 per cent are shorter than 5
minutes. A rule requiring a gap-free window would therefore never complete the
5 minute window for a server class while the machine is unattended, and would
never complete the 30 minute window at all. A rule that resets on any gap is
worse, because it guarantees the accumulator returns to zero roughly every
minute overnight. Either choice makes overnight reclaim impossible, which is
precisely when leaked processes pile up.

Accumulators are persisted, so they survive a restart. Each transition record
carries the accumulated awake milliseconds and the confirmation count at the
moment it was written, and the engine resumes from the last record rather than
from a wall-clock stamp. A daemon restart, a crash, or a reboot therefore costs
the time actually spent down rather than the progress already earned.

Gap detection compares a wall-clock stamp against a monotonic clock. When the
two disagree by more than one scan interval, the difference is a sleep gap, and
the watcher performs a full re-enumeration on wake before crediting any awake
time.

Two settings sound similar and mean different things, so the names are fixed
here. `grace_period` is the existing key, and it keeps its current meaning as
the wait between `SIGTERM` and escalation. `lifecycle_grace` is the new key,
and it is the per-class awake-time budget between a recorded owner exit and
candidacy. The lifecycle state is still called `GRACE_PERIOD`, because it names
a position in the state machine rather than a configuration key.

### Storage

The store is an append-only journal of newline-delimited JSON, plus a periodic
compacted snapshot, plus an in-memory index rebuilt at start.

This choice beats an embedded database for this workload. There is one writer,
one reader, and a fixed set of three queries. An append-only file is crash-safe
by construction: a torn final line is discarded at load, and every record
before it stands. Adding a database engine would introduce either a cgo
dependency, which complicates the release pipeline, or a pure-Go engine whose
value is unused at this scale. The condition that would reverse this decision
is a second writer process or ad-hoc historical queries beyond the fixed set.

The journal owns its own rotation rather than reusing the logger. The existing
rotation code is bound to the log file's own configuration, size limits, and
file mode, and the store needs different limits and a stricter mode. The store
creates its directory at 0700 and every file at 0600, because a birth record
holds command lines and repository paths even after redaction. Sharing the log
rotation would either loosen those permissions or entangle two retention
policies, so the store gets a small rotation of its own, modelled on the
existing one and tested separately.

Volume control matters more than format. The watcher holds every live process
in memory, because ancestry chains need the full tree, and persists only
records that are session-attributed or pattern-matched. On this machine that is
96 of 755 processes by environment marker alone. The idle sample measured 2 new
processes in 30 seconds, which extrapolates to about 5,800 births a day, and an
active day is budgeted at 25,000.

The daily budget totals about 3 megabytes. Persisted births contribute roughly
2 megabytes, at about 512 bytes a record and a 15 per cent persistence rate.
Heartbeats at one a minute contribute about 0.6 megabytes. Transitions and
owner exit events contribute a few hundred kilobytes, because they are written
per state change rather than per scan. Across the 7 day retention window that
is about 21 megabytes, which fits under the ceiling with headroom for an
unusually busy day.

Retention is 7 days for records of exited processes, unbounded for records of
live processes, and a 32 megabyte hard ceiling enforced by rotation. Snapshots
are written every 5 minutes and on clean shutdown, so a restart replays only
the journal tail.

Snapshot and journal can disagree after a crash, and the rule is to take the
more restrictive answer. When the snapshot shows `ACTIVE` and the journal tail
shows `ORPHAN_CANDIDATE`, the engine adopts `GRACE_PERIOD` with the recorded
accumulator rather than either extreme, so progress is preserved and eligibility
is not. A member record whose session root has no matching record after recovery
is demoted to `UNATTRIBUTED`, never joined to a guessed session.

## Failure modes and degraded behavior

Watcher not running, not installed, or crashed. Every process is unattributed,
so nothing is eligible under attribution gating, and the scanner behaves as it
does today. This is the safe direction by construction, because attribution can
only subtract eligibility.

Watcher and scanner share one process, so their failure modes must not share a
fate. The watcher runs on its own goroutine with its own ticker, and every poll
body is wrapped in a deferred recover, so a panic in parsing a malformed
argument buffer kills the poll rather than the daemon. Three consecutive
panicking polls stop the watcher, mark attribution untrusted, and leave the
scanner running with today's behavior. The scanner never calls into watcher
code on its scan path; it reads the store through a lookup that returns "not
attributed" on any error. Nothing in the watcher holds a lock the scanner
needs, so a stuck watcher cannot stall a scan.

Watcher heartbeat stale. Missing a heartbeat for 3 intervals marks attribution
data untrusted. All states freeze, no state advances toward candidacy, and
phase B gating refuses every process until the watcher recovers.

Store corrupted or truncated. The loader discards the unparseable tail and
keeps the valid prefix. Records lost this way become unattributed. A store with
an unrecognized schema version is ignored entirely rather than guessed at.

Sleep and wake gaps. A gap pauses every lifecycle window and every confirmation
counter, and it neither resets nor invalidates them. Windows count awake time
only, as specified under Sleep semantics, and the accumulators persist in
transition records so a restart during a gap loses nothing. The watcher
performs a full re-enumeration on wake before crediting awake time. Births that
happened during the gap were never witnessed, so they fall to the backfill path
and are `inferred` at best rather than reconstructed.

PID reuse. Identity is the pair of PID and start time, so a reused PID does not
match the stored key. The record is invalidated and the new process starts
unattributed. Any lookup that cannot compare start times fails closed.

Short-lived parent race. A process born and orphaned inside one poll interval
is never seen with a live parent, so the primary channel cannot resolve it.
Backfill still applies: a marker names the owner and the claim is recorded as
`inferred`, which reports and never acts. Without a marker the process stays
unattributed forever, which is the intended outcome.

Watcher started after the processes it observes. Every pre-existing process is
`inferred` at best, so a harness that publishes no markers yields an
unattributed tree until its next session starts. This is the cold-start cost of
preferring witnessed facts, and it clears as new sessions begin.

Adapter entry missing for a harness. Root recognition falls to the generic
descriptor, the label reads `unknown-harness`, and attribution confidence is
unaffected. A labelling gap never becomes an attribution gap.

Adapter data file corrupt or unreadable. The registry keeps the built-in
descriptors and reports the failure through `doctor`. The generic descriptor is
compiled in, so recognition never depends on that file parsing.

Environment read failure. The claim is downgraded rather than guessed.

Forged session marker. Confidence caps at `inferred`, which never gates an
action.

Retries and idempotency. The watcher does not retry a failed read inside a
cycle, because the next cycle re-reads anyway. Writes are append-only and
idempotent by key plus timestamp. There is no unbounded retry anywhere in the
design.

## Threats, privacy, secrets, and abuse

The trust boundary is the user account. Everything inside it is same-user, and
nothing crosses out.

Secrets in environment blocks are the sharpest hazard this design introduces.
Agent children on this machine carry a messaging token variable alongside their
session identifier. The rule is therefore strict: the watcher reads the
environment in memory, extracts only an allowlist of variable names, keeps the
values of only the session identifier, the project directory, and the agent
name, and discards the rest before the record leaves the function. The full
environment is never written to the journal, never shown by `top`, and never
logged.

Command lines carry secrets too. Records and displays reuse the existing
redaction for token-shaped and key-shaped arguments, and the display truncates.

Environment values are untrusted input. Any process can set a variable claiming
membership in a session, and the abuse case is a process that forges membership
in a session about to end, hoping to be reclaimed. Making watched ancestry the
primary channel shrinks this hazard to almost nothing, because a forged marker
cannot manufacture a spawn link the watcher never saw. A forged claim resolves
to `inferred`, which is display-only. Same-user restriction and the built-in
blocklist stay in force underneath.

The adapter data file is user-writable, so it is treated as configuration
rather than as authority. The safety bound it obeys must be stated exactly,
because an earlier draft overstated it.

The true bound: attribution can never make a process eligible that the phase A
path would not already permit. The phase B eligible set is a subset of the
phase A eligible set, and every existing check still applies, meaning the
pattern match, the strong lifecycle signal on a live read, the same-user
restriction, the built-in blocklist, and full identity re-verification
immediately before signalling.

What the bound does not promise: inside that set, a wrong adapter entry can
still cause a wrong kill. Naming a shell or a terminal emulator as a harness
root would make every command run in that terminal look like a session member,
and closing the terminal would then look like a session ending. The processes
affected are ones the phase A path already permits, so the blast radius is
bounded, and it is not zero.

The registry therefore validates every descriptor at load and rejects four
shapes outright. It rejects a root rule naming a shell, a terminal emulator, a
multiplexer, `launchd`, process 1, or any entry on the built-in blocklist. It
rejects a rule that would match an ancestor of another recognized root, because
roots must not nest. It rejects a rule with no discriminating field, meaning
one that would match on a bare name shared with a system binary. It bounds the
ancestry walk at a maximum link depth, 32 by default, beyond which attribution
stops and the process is recorded as unattributed rather than joined to a
distant session. A rejected descriptor is skipped with a `doctor` finding, and
the rest of the file still loads.

The process-arguments reader carries its own risk and is worth naming
separately. It reads another process's argument and environment memory through
`KERN_PROCARGS2`, which is the most sensitive read anywhere in this design. It
runs same-user only, it never writes to a foreign process, it treats the packed
buffer as untrusted input and bounds every length it parses, and it returns
nothing to a caller without passing the redaction filter first. A parse failure
returns an error rather than a partial result, because a half-parsed
environment block is exactly how a secret leaks into a log.

The store is a user-writable file, so it is not an authority. Nothing recorded
in it may authorize an action that a live re-read would not independently
confirm. Phase B keeps the existing full identity re-verification immediately
before signalling, and attribution is an additional check rather than a
substitute.

Least privilege holds throughout. No elevated privileges, no system extension,
no entitlement, no network listener, and no data leaving the machine. Store
files are created with owner-only permissions.

Personal data is present in the incidental sense that repository paths and
command lines reveal the user's own work. It stays local, and retention bounds
it.

## Observability and audit evidence

The heartbeat cadence is decoupled from the poll cadence. The watcher polls
every second and writes a heartbeat every 60 seconds, carrying counters
aggregated over that minute: polls completed, births seen, births persisted,
environment read failures, sleep gap milliseconds, tracked process count,
attributed count, upgraded count, and journal size. Attribution coverage is
derived from the tracked and attributed counters after upgrades are applied.

Writing a heartbeat per poll was the original plan and it does not survive
arithmetic. At roughly 400 bytes a record, one heartbeat a second is about 35
megabytes a day, which alone breaks the 32 megabyte ceiling in under a day and
would push the coverage history out of the store long before a 7 day
measurement could finish. At one a minute the same series costs about 0.6
megabytes a day and 4 megabytes across the full 7 day window, which fits inside
retention with room to spare.

Every state transition is written with its trigger, its evidence, the
confirmation counter, and the accumulated awake milliseconds. A user can
therefore reconstruct why any process reached any state, which is the same
standard the existing per-kill signal logs set.

`devreap doctor` reports watcher liveness, last heartbeat age, store size,
snapshot age, schema version, and coverage. Staleness is judged in awake time,
not wall-clock time, so an overnight sleep does not report a healthy watcher as
dead. The watcher is stale when three heartbeat intervals of awake time pass
with no heartbeat, and a stale watcher is an explicit failure line rather than
silence.

`devreap status` prints coverage and the count of processes in each lifecycle
state.

`devreap top --json` emits the same view as machine-readable output.

`devreap evidence <session>` exports one session as a single JSON document: the
spawn tree with per-process keys and link depths, the birth timings, the owner
exit event, every lifecycle transition with its trigger and evidence, and the
resident memory series. This is the artifact a developer attaches to an
upstream bug report, and the market research named its absence as an unfilled
gap across every tool in this category. The export runs the same redaction
filter as every other output path.

`devreap top` renders the operator view.

```
SESSION   HARNESS          REPO                OWNER            PROCS   RSS
5f1c9a2e  claude-code-cli  ~/projects/example  exited 12m ago      11   4.2G
a71b0c34  codex-cli        ~/projects/tools    alive                6   1.1G
c0d3e4f5  unknown-harness  ~/projects/lab      exited 3m ago        4   0.8G
--        --               --                  unattributed         9   0.7G

  5f1c9a2e  exited 12m ago
    node ./server.js --port 7333        pid 98925   1.4G  ORPHAN_CANDIDATE 2/3
    node ./indexer.js                   pid 98931   0.9G  GRACE_PERIOD 3m left
    chrome --headless                   pid 99010   1.9G  CONFIRMED_ORPHAN
```

The view is read-only in phase A. It sorts by resident memory, groups by
session, shows the unattributed bucket separately so coverage gaps are visible
rather than hidden, and refreshes at the watcher cadence.

Evidence needed to prove correct behavior is the transition journal plus the
heartbeat series. Both are local files, and both are already in the format the
existing log tooling reads.

## Capacity, performance, and cost

Load is one machine with a few hundred to about a thousand processes. Measured
here: 755 processes, 96 carrying session markers, 9 sessions, 7 project
directories.

The watcher polls once a second, and the per-poll cost is the diff, not a full
metadata collection. That distinction carries the budget, so it is stated
precisely rather than borrowed from the earlier measurement.

Each poll does one `KERN_PROC_ALL` call and compares keys. Measured on this
machine at roughly 800 processes: a bare identifier list costs under 1
millisecond, adding start time costs 8 to 9 milliseconds, and adding the parent
identifier costs 13 to 15 milliseconds. The parent identifier and the start
time both arrive inside the same `kinfo_proc` array, so the realistic
steady-state poll is that single call plus a key comparison.

Full metadata collection, meaning the executable path, the command line, and
the environment through `KERN_PROCARGS2`, runs only for processes that are new
in this poll. At the measured idle rate of about 2 births per 30 seconds, that
work is idle most seconds. The earlier figure of 56 to 64 milliseconds measured
a full environment read across every process through the `ps` binary, which is
neither what the watcher does per poll nor a fair basis for the budget, and it
is no longer cited as one.

The resulting steady-state budget is roughly 15 milliseconds per second, which
is about 1.5 per cent of one core, with brief spikes when a session starts and
spawns a burst of children. The poll interval is configurable upward, and
phase A reports the actual poll duration in the heartbeat so the number is
verified rather than assumed.

The `kqueue` watches cost effectively nothing, because there is one per session
root and about nine roots exist here.

Memory holds one entry per live process plus the session index, which is
kilobytes.

Disk grows by roughly 3 megabytes a day, totals about 21 megabytes across the
7 day retention window, and is capped at 32 megabytes by the store's own
rotation. The existing logger keeps its separate 50 megabyte reservation.

There is no monetary cost. Nothing calls a paid service, and nothing leaves the
machine.

## Deployment, migration, compatibility, and rollback

Phase A ships the watcher, the store, the lifecycle engine, and `devreap top`,
all observe-only. The watcher runs inside the existing daemon process rather
than as a second LaunchAgent, so there is one supervised process and one
lifecycle to reason about. Attribution is enabled by default in read-only form
because it cannot act, and it can be disabled with one configuration key.

Phase B adds attribution gating behind an explicit opt-in that is separate from
the existing kill opt-in. Both must be set. The gate can only subtract
eligibility from the set the current scorer already produces, and the property
"the phase B kill set is a subset of the phase A kill set" is asserted by test.

Compatibility. There is no migration, because there is no prior state. An older
binary ignores an unknown store, and a newer binary ignores an unrecognized
schema version. Existing configuration files keep their meaning, the existing
`grace_period` key is untouched, and `lifecycle_grace` is a new key that is
absent by default and falls back to the class table above.

Rollback is one key or one uninstall. Setting the attribution key off stops the
watcher and leaves the scanner untouched. Deleting the store directory removes
all recorded data with no effect on scanning. Downgrading the binary is safe
because nothing else reads the store.

Recovery after a crash replays the snapshot plus the journal tail. If both are
unusable, the tool starts with an empty store and every process is
unattributed, which is the safe state.

## Testing and verification

Unit tests for ownership resolution cover each channel alone, each pair,
disagreement between channels, a forged environment marker, and every read
failure, asserting the resulting confidence value.

A table-driven state machine test drives every transition in the table above,
including each recovery path and the confirmation reset, and asserts that no
input sequence reaches `CONFIRMED_ORPHAN` without all six conditions of R6.

A clock injection test simulates sleep and wake by advancing wall time without
advancing the monotonic clock. It asserts that the gap credits zero awake time,
that the accumulator and the confirmation counter both survive the gap
unchanged, that a full re-enumeration follows the wake, and that a window
spanning many gaps still completes once enough awake time accrues. A companion
test drives the measured overnight profile, meaning repeated 11 second awake
bursts, and asserts a 5 minute window completes rather than stalling forever.

A restart test writes transition records mid-window, discards the in-memory
state, reloads from the journal, and asserts the accumulator and confirmation
count resume at their recorded values rather than at zero.

A recovery test seeds a snapshot and a journal tail that disagree, and asserts
the engine adopts the more restrictive state with the recorded accumulator
preserved, and that a member record with no surviving root is demoted to
`UNATTRIBUTED`.

A claim upgrade test starts a process at `inferred`, later supplies the missing
parent record, and asserts an upgrade record is written, the birth record is
untouched, the claim reads `observed`, and coverage counts it after the upgrade.

A state machine reachability test asserts that `RECLAIM_REQUESTED` is reachable
from `CONFIRMED_ORPHAN`, that every state except `EXITED` and `RECLAIMED` has
an edge back to `ACTIVE`, and that no state is a dead end.

A procargs test parses a captured `KERN_PROCARGS2` buffer, a truncated one, and
one with a corrupt length prefix, asserting a clean error rather than a partial
result on the last two.

An adapter validation test asserts that descriptors naming a shell, a terminal
emulator, `launchd`, or a blocklisted binary are rejected at load, that a
nesting root is rejected, that the rest of the file still loads, and that each
rejection produces a `doctor` finding.

An evidence export test asserts the exported document contains the spawn tree,
the timings, the owner exit, and every transition, and that it passes the
redaction filter.

A PID reuse test reuses a PID with a different start time and asserts the
stored record is invalidated and the process becomes unattributed.

A store test truncates the journal mid-record, asserts the valid prefix loads,
asserts the torn record is dropped, and asserts an unknown schema version is
ignored rather than parsed.

A secret redaction test asserts that no value outside the allowlist reaches the
journal, and that a token-shaped variable never appears in any output.

A degradation test stops the watcher and asserts the eligible set equals
today's eligible set exactly.

A subset test asserts that phase B eligibility is a strict subset of phase A
eligibility across the whole fixture corpus.

A harness neutrality test builds a spawn chain from a root that matches no
adapter entry and publishes no markers, and asserts the descendants reach
`observed` confidence with the label `unknown-harness`. The same test runs with
the adapter data file removed entirely, and asserts an identical result.

A recognition test drives the four verified harness shapes from recorded
fixtures: a terminal root, an editor-extension chain three levels deep, an
application bundle root, and a wrapper plus vendor binary pair. Each asserts
the correct root, session boundary, and label.

A backfill test starts the resolver with an empty spawn index, feeds processes
carrying markers, and asserts every claim is `inferred` and none is
action-eligible.

The fixture corpus grows from the anonymized regression corpus already in the
repository. Each new fixture pairs a real observed process shape with a birth
record and a transition sequence, and home directories and project names stay
anonymized.

Live verification runs on a real machine behind the same opt-in flag the
existing live test uses. It starts a session, records what it spawns, ends the
session, and asserts the recorded owner exit, the state progression, and the
timing against the class window.

Commands are listed in `design.yaml`.

## Human authority and approvals

The maintainer authorizes each phase separately, and phase A authorization does
not carry to phase B.

Enabling attribution gating requires the user to set an explicit configuration
key by hand. No upgrade, no default change, and no automatic behavior may set
it. An install or upgrade never enables killing.

The tool never widens its own authority. The store cannot authorize an action,
a recorded state cannot substitute for a live re-verification, and the
blocklist and same-user restriction stay in force under every path.

Any future change that would let attribution add kill eligibility rather than
subtract it is a new design and a new approval.

## Alternatives and architecture decisions

Decision 1: fast polling with environment capture is the primary birth
mechanism, and `kqueue` `NOTE_EXIT` on session roots is the primary death
mechanism.

The Endpoint Security framework was rejected. It gives exact exec events with
no race, and it requires an Apple-granted entitlement, an application bundle
with a provisioning profile, a user-approved system extension, and full disk
access. A Homebrew-distributed Go binary cannot meet those terms, and asking a
user to approve a system extension for a janitor tool is worse than the problem
it solves.

`kqueue` `EVFILT_PROC` was rejected as the birth mechanism. Watches attach per
process ID and require the process to exist first, which is the wrong ordering
for catching a birth. `NOTE_FORK` reports the fork without the child PID.
`NOTE_TRACK`, which would deliver the child, does not appear in this system's
`kqueue` manual page at all, and current reports say it returns `ENOTSUP` on
modern Darwin. The same filter is excellent for the death half of the problem,
where the PID is known in advance and there are about nine of them.

The residual race is stated plainly. A process born and orphaned inside one
poll interval is never observed with a live parent, so no spawn link exists for
it. Backfill reaches part of that case, because a marker is captured at exec
and outlives the parent, and the resulting claim is `inferred` and display
only. Shrinking the race means shortening the poll interval, which is a cost
knob rather than a correctness fix.

Decision 2: watched ancestry is the primary ownership channel, and vendor
markers are demoted to corroboration and backfill.

The alternative was to lead with environment markers, which is tempting because
one harness family publishes an excellent session identifier and 96 processes
on this machine carry it. It was rejected on three grounds. It is
vendor-dependent, so a harness that publishes nothing is excluded from the
feature entirely, and a second harness family on this same machine publishes
exactly nothing. It is forgeable, because any process can set any variable. It
is fragile, because the variable names are observed behavior rather than a
published contract and can change in any release.

Watched ancestry has none of those properties. A spawn link is a fact the
watcher observed, it is identical across every vendor because every vendor uses
the same system calls, it cannot be forged by the child, and no vendor can
break it by changing a string. It also degrades honestly: the only way to lose
it is to miss the birth, and a missed birth is visible in the heartbeat
counters rather than silent.

Process groups alone are what the closest competitor uses, and the observation
of a real server with parent PID 1 and a foreign process group shows why that
is insufficient. They stay as a corroborator and as the seed for the generic
descriptor.

The consequence that matters most: this ordering removes the vendor-exclusion
problem entirely. A harness with no session variable, no hook, and no name in
the adapter file still receives full `observed` attribution through the spawn
link, labelled `unknown-harness`.

Decision 2a: harness recognition lives in a data file rather than in code, with
a compiled-in generic fallback. Adding a harness becomes a data change instead
of a release, matching how the pattern registry already works. The fallback is
compiled in so that a missing or broken data file cannot disable recognition.

Decision 2b: the design builds its own `KERN_PROCARGS2` reader rather than
calling the process library. This is not a preference. The library's
environment call returns "not implemented yet" for every process on this
platform, verified in its source, so the environment channel would silently
return nothing at all if the design assumed it. The same reader also takes the
bulk snapshot through `KERN_PROC_ALL`, which returns parent, group, terminal,
and start time for every process in one call rather than one call per field per
process.

Decision 2c: lifecycle windows count awake time and pause across sleep, rather
than running on wall-clock deadlines. The measured sleep profile of this
machine leaves no alternative that reclaims anything overnight, and the
reasoning is recorded under Sleep semantics.

Decision 3: an append-only journal with snapshots, rather than an embedded
database. One writer, three fixed queries, and no new dependency. The store
carries its own rotation at mode 0600 rather than reusing the logger's, because
the two need different limits and different permissions. The reversing
condition is named in the storage section.

Decision 4: attribution subtracts eligibility and never adds it. This is the
single property that makes the whole feature safe to ship, because the worst
case of a wrong attribution is a missed orphan.

Decision 5: an agent-side session-end hook stays optional. It gives an exact
end time when present, and it cannot be a dependency, because it does not fire
on a killed session and it requires user configuration.

Consequences accepted: attribution coverage will be incomplete, unattributed
processes accumulate in the display, and the tool will report more than it acts
on. All three are preferable to a wrong kill.

Decision 6, ratified: the per-class lifecycle window is configured as
`lifecycle_grace`. The existing `grace_period` key keeps its current meaning as
the wait between `SIGTERM` and escalation, and neither key is renamed. Two
distinct names remove the ambiguity that a shared name would create.

Decision 7, ratified: phase B stays in the plan as a future step behind its own
explicit opt-in, separate from the existing kill opt-in. It is planned rather
than scheduled, and it needs its own authorization when its time comes.

Decision 8, ratified: the harness adapter file ships as compiled-in built-in
descriptors plus user extras, matching how the pattern registry already loads
built-ins alongside user additions. The generic descriptor stays compiled in,
so a missing or broken user file cannot disable recognition.

## Unresolved questions

Whether `top` is a plain table or an interactive full-screen view is open. The
table needs no new dependency, and the interactive view needs one.

Every other question raised during design has been resolved and recorded as a
ratified decision in the preceding section.

## Implementation and commit sequence

Each step below is independently testable and independently revertible, and no
step changes kill behavior.

1. Redaction filter: the variable-name allowlist, token-shaped argument
   masking, and its tests. Nothing else may read a foreign process until this
   exists, because it is the choke point every later step depends on.
2. Process-arguments reader: `KERN_PROC_ALL` bulk snapshot and
   `KERN_PROCARGS2` single-process reads, bounded parsing, clean errors on
   truncated buffers, and results handed straight to the redaction filter.
3. Harness adapter registry: the descriptor type, the data file loader with
   built-in entries plus user extras, load-time validation with its rejection
   rules, the compiled-in generic descriptor, and recognition tests against the
   four verified shapes.
4. Ownership resolution as a pure function over already-collected process
   fields and a spawn-link index, with the confidence ladder, claim upgrades,
   and the link depth bound, plus its unit tests.
5. Store package: record types, append, load with a torn-tail test, snapshot,
   its own rotation at 0600, retention, recovery disagreement rules, and schema
   version handling.
6. Lifecycle engine as a pure state machine over injected events and an
   injected clock, with the full transition table, the awake-time accumulators,
   and the sleep and restart tests.
7. Watcher loop behind a configuration key that defaults off, writing births,
   spawn links, and 60 second heartbeats only, with panic isolation and no
   consumer wired in.
8. Owner exit watches through `kqueue`, with poll absence as the fallback
   source.
9. Wire the lifecycle engine to scan results, still with no effect on
   eligibility, and add coverage reporting to `status` and `doctor`.
10. `devreap top` as a read-only table view, with `--json` output.
11. `devreap evidence <session>` export.
12. Phase A default on, documented in the README as observe-only.
13. Phase B gating behind a separate opt-in key, with the subset test as the
    gate on the change.

Commit boundaries follow the same numbering, one commit each, each green under
`go vet ./...` and `go test -race ./...` before the next begins.

## Exit criteria

Phase A is complete when all of the following hold on the maintainer's machine
over 7 consecutive days of ordinary use.

Attribution coverage reaches 90 per cent or higher of pattern-matched
processes, counted after claim upgrades are applied. The measurement reads the
60 second heartbeat series, which yields about 10,080 samples across 7 days and
survives retention, and it is reported as the median of daily medians rather
than a single instantaneous reading.

Watcher uptime reaches 99 per cent or higher of awake time, measured as the
share of expected 60 second heartbeats actually present, with sleep gaps
subtracted from the expected count.

The journal stays under the 32 megabyte ceiling across the full 7 days, the
store's own rotation is observed to run at least once, and every store file is
observed at mode 0600.

The measured poll duration reported in the heartbeat stays within the stated
budget, confirming the performance claim rather than assuming it.

At least one real session end produces a recorded owner exit, a correct
per-session tree in `top`, and a state progression that matches the class
window and the confirmation count.

Coverage holds across at least two distinct harness families, one of which
publishes no session marker, proving the mechanism is harness-neutral in
practice rather than only in design. At least one session attributed through
the generic descriptor appears in `top` with the `unknown-harness` label.

Zero changes to kill behavior are observed, confirmed by comparing the eligible
set with and without the watcher running.

The full test suite passes with the race detector, and the degradation test and
the redaction test are both present and green.

Phase B is complete when, in addition, 14 consecutive days of gating in
observe-only mode show the gated eligible set is a strict subset of the
ungated set with no exceptions, and the maintainer sets the opt-in key by hand
after reviewing that record.
