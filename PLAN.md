# adanext-exec plan

The work queue, milestone by milestone, and the decisions log. `docs/design.md` describes the
whole; each milestone below is one or more issues, each a branch and a pull request.

## Milestones

- [x] **M0: the repository.** AI setup, CI on GitHub's hosted runners, the design, the SDK's first
  package (`broker.ProtocolVersion`). Done: PR #3, 2026-10-14. Its review's design concerns are
  issues #4 to #22, each naming its milestone.
- [x] **M1: one command, contained.** `broker.Run` with a timeout, a whole-tree kill, an output
  cap and an audit line, on Windows (Job Objects), Linux and macOS (process groups). Done when
  M1a to M1d are. Done: PRs #27, #28, #30, #35, 2026-11-06.
  - [x] **M1a: run and kill the tree.** `broker.Run` takes argv or shell text with a named shell
    (#4), stops at a timeout or a cancelled context, and kills the whole tree, also what is left
    when the command exits: a Job Object on Windows (from harness), a process group on Linux and
    macOS. Refuses batch files as argv on Windows (#18); the job never allows breakaway and
    nests (#19). Done: PR #27, 2026-11-06.
  - [x] **M1b: what leaves the group on Unix.** Kill descendants that called `setsid` (#10) and
    the group when the caller dies (#11), or say where it cannot. Done: PR #28, 2026-11-06.
  - [x] **M1c: the output cap.** Head and tail kept, the middle counted, as harness's bounded
    buffer does. Done: PR #30, 2026-11-06.
  - [x] **M1d: the audit line.** One JSON line per command, secrets redacted by pattern (#14),
    and what the audit and the broker do not cover said in the design (#13). The patterns are
    the audit's own until M2's policy brings its secret list, which then shares them. Done:
    PR #35, 2026-11-06.
- [x] **M2: policy.** Rules, decisions (allow, ask, refuse) and harness's guard rule set with its
  tests. Done when M2a to M2c are.
  - [x] **M2a: the guard** (#36). harness's command guard in `internal/guard`, with its tests.
    Done: PR #39, 2026-11-06.
  - [x] **M2b: the policy** (#37). The JSON rules, the decisions, `Run` keeping to them, the
    audit outcome, and the trust statement (#21). Done: PR #40, 2026-11-06.
  - [x] **M2c: ask** (#20). A callback in the SDK for callers that can ask a person.
    Done: PR #41, 2026-11-06.
- [x] **M3: the binary.** `adanext-exec run` and `check`, and the JSON protocol v1
  (`docs/protocol.md`). Done when M3a and M3b are.
  - [x] **M3a: run** (#42). `adanext-exec run` with the policy and the audit always set,
    `--approved`, Ctrl+C, no elevation, and distinct exit codes (#12). Done: PR #45, 2026-11-06.
  - [x] **M3b: check** (#43). `adanext-exec check --json` and the protocol document.
    Done: PR #46, 2026-11-06.
- [x] **M4: shared limits.** `adanext-exec serve` and the heavy-command slots. Done when M4a to
  M4c are.
  - [x] **M4a: the daemon's messages** (acquire, queue, release a slot) added to M3's protocol
    in `docs/protocol.md` (#47). Done: PR #48, 2026-11-06.
  - [x] **M4b: the daemon and leases.** `serve`, its lifecycle, slots held as leases on the client
    connection (#9, #16), and what the Windows socket does not protect (#17). Done: PR #49,
    2026-11-06.
  - [x] **M4c: classification.** Which commands are heavy, configurable, and their fair share
    (#15). Done: PR #53, 2026-11-07.
- [x] **M5: memory ceilings,** per platform, reported where they cannot be enforced. Done when
  M5a to M5c are.
  - [x] **M5a: the ceiling and Windows** (#50). `Command.MemoryLimit`, `run --memory`, exit 122,
    the audit fields, and Windows' hard ceiling through the Job Object; the other platforms say
    not enforced. Done: PR #54, 2026-11-07.
  - [x] **M5b: the watchdog** (#51). Polling the tree's memory on Linux and macOS. Done: PR #55,
    2026-11-07.
  - [x] **M5c: a delegated cgroup** (#52). A hard ceiling on Linux through cgroup v2 where one is
    delegated, chosen by `--memory-ceiling` or the policy's `memory_ceiling`. Done: PR #60,
    2026-11-07.
- [x] **M6: Claude Code.** A PreToolUse hook for Bash and PowerShell. Done when M6a and M6b
  are.
  - [x] **M6a: the hook** (#56, #8, #5). `adanext-exec hook claude-code` judges Bash and
    PowerShell commands and rewrites them to `run --script-b64`, so no shell reparses the text.
    Done: PR #58, 2026-11-07.
  - [x] **M6b: the user's rules and the binary** (#57, #6, #7). The user's permission rules
    applied to the original text, the hook's version carried to `run`, and installing
    (`docs/claude-code.md`). Done: PR #59, 2026-11-07.
- [ ] **M7: hrn.** harness's tools run shell commands through the `adanext-exec run` binary;
  harness never imports this module (harness#435, its slices in harness; slice 1 is
  harness#872).
- [ ] **M8: AdaNext OS.** `adad` runs each task command through the `adanext-exec run` binary
  (`--slots` for the daemon's slots), each in a cgroup of its own where adad holds a delegated
  one; a small client module for that (`client/`, its own `go.mod`) comes when M8 starts.
- [x] **M9: output shaping** (#1). A large output replaced by a checked summary at the gate,
  the original kept behind a handle; what harness#435's slice 6 needs. Done when M9a to M9c are.
  - [x] **M9a: the summarizer** (#1). harness's `internal/shape` at its commit `3b9d466`, with
    its adversarial, structured and fuzz tests, unchanged. Done: PR #72, 2026-11-11.
  - [x] **M9b: `run --shape` and the originals** (#70). The caller names the originals
    directory and its caps (64 MiB a directory, 1 GiB in all), refused rather than evicted; the
    audit says a shaping happened and gives the handle. Done: PR #73, 2026-11-11.
  - [x] **M9c: reading an original** (#71). `adanext-exec original <handle> [--range a-b]`,
    bounded, which `run --shape` passes through neither shaped again nor cut; the hook carries
    `--shape`; a measurement on real outputs. Done: PR #74, 2026-11-11.

## Decisions log

Newest first.

- 2026-11-11: M9c (PR #74, #71), which completes M9. `run --shape` recognises a read of an
  original only in its exact form, the bare program name included, and runs its own binary's
  read in its place, so a program elsewhere in PATH named `adanext-exec`, or text around the
  read, cannot borrow the unshaped, uncut pass; the read bounds itself at 1 MiB. A range's end
  past the original reads to its end, so an agent can step through without knowing the size;
  its start past the end is refused. harness's `read_original` also reads lines and JSON
  pointers; the gate's reader takes byte ranges only, as #1 asked. The hook leaves a background
  call unshaped, as its output is read while it runs.

- 2026-11-11: M9b (PR #73, #70). Shaping is `Command.Shape` in the SDK, so the audit line can say
  what was done, and `run --shape --originals DIR` in the binary. #1's per-command opt-out and
  global switch are the caller's: a command is shaped only when its caller asks, so hrn's own
  setting is the switch and a command run without `--shape` the opt-out. Under it both streams
  are one output written once the command ends, as harness's `run_command` returns combined
  output, and `--output-cap` is the threshold (30,000 bytes, harness's). The caps measure as
  harness's do; the "in all" cap counts the runs under a root the caller names, since the gate
  has no list of runs. Writers in several processes keep within the caps through a lock file;
  hrn's own writer does not take it. An original that cannot be kept for a reason other than a
  cap (other bytes at its name, a directory that went) fails the command's result (exit 120),
  since an output silently cut there would hide it.

- 2026-11-11: M9 split into the summarizer (M9a), `run --shape` with the caller's originals
  directory (M9b, #70) and reading an original back (M9c, #71), one issue, branch and PR each;
  #70 and #71 are the gaps harness's design note for harness#435 names. M9a (PR #72, #1) moves
  harness's summarizer byte for byte, so harness's copy for MCP results is held in step by a
  diff and the same tests rather than an import (harness calls the binary, 2026-11-07).

- 2026-11-09: The hook carries a call's `timeout` into the rewrite as `--timeout`, so the gate
  ends the command's tree where Claude Code's tool would only move it to the background; a
  background call gets none (PR #68, #67).
- 2026-11-09: The push guard joins sh's backslash-newline in its parser, at the top level, in
  double quotes and in unquoted heredocs, and keeps it elsewhere (PR #66, #25).
- 2026-11-09: The push guard keeps a tab inside quotes and splits on one outside them; a heredoc's
  word ends at a blank outside quotes only (PR #65, #24).
- 2026-11-09: The push guard reads `tool_input.command`, `cwd` and `tool_name` by their place in
  the hook's JSON, not as the first key of that name anywhere in it (PR #64, #23).
- 2026-11-07: M7 and M8 run commands through the `adanext-exec run` binary, never through an
  import of the broker (the owner's choice). This replaces the M0 plan (2026-10-14) that
  harness and adad import the SDK. The binary always sets a policy and an audit, where the
  SDK's are opt-in; it keeps the per-process parts (the parent-death thread, the console, the
  M5c setup that moves its own process into a leaf) out of hrn and adad; and it releases apart
  from them. `run` is a supervisor per command, and `serve` keeps only what is shared, the
  heavy-command slots: a socket that runs commands would carry the caller's environment,
  directory and identity across, and be the most dangerous part to get wrong. harness stays
  standard library only (slice 1 of harness#435 is harness#872). adad starts each `run`
  straight into a cgroup of its own where it holds a delegated one, so the kernel holds the
  ceiling (M5c), and removes it once `run` has exited; a dependency-free client for that comes
  when M8 starts, as a module of its own in this repository (`client/`, its own `go.mod`), so
  adad requires only that. No new repository. This settles the open "require" question of
  harness's design note for both.
- 2026-11-07: M5c (PR #60, #52), which completes M5. The owner chose the kernel's cgroup files
  with no dependency ("Kernel cgroup, no deps": no systemd-run, no D-Bus, no library) and a
  setting with three modes, `auto` (the default), `cgroup` and `watchdog`, given by `run
  --memory-ceiling`, `Command.MemoryCeiling` or the policy's `memory_ceiling`. The serve daemon
  only hands out slots, so the cgroup is made by whatever runs the command, `run` or an SDK
  caller, and `run` and `serve` need no separate rule. The caller's cgroup must hold the caller
  alone: it moves into a leaf of its own and gives each command a cgroup beside it, started
  straight into it with clone3, which is tried once first since a failed start cannot be
  retried. The watch keeps running beside the cgroup and stops the tree when a process leaves
  it, since the same user can always move a process out of a delegated cgroup; the review
  asked for that, and for cleanup that never removes a cgroup the broker did not make. CI hands
  the Linux runner a delegated cgroup through sudo, as systemd would; the tests run
  unelevated.
- 2026-11-07: The memory-ceiling test's helper takes its memory in 16 MiB steps with a 10 ms pause
  between them (PR #62, #61). On a newer macOS runner image it took all 512 MiB between two of
  the watch's reads, so the peak stood past the test's 512 MiB bound; the bound stays.
- 2026-11-07: M6b (PR #59, #57). Claude Code gives a hook no merged view of its rules, so the
  hook reads the files Claude Code reads (managed with its drop-ins, user, project, local and
  the repository's local file) and applies only their Bash and PowerShell deny and ask rules,
  the strictest of those and the policy winning. Allow rules cannot lift the policy, so they
  are not read. Managed policy from the registry, configuration profiles, MDM or the console,
  and `--settings`, cannot be read, and the install guide says so. A file that exists but
  cannot be read denies, and only a regular file is read, so a pipe cannot hold the hook.
  Matching follows Claude Code's documentation and reads more as a match, never on purpose
  less: each command of a compound one (its own splitter, which keeps the text as written next
  to the text with quotes taken off), `{ }` blocks and substitutions included, is tried both
  ways; a reading only ever adds candidates. After a wrapper every word is tried, since its
  options cannot be parsed for every form. A command is read once for all the rules, within a
  2 MiB budget, and one over 10,000 characters or past the budget asks, as Claude Code asks
  about long commands: a hook that times out lets the command run. A
  rewrite that would let Claude Code's own matcher see the original (`if false; then <text>;
  fi; ...`) was weighed and dropped: the text could close the `if` and run outside `run`. The
  binary is the hook itself (#7), and a rewrite names its build (`--hook-build`), which `run`
  checks. A build is a hash of the binary's own file: the toolchain's version string is the
  same for local builds and for different changes at one commit. A missing binary fails closed through the install line's `|| exit 2`, since Claude
  Code runs a command whose hook fails with another code.
- 2026-11-07: M6 split into the hook (M6a) and the user's rules with the binary's discovery
  (M6b), one issue and PR each. M6a (PR #58, #56): the hook is the binary's own subcommand, so
  it needs no shell of its own (#8), and the rewrite carries the text as URL-safe base64 without
  padding, which neither Bash, PowerShell nor Git Bash's path conversion changes (#5). The design
  had the rewrite as `run -- <command>`. The PowerShell tool's text runs under `pwsh` when it is
  in PATH, else Windows PowerShell. A command that passes `-approved` or `-script-b64` itself is
  denied: through the hook an agent could otherwise approve its own ask or hide a script from
  the policy. Every failure is a deny, or exit 2, since Claude Code runs the command as it is
  when a hook gives no answer or fails another way. An empty `--approved=` now approves an ask
  by the policy's default, which names no rule, so such an ask can run once approved. The
  session id must be a plain id, since Windows PowerShell hands a native argument's quotes to
  the program's own parser.
- 2026-11-07: M5b (PR #55, #51). Linux and macOS watch the tree's resident memory every 100 ms
  and kill it past the ceiling: best effort, and written so. The watch counts what the kill
  reaches (the group, marked processes, their descendants), reading each process's marker once,
  and counts a sample only if its pid still names the process scanned. It ends before the
  leader is reaped, while the group id is still the tree's. A memory that cannot be read stops
  the tree (fail closed) and is reported.
- 2026-11-07: M5 split into the ceiling and Windows (M5a), the watchdog on Linux and macOS
  (M5b) and a delegated cgroup on Linux (M5c), one issue and PR each. M5a (PR #54, #50): a
  limit a platform does not enforce is reported in the result, the audit and the binary, and
  the command runs; it is not refused, since a ceiling is a protection for the machine, not a
  rule about what may run. On Windows the job's own memory limit is the ceiling, and the broker
  reads the job's peak (which counts a refused commit) to stop the tree once past it and to say
  it was over; the job's completion-port notice is not used, since Windows does not promise it.
  A peak that cannot be read stops the tree and is reported, never read as "not over".
- 2026-11-07: M4c (PR #53, #15), which completes M4. Heavy commands are a built-in table read
  the way each tool reads its own command line (global flags and their values, wrappers,
  executors, shell substitutions), not regexes over the line, so arguments that name a tool are
  data; a strict, owner-checked `serve --config` adds line patterns or replaces the table (the
  owner approved this design). A freed slot goes to the client holding the fewest, the oldest
  first among equals, a client being its caller's agent and session; a self-reported caller can
  split itself, within the same-user trust the design states. A slot carries GOFLAGS,
  GOMAXPROCS, MAKEFLAGS and CARGO_BUILD_JOBS at CPUs / slots, and a caller's own values win.
- 2026-11-06: M4b (PR #49, #9, #16, #17). Nothing starts the daemon on its own; one per socket,
  by a lock held for its life. A client that asks for a slot and finds no daemon does not run
  its command, and one that does not ask shares no limits. A client watches its slot: a daemon
  that ends kills the command's tree, and a restarted daemon waits, through a shared lock
  every slot holder keeps until its tree is dead, before it grants anything. The socket needs
  a directory of its own, which the daemon makes owner-only (on Windows a protected access
  list, since the socket has none and Windows gives no peer credentials); a client talks only
  to a socket the user owns. Classification comes with M4c; until then every command that asks
  for a slot is heavy.
- 2026-11-06: M4a (PR #48, #9). A slot is a lease on the client's connection: released when the
  connection closes, however the client ends, so the design's process-identity row goes. The
  messages are newline-delimited JSON, one connection per command. A reader refuses a null
  anywhere and a key given twice before Go's decoding could hide either, nesting past eight
  levels, and any field outside the message's type; a writer reads its own bytes back the same
  way before sending. `daemon.Version` repeats `broker.ProtocolVersion` so the SDK can import
  the daemon package, and a test keeps them equal.
- 2026-11-06: M3b (PR #46), which completes M3. `check --json` judges without running or
  auditing anything. Every outcome has one versioned JSON answer, and an answer that cannot
  be written exits 126, so a hook reading only the code fails closed. A request is strict:
  unknown fields, other types (a `null` argument too), unknown shells and trailing data are
  errors, never a decision. A version names its major only; a change an existing client could
  misread is a new major, and readers refuse what they do not know.
- 2026-11-06: M3a (PR #45, #12). The binary's own exit codes sit in 120 to 130, following
  GNU timeout (124 to 127) and shells (128+n, 130 for an interrupt), and a command's own code
  passes through; one that exits with a code of that band by itself cannot be told from the
  binary's, and the audit line's outcome settles it. A command that ran but whose output,
  audit line or clean-up failed exits 120, never its own code, since a 0 there would hide the
  failure. A policy that cannot be used is refused through `Run`, so the audit says why; the
  Policy `LoadPolicy` returns with an error now carries that error. With no policy file the built-in rules
  apply, since they are a policy of their own; a policy file that is named, or present at the
  default place, but unusable refuses. The binary's code is in `internal/cli`, so its tests
  run it in-process against real commands.
- 2026-11-06: M3 split into `run` with its exit codes (M3a) and `check` with the protocol
  document (M3b), one issue, branch and PR each. The protocol of M3 is what a client says to
  the binary (the `check` request and answer, `run`'s exit codes); the daemon's messages come
  with M4a.
- 2026-11-06: M2c (PR #41, #20), which completes M2. `Command.Ask` puts an ask to a person
  before the command starts: yes runs it, no refuses it, an error leaves it not run, and none
  set refuses as before. A refusal is never asked about, so a person cannot overrule one. The
  wait counts against the caller's context, not the command's timeout. The binary's
  `--approved RULE` (M3) answers through the same callback.
- 2026-11-06: M2b (PR #40, #21). A command meeting several rules gets the strictest decision,
  and a built-in rule is lifted only by naming it, so a policy's own `allow` cannot undo a
  refusal it did not mean to; `default: refuse` makes a list of what may run. An argv is matched
  as the line a POSIX shell would read back. The policy is opt-in in the SDK, as the audit is,
  and the binary always sets one (M3); a zero `Policy` refuses everything. `LoadPolicy` refuses
  a file others can write. The trust statement for the whole (policy, socket, audit) is in the
  design: the gate does not contain a hostile agent running as the same user.
- 2026-11-06: M2a (PR #39). The guard is harness's at its commit `014b929`, with its tables:
  nine rules, without `guard-env`, whose 41 rows were left out (40 match nothing here; one names
  `.npmrc` and meets `secret-file`). It reports every rule a line meets, not the first, so a
  policy can give one rule another decision and still see the rest. harness's tests of its
  workspace, environment and error text stay there.
- 2026-11-06: M2 split into the guard (M2a), the policy (M2b) and ask (M2c), one issue, branch
  and PR each. The guard comes first, as a plain port that harness's tests prove; the policy is
  then built around it. harness's `guard-env` rule stays in harness: it guards the environment
  hrn builds for its runs, which the broker does not build.
- 2026-11-06: M1d (PR #35). The audit is opt-in in the SDK (`Command.Audit`; nil writes none)
  and the binary will always set it (M3); when set, a command whose line cannot be opened is not
  run. Redaction ports harness's masking rules (`internal/mask`) into `internal/redact`, adds
  private keys and the value after a credential flag in argv, and masks the values of
  secret-named environment variables. Dates are UTC.
- 2026-11-06: #33 (PR #34), a fix to M1a's macOS group check. A kill that came while signalled
  members were still exiting was reported as refused, since `kinfo_proc` shows an exiting
  process as running; its `P_WEXIT` flag now counts it as gone. CI caught it as a flaky test.
- 2026-11-06: #31 (PR #32), a fix to M1b. A leader reaped before its start time was read
  (its caller ignoring SIGCHLD) left the tree with no bound, so Kill refused to kill anything
  and a marked descendant survived; CI caught it as a flaky test. The bound now comes from the
  clock before the start, narrowed to the leader's own start time when it can be read.
  On macOS both bounds follow the wall clock (no monotonic start time is readable there), so a
  backward step of more than a second can hide escaped processes from the sweep; accepted and
  written in the design, as the leader's own start time already had that limit.
- 2026-11-06: M1c (PR #30, #29). The cap is per stream and optional (zero is none; the binary
  can set a default in M3). Its head passes through live, so a caller still sees a command's
  output as it runs; the tail is held and written at the end, which delays output past the
  head until the command ends. Cutting may split a multi-byte character, as harness's buffer
  does.
- 2026-11-06: M1b (PR #28). A process that leaves the group is found by a marker in the
  environment, the technique of build servers' process-tree killers, and by parent links at
  kill time, instead of `PR_SET_CHILD_SUBREAPER` (#10): a subreaper changes the whole caller,
  and the broker could not tell its commands' orphans from the caller's other children's. The
  gaps (a cleared environment plus an orphaned process, unreadable processes, other services)
  are in the design. For #11 only Linux's parent-death signal exists, and it reaches the
  command's own process, not its descendants; macOS has none; the design says so.
- 2026-11-06: M1a (PR #27). `broker.Run` also kills what a command leaves running when its own
  process exits, on every platform, so nothing a command starts outlives it; the design said
  only "when it must stop". A job that cannot be made or assigned refuses the command
  (`ErrNotContained`) instead of harness's taskkill fallback, since the broker fails closed. An
  argv naming `cmd.exe`, also through a link, is refused like a batch file (#18), as cmd.exe
  reparses it the same way. `bash` on Windows passes over WSL's launchers, whose Linux processes
  no job holds. Run owns the pipes: it waits for the caller's writes but not for a blocked stdin
  reader, and abandons, and reports, output held open from outside the tree.
  On Unix the leader is waited for with `waitid(WNOWAIT)` before the group is killed, so the
  group id cannot be reused in between. The SDK leaves console groups alone (#19): reporting
  after Ctrl+C is the caller's, and `adanext-exec run` does it in M3. PowerShell text goes
  through `-Command`; passing it without a reparse stays with #5 (M6).
  On macOS a signal to a group of zombies fails with EPERM; the group counts as gone when two
  reads of the process table show no live member. The race this leaves (a member the user
  cannot signal hiding a child during both reads) is accepted and written in the design: macOS
  has no atomic listing, and such a process could not be killed anyway.
- 2026-11-06: M1 and M4 split into smaller milestones (#22). M1 into running with a timeout and
  the tree kill on every platform (M1a), what leaves the group on Unix (M1b), the output cap
  (M1c) and the audit (M1d); M4 into the protocol, the daemon with its leases, and the
  classification. Each sub-milestone is one issue, branch and PR, ticked here when it merges.
  PR #26.
- 2026-10-14: the repository (M0). Decided by the owner: the name `adanext-exec` (an AdaNext
  tool), public (everything AdaNext OS or harness uses is public), MIT, CI on GitHub's hosted
  runners, protected by rulesets on `main` and `v*` tags. Go, standard library only, so harness
  and `adad` import the SDK directly and the Windows Job Object and guard code come from harness;
  other languages use the binary's JSON protocol. Rust was weighed and left for a possible later
  sandboxing component.
