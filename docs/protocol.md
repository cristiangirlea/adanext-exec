# adanext-exec protocol

What a client, a hook or a program in any language, says to the `adanext-exec` binary and gets
back. The version is `adanext-exec/1` (`broker.ProtocolVersion`). [design.md](design.md) says why.

## `adanext-exec run`

```
adanext-exec run [flags] -- <program> [args...]
adanext-exec run [flags] --shell <name> -- <script>
```

Runs one command through `broker.Run` and exits with its code. The command's input, output and
exit code pass through. The command's whole process tree is killed when it times out, when the
binary is interrupted (Ctrl+C, or a terminate request), and when the command's own process
exits.

| Flag | Meaning |
|---|---|
| `--shell NAME` | The one argument after `--` is shell text for `sh`, `bash`, `powershell`, `pwsh` or `cmd`. |
| `--policy FILE` | The policy file. |
| `--profile NAME` | The policy in `<config dir>/adanext-exec/profiles/NAME.json`; NAME is lower-case letters, digits and dashes. |
| `--approved RULE` | A person approved an ask under RULE. It counts only when the policy's own answer for the command is ask under that same rule. |
| `--audit-dir DIR` | The audit directory, instead of the user's state directory. |
| `--timeout D` | Kill the command's tree after D (Go's duration syntax: `90s`, `5m`). |
| `--output-cap N` | Bound each output stream to N bytes. |
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
| 122 | Reserved: the command went over its memory ceiling (M5). |
| 123 | Reserved: the daemon is needed and not running (M4). |
| 124 | The command ran past `--timeout`; its tree was killed. |
| 125 | Not run: the policy refused it, answered ask with no matching `--approved`, or could not be used. |
| 126 | Not run: it could not start, be contained or be audited, the shell is unknown, or the binary runs elevated. |
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

## Versions

Every message carries `version`, `adanext-exec/<major>`. A change that an existing client
could misread (a field removed or given another meaning) is a new major version; a reader
refuses a version it does not speak, and refuses fields it does not know, so neither side
guesses. The daemon's messages (M4) join this document under the same version rule.
