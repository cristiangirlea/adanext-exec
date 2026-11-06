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
- [ ] **M4: shared limits.** `adanext-exec serve` and the heavy-command slots. Done when M4a to
  M4c are.
  - [x] **M4a: the daemon's messages** (acquire, queue, release a slot) added to M3's protocol
    in `docs/protocol.md` (#47). Done: PR #48, 2026-11-06.
  - [x] **M4b: the daemon and leases.** `serve`, its lifecycle, slots held as leases on the client
    connection (#9, #16), and what the Windows socket does not protect (#17). Done: PR #49,
    2026-11-06.
  - [ ] **M4c: classification.** Which commands are heavy, configurable, and their fair share
    (#15).
- [ ] **M5: memory ceilings,** per platform, reported where they cannot be enforced. Windows
  reuses M1a's Job Object and can start once M1a is in.
- [ ] **M6: Claude Code.** A PreToolUse hook for Bash and PowerShell.
- [ ] **M7: hrn.** harness's tools run shell commands through the SDK.
- [ ] **M8: AdaNext OS.** `adad` runs task commands through the SDK.

## Decisions log

Newest first.

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
