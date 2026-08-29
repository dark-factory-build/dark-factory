#!/bin/sh
set -eu

repository_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd -P)
temporary=$(/usr/bin/mktemp -d "${TMPDIR:-/tmp}/dark-factory-go-e2e-tools.XXXXXX")
trap '/bin/rm -rf "$temporary"' EXIT HUP INT TERM

fail() {
    echo "Go E2E tool test failed: $*" >&2
    exit 1
}

fake_go="$temporary/fake-go"
fake_node="$temporary/fake-node"
fake_corepack="$temporary/fake-corepack"
log="$temporary/tools.log"

make_fake_tool() {
    tool=$1
    output=$2
    /bin/cat >"$tool" <<EOF
#!/bin/sh
printf '%s\\n' '$output' >>"$log"
EOF
    /bin/chmod 700 "$tool"
}

make_fake_tool "$fake_go" go-used
make_fake_tool "$fake_node" node-used
/bin/cat >"$fake_corepack" <<EOF
#!/bin/sh
printf 'corepack-used:%s\n' "\${CI-unset}" >>"$log"
EOF
/bin/chmod 700 "$fake_corepack"
. "$repository_root/scripts/go-e2e-tools.sh"
if go_e2e_resolve_tool go relative-go >/dev/null 2>&1; then
    fail "relative tool path was accepted"
fi

# Exercise the real browser wrapper in both arms with no tool available from
# PATH. The fakes make build, package-manager and test commands observable
# without compiling Go or installing packages.
for browser_mode in serial race; do
    : >"$log"
    if [ "$browser_mode" = serial ]; then
        set --
    else
        set -- --race
    fi
    /usr/bin/env -i \
        PATH=/usr/bin:/bin HOME="$temporary" TMPDIR="$temporary" \
        DARK_FACTORY_E2E_GO="$fake_go" \
        DARK_FACTORY_E2E_NODE="$fake_node" \
        DARK_FACTORY_E2E_COREPACK="$fake_corepack" \
        /bin/sh "$repository_root/scripts/go-browser-e2e.sh" "$@" \
        >"$temporary/browser-$browser_mode.log"
    [ "$(/usr/bin/grep -c -F -x go-used "$log")" -eq 3 ] \
        || fail "browser $browser_mode mode did not use injected Go exactly three times"
    [ "$(/usr/bin/grep -c -F -x corepack-used:true "$log")" -eq 2 ] \
        || fail "browser $browser_mode mode did not use injected Corepack exactly twice"
    if /usr/bin/grep -F -x node-used "$log" >/dev/null; then
        fail "browser $browser_mode mode executed Node outside the Go test"
    fi
    /usr/bin/grep -F "go-browser-e2e: PASS" "$temporary/browser-$browser_mode.log" >/dev/null \
        || fail "browser $browser_mode mode did not complete"
done

probe="$temporary/probe"
/bin/cat >"$probe" <<'EOF'
#!/bin/sh
set -eu

helper=$1
log=$2
expected_go=$3
expected_node=$4
expected_corepack=$5
. "$helper"

go=$(go_e2e_resolve_tool go "${DARK_FACTORY_E2E_GO-}")
node=$(go_e2e_resolve_tool node "${DARK_FACTORY_E2E_NODE-}")
corepack=$(go_e2e_resolve_tool corepack "${DARK_FACTORY_E2E_COREPACK-}")
[ "$go" = "$expected_go" ]
[ "$node" = "$expected_node" ]
[ "$corepack" = "$expected_corepack" ]
"$go"
"$node"
"$corepack"
[ "$(/usr/bin/grep -c . "$log")" -eq 3 ]
EOF
/bin/chmod 700 "$probe"

# The production stage has no ambient Homebrew or Node directory in PATH. The
# only usable tools are the absolute values injected by go_gate_e2e_stage.
PATH=/usr/bin:/bin
export PATH
go_gate_env=/usr/bin/env
go_gate_go=$fake_go
go_gate_node=$fake_node
go_gate_corepack=$fake_corepack
go_gate_stage() {
    [ "$1" = 7 ] || fail "stage timeout was not preserved"
    shift
    "$@"
}
: >"$log"
export CI=true
go_gate_e2e_stage 7 "$probe" "$repository_root/scripts/go-e2e-tools.sh" \
    "$log" "$fake_go" "$fake_node" "$fake_corepack"
/usr/bin/grep -F -x go-used "$log" >/dev/null || fail "injected Go was not executed"
/usr/bin/grep -F -x node-used "$log" >/dev/null || fail "injected Node was not executed"
/usr/bin/grep -F -x corepack-used:true "$log" >/dev/null || fail "injected Corepack was not executed"

for e2e_script in go-browser-e2e.sh go-daemon-e2e.sh go-service-e2e.sh; do
    path="$repository_root/scripts/$e2e_script"
    if /usr/bin/grep -Eq '(^|[[:space:]])go[[:space:]]+(build|test)' "$path"; then
        fail "$e2e_script regressed to a bare Go command"
    fi
done
if /usr/bin/grep -Eq '(^|[[:space:]])corepack[[:space:]]+pnpm' \
    "$repository_root/scripts/go-browser-e2e.sh"; then
    fail "go-browser-e2e.sh regressed to a bare Corepack command"
fi
for expected in \
    'go=$(go_e2e_resolve_tool go "${DARK_FACTORY_E2E_GO-}")' \
    'node=$(go_e2e_resolve_tool node "${DARK_FACTORY_E2E_NODE-}")' \
    'corepack=$(go_e2e_resolve_tool corepack "${DARK_FACTORY_E2E_COREPACK-}")'; do
    /usr/bin/grep -F "$expected" "$repository_root/scripts/go-browser-e2e.sh" >/dev/null \
        || fail "browser wrapper lost explicit tool resolution: $expected"
done
for e2e_script in go-daemon-e2e.sh go-service-e2e.sh; do
    /usr/bin/grep -F 'go=$(go_e2e_resolve_tool go "${DARK_FACTORY_E2E_GO-}")' \
        "$repository_root/scripts/$e2e_script" >/dev/null \
        || fail "$e2e_script lost explicit Go resolution"
done

echo "Go E2E tool tests passed"
