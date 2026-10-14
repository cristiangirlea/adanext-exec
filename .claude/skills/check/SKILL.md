---
name: check
description: Run what CI runs on this module (vet, build, tests, gofmt, the hook scripts) before a PR
---
Run the steps in order from the repository root and stop at the first failure. Report each
step's result in one line; on a failure, show the error text and fix it before re-running.

```bash
go vet ./...
go build ./...
go test -count=1 ./...
test -z "$(git ls-files -co --exclude-standard -- '*.go' | xargs -r gofmt -l)"
bash -n .claude/hooks/go-rules.sh && bash -n .claude/hooks/go-rules.test.sh
bash -n .claude/hooks/push-guard.sh && bash -n .claude/hooks/push-guard.test.sh
sh .claude/hooks/go-rules.test.sh
sh .claude/hooks/push-guard.test.sh
bash -n .claude/hooks/hidden-chars.sh && bash -n .claude/hooks/hidden-chars.test.sh
sh .claude/hooks/hidden-chars.test.sh
sh .claude/hooks/hidden-chars.sh
```

The gofmt step lists the repository's own Go files, so it does not walk worktrees; CI's `gofmt -l .` sees the same files on a clean checkout.

CI also runs `go test -race -count=1 ./...` on Linux. It needs cgo: run it only where
`go env CGO_ENABLED` prints 1 and a C compiler is present; on the owner's Windows PC report it as
"left to CI" and run `go test -count=3 ./...` instead. Nothing here may call a model API.

This skill does not merge: merging is through the PR, under AGENTS.md's conditions.
