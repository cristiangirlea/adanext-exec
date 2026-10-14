#!/bin/sh
# After an edit: gofmt must print nothing, as CI requires.
cd "${CLAUDE_PROJECT_DIR:-.}" || exit 0
command -v gofmt >/dev/null 2>&1 || exit 0
# The file list comes from git: outside a repository there is nothing to check.
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || exit 0
# Tracked and new files only, so a worktree under the repository is not walked;
# NUL-separated, so a name with a space stays one name. gofmt's own errors (a file it cannot
# parse) stay visible and fail the hook: xargs exits non-zero when any gofmt run does.
unformatted=$(git ls-files -z -co --exclude-standard -- '*.go' | xargs -0 -r gofmt -l) || {
  printf 'gofmt failed on a file named above: fix it before going on.\n' >&2
  exit 2
}
if [ -n "$unformatted" ]; then
  printf 'gofmt would change these files (run gofmt -w on them):\n%s\n' "$unformatted" >&2
  exit 2
fi
exit 0
