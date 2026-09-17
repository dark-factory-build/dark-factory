#!/bin/sh
# A worker Change that integrated a same-line main prerequisite publishes as
# the merge it made and merges cleanly; the copied single-parent form does not.
set -eu
script_dir=$(CDPATH='' cd -- "$(dirname "$0")" && pwd)
root=$(mktemp -d "${TMPDIR:-/tmp}/df-publication-parents.XXXXXX")
trap 'rm -rf "$root"' EXIT HUP INT TERM
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.invalid GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.invalid
g() { git -C "$root" "$@"; }
git_dir="$root/.git"

# Publish the head through the App's contract: DIFF_FROM's tree plus the
# worker's diff from it, committed onto the stated parents.
publish() { # HEAD_COMMIT FROM DIFF_FROM MERGE_PARENT
    tmp_index="$root/.git/publish-index"
    rm -f "$tmp_index"
    GIT_INDEX_FILE="$tmp_index" g read-tree "$3"
    g diff-tree -r --no-renames "$3" "$1" | awk -F'\t' '{split($1, m, " "); print m[2] " " m[4] "\t" $2}' |
        GIT_INDEX_FILE="$tmp_index" g update-index --index-info
    tree=$(GIT_INDEX_FILE="$tmp_index" g write-tree)
    if [ "$4" = - ]; then g commit-tree "$tree" -p "$2" -m publish; else g commit-tree "$tree" -p "$2" -p "$4" -m publish; fi
}

g init -q -b main
printf 'one\ntwo\nthree\n' > "$root/shared"
g add shared && g commit -qm base
base=$(g rev-parse HEAD)
g checkout -qb worker
printf 'one\ntwo\n3\n' > "$root/shared"
g commit -qam work
# The prerequisite lands on main on the line next to the worker's.
g checkout -q main
printf 'one\n2\nthree\n' > "$root/shared"
g commit -qam prerequisite
main=$(g rev-parse HEAD)

# First publication of a head that integrated main: the integrated commit is
# the valid current base and the published commit has one parent.
g checkout -q worker
g merge -q --no-edit main > /dev/null 2>&1 || true
printf 'one\n2\n3\n' > "$root/shared"
g add shared && g commit -qm integrate
head=$(g rev-parse HEAD)
[ "$(g rev-parse HEAD^2)" = "$main" ]
set -- $("$script_dir/publication-parents.sh" "$git_dir" "$base" "$head" "$main" 0)
[ "$1 $2 $3" = "$main $main -" ] || { echo "first publication chose: $*" >&2; exit 1; }
published=$(publish "$head" "$1" "$2" "$3")
[ "$(g rev-parse "$published^{tree}")" = "$(g rev-parse "$head^{tree}")" ]
g merge-tree --write-tree "$main" "$published" > /dev/null

# A follow-up after an earlier publication that predates the prerequisite:
# the copied single-parent form conflicts, the merge form does not.
previous=$(g commit-tree "$(g rev-parse "$base^{tree}")" -p "$base" -m earlier)
set -- $("$script_dir/publication-parents.sh" "$git_dir" "$previous" "$head" "$main" 1)
[ "$1 $2 $3" = "$previous $main $main" ] || { echo "follow-up chose: $*" >&2; exit 1; }
copied=$(publish "$head" "$previous" "$previous" -)
[ "$(g rev-parse "$copied^{tree}")" = "$(g rev-parse "$head^{tree}")" ]
if g merge-tree --write-tree "$main" "$copied" > /dev/null 2>&1; then
    echo "the copied single-parent publication merged cleanly, so this test proves nothing" >&2; exit 1
fi
published=$(publish "$head" "$1" "$2" "$3")
[ "$(g rev-parse "$published^{tree}")" = "$(g rev-parse "$head^{tree}")" ]
[ "$(g rev-parse "$published^1")" = "$previous" ] && [ "$(g rev-parse "$published^2")" = "$main" ]
g merge-tree --write-tree "$main" "$published" > /dev/null
[ "$(g diff --numstat "$main" "$published" | wc -l | tr -d ' ')" = 1 ]

# Nothing integrated: the branch head stays the only parent and diff base.
set -- $("$script_dir/publication-parents.sh" "$git_dir" "$previous" "$(g rev-parse worker~1)" "$base" 1)
[ "$1 $2 $3" = "$previous $previous -" ] || { echo "plain publication chose: $*" >&2; exit 1; }

# An existing branch at an ancestor of integrated main keeps that branch as
# the exact-head precondition and carries main as the merge parent.
set -- $("$script_dir/publication-parents.sh" "$git_dir" "$base" "$head" "$main" 1)
[ "$1 $2 $3" = "$base $main $main" ] || { echo "existing ancestor chose: $*" >&2; exit 1; }
echo "publication parents: ok"
