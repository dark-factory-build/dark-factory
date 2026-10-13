#!/bin/sh

# Public registries are the only dependency network. Callers still use their
# lockfile/checksum enforcement and disable lifecycle scripts.
df_locked_dependency_env() {
    export GOPROXY=https://proxy.golang.org
    export GOSUMDB=sum.golang.org
    export GOPRIVATE= GONOPROXY= GONOSUMDB= GOINSECURE=
    # A worker's shared module cache is read-only: serve it ahead of the proxy
    # and download misses into the private GOPATH.
    if [ -n "${DF_CI_GO_MODULE_CACHE-}" ]; then
        GOPROXY="file://$DF_CI_GO_MODULE_CACHE/cache/download,$GOPROXY"
        [ "${GOMODCACHE-}" != "$DF_CI_GO_MODULE_CACHE" ] || unset GOMODCACHE
    fi
    case "${GOFLAGS-}" in
        *-mod=readonly*) ;;
        '') export GOFLAGS=-mod=readonly ;;
        *) export GOFLAGS="$GOFLAGS -mod=readonly" ;;
    esac
    export COREPACK_ENABLE_NETWORK=1 COREPACK_ENV_FILE=0 COREPACK_NPM_REGISTRY=https://registry.npmjs.org
    export npm_config_registry=https://registry.npmjs.org
    export NPM_CONFIG_REGISTRY=https://registry.npmjs.org
    export npm_config_userconfig=/dev/null npm_config_globalconfig=/var/empty/npmrc
    export NPM_CONFIG_USERCONFIG=/dev/null NPM_CONFIG_GLOBALCONFIG=/var/empty/npmrc
    export npm_config_ignore_scripts=true
    export NPM_CONFIG_IGNORE_SCRIPTS=true
    export CARGO_NET_OFFLINE=false
    export CARGO_REGISTRIES_CRATES_IO_PROTOCOL=sparse
    export NETRC=/dev/null
    export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_NOSYSTEM=1
}

df_verify_dependency_lockfiles() {
    for df_lockfile in "$@"; do
        [ -f "$df_lockfile" ] || {
            echo "dependency lockfile is missing: $df_lockfile" >&2
            return 1
        }
        # Project configuration, or a shrinkwrap npm prefers over the
        # lockfile, could name another registry.
        df_lock_dir=$(dirname "$df_lockfile")
        if [ -e "$df_lock_dir/.npmrc" ] || [ -e "$df_lock_dir/npm-shrinkwrap.json" ] || grep -qE 'registr|://|\\' "$df_lock_dir/pnpm-workspace.yaml" 2>/dev/null; then
            echo "project registry configuration or shrinkwrap is refused beside $df_lockfile" >&2
            return 1
        fi
        # Every source must be the exact public registry: parsed npm resolved
        # URLs, pnpm integrity-only registry resolutions, crates.io sources.
        # Escapes could spell a key the line rules miss, so they are refused.
        case "$df_lockfile" in
            *.json)
                node -e 'let bad = 0; (function walk(v) { for (const [k, x] of Object.entries(v && typeof v === "object" ? v : {})) {
                    if (k === "resolved" && !String(x).startsWith("https://registry.npmjs.org/")) { console.error(`${process.argv[1]}: unlisted dependency source: ${x}`); bad = 1 }
                    walk(x) } })(JSON.parse(require("fs").readFileSync(process.argv[1], "utf8"))); process.exit(bad)' "$df_lockfile" || return 1
                continue ;;
            *.yaml) df_lock_rule='/\\|:\/\/|(resolution|tarball|repo):/ && !/^[[:space:]]*resolution:[[:space:]]*\{integrity:[[:space:]]*[A-Za-z0-9+\/=-]+\}[[:space:]]*$/' ;;
            # Cargo reads config from every ancestor and CARGO_HOME; none may
            # replace or add a source. Only the line shapes Cargo writes, so
            # no TOML key spelling reaches source.
            *.lock)
                df_cargo_dir=$(CDPATH= cd -- "$df_lock_dir" && pwd -P) || return 1
                df_cargo_home=${CARGO_HOME:-${HOME:-/var/empty}/.cargo}
                while :; do
                    ! grep -qsE 'source|replace|registr|\\' "$df_cargo_home/config" "$df_cargo_home/config.toml" "$df_cargo_dir/.cargo/config" "$df_cargo_dir/.cargo/config.toml" || {
                        echo "Cargo source configuration is refused in $df_cargo_home or under $df_cargo_dir" >&2
                        return 1
                    }
                    [ "$df_cargo_dir" != / ] || break
                    df_cargo_dir=$(dirname "$df_cargo_dir")
                done
                df_lock_rule='!(/^(#.*|\[\[package\]\]|\]|dependencies = \[|version = [0-9]+| "[^"\\]*",|(name|version|checksum) = "[^"\\]*")?$/ || $0 == "source = \"registry+https://github.com/rust-lang/crates.io-index\"")' ;;
            *) echo "unsupported dependency lockfile: $df_lockfile" >&2; return 1 ;;
        esac
        awk "$df_lock_rule"' { print FILENAME ": unlisted dependency source: " $0 > "/dev/stderr"; bad=1 } END { exit bad }' "$df_lockfile" || return 1
    done
}

# go mod download writes missing go.sum hashes and can raise go.mod's go
# version; the committed files must suffice, and drift is restored away.
df_go_mod_download() {
    df_go_saved=$(mktemp -d "${TMPDIR:-/tmp}/df-go-mod.XXXXXX") && cp go.mod go.sum "$df_go_saved/" || return 1
    df_go_status=0
    "$1" mod download || df_go_status=$?
    for df_go_file in go.mod go.sum; do
        cmp -s "$df_go_file" "$df_go_saved/$df_go_file" || { echo "$df_go_file drifted from its committed contents" >&2; cp "$df_go_saved/$df_go_file" "$df_go_file"; df_go_status=1; }
    done
    rm -rf "$df_go_saved"
    return "$df_go_status"
}
