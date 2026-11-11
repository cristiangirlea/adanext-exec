# Shaping on real outputs

[← design](design.md#shaping-output-m9-1)

Measured on 2026-11-11 with `adanext-exec run --shape` (threshold 30,000 bytes), built from M9c's
branch, on Windows under Git Bash. Each command ran through the binary; "in" is the original's
size from the audit line, "out" what the binary wrote, the original's last line included (about
130 bytes).

| Command | Kind | In | Out | What came out |
|---|---|---:|---:|---|
| `go test -v` of a table test with 3,000 subtests, one failing | Go test, failing | 214,119 | 354 | A summary: the counts, the failing test with its message, the package lines |
| `git log --format='%h%x09%ad%x09%s'` over 1,500 commits | Table | 134,690 | 3,590 | A summary: the size, the first and last rows, every row holding an error word |
| `go test -v` of this repository's seven `internal` packages | Go test, passing | 55,453 | 30,163 | Head and tail: output the checks cannot attribute to one test |
| `gh run view --log` of one CI run | Other (each line starts with the job's name) | 102,788 | 30,164 | Head and tail: lines with an error word fall in the middle |
| `gh api` of 60 issues | JSON, on one line | 355,978 | 30,166 | Head and tail: the one line holds an error word, so it must stay whole |
| `git log -p` of 8 commits | Other (a diff) | 51,724 | 30,163 | Head and tail: lines with an error word fall in the middle |
| `go build -a -x` | Other (a build's commands) | 214,115 | 30,165 | Head and tail: lines with an error word fall in the middle |

Summing the seven, 1,128,867 bytes went in and 154,765 came out (14%); the two summaries alone
took 348,809 bytes to 3,944 (1%). An output past the threshold costs at most about 30 KB either
way, and every original read back whole: a 50,000-byte range of the JSON original, read through
`run --shape` with the command its last line names, came back byte for byte.

What it says about the summarizer: it is conservative by design. A summary must keep every line
holding "error", "FAIL" or "panic" as it is, so the head and tail of plain terminal output fail
whenever such a line sits in the part they leave out, and a single long line holding one cannot be
summarized at all; test output it cannot attribute line by line is refused too. Each of those
falls back to the head and tail, never to a summary that could lose a fact. Keeping such lines in
the head-and-tail summary would shape more of these outputs; that is a change to the summarizer
that harness's copy would take too.
