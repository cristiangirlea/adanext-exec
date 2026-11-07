# adanext-exec protocol

What a client, a hook or a program in any language, says to the `adanext-exec` binary and gets
back. The version is `adanext-exec/1` (`broker.ProtocolVersion`). [design.md](design.md) says why.

## `adanext-exec run`

```
adanext-exec run [flags] -- <program> [args...]
adanext-exec run [flags] --shell <name> -- <script>
adanext-exec run [flags] --shell <name> --script-b64 <script as base64>
```

Runs one command through `broker.Run` and exits with its code. The command's input, output and
exit code pass through. The command's whole process tree is killed when it times out, when the
binary is interrupted (Ctrl+C, or a terminate request), and when the command's own process
exits.

| Flag | Meaning |
|---|---|
| `--shell NAME` | The one argument after `--` is shell text for `sh`, `bash`, `powershell`, `pwsh` or `cmd`. |
| `--hook-build V` | The build of the hook that wrote this command line. `run` refuses (126) when it is another build than its own. |
| `--script-b64 B` | With `--shell`, and nothing after it: the shell text as URL-safe base64 without padding (RFC 4648, section 5), in place of the argument after `--`. What the hook's rewrite writes, so no shell between the hook and `run` reads the text. |
| `--policy FILE` | The policy file. |
| `--profile NAME` | The policy in `<config dir>/adanext-exec/profiles/NAME.json`; NAME is lower-case letters, digits and dashes. |
| `--approved RULE` | A person approved an ask under RULE. It counts only when the policy's own answer for the command is ask under that same rule. An empty RULE (`--approved=`) is an ask by the policy's default, which names no rule. |
| `--audit-dir DIR` | The audit directory, instead of the user's state directory. |
| `--timeout D` | Kill the command's tree after D (Go's duration syntax: `90s`, `5m`). |
| `--output-cap N` | Bound each output stream to N bytes. |
| `--slots` | Wait for one of the daemon's heavy-command slots before the command starts, and hold it until the command ends. The wait is shown on standard error with its place in the queue. |
| `--socket PATH` | The daemon's socket, instead of `<state dir>/adanext-exec/daemon/daemon.sock`; implies `--slots`. |
| `--max-wait D` | With `--slots`: give up after waiting D for a slot. The wait counts toward no `--timeout`, which starts with the command. |
| `--memory SIZE` | A ceiling on the memory of the command and everything it starts, together: bytes, or a whole number with `K`, `M` or `G` (powers of 1024). Where the platform does not enforce it, the binary says so on standard error and the command runs without it. |
| `--memory-ceiling MODE` | How `--memory` is held on Linux: `auto` in a cgroup of the command's own where one can be made, and watched otherwise; `cgroup` by the kernel or not at all (not run, 126); `watchdog` only watched. Without it, the policy's `memory_ceiling`, else `auto`. Windows' job always holds the ceiling; macOS can only watch it, so `cgroup` runs nothing there. [design.md](design.md) says which hosts have a cgroup to give. |
| `--agent`, `--session`, `--run-id` | Who asked, for the audit; self-reported. |

