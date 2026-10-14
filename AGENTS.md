# adanext-exec, the gate for agent shell commands

One Go module, standard library only: a Go SDK (`broker/`) and a binary, `adanext-exec`, that
every agent's shell commands go through on Windows, Linux and macOS. It decides (allow, ask,
refuse) under a policy, runs a command within limits (a timeout, a memory ceiling, a fair share
of heavy-command slots), kills the whole process tree when it must stop, and writes an audit
line. One of the `adanext-*` tools: harness (hrn) and AdaNext OS are its first users.

`docs/design.md` is the design: read it before taking an item. `PLAN.md` is the work queue and
the decisions log; don't reopen a recorded decision.

## Layout

| Path | Holds |
|---|---|
| `broker/` | The public Go SDK. Its API is the project's contract: change it additively. |
| `internal/` | Platform code (Job Objects, process groups, cgroups, rlimits), arriving with M1. |
| `cmd/adanext-exec/` | The binary: `run`, `check`, `serve`, arriving with M3. |
| `docs/` | `design.md`; `protocol.md` once the JSON protocol lands. |
| `.claude/hooks/` | gofmt, push and hidden-character checks; their tests run in CI. |

A package that does not exist yet arrives with its milestone in `PLAN.md`.

## Build and test

Go as pinned in `go.mod`, nothing else. What CI (`.github/workflows/ci.yml`, GitHub's hosted
Ubuntu and Windows runners: this repository is public) runs; run the same before a PR (`/check`):

```bash
go vet ./...
go build ./...
go test -count=1 ./...
go test -race -count=1 ./...                                  # Linux only in CI
test -z "$(gofmt -l .)"
sh .claude/hooks/go-rules.test.sh && sh .claude/hooks/push-guard.test.sh
sh .claude/hooks/hidden-chars.test.sh && sh .claude/hooks/hidden-chars.sh
```

`-race` needs cgo, which the owner's Windows PC lacks: run `go test -count=3 ./...` there and
leave `-race` to CI. Cross-compile the macOS code until a Mac runner exists:
`GOOS=darwin go vet ./...`. Never attach self-hosted runners: a public repository's pull
requests must never reach the owner's machines.

## Rules of this repository

- **Standard library only.** No `require` in `go.mod`, no `go.sum`. Windows calls go through
  `syscall.NewLazyDLL`, never `golang.org/x/sys`. This is a security component: every
  dependency would be trusted with every agent command.
- **Fail closed.** A command the broker cannot judge, place or contain is refused, and the
  answer says why. A limit a platform cannot enforce is reported, never pretended.
- **No network.** The daemon listens on an owner-only local socket; nothing leaves the machine.
- **Never elevated.** The binary refuses to run as administrator or root.
- **Tests start real processes** (the test binary re-run as a helper, `TestHelper*`), never an
  installed agent CLI, and clean up every process they start.
- Every PR updates `docs/` for a changed behaviour or protocol, and `PLAN.md` for the item it
  delivers (tick the box, a `Done: PR #n, <date>` line, deviations in the Decisions log); a fix
  without a PLAN.md item adds a Decisions-log entry naming the issue and PR.

## Working rules

- **A branch delivers what it is for, nothing else.** One purpose per branch (one issue or PLAN.md
  item; short name, as in `tree-kill-windows`) and one PR, whose description lists the files it
  touches and why. Larger work is a feature branch: each piece is a branch whose PR targets the
  feature branch under every condition below, and the feature branch reaches `main` through one PR
  reviewed for how the pieces fit. A problem found on the way is fixed here only if this branch
  caused it or it stops the change meeting its acceptance criteria; otherwise it gets an issue and
  a branch of its own. No refactors, renames or extras the item did not ask for.
- **Everything is tested before a merge**: the full local checks (`/check`), tests for every
  behaviour the change adds or alters, and CI green. Only then may an agent merge its PR, and only
  through the PR (`gh pr merge <n> --merge --delete-branch --match-head-commit <sha>`), when an
  independent review ran and its blocking findings are fixed, a security read of the diff has
  nothing open, and the head being merged is the commit all of that ran on. Merge the base branch
  in only for a conflict, or once before those final checks and review. What could not be tested
  is said in the PR and waits for the owner. Never auto-merge (`--auto`) or `--admin`, never push
  to `main`, never tag or release: hand over the command. Rulesets with no bypass protect `main`
  and `v*` tags; GitHub itself refuses a direct push, a squash or rebase merge, and a moved tag.
- Commit messages: the area first, lower case, then a plain sentence (`broker: kill the whole tree
  when a command times out`), one logical change per commit. No emoji, no AI attribution anywhere
  (no `Co-Authored-By`, `Assisted-by` or "Generated with", in commits, PR text, comments or
  docs). Commits carry the owner's identity, `cristiangirlea@gmail.com`: check
  `git config user.email` first.
- Self-review with `/code-review`. A finding is blocking when it is a defect this branch brings in,
  a missing test for what it changes, a broken house rule, or a security hole: fix each as its own
  commit, with the smallest change that closes it, and don't squash. Everything else (problems
  already on `main`, improvements, style) becomes an issue linked from the PR.
- Tests first, seen failing. On Windows the temp dir has 8.3 short names: compare paths after
  `filepath.EvalSymlinks`. Files are LF (`.gitattributes`).
- Secrets are never read, printed or committed. Small diffs: don't restyle what you didn't change.
  Not sure? Ask, with one proposal.
