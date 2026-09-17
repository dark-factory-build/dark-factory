#!/bin/sh
# Choose the publication parents for a worker head that may have integrated
# main. Prints "FROM DIFF_FROM MERGE_PARENT": FROM is the publish_commit
# expected_head_sha, DIFF_FROM is where the published diff starts, and
# MERGE_PARENT is its merge_parent_sha or "-" when the commit has one parent.
# usage: publication-parents.sh GIT_DIR FROM HEAD_COMMIT MAIN_HEAD BRANCH_EXISTS
set -eu
git_dir=$1 from=$2 head=$3 main=$4 branch_exists=$5
integrated=$(git --git-dir="$git_dir" merge-base "$head" "$main")
case "$branch_exists" in
    0)
        if git --git-dir="$git_dir" merge-base --is-ancestor "$integrated" "$from"; then
            echo "$from $from -"
        else
            echo "$integrated $integrated -"
        fi
        ;;
    1)
        if git --git-dir="$git_dir" merge-base --is-ancestor "$integrated" "$from"; then
            echo "$from $from -"
        else
            echo "$from $integrated $integrated"
        fi
        ;;
    *)
        echo "branch_exists must be 0 or 1" >&2
        exit 2
        ;;
esac
