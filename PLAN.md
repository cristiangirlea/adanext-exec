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
- [ ] **M2: policy.** Rules, decisions (allow, ask, refuse) and harness's guard rule set with its
  tests. Done when M2a to M2c are.
  - [ ] **M2a: the guard** (#36). harness's command guard in `internal/guard`, with its tests.
  - [ ] **M2b: the policy** (#37). The JSON rules, the decisions, `Run` keeping to them, the
    audit outcome, and the trust statement (#21).
  - [ ] **M2c: ask** (#20). A callback in the SDK for callers that can ask a person.
- [ ] **M3: the binary.** `adanext-exec run` and `check`, and the JSON protocol v1
  (`docs/protocol.md`).
- [ ] **M4: shared limits.** `adanext-exec serve` and the heavy-command slots. Done when M4a to
  M4c are.
  - [ ] **M4a: the daemon's messages** (acquire, queue, release a slot) added to M3's protocol
    in `docs/protocol.md`.
  - [ ] **M4b: the daemon and leases.** `serve`, its lifecycle, slots held as leases on the client
    connection (#9, #16), and what the Windows socket does not protect (#17).
  - [ ] **M4c: classification.** Which commands are heavy, configurable, and their fair share
    (#15).
- [ ] **M5: memory ceilings,** per platform, reported where they cannot be enforced. Windows
  reuses M1a's Job Object and can start once M1a is in.
- [ ] **M6: Claude Code.** A PreToolUse hook for Bash and PowerShell.
- [ ] **M7: hrn.** harness's tools run shell commands through the SDK.
- [ ] **M8: AdaNext OS.** `adad` runs task commands through the SDK.

## Decisions log

Newest first.

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
