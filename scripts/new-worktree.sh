#!/bin/sh
set -eu

usage() {
    echo "usage: scripts/new-worktree.sh <slug>" >&2
    echo "  creates .worktrees/<slug> on a new branch <slug>, from a freshly fetched origin default branch when available" >&2
}

slug="${1:-}"
if [ "$slug" = "-h" ] || [ "$slug" = "--help" ]; then
    usage
    exit 0
fi
if [ -z "$slug" ]; then
    usage
    exit 1
fi
case "$slug" in
    */*|.*)
        echo "invalid slug: $slug (no slashes, no leading dot)" >&2
        exit 1
        ;;
esac

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
target="$repository_root/.worktrees/$slug"
branch="$slug"

if [ -e "$target" ]; then
    echo "worktree path already exists: $target" >&2
    exit 1
fi
if git -C "$repository_root" show-ref --verify --quiet "refs/heads/$branch"; then
    echo "branch already exists: $branch" >&2
    exit 1
fi

if git -C "$repository_root" remote get-url origin >/dev/null 2>&1; then
    default_ref=$(git -C "$repository_root" symbolic-ref --quiet refs/remotes/origin/HEAD 2>/dev/null || printf '%s\n' refs/remotes/origin/main)
    default_branch=${default_ref#refs/remotes/origin/}
    git -C "$repository_root" fetch --no-tags origin "refs/heads/$default_branch:refs/remotes/origin/$default_branch"
    base="origin/$default_branch"
else
    base="main"
fi

git -C "$repository_root" -c core.hooksPath=/dev/null \
    worktree add -b "$branch" "$target" "$base"

cat <<EOF

Created $target on branch $branch (from $base).

Next steps:
  cd $target
  ./scripts/go-check.sh
  Run focused tests for the changed behavior.
  Publish $branch and open a PR.
EOF
