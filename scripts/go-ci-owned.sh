#!/bin/sh
set -eu

usage="usage: scripts/go-ci-owned.sh [--client-built] [--cacheable|daemon|packages|source|--affected BASE]"
client_built=
shard=
affected_base=
cacheable_only=
while [ "$#" -gt 0 ]; do
    case "$1" in
        --client-built) client_built=$1 ;;
        --cacheable) cacheable_only=1 ;;
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
export GOTOOLCHAIN=local
go=${DF_CI_GO-}
[ -n "$go" ] || go=$(command -v go 2>/dev/null || true)
[ -n "$go" ] || {
    echo "go-ci: Go is unavailable; add Go's bin directory to factoryd --tool-path (and its install root to --toolchain-read-roots)" >&2
    exit 1
}
module=
affected_packages=
module=$("$go" list -m)
if [ "$shard" = affected ]; then
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
# Tests in these packages cross a process boundary (or deliberately inspect
# one), so Go's result cache cannot see all of their inputs. Keep them uncached.
# The remaining packages are cacheable and are deliberately discovered below so
# a new package cannot silently skip tests.
process_sensitive_packages='
github.com/dark-factory-build/dark-factory/internal/api
github.com/dark-factory-build/dark-factory/internal/buildinfo
github.com/dark-factory-build/dark-factory/internal/change
github.com/dark-factory-build/dark-factory/internal/changeworker
github.com/dark-factory-build/dark-factory/internal/daemon
github.com/dark-factory-build/dark-factory/internal/e2e
github.com/dark-factory-build/dark-factory/internal/install
github.com/dark-factory-build/dark-factory/internal/kernel
github.com/dark-factory-build/dark-factory/internal/opgraph
github.com/dark-factory-build/dark-factory/internal/provider
github.com/dark-factory-build/dark-factory/internal/review
github.com/dark-factory-build/dark-factory/internal/runner'
process_sensitive_packages="$process_sensitive_packages
github.com/dark-factory-build/dark-factory/cmd/factoryd
github.com/dark-factory-build/dark-factory/cmd/factory-runner
github.com/dark-factory-build/dark-factory/scripts/notices"
is_process_sensitive() {
    printf '%s\n' "$process_sensitive_packages" | /usr/bin/grep -qxF "$1"
}

if [ -z "$cacheable_only" ] && runs source "$module/internal/change"; then
    echo "go-ci: Git boundary resource census"
    "$go" test -short -timeout=20m -count=1 ./internal/change
fi

if [ -z "$cacheable_only" ] && runs source "$module/internal/changeworker"; then
    echo "go-ci: Change worker process tests"
    "$go" test -short -timeout=20m -count=1 ./internal/changeworker
fi

if [ -z "$cacheable_only" ] && runs daemon "$module/internal/daemon"; then
    echo "go-ci: daemon process tests"
    "$go" test -short -timeout=20m -count=1 ./internal/daemon
fi

if [ -z "$cacheable_only" ] || [ "$cacheable_only" = 1 ]; then
process_args=
cacheable_args=
packages=$("$go" list ./...)
for package in $packages; do
    if [ "$package" = "$module/internal/change" ] ||
        [ "$package" = "$module/internal/changeworker" ] ||
        [ "$package" = "$module/internal/daemon" ] ||
        [ "$package" = "$module/internal/e2e" ]; then
        continue
    fi
    if ! runs packages "$package"; then continue; fi
    if is_process_sensitive "$package"; then
        process_args="$process_args $package"
    else
        cacheable_args="$cacheable_args $package"
    fi
done
if [ -z "$cacheable_only" ] && [ -n "$process_args" ]; then
    echo "go-ci: process-sensitive Go tests"
    # shellcheck disable=SC2086
    "$go" test -short -timeout=20m -count=1 $process_args
fi
if [ -n "$cacheable_args" ]; then
    echo "go-ci: cacheable Go tests"
    # shellcheck disable=SC2086
    "$go" test -short -timeout=20m $cacheable_args
fi
fi

if [ -z "$cacheable_only" ] && runs source "$module/internal/e2e"; then
    echo "go-ci: browser, daemon and runner E2E"
    "$script_dir/go-e2e.sh" all ${client_built:+"$client_built"}
fi
echo "go-ci: PASS"
