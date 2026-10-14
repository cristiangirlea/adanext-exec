#!/bin/sh
# Fails when a tracked text file holds a character that makes source read differently from what
# it compiles to: the bidi controls U+202A-U+202E and U+2066-U+2069, and the zero-width
# characters U+200B-U+200D and U+FEFF. Write them as escapes instead (a backslash, u, and the
# four hex digits). Names each file and line.
# Usage: hidden-chars.sh [directory]   (default: the current directory, a git repository)
#
# The characters are built with printf octal escapes (UTF-8 bytes), so this file holds none of
# them raw and passes its own check.
set -u
dir=${1:-.}
tab=$(printf '\t')
found=0
tmp=$(mktemp) || exit 2
trap 'rm -f "$tmp"' EXIT

while read -r esc label; do
	seq=$(printf "$esc")
	# --null puts a NUL after the file name and after the line number, so a name with a colon in
	# it stays whole; the output goes through a file because a shell variable cannot hold a NUL.
	git -C "$dir" grep -nI -F --null -e "$seq" >"$tmp" 2>&1; code=$?
	case $code in
	0) ;;
	1) continue ;; # no match
	*)
		printf 'hidden-chars: git grep failed in %s: ' "$dir" >&2
		cat "$tmp" >&2
		exit 2
		;;
	esac
	found=1
	tr '\0' '\t' <"$tmp" | sed "s/^\\([^$tab]*\\)$tab\\([0-9]*\\)$tab.*/\\1:\\2: $label/" >&2
done <<'EOF'
\342\200\252 U+202A
\342\200\253 U+202B
\342\200\254 U+202C
\342\200\255 U+202D
\342\200\256 U+202E
\342\201\246 U+2066
\342\201\247 U+2067
\342\201\250 U+2068
\342\201\251 U+2069
\342\200\213 U+200B
\342\200\214 U+200C
\342\200\215 U+200D
\357\273\277 U+FEFF
EOF

if [ "$found" -ne 0 ]; then
	echo "hidden-chars: the files above hold invisible or bidi control characters; write them as escapes" >&2
	exit 1
fi
echo "hidden-chars: ok"
