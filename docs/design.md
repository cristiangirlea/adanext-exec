# adanext-exec design

Status: proposed, 2026-10-14. Decisions taken here are recorded in `PLAN.md`'s Decisions log as
they are accepted; this document describes the intended whole.

## Why

Agents run shell commands on the owner's computers: Claude Code through its Bash and PowerShell
tools, Codex and the other CLIs through their own runners, hrn's api loop through
`internal/tools`, and soon AdaNext OS's coordinator (`adad`). Today each has its own partial
controls, or none:

- hrn's guard (a textual allow/refuse check) covers only hrn's own runs.
- Claude Code's permission rules cover only Claude Code, and only match the command's text.
- Nothing limits how many heavy commands run at once. Several agents building and testing in
  parallel on one PC make tests flaky and the machine slow (seen repeatedly in harness).
- Nothing kills a runaway command's whole process tree in every case, caps its memory, or
  records what ran, for whom and how it ended.

adanext-exec is one local gate that every agent's commands go through, on Windows, Linux and
macOS, with one policy, one set of limits and one audit log.

## What it is

1. **A Go SDK** (`github.com/cristiangirlea/adanext-exec/broker`): Go programs can import it and
   run commands through it directly. Standard library only. hrn and `adad` do not: they call the
   binary (M7, M8), which always sets a policy and an audit, keeps the per-process parts (the
   parent-death thread, the console, the cgroup setup that moves its own process) in a process
   of its own, and releases apart from them.
2. **A binary, `adanext-exec`**, that is three things:
   - `adanext-exec run [--profile P] [--approved RULE] -- <command…>`: run one command under the
     broker and exit with its code. Usable from any shell, hook or language. `--approved RULE`
     says a person approved an ask; it counts only when the policy's own answer for the command
     is ask under that same rule, so it can never turn a refusal into a run.
   - `adanext-exec check --json`: a decision only (allow, ask, refuse, with the rule), for
     hooks that must answer before a tool runs.
   - `adanext-exec serve`: the local daemon that holds the shared limits (the queue of heavy
     commands) across all clients.
3. **A JSON protocol** between clients and the binary or the daemon, versioned
   (`broker.ProtocolVersion`), documented in [protocol.md](protocol.md). Other languages speak
   it directly.

`adanext-exec run` (M3a) always sets a policy (`--policy`, `--profile`, the user's
`policy.json`, or the built-in rules) and an audit, so the SDK's opt-in policy and audit are
never left out by the binary. `--approved RULE` answers through `Command.Ask` and says yes only
to an ask under RULE. The binary handles Ctrl+C and a terminate request by cancelling the
command, whose tree is then killed, and refuses to run as root or an elevated administrator.
`adanext-exec check --json` (M3b) answers a JSON request with the decision and its rule,
running nothing. Its exit codes (#12) tell a refusal, a timeout, an interruption, a signal, a program not found,
a command that could not run, and one that ran but whose output, audit line or clean-up failed
apart; [protocol.md](protocol.md) lists them.

The daemon is optional for a single client (the SDK enforces per-command limits in-process),
and required for limits shared across agents (the heavy-command queue).

## What it decides: policy

A policy is a JSON file of rules, each matching a command and naming a decision:

- **allow**: run it.
- **ask**: a person must approve. Where the caller can ask (a Claude Code hook returning "ask"),
  it asks; where it cannot (an hrn run, a daemon client with no person), ask means refuse.
- **refuse**: do not run it, and say which rule refused it.

Rules match on the command line as written and, like harness's guard after #222, on a copy in
which separators inside quotes are read as text, refusing when either matches, so a misread
quote can only add refusals. The first rule set is harness's guard (git push, publishing,
deploys, recursive removal of a root, disk formatting, downloads piped to a shell, sudo,
reading secret files), moved here and kept in step with harness through tests.

The guard (M2a, `internal/guard`) has nine rules, each reported by name, and reports every rule
a line meets, so a policy that lifts one still sees the others:

