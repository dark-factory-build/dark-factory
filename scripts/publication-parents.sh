#!/bin/sh
# Choose the publication parents for a worker head that may have integrated
# main. Prints "FROM DIFF_FROM MERGE_PARENT": FROM is the publish_commit
# expected_head_sha, DIFF_FROM is where the published diff starts, and
# MERGE_PARENT is its merge_parent_sha or "-" when the commit has one parent.
# usage: publication-parents.sh GIT_DIR FROM HEAD_COMMIT MAIN_HEAD
set -eu
git_dir=$1 from=$2 head=$3 main=$4
integrated=$(git --git-dir="$git_dir" merge-base "$head" "$main")
if git --git-dir="$git_dir" merge-base --is-ancestor "$integrated" "$from"; then
    echo "$from $from -"
elif git --git-dir="$git_dir" merge-base --is-ancestor "$from" "$integrated"; then
    echo "$integrated $integrated -"
else
    echo "$from $integrated $integrated"
fi