**The policy is always set.** It comes from `--policy`, `--profile`, or
`<config dir>/adanext-exec/policy.json` when that file exists, and otherwise from the built-in
rules alone. `<config dir>` is Go's `os.UserConfigDir`: `%AppData%`, `~/Library/Application
Support`, or `$XDG_CONFIG_HOME` (by default `~/.config`). A policy file that is named, or that
exists at the default place, but cannot be used refuses the command (125), and the audit line
says why.

**The audit is always written**, one line per command, also for one that did not run. A command
whose line cannot be opened does not run.

**The binary refuses to run as root or as an elevated administrator** (exit 126). A token whose
elevation cannot be read counts as elevated.

### Exit codes

Any code not in this table is the command's own. A command that itself exits with one of these
codes cannot be told from the binary's; a caller that must tell reads the audit line, whose
`outcome` says what happened.

| Code | Meaning |
|---|---|
| 120 | The command ran, but something after it failed: writing its output, writing its audit line, or killing what it left running. Its own code is in the audit line, when that was written. |
| 121 | The arguments are not valid (usage), including a negative `--timeout` or `--output-cap`. |
| 122 | The command and what it started went over `--memory` together, and the tree was stopped. |
| 123 | The command asked for a slot (`--slots`, `--socket`) and no daemon answered, or the socket is not the user's (not run); or the daemon went away while the command held its slot, and the command was killed with its tree. |
| 124 | The command ran past `--timeout`, and its tree was killed; or, not run, it waited past `--max-wait` for a slot, or the daemon refused it one. |
| 125 | Not run: the policy refused it, answered ask with no matching `--approved`, or could not be used. |
| 126 | Not run: it could not start, be contained or be audited, the shell is unknown, the binary runs elevated, `--hook-build` names another build, or `--memory-ceiling cgroup` (or the policy's) asked for a hard ceiling that cannot be held here. |
| 127 | Not run: the program (in PATH, or at the path given) or the shell was not found. |
| 128+n | The command was ended by signal n (Linux and macOS), as shells report it. |
| 130 | The binary was interrupted; the command's tree was killed (or it never started). |

The binary's own messages go to standard error.

## `adanext-exec check --json`

```
adanext-exec check --json [--policy FILE | --profile NAME] < request.json
```

The policy's decision for one command, without running it and without an audit line: for a
hook that must answer before a tool runs. The policy is chosen as for `run`.

The request is one JSON object on standard input (at most 1 MiB), either an argv (`args`, a
list of strings, the first not empty) or shell text (`shell`, one of `sh`, `bash`,
`powershell`, `pwsh` and `cmd`, with its `script`):

```json
{"version": "adanext-exec/1", "args": ["git", "push", "origin", "main"]}
{"version": "adanext-exec/1", "shell": "bash", "script": "git push origin main"}
```

The answer is one JSON line on standard output:

```json
{"version": "adanext-exec/1", "decision": "ask", "rule": "git-push"}
```

`decision` is `allow`, `ask` or `refuse`; `rule` names the rule that decided, and is left out
when the policy's default did. A hook acts on it: `refuse` blocks the tool, `ask` asks the
person and, on a yes, runs the command with `run --approved RULE`, and `allow` runs it through
`run`.

When there is no decision, the answer carries `error` instead, and the exit code says why. Every
outcome but `-h` has its one answer line, a usage error included:

| Code | Meaning |
|---|---|
| 0 | A decision. |
| 121 | The request or the flags are not valid: not one JSON object, an unknown field or a field of the wrong type (a `null` argument included), another version, both an argv and shell text or neither, an empty program name, an unknown shell, more than 1 MiB, or input that could not be read. |
| 125 | The policy cannot be used. A hook treats this as a refusal. |
| 126 | No decision: the binary runs elevated (the answer says so), or the answer could not be written. |

## `adanext-exec hook claude-code`

```
adanext-exec hook claude-code [--policy FILE | --profile NAME] < PreToolUse.json
```

Claude Code's PreToolUse hook, the same on Windows, Linux and macOS. [claude-code.md](claude-code.md)
says how to install it, and why the `|| exit 2`. In Claude Code's settings:

```json
{"hooks": {"PreToolUse": [{"matcher": "Bash|PowerShell",
  "hooks": [{"type": "command", "command": "adanext-exec hook claude-code || exit 2"}]}]}}
```

It reads Claude Code's input, one JSON object of at most 1 MiB with `hook_event_name`
`PreToolUse`, and acts on the `Bash` and `PowerShell` tools. For any other tool it writes nothing
and exits 0, so Claude Code's own permissions decide. It judges `tool_input.command` by the
policy, chosen as for `run` (a relative `--policy` is made absolute), as shell text for `bash`
or, for the PowerShell tool, `pwsh` when it is in PATH and otherwise `powershell` (Windows).
The answer is one JSON line on standard output, exit 0:

```json
{"hookSpecificOutput": {"hookEventName": "PreToolUse", "permissionDecision": "allow",
  "permissionDecisionReason": "...", "updatedInput": {"command": "...", "description": "..."}}}
```

| Policy | `permissionDecision` | The command |
|---|---|---|
| allow, with your Claude Code ask rule | `ask` | Rewritten. The reason names your rule and shows the original command. |
| allow | `allow` | Rewritten. |
| your Claude Code deny rule | `deny` | Not run. The reason names your rule. |
| ask | `ask` | Rewritten with `--approved=RULE` (empty for an ask by the policy's default). The reason names the rule and shows the original command, since the person sees the rewrite. |
| refuse | `deny` | Not run. The reason names the rule. |

The rewrite keeps every other field of `tool_input` and replaces `command` with the binary
itself (its own path) running the text through `run`:

```
'<binary>' 'run' '--hook-build' '<build>' '--shell' 'bash' ['--policy' 'FILE' | '--profile' 'NAME'] ['--approved=RULE']
  '--agent' 'claude-code' ['--session' '<session_id>'] '--script-b64' '<text>'
