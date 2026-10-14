# adanext-exec plan

The work queue, milestone by milestone, and the decisions log. `docs/design.md` describes the
whole; each milestone below is one or more issues, each a branch and a pull request.

## Milestones

- [ ] **M0: the repository.** AI setup, CI on GitHub's hosted runners, the design, the SDK's first
  package (`broker.ProtocolVersion`).
- [ ] **M1: one command, contained.** `broker.Run` with a timeout, a whole-tree kill, an output
  cap and an audit line, on Windows (Job Objects), Linux and macOS (process groups).
- [ ] **M2: policy.** Rules, decisions (allow, ask, refuse) and harness's guard rule set with its
  tests.
- [ ] **M3: the binary.** `adanext-exec run` and `check`, and the JSON protocol v1
  (`docs/protocol.md`).
- [ ] **M4: shared limits.** `adanext-exec serve` and the heavy-command slots.
- [ ] **M5: memory ceilings,** per platform, reported where they cannot be enforced.
- [ ] **M6: Claude Code.** A PreToolUse hook for Bash and PowerShell.
- [ ] **M7: hrn.** harness's tools run shell commands through the SDK.
- [ ] **M8: AdaNext OS.** `adad` runs task commands through the SDK.

## Decisions log

Newest first.

- 2026-10-14: the repository (M0). Decided by the owner: the name `adanext-exec` (an AdaNext
  tool), public (everything AdaNext OS or harness uses is public), MIT, CI on GitHub's hosted
  runners, protected by rulesets on `main` and `v*` tags. Go, standard library only, so harness
  and `adad` import the SDK directly and the Windows Job Object and guard code come from harness;
  other languages use the binary's JSON protocol. Rust was weighed and left for a possible later
  sandboxing component.
