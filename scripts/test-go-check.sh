#!/bin/sh
set -eu

repository_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd -P)
temporary=$(mktemp -d "${TMPDIR:-/tmp}/dark-factory-go-check-test.XXXXXX")
trap 'rm -rf "$temporary"' EXIT HUP INT TERM
fail() { echo "go-check test failed: $*" >&2; exit 1; }

fixture=$temporary/repository
bin=$temporary/bin
mkdir -p "$fixture/scripts" "$bin"
cp "$repository_root/scripts/go-check.sh" "$fixture/scripts/"
cat >"$fixture/scripts/locked-dependency-env.sh" <<'EOF'
df_locked_dependency_env() { :; }
df_verify_dependency_lockfiles() { :; }
df_go_mod_download() { :; }
EOF
cat >"$bin/go" <<'EOF'
#!/bin/sh
case "${1-} ${2-}" in
    "env GOVERSION") echo go1.27.0 ;;
    "mod verify"|"test "*|"vet "*) ;;
    *) echo "unexpected fake go invocation: $*" >&2; exit 1 ;;
esac
EOF
cat >"$bin/gofmt" <<'EOF'
#!/bin/sh
[ "$1" = -l ]
EOF
chmod 755 "$bin/go" "$bin/gofmt"
printf 'module fixture\n\ngo 1.27.0\n' >"$fixture/go.mod"
git -C "$fixture" init -q
git -C "$fixture" config user.name fixture
git -C "$fixture" config user.email fixture@example.invalid
git -C "$fixture" add .
git -C "$fixture" commit -qm base
git -C "$fixture" update-ref refs/remotes/origin/main HEAD

printf 'bad trailing space \n' >"$fixture/committed-whitespace.txt"
git -C "$fixture" add committed-whitespace.txt
git -C "$fixture" commit -qm whitespace
if (cd "$fixture" && env PATH="$bin:$PATH" DF_CI_GO="$bin/go" ./scripts/go-check.sh >"$temporary/failure" 2>&1); then
    fail "committed trailing whitespace passed"
fi
grep -Fq 'committed-whitespace.txt:1: trailing whitespace.' "$temporary/failure" ||
    fail "missing file/line diagnostic: $(cat "$temporary/failure")"

printf 'clean\n' >"$fixture/committed-whitespace.txt"
git -C "$fixture" add committed-whitespace.txt
git -C "$fixture" commit -qm clean
(cd "$fixture" && env PATH="$bin:$PATH" DF_CI_GO="$bin/go" ./scripts/go-check.sh >"$temporary/clean" 2>&1) ||
    fail "clean committed change failed: $(cat "$temporary/clean")"

echo "go-check fixture passed"
