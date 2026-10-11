#!/bin/sh
# Hotspots and change coupling from git history, offline (code-maat's
# revisions and coupling analyses without a JVM). Arguments go to git log,
# e.g. scripts/hotspots.sh --since=2026-09-01 -- internal ':!*_test.go'
# Prints the top 20 files by commits (with lines churned), then the
# top 20 file pairs changed together, with the share of the rarer file's
# commits that also touched the other.
set -eu
git log --no-merges --no-renames --numstat --format=tformat:@ "$@" | awk '
function flush(   i, j, a, b) {
    # ponytail: commits touching over 30 files are sweeps, not coupling; skip them (quadratic pairs).
    if (n > 30) { n = 0; return }
    for (i = 1; i <= n; i++) for (j = i + 1; j <= n; j++) {
        a = f[i]; b = f[j]; if (a > b) { a = f[j]; b = f[i] }
        pair[a SUBSEP b]++
    }
    n = 0
}
/^@/ { flush(); next }
NF == 3 {
    revs[$3]++; churn[$3] += ($1 == "-" ? 0 : $1 + $2); f[++n] = $3
}
END {
    flush()
    print "commits\tchurn\tfile"
    for (k in revs) print revs[k] "\t" churn[k] "\t" k | "sort -rn | head -20"
    close("sort -rn | head -20")
    print "\ntogether\tshare%\tfile pair"
    for (k in pair) {
        split(k, p, SUBSEP); m = revs[p[1]] < revs[p[2]] ? revs[p[1]] : revs[p[2]]
        if (pair[k] >= 3) print pair[k] "\t" int(100 * pair[k] / m) "\t" p[1] "  " p[2] | "sort -rn | head -20"
    }
}'
