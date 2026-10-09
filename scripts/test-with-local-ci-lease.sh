#!/bin/sh
set -eu

repository_root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
temporary=$(mktemp -d "${TMPDIR:-/tmp}/dark-factory-local-ci-lease-test.XXXXXX")
trap 'rm -rf "$temporary"' EXIT HUP INT TERM

fail() {
    echo "local-ci lease test failed: $*" >&2
    exit 1
}

mkdir "$temporary/bin"
cat >"$temporary/bin/lockf" <<'EOF'
#!/bin/sh
set -eu
[ "$1" = -k ] || exit 2
shift 2
exec "$@"
EOF
chmod 755 "$temporary/bin/lockf"

lease_dir=$temporary/lease
PATH="$temporary/bin:$PATH" \
    DARK_FACTORY_LOCAL_CI_DIRECTORY="$lease_dir" \
    DARK_FACTORY_LOCAL_CI_LEASE_HELD= \
    /bin/sh "$repository_root/scripts/with-local-ci-lease.sh" \
    /bin/sh -c 'exit 7' || status=$?
[ "${status-0}" -eq 7 ] || fail "command status was not preserved"

log=$lease_dir/.dark-factory-local-ci.lock/lease.log
[ -f "$log" ] || fail "lease log was not written"
[ "$(wc -l <"$log")" -eq 1 ] || fail "lease log did not contain one entry"
line=$(cat "$log")
case "$line" in
    command=/bin/sh\ -c\ exit\ 7\ requested_at=*\ acquired_at=*\ released_at=*) ;;
    *) fail "unexpected lease log entry: $line" ;;
esac

requested=${line#*requested_at=}; requested=${requested%% acquired_at=*}
acquired=${line#*acquired_at=}; acquired=${acquired%% released_at=*}
released=${line#*released_at=}
for timestamp in "$requested" "$acquired" "$released"; do
    case "$timestamp" in
        ????-??-??T??:??:??Z) ;;
        *) fail "invalid timestamp: $timestamp" ;;
    esac
done
