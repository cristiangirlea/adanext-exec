# adanext-exec

**Status: built and in use by harness; AdaNext OS's side is still planned.** Built: one command
contained (a timeout, a whole-tree kill, an output cap, an audit line), the policy, the binary's
`run` and `check`, shared heavy-command slots (`serve`), memory ceilings, the Claude Code hook,
output shaping with `original` to read back what was shaped, and harness running its commands
through the binary (M0 to M7 and M9). Planned: AdaNext OS's daemon running its task commands
through the binary (M8). [docs/design.md](docs/design.md) is the design, and [PLAN.md](PLAN.md)
lists the milestones in the order they land.

AI coding agents run shell commands on your computer: builds, tests, git, package managers.
Each agent has its own partial controls, or none. adanext-exec is one local gate they all go
through, on Windows, Linux and macOS:

- **Policy**: each command is allowed, asked about, or refused, by rules you can read, and the
  answer names the rule.
- **Limits**: a timeout, a memory ceiling, and a fair share of heavy-command slots, so several
  agents building at once do not starve the machine.
- **Control**: when a command must stop, its whole process tree stops with it.
- **Audit**: one JSON line per command, what ran, for whom, and how it ended. Nothing leaves the
  machine.

It is a Go SDK (`broker`) for Go programs, and a binary, `adanext-exec`, with a documented JSON
protocol for everything else, including a Claude Code hook. Standard library only. Part of the
AdaNext tools: [harness](https://github.com/cristiangirlea/harness)'s `hrn` runs the commands of
its api, claude and codex backends through the `adanext-exec run` binary when its gate option is
on, and [AdaNext OS](https://github.com/cristiangirlea/adanext-os) is planned to do the same (M8).

The matching is textual: it stops plausible accidents, not a determined adversary, and says so.
Sandboxing is out of scope for now.

## Install

There is no tagged release yet. With Go 1.26.4 or later, the version `go.mod` names:

```bash
go install github.com/cristiangirlea/adanext-exec/cmd/adanext-exec@latest
```

The binary has no `version` subcommand; `adanext-exec help` lists the ones it has.
[docs/protocol.md](docs/protocol.md) is what a client says to the binary and the daemon, and
[docs/claude-code.md](docs/claude-code.md) sets the binary up as Claude Code's hook.

MIT licensed.
