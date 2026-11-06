# adanext-exec plan

The work queue, milestone by milestone, and the decisions log. `docs/design.md` describes the
whole; each milestone below is one or more issues, each a branch and a pull request.

## Milestones

- [x] **M0: the repository.** AI setup, CI on GitHub's hosted runners, the design, the SDK's first
  package (`broker.ProtocolVersion`). Done: PR #3, 2026-10-14. Its review's design concerns are
  issues #4 to #22, each naming its milestone.
- [ ] **M1: one command, contained.** `broker.Run` with a timeout, a whole-tree kill, an output
  cap and an audit line, on Windows (Job Objects), Linux and macOS (process groups). Done when
  M1a to M1d are.
  - [x] **M1a: run and kill the tree.** `broker.Run` takes argv or shell text with a named shell
    (#4), stops at a timeout or a cancelled context, and kills the whole tree, also what is left
    when the command exits: a Job Object on Windows (from harness), a process group on Linux and
    macOS. Refuses batch files as argv on Windows (#18); the job never allows breakaway and
    nests (#19). Done: PR #27, 2026-11-06.
  - [x] **M1b: what leaves the group on Unix.** Kill descendants that called `setsid` (#10) and
    the group when the caller dies (#11), or say where it cannot. Done: PR #28, 2026-11-06.
  - [ ] **M1c: the output cap.** Head and tail kept, the middle counted, as harness's bounded
    buffer does.
  - [ ] **M1d: the audit line.** One JSON line per command, secrets redacted by pattern (#14),
    and what the audit and the broker do not cover said in the design (#13). The patterns are
    the audit's own until M2's policy brings its secret list, which then shares them.
- [ ] **M2: policy.** Rules, decisions (allow, ask, refuse) and harness's guard rule set with its
  tests.
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
