#!/bin/sh
set -eu

repository_root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
temporary=$(mktemp -d "${TMPDIR:-/tmp}/dark-factory-new-worktree-test.XXXXXX")
trap 'rm -rf "$temporary"' EXIT HUP INT TERM

fail() {
    echo "new-worktree test failed: $*" >&2
    exit 1
}

test_repository=$temporary/repository
configured_hooks=$temporary/configured-hooks
sentinel=$temporary/post-checkout-ran
mkdir -p "$test_repository/scripts" "$configured_hooks"
cp "$repository_root/scripts/new-worktree.sh" "$test_repository/scripts/new-worktree.sh"

git -C "$test_repository" init -q -b main
git -C "$test_repository" config user.name fixture
git -C "$test_repository" config user.email fixture@example.invalid
printf 'fixture\n' >"$test_repository/README.md"
git -C "$test_repository" add README.md
git -C "$test_repository" commit -q -m fixture

cat >"$configured_hooks/post-checkout" <<'EOF'
#!/bin/sh
set -eu
: >"$DARK_FACTORY_POST_CHECKOUT_SENTINEL"
EOF
chmod 700 "$configured_hooks/post-checkout"
git -C "$test_repository" config core.hooksPath "$configured_hooks"

DARK_FACTORY_POST_CHECKOUT_SENTINEL=$sentinel \
    "$test_repository/scripts/new-worktree.sh" fixture-worktree >/dev/null

[ ! -e "$sentinel" ] || fail "configured post-checkout hook executed"
git -C "$test_repository/.worktrees/fixture-worktree" diff --quiet \
    || fail "created worktree is dirty"
test "$(git -C "$test_repository/.worktrees/fixture-worktree" branch --show-current)" = fixture-worktree \
    || fail "created worktree has the wrong branch"

# A clone whose local origin/HEAD still says trunk follows the remote's current default branch.
remote=$temporary/remote.git
clone=$temporary/clone
git clone -q --bare "$test_repository" "$remote"
git -C "$remote" symbolic-ref HEAD refs/heads/trunk
git -C "$remote" branch -q trunk main
git clone -q "$remote" "$clone"
git -C "$clone" config user.name fixture
git -C "$clone" config user.email fixture@example.invalid
git -C "$clone" -c core.hooksPath=/dev/null checkout -q -b develop
printf 'develop\n' >"$clone/DEVELOP.md"
git -C "$clone" add DEVELOP.md
git -C "$clone" commit -q -m develop
git -C "$clone" push -q origin develop
git -C "$remote" symbolic-ref HEAD refs/heads/develop
test "$(git -C "$clone" symbolic-ref refs/remotes/origin/HEAD)" = refs/remotes/origin/trunk \
    || fail "fixture clone should still point origin/HEAD at trunk"
mkdir -p "$clone/scripts"
cp "$repository_root/scripts/new-worktree.sh" "$clone/scripts/new-worktree.sh"
"$clone/scripts/new-worktree.sh" remote-default >/dev/null
test "$(git -C "$clone/.worktrees/remote-default" rev-parse HEAD)" = "$(git -C "$remote" rev-parse refs/heads/develop)" \
    || fail "created worktree does not start from the remote's current default branch"

echo "new-worktree tests passed"
