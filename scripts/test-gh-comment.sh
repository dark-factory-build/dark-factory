#!/bin/sh
set -eu

repository_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd -P)
script=$repository_root/scripts/gh-comment.sh
temporary=$(mktemp -d "${TMPDIR:-/tmp}/dark-factory-gh-comment.XXXXXX")
trap 'rm -rf "$temporary"' EXIT HUP INT TERM

fail() {
    echo "gh-comment test failed: $*" >&2
    exit 1
}

mkdir "$temporary/bin"
cat >"$temporary/bin/gh" <<'EOF'
#!/bin/sh
touch "$GH_COMMENT_GH_CALLED"
printf '%s\n' "$@" >"$GH_COMMENT_GH_ARGS"
EOF
chmod 755 "$temporary/bin/gh"
export GH_COMMENT_GH_CALLED=$temporary/gh-called
export GH_COMMENT_GH_ARGS=$temporary/gh-args

expect_invalid() {
    rm -f "$GH_COMMENT_GH_CALLED"
    if PATH="$temporary/bin:$PATH" "$script" "$@" >"$temporary/stdout" 2>"$temporary/stderr"; then
        fail "accepted invalid arguments: $*"
    else
        status=$?
    fi
    [ "$status" -eq 2 ] || fail "invalid arguments did not return usage status: $*"
    [ ! -e "$GH_COMMENT_GH_CALLED" ] || fail "invoked gh for invalid arguments: $*"
    grep -F 'usage:' "$temporary/stderr" >/dev/null || fail "did not print usage: $*"
}

expect_invalid
expect_invalid 0
expect_invalid 42 extra-body
expect_invalid --body-file
expect_invalid --body-file "$temporary/missing" 42
expect_invalid --repo owner/repo/extra 42

body=$temporary/body
printf '%s\n' 'comment body from a file' >"$body"
PATH="$temporary/bin:$PATH" "$script" --repo owner/repo --body-file "$body" 42
[ -e "$GH_COMMENT_GH_CALLED" ] || fail "valid invocation did not call gh"
grep -Fx 'issue' "$GH_COMMENT_GH_ARGS" >/dev/null || fail "wrong gh command"
grep -Fx 'comment' "$GH_COMMENT_GH_ARGS" >/dev/null || fail "wrong gh subcommand"
grep -Fx '42' "$GH_COMMENT_GH_ARGS" >/dev/null || fail "wrong issue or PR number"
grep -Fx -- '--body-file' "$GH_COMMENT_GH_ARGS" >/dev/null || fail "body was not passed as a file"
grep -Fx "$body" "$GH_COMMENT_GH_ARGS" >/dev/null || fail "wrong body file"
if grep -Fx 'comment body from a file' "$GH_COMMENT_GH_ARGS" >/dev/null; then
    fail "body text was passed as a gh argument"
fi

echo "gh comment argument validation passed"
