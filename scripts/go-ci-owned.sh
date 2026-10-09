#!/bin/sh
set -eu

usage="usage: scripts/go-ci-owned.sh [--client-built] [daemon|packages|source|--affected BASE]"
client_built=
shard=
affected_base=
while [ "$#" -gt 0 ]; do
    case "$1" in
        --client-built) client_built=$1 ;;
        daemon|packages|source) shard=$1 ;;
        --affected) [ "$#" -ge 2 ] || { echo "$usage" >&2; exit 2; }; shard=affected; affected_base=$2; shift ;;
        *) echo "$usage" >&2; exit 2 ;;
    esac
    shift
done
# With no shard every stage runs; CI runs each shard on its own Mac. The
# affected shard runs only stages whose package tests depend on a package
# changed since BASE: a pull request's own check before it can be queued.
in_shard() { [ -z "$shard" ] || [ "$shard" = "$1" ]; }
runs() {
    stage=$1
    shift
    if [ "$shard" = affected ]; then
        for package; do
            printf '%s\n' "$affected_packages" | /usr/bin/grep -qxF "$package" && return 0
        done
        return 1
    fi
    in_shard "$stage"
}
script_dir=$(CDPATH= cd -- "$(/usr/bin/dirname "$0")" && pwd -P)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd -P)
CDPATH= cd -- "$repository_root"
. "$script_dir/local-ci-environment.sh"
. "$script_dir/go-gate-environment.sh"

go_gate_supervisor_pid=
go_gate_signal() {
    signal=$1
    trap - EXIT HUP INT TERM
    go_gate_join_supervisor
    exit $((128 + signal))
}
trap 'go_gate_signal 1' HUP
trap 'go_gate_signal 2' INT
trap 'go_gate_signal 15' TERM

export GOTOOLCHAIN=local
go=${DF_CI_GO-}
[ -n "$go" ] || {
    echo "go-ci: Go is unavailable; add Go's bin directory to factoryd --tool-path (and its install root to --toolchain-read-roots)" >&2
    exit 1
}
module=
affected_packages=
if [ "$shard" = affected ]; then
    module=$("$go" list -m)
    changed=$(git diff --name-only "$affected_base" HEAD --)
    if printf '%s\n' "$changed" | /usr/bin/grep -qxE 'go\.(mod|sum)'; then
        affected_packages=$("$go" list ./...)
    else
        # A changed file belongs to the nearest enclosing Go package; files
        # outside every package (docs, scripts, web) select no Go tests.
        changed_packages=$(printf '%s\n' "$changed" | while IFS= read -r file; do
            directory=$(/usr/bin/dirname -- "$file")
            while [ "$directory" != . ] && ! /bin/ls "$directory"/*.go >/dev/null 2>&1; do
                directory=$(/usr/bin/dirname -- "$directory")
            done
            [ "$directory" = . ] || printf '%s/%s\n' "$module" "$directory"
        done)
        affected_packages=$("$go" list -test -f '{{.ImportPath}}|{{join .Deps "|"}}' ./... |
            DF_CHANGED_PACKAGES=$changed_packages /usr/bin/awk -F'|' '
            BEGIN { n = split(ENVIRON["DF_CHANGED_PACKAGES"], c, "\n"); for (i = 1; i <= n; i++) if (c[i] != "") want[c[i]] = 1 }
            $1 ~ /\.test$/ {
                package = substr($1, 1, length($1) - 5)
                hit = package in want
                for (i = 2; i <= NF && !hit; i++) { dep = $i; sub(/ \[.*$/, "", dep); if (dep in want) hit = 1 }
                if (hit) print package
            }')
    fi
    echo "go-ci: affected packages:" $affected_packages
fi
# These three packages own process boundaries that have demonstrated cross-
# package scheduling sensitivity. Keep each causal stage uncached and isolated;
# every other package is discovered below so a new package cannot silently skip
# tests. The five ordinary packages and internal/e2e have their own gates.
if runs source "$module/internal/change"; then
    echo "go-ci: Git boundary resource census"
    go_gate_stage 1200 "$go" test -short -timeout=20m -count=1 ./internal/change
fi

if runs source "$module/internal/changeworker"; then
    echo "go-ci: Change worker process tests"
    go_gate_stage 1200 "$go" test -short -timeout=20m -count=1 ./internal/changeworker
fi

if runs daemon "$module/internal/daemon"; then
    echo "go-ci: daemon process tests"
    go_gate_stage 1200 "$go" test -short -timeout=20m -count=1 ./internal/daemon
fi

if in_shard packages || [ "$shard" = affected ]; then
echo "go-ci: process-sensitive Go tests"
set --
packages=$("$go" list ./...)
for package in $packages; do
    case "$package" in
        github.com/dark-factory-build/dark-factory/internal/browserprotocol|\
        github.com/dark-factory-build/dark-factory/internal/provider|\
        github.com/dark-factory-build/dark-factory/internal/opgraph|\
        github.com/dark-factory-build/dark-factory/internal/change|\
        github.com/dark-factory-build/dark-factory/internal/changeworker|\
        github.com/dark-factory-build/dark-factory/internal/daemon|\
        github.com/dark-factory-build/dark-factory/internal/e2e)
            ;;
        *) if runs packages "$package"; then set -- "$@" "$package"; fi ;;
    esac
done
if [ "$#" -gt 0 ]; then
    go_gate_stage 1200 "$go" test -short -timeout=20m -count=1 "$@"
fi
fi

if runs source "$module/internal/e2e"; then
    echo "go-ci: browser, daemon and runner E2E"
    go_gate_stage 1500 "$script_dir/go-e2e.sh" all ${client_built:+"$client_built"}
fi
echo "go-ci: PASS"
