#!/bin/sh
# Checks hidden-chars.sh in the cases that matter: a clean repository, an escape written out in
# source text, every forbidden character raw (each is named with its line), a file name with a
# space, a binary file, an untracked file, and a directory that is not a git repository. Like
# the script, this file builds the characters with printf octal escapes and holds none raw.
set -u
check="$(cd "$(dirname "$0")" && pwd)/hidden-chars.sh"
fail() { printf 'hidden-chars.test: %s\n' "$1" >&2; exit 1; }
tmp=$(mktemp -d) || fail "mktemp failed"
trap 'rm -rf "$tmp"' EXIT

# repo NAME: a new empty git repository at $tmp/NAME.
repo() { mkdir "$tmp/$1" && git -C "$tmp/$1" init -q || fail "git init failed"; }
# track NAME: stage everything in $tmp/NAME so git ls-files lists it.
track() { git -C "$tmp/$1" add -A >/dev/null 2>&1 || fail "git add failed"; }

# 1. A clean repository passes.
repo clean
printf 'package main\n\nfunc main() {}\n' > "$tmp/clean/main.go"
track clean
sh "$check" "$tmp/clean" >/dev/null 2>&1 || fail "clean: expected exit 0"

# 2. An escape written out as source text (a backslash, u, 202e) is not the character itself.
repo escaped
printf 'const hostile = "a\\%s%s"\n' u 202e > "$tmp/escaped/main.go"
track escaped
sh "$check" "$tmp/escaped" >/dev/null 2>&1 || fail "escaped: expected exit 0"

# 3. A raw U+202E fails and the file and line are named; the character is not echoed.
repo raw
printf 'package main\n\nconst hostile = "a\342\200\256b"\n' > "$tmp/raw/main.go"
track raw
out=$(sh "$check" "$tmp/raw" 2>&1); code=$?
[ "$code" -eq 1 ] || fail "raw U+202E: exit $code, want 1"
case "$out" in *main.go:3:*U+202E*) ;; *) fail "raw U+202E: the output does not name main.go:3: $out" ;; esac
printf '%s\n' "$out" | LC_ALL=C grep -q "$(printf '\342\200\256')" && fail "raw U+202E: the output echoes the character"

# 4. Every forbidden character fails, and a neighbouring one that is allowed does not.
for entry in '\342\200\252 U+202A' '\342\200\253 U+202B' '\342\200\254 U+202C' '\342\200\255 U+202D' \
	'\342\200\256 U+202E' '\342\201\246 U+2066' '\342\201\247 U+2067' '\342\201\250 U+2068' \
	'\342\201\251 U+2069' '\342\200\213 U+200B' '\342\200\214 U+200C' '\342\200\215 U+200D' \
	'\357\273\277 U+FEFF'; do
	esc=${entry% *}
	label=${entry#* }
	repo each
	printf 'x\n%s\n' "$(printf "a${esc}b")" > "$tmp/each/f.txt"
	track each
	out=$(sh "$check" "$tmp/each" 2>&1); code=$?
	[ "$code" -eq 1 ] || fail "$label: exit $code, want 1"
	case "$out" in *f.txt:2:*"$label"*) ;; *) fail "$label: the output does not name f.txt:2 and $label: $out" ;; esac
	rm -rf "$tmp/each"
done
repo allowed
# U+200E (left-to-right mark), U+2065 (unassigned, next to the isolates) and U+00E9 are fine.
printf 'a\342\200\216b\n\342\201\245\n\303\251\n' > "$tmp/allowed/f.txt"
track allowed
sh "$check" "$tmp/allowed" >/dev/null 2>&1 || fail "allowed neighbours: expected exit 0"

# 5. Several files and lines are all listed, and a file name with a space is named whole.
repo several
printf 'ok\na\342\200\213b\n' > "$tmp/several/my file.txt"
printf '\357\273\277first line\n' > "$tmp/several/bom.txt"
track several
out=$(sh "$check" "$tmp/several" 2>&1); code=$?
[ "$code" -eq 1 ] || fail "several: exit $code, want 1"
case "$out" in *"my file.txt:2:"*) ;; *) fail "several: 'my file.txt:2' is not named whole: $out" ;; esac
case "$out" in *bom.txt:1:*) ;; *) fail "several: bom.txt:1 is not named: $out" ;; esac

# 6. A binary file (it holds a NUL byte) is not searched.
repo binary
printf 'a\000\342\200\256b\n' > "$tmp/binary/blob.bin"
track binary
sh "$check" "$tmp/binary" >/dev/null 2>&1 || fail "binary: expected exit 0"

# 7. Only tracked files are checked: a file git does not know about is left alone.
repo untracked
printf 'package main\n' > "$tmp/untracked/main.go"
track untracked
printf 'a\342\200\256b\n' > "$tmp/untracked/new.txt"
sh "$check" "$tmp/untracked" >/dev/null 2>&1 || fail "untracked: expected exit 0"

# 8. A directory that is not a git repository is an error (exit 2), not a pass.
mkdir "$tmp/plain"
out=$(sh "$check" "$tmp/plain" 2>&1); code=$?
[ "$code" -eq 2 ] || fail "not a repository: exit $code, want 2"

echo "hidden-chars.test: ok"