& '<binary>' 'run' '--hook-build' '<build>' '--shell' 'pwsh' ...
```

Each word is single-quoted for the tool's shell (Bash, or PowerShell after its call operator),
and the text is base64 without `+`, `/` or `=`, so neither shell, nor Git Bash's path
conversion, changes it. `run` judges the command again and runs it.

**It denies what it cannot judge**: input that is not valid JSON or is larger than 1 MiB,
another event, no tool, a `tool_input` that is not an object or has no command string, a policy
or a Claude Code settings file that cannot be used, unknown arguments, a binary running elevated, a `session_id` other than
1 to 128 letters, digits and `. _ : -` (Windows PowerShell would let its quotes reach `run`'s
own parser), and a command that contains `-approved` or `-script-b64` (only the rewrite
carries those). Each deny says why. When the answer cannot be written, the reason goes to standard error and the exit code is 2, which
blocks the command. Claude Code still applies its own deny and ask rules, to the rewrite.

**Your Claude Code rules** (M6b) count on the original text before the policy. The hook reads
the `Bash(...)` and `PowerShell(...)` deny and ask rules from the settings files Claude Code
reads, for the project in `CLAUDE_PROJECT_DIR` (else the input's `cwd`), and matches them as
claude-code.md describes. The strictest of those rules and the policy decides. The rewrite
carries no approval for an ask that is only yours.

## The daemon (M4)

```
adanext-exec serve [--slots N] [--socket PATH] [--config FILE]
```

Runs the daemon until it is interrupted: N heavy-command slots (default: the number of CPUs
divided by 4, at least 1) on PATH (default: `<state dir>/adanext-exec/daemon/daemon.sock`). It
refuses to start elevated, or when another daemon holds the socket (exit 126), and refuses a
configuration it cannot use (exit 121).

The configuration says which commands are heavy, beyond the built-in list in
[design.md](design.md):

```json
{"version": 1, "heavy": ["\\bterraform\\s+apply\\b"], "only": false}
```

`heavy` holds Go regular expressions matched against the whole command line (an argv joined
with spaces, or shell text as written); with `"only": true` they are used alone, without the
built-in list. The file is read strictly (no unknown field, no `null`, no key
twice), at most 1 MiB, and on Linux and macOS must be the user's and writable by no one else.

`adanext-exec serve` shares heavy-command slots among every client on the machine. A client
opens one connection to the daemon's local socket per command, and both sides write one JSON
object per line (at most 1 MiB, ending in a newline). Every message carries `version` and
`type`, and only the fields of its type. A `null` anywhere, a key given twice, nesting deeper
than 8, an unknown field or type, a field of another type, or another version is refused with
an `error` message, after which the daemon closes the connection.

The client asks once:

```json
{"version": "adanext-exec/1", "type": "acquire", "args": ["go", "test", "./..."],
 "caller": {"agent": "claude", "session": "s1", "run_id": "r1"}, "max_wait_ms": 60000}
```

`args`, or `shell` and `script`, is the command as `check` reads it: one form only, judged by
the fields given, `args` not empty, and a shell the broker runs. `caller` is optional and
self-reported. `max_wait_ms` is how long the client will wait for a slot (left out or 0: as
long as it takes).

The daemon answers with one of:

| `type` | Fields | Meaning |
|---|---|---|
| `light` | none | The command needs no slot; the client runs it and closes the connection. |
| `queued` | `position` (1 is next) | The command waits; a new `queued` comes whenever the position changes. |
| `granted` | `slot`, `waited_ms`, `env` | The slot is the client's. `env` holds `KEY=VALUE` hints that bound the command's own parallelism, for the client to set on the command. |
| `error` | `error` | The request is refused, or the wait passed `max_wait_ms`; the connection closes. |

**A granted slot is a lease on the connection.** It is held for as long as the connection stays
open, and released when it closes, whichever side closes it and however the client ended (#9):
a client that crashes or is killed gives its slot back, and nothing needs to tell processes
apart. A client keeps the connection open until its command has ended, then closes it.

## Versions

Every message carries `version`, `adanext-exec/<major>`. A change that an existing client
could misread (a field removed or given another meaning) is a new major version; a reader
refuses a version it does not speak, and refuses fields it does not know, so neither side
guesses. The daemon's messages follow the same rule.
