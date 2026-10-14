#!/bin/sh
# Checks go-rules.sh in the cases that matter: a repository with no Go files, an unformatted
# file, a file gofmt cannot parse, and a directory that is not a git repository.
set -u
hook="$(cd "$(dirname "$0")" && pwd)/go-rules.sh"
fail() { printf 'go-rules.test: %s\n' "$1" >&2; exit 1; }
tmp=$(mktemp -d) || fail "mktemp failed"
trap 'rm -rf "$tmp"' EXIT

# 1. No Go files: a no-op.
mkdir "$tmp/empty" && git -C "$tmp/empty" init -q
CLAUDE_PROJECT_DIR="$tmp/empty" sh "$hook" || fail "no Go files: expected exit 0"

# 2. An unformatted file is named and the hook fails.
mkdir "$tmp/unformatted" && git -C "$tmp/unformatted" init -q
printf 'package main\nfunc main(){}\n' > "$tmp/unformatted/main.go"
out=$(CLAUDE_PROJECT_DIR="$tmp/unformatted" sh "$hook" 2>&1); code=$?
[ "$code" -eq 2 ] || fail "unformatted: exit $code, want 2"
case "$out" in *main.go*) ;; *) fail "unformatted: the output does not name main.go: $out" ;; esac

# 3. A file gofmt cannot parse fails too, with gofmt's error shown.
mkdir "$tmp/broken" && git -C "$tmp/broken" init -q
printf 'package main\nfunc {\n' > "$tmp/broken/main.go"
out=$(CLAUDE_PROJECT_DIR="$tmp/broken" sh "$hook" 2>&1); code=$?
[ "$code" -eq 2 ] || fail "broken: exit $code, want 2"
case "$out" in *expected*) ;; *) fail "broken: gofmt's error is not shown: $out" ;; esac

# 4. Outside a git repository the hook does nothing, even with an unformatted file there.
mkdir "$tmp/plain"
printf 'package main\nfunc main(){}\n' > "$tmp/plain/main.go"
CLAUDE_PROJECT_DIR="$tmp/plain" sh "$hook" || fail "outside git: expected exit 0"

# 5. A file name with a space is one name: formatted, it passes; unformatted, it is named whole.
mkdir "$tmp/spaced" && git -C "$tmp/spaced" init -q
printf 'package main\n\nfunc main() {}\n' > "$tmp/spaced/my file.go"
CLAUDE_PROJECT_DIR="$tmp/spaced" sh "$hook" || fail "space, formatted: expected exit 0"
printf 'package main\nfunc main(){}\n' > "$tmp/spaced/my file.go"
out=$(CLAUDE_PROJECT_DIR="$tmp/spaced" sh "$hook" 2>&1); code=$?
[ "$code" -eq 2 ] || fail "space, unformatted: exit $code, want 2"
case "$out" in *"my file.go"*) ;; *) fail "space, unformatted: the output does not name the file whole: $out" ;; esac

echo "go-rules.test: ok"
