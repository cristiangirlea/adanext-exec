# Claude Code through adanext-exec

Claude Code's Bash and PowerShell commands go through `adanext-exec`. Before each one, a
PreToolUse hook judges the command by your policy and your own Claude Code rules. It then
rewrites the command so that it runs through `adanext-exec run`, with the limits and the audit
line. [protocol.md](protocol.md) has the hook's exact input and answers.

## Install

1. Build the binary and put it in PATH, or note its absolute path:

   ```bash
   go build -o ~/bin/adanext-exec ./cmd/adanext-exec        # adanext-exec.exe on Windows
   ```

2. Add the hook to your user settings, `~/.claude/settings.json` (or
   `$CLAUDE_CONFIG_DIR/settings.json`), to cover every project:

   ```json
   {
     "hooks": {
       "PreToolUse": [
         {
           "matcher": "Bash|PowerShell",
           "hooks": [{"type": "command", "command": "adanext-exec hook claude-code || exit 2"}]
         }
       ]
     }
   }
   ```

   Use the binary's absolute path in place of `adanext-exec` if it is not in PATH. Add
   `--policy FILE` or `--profile NAME` after `claude-code` to pick a policy, as for `run`.

3. Optionally, write a policy (`<config dir>/adanext-exec/policy.json`; see the design).
   Without one, the built-in guard rules decide.

Keep the `|| exit 2`. Claude Code runs a command whose hook fails with any code other than 2
as if there were no hook. Without the `|| exit 2`, a missing or broken binary would let every
command through unjudged. With it, a missing binary blocks every Bash and PowerShell command
until it is fixed. `||` works in sh, bash, Git Bash, cmd and PowerShell 7, but not in Windows
PowerShell 5.1.

## What happens to a command

1. **Your Claude Code rules.** The hook reads the deny and ask rules for `Bash(...)` and
   `PowerShell(...)` from the settings files Claude Code reads:
   - `managed-settings.json` and `managed-settings.d/*.json`, in
     `C:\Program Files\ClaudeCode`, `/Library/Application Support/ClaudeCode` or
     `/etc/claude-code`;
   - your `settings.json`;
   - the project's `.claude/settings.json` and `.claude/settings.local.json`, and the
     repository root's `settings.local.json` when the session started in a subdirectory.

   The project is the one the session started in (`CLAUDE_PROJECT_DIR`). A managed
   `allowManagedPermissionRulesOnly` leaves only the managed rules.

   The rules match the original text, as Claude Code's documentation describes:
   - `*` and a trailing ` *` or `:*`;
   - every command of a compound one, including in `$( )`, backticks and loops;
   - past leading assignments, and past the wrappers `timeout`, `time`, `nice`, `nohup`,
     `stdbuf`, `command`, `builtin`, `noglob` and a bare `xargs`;
   - PowerShell's built-in aliases (`rm` is `Remove-Item`), with case ignored.

   A deny denies. An ask asks, unless the policy refuses. A settings file that exists but
   cannot be read denies every command, since its rules cannot be applied.
2. **The policy.** refuse denies, ask asks (the rewrite then carries the approval), and allow
   allows. The strictest of your rules and the policy wins.
3. **The rewrite.** The command becomes the binary itself running
   `run --hook-version V --shell bash|pwsh|powershell --script-b64 TEXT`, which no shell
   reparses. When you are asked, the reason shows the original command, since what you
   approve is the rewrite.
4. **`run`** judges the command again and runs it with its limits. It refuses (exit 126)
   when it is not the build that wrote the rewrite, for example after an upgrade between the
   two. Reinstall so that the hook and the binary are one build.

## What the hook cannot see

- **Policy that is not in a file.** Claude Code also takes managed policy from the Windows
  registry, macOS configuration profiles, MDM and the claude.ai console, and rules from
  `--settings` and `--disallowedTools`. A hook cannot read those. Claude Code still applies
  them, but to the rewrite, where a pattern such as `Bash(rm *)` no longer matches.
- **Allow rules.** Your allow rules do not lift the policy: a command the policy refuses or
  asks about stays refused or asked about. A command the policy allows runs without
  Claude Code's prompt, even when no allow rule covers it.
- **Matching.** The hook takes quotes off before it matches, so `git 'push'` matches
  `Bash(git push *)` here, which Claude Code itself would not match. Claude Code's
  "known-safe" assignment list for allow rules, which its documentation does not give, plays
  no part, since only deny and ask are applied.
- **The shell.** Under the Bash tool on Linux and macOS, the text runs under `bash`,
  whatever your login shell is.