| Rule | Refuses |
|---|---|
| `git-push` | `git` with `push` anywhere in its segment, or setting a `pushurl` or `pushInsteadOf` |
| `sudo` | `sudo` in any case |
| `rm-root` | `rm -r` of a root: `/`, `/*`, `~`, `$HOME`, `%USERPROFILE%`, a drive (`C:\`, `/c/`) |
| `pipe-to-shell` | a download (`curl`, `wget`, `irm`, `iwr`, PowerShell's web cmdlets, `WebClient.DownloadString`) piped into a shell or `iex`, or evaluated through `iex`, `Invoke-Expression` or a scriptblock |
| `disk` | `mkfs`, `format X:`, `dd of=` a device |
| `gh-publish` | `gh pr merge`, release and repository changes, `gh api` writes (PUT, PATCH, DELETE), endpoints that publish (releases, refs, hooks, keys, secrets, deployments and the like, GET included) and GraphQL mutations |
| `gh-token` | the `gh auth` commands that print the stored login |
| `secret-file` | a word or quoted path naming a secret file: `.env*`, `.dev.vars`, keys and certificates, `.netrc`, `.npmrc`, `.pypirc`, anything under `.ssh`, `.aws`, `.kube`, `.azure` or `.private`, and the agent CLIs' and tools' login files |
| `deploy` | `wrangler deploy`, package publishing, a project's deploy script, `kubectl`, `terraform`, `tofu`, `helm` and `docker push` changes, `vercel --prod`, `fly` and `netlify deploy` |

harness's `guard-env` rule stays in harness: it guards the variables of the environment hrn
builds for its runs (login redirects, its push refusal, its marker), which the broker does not
build. A secret file is recognised by name in any case, with Windows' trailing dots, spaces and
stream names, but not by an 8.3 short name or through a link: the command line is all the guard
sees.

Matching is textual and says so: it stops the plausible accident, not a determined adversary.
Real isolation (a VM, a container, a sandbox profile) is outside this project's first scope.

M2b delivers the policy as `broker.Policy`, read by `ParsePolicy` or `LoadPolicy`:

```json
{
  "version": 1,
  "default": "allow",
  "rules": [
    {"name": "git-push", "decision": "ask"},
    {"name": "no-force", "match": "--force\\b", "decision": "refuse"}
  ],
  "memory_ceiling": "auto"
}
```

- Every built-in rule (the guard's) refuses unless the policy names it with another decision. A
  rule with `match` is the policy's own: a regular expression in Go's syntax, tested like the
  built-in ones against every reading of the command below.
- A command meeting several rules gets the strictest of their decisions (refuse, then ask, then
  allow), and the verdict names the first rule with that decision: the built-in ones in the
  table's order, then the policy's own in its order. So a rule can lift only the built-in it
  names: an `allow` of the policy's own never overrides a built-in refusal.
- `default` decides a command no rule meets; it is `allow` when left out, and `refuse` turns
  the policy into a list of what may run.
- A command is read several ways. The faithful reading is the command as given: shell text as
  written, or an argv as shell text with each argument holding anything but letters, digits and
  `-_./:=@%+,~\` single-quoted, so an argument with a separator in it stays one word.
  `["git", "push"]` reads `git push`, so splitting a line into arguments cannot dodge a rule
  written for shell text. The other readings look for what the faithful one could miss: an argv
  joined plainly with spaces, so a quoted program path (`'/opt/node tools/npm' publish`) is not
  cut off from its arguments, and each reading with separators inside quotes read as text. A
  rule met in any reading counts for refuse and ask; an allow counts only when the faithful
  reading meets it, so `["./tools/check --safe"]`, a program of that name, does not earn an
  allow written for `./tools/check --safe`.
- A built-in rule given allow under a refusing default allows whatever its pattern meets, as the
  patterns were written to refuse broadly; to allow precisely, write a rule of the policy's own.
- A policy that is not valid is an error, never a partial policy: an unknown field or rule name,
  a name used twice, a decision (or a `default`, also `""` or `null`) other than allow, ask or
  refuse, a `memory_ceiling` other than auto, cgroup or watchdog, a match that does not compile,
  or a version other than 1. With the error comes a zero
  `Policy`, which refuses everything, so a caller that misses the error runs nothing.
  `LoadPolicy` reads a regular file of at most 1 MiB, following a link; it opens without
  waiting, so a pipe at the name is refused rather than hanging. On Linux and macOS the file
  must be the user's own and writable by no one else.
- `Command.Policy` makes `Run` judge the command before it starts. A refusal returns a
  `*RefusedError` (`ErrRefused`) naming the rule, and the command does not run.
- An ask goes to `Command.Ask` (M2c, #20), a callback for a caller that can put the command to
  a person, such as hrn in ask mode. It is given the command and the verdict, before the
  command starts: yes runs it, no refuses it (`RefusedError.Declined`), and an error leaves it
  not run. With no `Command.Ask`, an ask refuses. Only an ask is put to a person, never a
  refusal; the time spent asking counts against the caller's context, not the command's
  timeout, and a context that ended while the person answered runs nothing. A zero `Policy` judges nothing and runs nothing (`ErrPolicy`).
  Leaving the policy out of the SDK judges nothing, as leaving out the audit writes nothing;
  the binary always sets one (M3).

## The command

A command is either an argv, run directly with no shell (an SDK caller can pass these), or shell
text with a named shell (Claude Code passes Git Bash or PowerShell text): `sh`, `bash` (Git Bash
on Windows), `powershell` (System32's Windows PowerShell), `pwsh` and `cmd` (System32's
`cmd.exe /d /s /c`). The shell is found in a fixed place or in PATH, and the text reaches it
unchanged. On Windows `bash` passes over WSL's launchers (System32's `bash.exe` and `wsl.exe`
under any name, link or junction, and Store aliases in `WindowsApps`): their Linux processes run
outside any job, so the tree could not be killed.

On Windows an argv naming a batch file (`.bat`, `.cmd`, also with trailing dots or spaces, or
found through PATH) or `cmd.exe` (also through a link) is refused: cmd.exe parses those
arguments again, and they cannot be quoted safely. The same file runs as `cmd` shell text, whose
quoting is the caller's. A copy of cmd.exe under another name is not recognised.

`broker.Run` (M1a) runs one command, waits for it, and kills its whole tree when the timeout
passes or the caller's context ends. When the command's own process exits, whatever it left
running is killed too, so nothing a command starts outlives it. A non-zero exit is a result. An
error with no result means the command did not run: it was invalid, could not start, or could
not be contained. An error with a result comes after it ran: its output could not be written,
was held open from outside its tree, or killing or waiting for it failed.

Run waits for every write of output to return and makes none after it returns; output still
held open by a process outside the tree (on Unix, one that left the group) is abandoned one
second after the tree is dead, and reported. Run does not wait for a read of input that blocks
after the command has ended. On Unix it must be the only one reaping its command: if something
else reaps it (a caller that ignores SIGCHLD or waits for any child), Run reports it and leaves
the group unsignalled, since its id may then belong to another process.

## What it limits

Per command:

- **Timeout**, then a whole-tree kill (below).
- **Memory ceiling** for the command and everything it starts (`Command.MemoryLimit`, `run
  --memory`; M5). A tree over it is stopped and reported (`Result.MemoryExceeded`, exit 122).
  Where a platform cannot enforce it, the command runs without it and the result, the audit
  and the binary say so (`Result.MemoryEnforced` false), never pretending.
  - Windows (M5a): the Job Object's job memory limit caps the committed memory of every process
    in the job together: an allocation past it fails. The job counts the refused amount in its
    peak, so the broker reads the job's peak every 50 ms while the command runs and kills the
    tree once it passes the ceiling, so the command is stopped rather than left running with
    failing allocations; after the command ends, a peak past the ceiling also marks it. A peak
    that cannot be read stops the tree as well, and the result carries the error. The job's
    completion-port notice of the same event is not used, since Windows does not promise to
    deliver it. `Result.PeakMemory` is the job's peak committed memory, refused allocations
    included, so it can stand over the ceiling. A ceiling larger than the platform's SIZE_T is
    refused rather than cut down.
  - Linux and macOS (M5b): a watch reads the resident memory of the whole tree every 100 ms
    (`/proc/<pid>/statm` on Linux, `proc_info`'s task information on macOS): its group, the
    processes that carry its marker (also those that left the group), and their descendants,
    as the kill finds them. Whether a process carries the marker is read once per process, since
    its environment does not change. A sample counts only if, read again after it, its pid still
    names the process scanned, so a pid reused by another program is never counted. The watch
    kills the tree once the total passes the ceiling, and ends after the tree is killed and
    before its leader is reaped, while the group id is still the tree's. It is best effort, and
    says so: a spike shorter than one read can pass, and the peak reported is the most a read
    saw, which can be past the ceiling by what the tree took in one read. A tree process whose
    memory cannot be read (another user's) stops the tree, as the ceiling can no longer be
    watched, and the result carries the error. Where the broker cannot read even its own memory, the ceiling is reported
    as not enforced. On Linux the kernel can hold the ceiling instead (M5c, next).
  - Linux, a delegated cgroup (M5c): the kernel holds the ceiling through cgroup v2's own files,
    with nothing but the standard library. `Command.MemoryCeiling` (`run --memory-ceiling`, or
    the policy's `memory_ceiling`, in that order; auto when none says) chooses: `auto` uses a
    cgroup where one can be made and the watch otherwise, `cgroup` uses a cgroup or does not run
    the command (`ErrNoHardCeiling`, exit 126), and `watchdog` never touches cgroups.
    `Result.MemoryHard` (the audit's `memory_hard`) says whether the kernel held it; Windows'
    job always does, macOS never can.
    - The first command with a ceiling sets up the caller's own cgroup, once per process. It
      must be cgroup v2 (`/sys/fs/cgroup` is one; v1 or a hybrid setup is not), not the root,
      offer the memory controller, hold the caller alone, and let the caller start a child
      straight into it (clone3 with `CLONE_INTO_CGROUP`, Linux 5.7, tried once with a program
      that cannot exist, so only its exec can fail). Then the caller moves itself into a leaf of
      its own, `adanext-exec-broker`, since a cgroup holding processes cannot hand controllers
      to its children, and turns the memory controller on for them. A step that fails puts back
      what the earlier ones changed, and says so when it cannot. A cgroup the broker did not make
      is never removed, whatever its name; what a broker that crashed left goes when systemd
      stops the unit.
    - Each command then gets a cgroup of its own, `adanext-exec-cmd-<random>`, beside that leaf:
      `memory.max` is the ceiling, `memory.swap.max` 0 where swap is accounted, and
      `memory.oom.group` 1, so the kernel's out-of-memory killer stops the whole cgroup at
      once. The command is started straight into it, so nothing it starts is ever outside it,
      and the kill writes `cgroup.kill` (Linux 5.14) before the tree's own kill. An `oom_kill`
      in `memory.events` marks the command over its ceiling (122), and `memory.peak` (Linux
      5.19) is its peak. The cgroup is removed once the command is reaped; one that cannot be
      is reported (exit 120).
    - The watch runs as well, also where the broker cannot read its own memory, and reads where
      each process of the tree is. The same user may move a process out of the command's cgroup
      (into the broker's leaf), where the kernel no longer holds the ceiling for it, and no
      arrangement of the same user's files can forbid it: a delegated cgroup is the user's. The
      watch stops the tree when a read finds a process outside, and the result carries the error
      (exit 120). That is sampled, as the watch is: a process that leaves, allocates and ends
      between two reads (100 ms) is not seen. So hard means held by the kernel for every process
      that stays in the command's cgroup, which a command leaves only on purpose; a Job Object on
      Windows cannot be left at all. The ceiling protects the machine from a runaway command, not
      from one set on escaping it, as the policy does not either (#21).
    - A step that failed and could not be undone (the caller left in the leaf, a cgroup that
      stays) runs the command in no mode, with the error: the state is not what the fallback
      assumes.
    - Which hosts get it: a caller started in a cgroup delegated to its user and holding it
      alone, as a systemd unit with `Delegate=yes` is (a user service, or `systemd-run --user
      --scope -p Delegate=yes adanext-exec run ...`). The serve daemon only hands out slots, so
      the cgroup is always made by the process that runs the command: `adanext-exec run`, or a
      program using the SDK. `run` started by another program into that program's own cgroup
      shares it, and is watched instead under `auto`; a program that starts each `run` into a
      delegated cgroup of its own, as adad will (M8), gets the kernel's ceiling. Containers without a delegated
      cgroup namespace, cgroup v1 and hybrid hosts, kernels before 5.7, and a seccomp filter
      that refuses clone3 get the watch. Moving the caller into the leaf is the one change the
      broker makes to the caller itself.
- **Output cap** (head and tail kept, the middle counted), as harness's bounded buffer does.
  `Command.OutputCap` bounds each stream (stdout and stderr, or one stream when both go to the
  same writer): its first two thirds pass through as the command writes them, the rest is held,
  and once the command has ended the last third is written after a line counting the bytes
  left out (`[... N bytes cut ...]`). `Result.OutputCut` carries the count for the audit.

Across commands (daemon):

- **Heavy-command slots.** At most N heavy commands run at once on the machine (default: CPU
  count / 4, at least 1); light commands never wait. A slot is a lease on the client's
  connection to the daemon (M4a, #9): closing the connection, for any reason, releases it, so
  the daemon never has to tell processes apart. [protocol.md](protocol.md) has the messages.
- **Which commands are heavy (M4c, #15).** Builds, test runners and package installs:
  `go build|test|vet|install|generate`, cargo's builds and tests, `npm`/`pnpm`/`yarn`/`bun`
  installs, tests and build scripts, `gradle`, `mvn`, `make`, `ninja`, `msbuild`, `bazel`,
  `cmake --build`, `dotnet build|test|publish`, `pytest`, `tox`, `nox`, `docker build`, and test
  runners through `npx`. A command is read the way its program reads it: the program by its
  base name without `.exe`, `.cmd` or `.bat`, its global flags skipped with their values, then
  its subcommand, so `go -C dir test` and `npm --prefix web run build` are heavy, and `git grep
  make` or `echo "go test"` are not. Wrappers (`time`, `env`, `sudo`, `nice`, `timeout`,
  `xargs` and the like) and package executors (`npx`, `bunx`, `pnpm exec`, `yarn exec`) are
  read through to the command they run. Shell text is read command by command, split at `&&`,
  `||`, `;`, `|`, parentheses and new lines, with leading `VAR=value` assignments skipped; a
  command substitution (`$( )`, and backticks in sh and bash) is a command of its own, also
  inside double quotes, and the escape character is the shell's (backslash, PowerShell's
  backtick, cmd's caret). It reads ordinary command lines, not every form a shell has. `serve --config
  FILE` adds patterns of its own (Go regular expressions matched against the whole command
  line), or with `"only": true` uses only those; the file is checked as the policy file is.
- **A fair share per client.** A freed slot goes to the waiting client that holds the fewest
  slots, the one that asked first among equals, so one agent's batch of builds cannot keep
  another agent waiting behind all of it. A client is its caller's agent and session, or its
  own connection when it gives neither; the place each waiter is told follows the same order.
- **A build's own parallelism.** A build's load is its own parallelism as much as the count of
  builds, so a granted slot carries hints that give each heavy command its share of the CPUs
  (CPU count / N): `GOFLAGS=-p=K`, `GOMAXPROCS=K`, `MAKEFLAGS=-jK`, `CARGO_BUILD_JOBS=K`. A
  variable the caller set keeps its value.
- **Waiting within a tool's timeout.** A caller with a time limit of its own, such as a Claude
  Code tool call, bounds its wait with `--max-wait` (`Slots.MaxWait`) below that limit, so a
  queued command ends with a clear answer (exit 124) instead of being cut off.

## How it controls processes, per platform

| Concern | Windows | Linux | macOS |
|---|---|---|---|
| Whole-tree kill | Job Object with KILL_ON_JOB_CLOSE (from harness `internal/tools/exec_windows.go`), the command started suspended and assigned before it runs; never breakaway | New process group (`Setpgid`) and a marker in the environment; kill the group, every process carrying the marker, and their descendants; the leader dies with its caller (`Pdeathsig`); cgroup v2 when delegated | The same, without a parent-death signal |
| Memory ceiling (hard backstop) | Job Object `JobMemoryLimit`; the job's peak, read every 50 ms, stops the tree once past it (M5a), so the command is not left with failing allocations | cgroup v2 `memory.max` in a cgroup of the command's own, where the caller holds a delegated cgroup alone (M5c; `--memory-ceiling`) | none |
| Memory ceiling (portable, best effort: a short spike between polls can pass) | (the job's limit is enforced instead) | a watch reads the tree's resident memory every 100 ms and kills the tree past the ceiling (M5b) | the same watch, through `proc_info`; where it cannot read its own memory, the ceiling is reported as not enforced |
| CPU priority for heavy work | Job Object priority class | `nice` / cgroup `cpu.weight` | `nice` |

On Windows the job allows no breakaway (no `JOB_OBJECT_LIMIT_BREAKAWAY_OK` or its silent
form), and jobs nest (Windows 8 and later), so a caller that is itself in a job still contains
its commands. A job that cannot be made or assigned means the command is killed while still
suspended and refused. On Unix the group's leader is waited for without being reaped
(`waitid` with `WNOWAIT`) until the group has been killed, so its id cannot pass to another
process first. macOS refuses a signal to a group of zombies with EPERM; the broker then
reads the process table and counts such a group as gone. A process that is exiting
keeps its running state there until it is a zombie; its `P_WEXIT` flag marks it, and it counts
as gone too. It reads the table twice and counts the group gone
only if neither read shows a live member. macOS has no atomic listing, so this narrows but does
not close one race: a member the user cannot signal (another user's, as under `sudo`) that forks
and exits during both reads could hide a live child, and the refusal would go unreported. Such
a process could not have been killed either way.

A descendant that calls `setsid` or `setpgid` leaves the group (M1b). Each tree therefore also
carries a marker, an environment variable whose name holds the tree's random id
(`ADANEXT_EXEC_TREE_<id>=1`), which every descendant inherits; nested brokers add their own
beside it. To kill the tree, the broker reads the process table (`/proc` on Linux,
`kern.proc.all` and `KERN_PROCARGS2` on macOS, which reads each candidate's arguments and
environment into a buffer of `kern.argmax` bytes, about 1 MiB; only processes that started after
the command are read) and kills the group, every process of the user's
that carries the marker, and every descendant of either by its parent link, scanning again until
a scan finds none. Nothing that started before the tree's bound is taken (the command's own
start time, or a second before the command was started), and a parent link is followed only to a
child no older than its parent. On Linux a process is pinned
with a pidfd and its start time checked before the signal (before Linux 5.3, without pidfds,
the check comes just before the signal); on macOS the table is read again and the start time
checked just before the signal, which leaves a short window for a reused pid. The bound on
start times comes from the clock just before the command starts, a second early, and narrows to
the command's own start time when that can be read (a caller that ignores SIGCHLD can have it
reaped first). Only if neither can be read is only the group killed, and the error says so. On
macOS start times follow the wall clock, which has no monotonic per-process alternative there:
a clock stepped back by more than a second after the bound is taken can make the tree's later
processes look older than it, and the sweep then misses those that left the group. Linux counts
from boot and is not affected. The marker shows
which tree a process came from; it is not proof: a process the user starts after the command
and gives the marker on purpose is killed with the tree. What still escapes: a
process that both clears its environment and is orphaned before the kill (a double-forking
daemon started with `env -i`), one the user cannot read (setuid, or made undumpable), and work
handed to another service (docker, `systemd-run`, launchd), which was never the command's child.
Output such a process holds open is reported, not waited for. A per-user subreaper
(`PR_SET_CHILD_SUBREAPER`) was weighed and left out: it changes the whole caller (an SDK user),
whose other children's orphans would then become zombies the broker could not tell apart.

When the caller itself is killed before it can kill the tree (#11): on Windows the job closes
with the caller and Windows kills the whole tree. On Linux the command's own process gets SIGKILL
as its parent-death signal (set from a thread kept alive until the tree is closed, since Linux
sends it when the starting thread ends); its descendants stay. macOS has no parent-death signal,
so the whole tree stays.

The console's Ctrl+C reaches the command and its caller alike. The SDK does not change console
groups: a caller that must report after Ctrl+C handles the interrupt and cancels the context,
and Run kills the tree and returns; `adanext-exec run` (M3) does this.

Everything goes through `syscall` / `syscall.NewLazyDLL`, never `golang.org/x/sys`. Where a
platform cannot enforce a limit, the result says so instead of pretending. `RLIMIT_AS` is not
used as a ceiling: it limits each process's virtual address space, not the tree's memory, the
Go toolchain's reservations fail under a modest one, and macOS does not enforce it.

## Audit

One JSON line per command in `<state dir>/audit/YYYY-MM-DD.jsonl`: time, caller (agent,
session, run id when given), working directory, the command as run, the decision and its rule,
queue wait, duration, exit code or signal, peak memory where the platform reports it, and
whether output was cut. Secrets are redacted with the same rules as the policy's secret list
before anything is written. Nothing leaves the machine.

M1d delivers the line through `broker.Command.Audit`, with the fields there are so far (the
queue wait and peak memory arrive with M4 and M5): `time` (the start, UTC), `caller`, `dir`,
`args` or `shell` and `script`, `outcome` (exited, signalled, timed out, cancelled, over its
memory ceiling, refused, not run), `decision` and `rule` (M2b: the policy's verdict, when there is a policy, and the rule
that gave it), `approved` (M2c: a person's answer, when one was asked), `queue_wait_ms` (M4b:
the wait for a heavy-command slot, when there was one), `memory_limit`, `memory_enforced` and
`peak_memory` (M5a: the ceiling asked for, whether it was enforced, and the tree's peak where
known), `memory_hard` (M5c: whether the kernel held it), `exit_code`, `signal`, `duration_ms`, `output_cut` and `error`. A command
Run did not run gets its line too, with the reason; a command whose line cannot be opened is
not run. The date is the start's in UTC; `DefaultAuditDir` is the user's state directory
(`$XDG_STATE_HOME` or `~/.local/state` on Linux, `~/Library/Application Support` on macOS,
`%LocalAppData%` on Windows) under `adanext-exec/audit`. Each line is one append, so several
processes can share a file.

Redaction is best effort (#14). It takes out what it recognises: `NAME=value` pairs whose name
looks secret, credential flags (also when the value is the next argument), auth headers, URL
passwords, known token formats and private keys (the rules come from harness), and, wherever
they appear, the values of variables in the command's environment whose names hold KEY, TOKEN,
SECRET, PASSWORD, PASSWD or CREDENTIAL or end in `_PWD` or `_PASS`, when the value is 12
characters or longer (shorter ones would blank ordinary words). The environment is the one the
command starts with. A secret of another shape, or a short one passed on its own, is written as
given.

The audit file must be a regular file: a link or a special file (a device, a pipe) at its name
is refused, and so is the command. On Linux and macOS the directory and the file must be the
user's own, and any access for the group or others is taken away before the command runs. The
redaction rules are the audit's own: the policy's secret list names files, not the shapes of
secret values.

What the audit cannot promise (#13, #14):

- The environment passes through to the command unchanged and is never written to the audit.
- The working directory is recorded, not confined: a command can read and write anywhere the
  user can.
- The caller is whatever the caller says it is; nothing checks it.
- The audit lives in the user's own directory, owner-only where the platform has file modes
  (not on Windows, where it takes the directory's access list). The same user can change or
  delete it: it is a record, not tamper-evident.

## Transport and trust

- The daemon listens on a Unix domain socket (Go supports AF_UNIX on Windows 10 1803 and
  later): `<state dir>/adanext-exec/daemon/daemon.sock` (`broker.DefaultSocket`), in a directory of its
  own beside the audit's.
  No TCP listener, no network.
- The socket's directory must be the user's own, and the daemon makes it owner-only: 0700 on
  Linux and macOS (and the socket 0600), and on Windows a protected access list for the user,
  SYSTEM and Administrators only. Go sets no access list on a Windows socket file and Windows
  has no owner-only mode for it, so there the socket is exactly as private as that directory
  (#17). This holds for a `--socket` given elsewhere too.
- A client sends its command only to a socket the user owns, so a socket another account
  planted never learns what runs.
- The daemon does not ask who connects. Linux could tell it (`SO_PEERCRED`), Windows cannot
  (AF_UNIX there has no peer credentials), and the directory already keeps other users out, so
  a client is whoever can open the socket: the same user. The protocol carries no secrets.
- It runs as the user, never elevated, and refuses to start elevated.

**Its lifecycle (M4b, #16).** Nothing starts the daemon on its own: the user runs
`adanext-exec serve`, from a terminal or a login item, and it runs until stopped. It takes an
exclusive lock on `daemon.sock.lock` beside the socket for its whole life (`flock` on Linux and
macOS, `LockFileEx` on Windows; the lock goes with the process, however it ends), so a second
daemon refuses to start. Only under that lock does it remove a socket left by a daemon that
ended, and only one that is a socket, the user's, and refuses a connection; anything else at the
name (a file, another user's socket, one that fails some other way) is left alone and refused. A client and a daemon of different
protocol versions do not talk: the daemon answers an `error`, and the client's command does not
run. A client that asks for a slot (`Command.Slots`, `run --slots`) and finds no daemon does
not run its command (`ErrNoDaemon`, exit 123): it fails closed instead of running without the
shared limit it asked for. A client that does not ask runs with no shared limits, as before.
A client watches its slot while its command runs: if the daemon ends or closes the connection,
the command is killed with its tree, or never starts (`ErrSlotLost`, exit 123), so a restarted
daemon never hands out a slot that a command from before is still using. The daemon bounds
every write to a client, and ends every connection when it stops, so a client that stops
reading or holds a slot cannot stall the queue or the daemon's own exit.

The policy file, the socket and the audit all belong to the user the agents run as, so an
agent that means to can change the policy, talk to the daemon, or edit the audit (#21). On
Linux and macOS the broker refuses a policy others can write and keeps its files owner-only; on
Windows it relies on the access lists of the user's own directories. Either way that stops
another user, not the same one. The gate as a whole stops accidents and keeps an honest agent within
bounds; it does not contain a hostile one running as the same user. That needs a separate user
or a sandbox, outside this project's first scope.

## Integrations, in order

1. **Claude Code**: a PreToolUse hook for Bash and PowerShell, the binary's own
   `adanext-exec hook claude-code` (M6a), judges the command as `check` does and rewrites it to
   `adanext-exec run --shell bash|pwsh --script-b64 <text>` through the hook's `updatedInput`,
   so limits and audit apply to Claude Code's own commands. The text travels as base64, so the
   tool's shell runs the rewrite without reading the command, and `run` hands it to the same
   shell unchanged. When the policy answers ask, the hook answers ask too, shows the person the
   command, and writes the rewrite with `--approved=RULE`; Claude Code runs it only once the
   person approves. A command that passes `--approved` or `--script-b64` itself is denied, so an
   agent cannot approve its own ask or hide a script from the hook by going through it. The
   user's own Claude Code deny and ask rules count on the original text, read from the settings
   files Claude Code reads (M6b), since the rewrite hides that text from Claude Code's matching.
   The rewrite names its build, and `run` refuses another build's. [claude-code.md](claude-code.md)
   is the install guide. `run` judges the command again; an ask under RULE then runs instead of being
   refused (where no person can be asked, ask means refuse). This rests on the same-user trust
   below: `run` cannot tell who wrote `--approved`, and does not need to, since the flag only
   confirms an ask the policy itself gives.
2. **hrn**: `internal/tools` runs shell commands through `adanext-exec run` when the gate is on,
   after a handshake (an `adanext-exec check --json` request whose answer must carry
   `adanext-exec/1`), and its guard rules move here, with harness keeping a thin adapter.
   harness never imports this module, and stays standard library only.
3. **AdaNext OS**: `adad` runs every task command through `adanext-exec run`, each in a cgroup
   of its own where it holds a delegated one, so the kernel holds the ceiling; the policy and
   limits become per-project profiles. A small dependency-free client for this comes with M8, as
   a module of its own (`client/`, with its own `go.mod`), so adad requires only that, never the
   broker; it also removes each run's cgroup once `run` has exited, since `run` cannot remove the
   cgroup it is in.
4. **Codex and others**: through `adanext-exec run` as their shell, where they allow one.

## Milestones

| | Delivers | Proves |
|---|---|---|
| M0 | This repository: AI setup, CI on GitHub's runners, the design, the SDK's first package | `/check` and CI green |
| M1 | `broker.Run`: one command with a timeout, a whole-tree kill, an output cap and an audit line, on all three platforms | A grandchild process dies with its parent on Windows, Linux and macOS, in CI on all three |
| M2 | Policy: rules, decisions, harness's guard rule set with its tests | Every harness guard test passes against adanext-exec |
| M3 | `adanext-exec run` and `check`, the JSON protocol v1 | A Claude Code hook can call it |
| M4 | The daemon and heavy-command slots | Two clients' builds queue instead of running together |
| M5 | Memory ceilings | A command over its ceiling is stopped and reported, or reported as not enforced where the platform cannot, per platform |
| M6 | Claude Code integration (hook, docs) | The owner's sessions run through it |
| M7 | hrn integration | harness's tools run through `adanext-exec run` |
| M8 | AdaNext OS integration | `adad` runs tasks through `adanext-exec run` |

## Not in scope (for now)

- Sandboxing (seccomp, Landlock, macOS sandbox profiles, containers). A later, separate
  component may add it; Rust may suit that part better than Go.
- Remote execution, or any network listener.
- Approving "ask" decisions from a phone (harness #38 covers the bot side).
