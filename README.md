# adanext-exec

**Status: being designed; nothing to install yet.** [docs/design.md](docs/design.md) is the
design, and [PLAN.md](PLAN.md) lists the milestones in the order they land.

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
AdaNext tools; [harness](https://github.com/cristiangirlea/harness) and
[AdaNext OS](https://github.com/cristiangirlea/adanext-os) are its first users.

The matching is textual: it stops plausible accidents, not a determined adversary, and says so.
Sandboxing is out of scope for now.

MIT licensed.
