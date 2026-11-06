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

1. **A Go SDK** (`github.com/cristiangirlea/adanext-exec/broker`): Go programs (hrn, `adad`)
   import it and run commands through it directly. Standard library only.
2. **A binary, `adanext-exec`**, that is three things:
   - `adanext-exec run [--profile P] [--approved RULE] -- <command…>`: run one command under the
     broker and exit with its code. Usable from any shell, hook or language. `--approved RULE`
     says a person approved an ask; it counts only when the policy's own answer for the command
     is ask under that same rule, so it can never turn a refusal into a run.
   - `adanext-exec check --json`: a decision only (allow, ask, refuse, with the rule), for
     hooks that must answer before a tool runs.
   - `adanext-exec serve`: the local daemon that holds the shared limits (the queue of heavy
     commands) across all clients.
3. **A JSON protocol** between clients and the daemon, versioned (`broker.ProtocolVersion`),
   documented in `docs/protocol.md` when it lands. Other languages speak it directly.

The daemon is optional for a single client (the SDK enforces per-command limits in-process),
and required for limits shared across agents (the heavy-command queue).

## What it decides: policy

A policy is a JSON file of rules, each matching a command and naming an outcome:

- **allow**: run it.
- **ask**: a person must approve. Where the caller can ask (a Claude Code hook returning "ask"),
  it asks; where it cannot (an hrn run, a daemon client with no person), ask means refuse.
- **refuse**: do not run it, and say which rule refused it.

Rules match on the command line as written and, like harness's guard after #222, on a copy in
which separators inside quotes are read as text, refusing when either matches, so a misread
quote can only add refusals. The first rule set is harness's guard (git push, publishing,
deploys, recursive removal of a root, disk formatting, downloads piped to a shell, sudo,
reading secret files), moved here and kept in step with harness through tests.

Matching is textual and says so: it stops the plausible accident, not a determined adversary.
Real isolation (a VM, a container, a sandbox profile) is outside this project's first scope.

## The command

A command is either an argv, run directly with no shell (hrn and `adad` pass these), or shell
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
- **Memory ceiling** for the command and everything it starts.
- **Output cap** (head and tail kept, the middle counted), as harness's bounded buffer does.

Across commands (daemon):

- **Heavy-command slots.** Commands are classified (`go test`, `go build`, `cargo build`,
  `npm test`, `npm run build`, `gradle`, `make`, test runners; configurable). At most N heavy
  commands run at once on the machine (default: CPU count / 4, at least 1); the rest wait in a
  fair queue, oldest first, with the wait shown to the caller. Light commands never wait.

## How it controls processes, per platform

| Concern | Windows | Linux | macOS |
|---|---|---|---|
| Whole-tree kill | Job Object with KILL_ON_JOB_CLOSE (from harness `internal/tools/exec_windows.go`), the command started suspended and assigned before it runs; never breakaway | New process group (`Setpgid`); kill the group; cgroup v2 when delegated | New process group; kill the group |
| Memory ceiling (hard backstop) | Job Object `JobMemoryLimit`, with its completion-port notice so the command is stopped, not left with failing allocations | cgroup v2 `memory.max` when a delegated cgroup is available | none |
| Memory ceiling (portable, best effort: a short spike between polls can pass) | A watchdog polls the tree's resident memory and kills the tree over the ceiling | the same watchdog | the same watchdog; where it cannot read the tree, the ceiling is reported as not enforced |
| CPU priority for heavy work | Job Object priority class | `nice` / cgroup `cpu.weight` | `nice` |
| Process identity (who holds a slot) | creation time (`GetProcessTimes`) | start ticks (`/proc/<pid>/stat`) | start time from `kern.proc.pid` (to verify with the standard library) |

On Windows the job allows no breakaway (no `JOB_OBJECT_LIMIT_BREAKAWAY_OK` or its silent
form), and jobs nest (Windows 8 and later), so a caller that is itself in a job still contains
its commands. A job that cannot be made or assigned means the command is killed while still
suspended and refused. On Unix the group's leader is waited for without being reaped
(`waitid` with `WNOWAIT`) until the group has been killed, so its id cannot pass to another
process first. macOS refuses a signal to a group of zombies with EPERM; the broker then
reads the process table and counts such a group as gone. A descendant that calls `setsid` or `setpgid` leaves the group (M1b).

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

## Transport and trust

- The daemon listens on a Unix domain socket (Go supports AF_UNIX on Windows 10 1803 and
  later): `<state dir>/adanext-exec.sock`, created owner-only. No TCP listener, no network.
- It runs as the user, never elevated, and refuses to start elevated.
- A client is whoever can open the socket, i.e. the same user. The protocol carries no secrets.

## Integrations, in order

1. **Claude Code**: a PreToolUse hook for Bash and PowerShell calls `adanext-exec check`, and
   rewrites the command to `adanext-exec run -- <command>` through the hook's `updatedInput`,
   so limits and audit apply to Claude Code's own commands. When `check` answers ask, the hook
   answers ask too and writes the rewrite as `adanext-exec run --approved RULE -- <command>`;
   Claude Code runs it only once the person approves. `run` judges the command again; an ask under RULE then runs instead of being
   refused (where no person can be asked, ask means refuse). This rests on the same-user trust
   below: `run` cannot tell who wrote `--approved`, and does not need to, since the flag only
   confirms an ask the policy itself gives.
2. **hrn**: `internal/tools` runs shell commands through the SDK when it is present, and its
   guard rules move here, with harness keeping a thin adapter.
3. **AdaNext OS**: `adad` runs every task command through the SDK; the policy and limits become
   per-project profiles.
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
| M7 | hrn integration | harness's tools use the SDK |
| M8 | AdaNext OS integration | `adad` runs tasks through it |

## Not in scope (for now)

- Sandboxing (seccomp, Landlock, macOS sandbox profiles, containers). A later, separate
  component may add it; Rust may suit that part better than Go.
- Remote execution, or any network listener.
- Approving "ask" decisions from a phone (harness #38 covers the bot side).
