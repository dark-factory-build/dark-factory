#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
. "$script_dir/locked-dependency-env.sh"
temporary=$(mktemp -d "${TMPDIR:-/tmp}/dark-factory-lockfile-test.XXXXXX")
trap 'chmod -R u+w "$temporary"; rm -rf "$temporary"' EXIT HUP INT TERM
fail() { echo "locked dependency test failed: $*" >&2; exit 1; }

cd "$repository_root"
df_verify_dependency_lockfiles web/pnpm-lock.yaml relay/package-lock.json control-plane/package-lock.json control-plane/Cargo.lock ||
    fail "committed lockfiles rejected"

reject() {
    printf '%s\n' "$2" >"$temporary/$1"
    ! df_verify_dependency_lockfiles "$temporary/$1" 2>/dev/null || fail "accepted $2"
}
npm_lock() { printf '{"lockfileVersion":3,"packages":{"node_modules/x":{"version":"1.0.0","%s":"%s"}}}' "$1" "$2"; }
reject package-lock.json "$(npm_lock resolved https://unlisted.example/registry.npmjs.org/example.tgz)"
reject package-lock.json "$(npm_lock resolved git+ssh://git@github.com/example/example.git)"
reject pnpm-lock.yaml '    resolution: {tarball: https://unlisted.example/example.tgz}'
reject pnpm-lock.yaml '    resolution: {commit: abc, repo: https://unlisted.example/x.git, type: git}'
reject pnpm-lock.yaml '    resolution:
      tarball: https://registry.npmjs.org.unlisted.example/example.tgz'
reject Cargo.lock 'source = "git+https://unlisted.example/crate#abc"'
reject Cargo.lock '"source" = "git+https://unlisted.example/crate#abc"'
reject Cargo.lock "'source' = \"git+https://unlisted.example/crate#abc\""
reject Cargo.lock 'package = [{ name = "foo", version = "0.1.0", source = "git+https://unlisted.example/foo#abc" }]'
# Project configuration beside a lockfile could name another registry.
mkdir -p "$temporary/project"
printf '{"lockfileVersion":3,"packages":{}}\n' >"$temporary/project/package-lock.json"
printf '@cloudflare:registry=https://unlisted.example\n' >"$temporary/project/.npmrc"
! df_verify_dependency_lockfiles "$temporary/project/package-lock.json" 2>/dev/null || fail "accepted project .npmrc"
rm "$temporary/project/.npmrc"
printf '{"lockfileVersion":3,"packages":{"node_modules/x":{"version":"1.0.0","resolved":"https://unlisted.example/example.tgz"}}}\n' >"$temporary/project/npm-shrinkwrap.json"
! df_verify_dependency_lockfiles "$temporary/project/package-lock.json" 2>/dev/null || fail "accepted npm-shrinkwrap.json beside package-lock.json"
rm "$temporary/project/npm-shrinkwrap.json"
printf "packages:\n  - packages/*\n'@cloudflare:registry': https://unlisted.example\n" >"$temporary/project/pnpm-workspace.yaml"
printf 'lockfileVersion: 9.0\n' >"$temporary/project/pnpm-lock.yaml"
! df_verify_dependency_lockfiles "$temporary/project/pnpm-lock.yaml" 2>/dev/null || fail "accepted pnpm-workspace registry"
# Cargo config beside, above, or in CARGO_HOME could replace crates.io.
mkdir -p "$temporary/cargo/crate/.cargo" "$temporary/cargo/home"
cp control-plane/Cargo.toml control-plane/Cargo.lock "$temporary/cargo/crate/"
printf '[source.crates-io]\nreplace-with = "unlisted"\n\n[source.unlisted]\nregistry = "sparse+https://unlisted.example/index/"\n' >"$temporary/cargo/replace.toml"
for config in crate/.cargo/config.toml .cargo/config.toml home/config.toml; do
    mkdir -p "$(dirname "$temporary/cargo/$config")"
    cp "$temporary/cargo/replace.toml" "$temporary/cargo/$config"
    ! CARGO_HOME="$temporary/cargo/home" df_verify_dependency_lockfiles "$temporary/cargo/crate/Cargo.lock" 2>/dev/null ||
        fail "accepted Cargo source replacement in $config"
    rm "$temporary/cargo/$config"
done
CARGO_HOME="$temporary/cargo/home" df_verify_dependency_lockfiles "$temporary/cargo/crate/Cargo.lock" || fail "committed Cargo.lock rejected"
# Escaped keys parse as "resolved", "resolution" and "source".
u="$(printf '\134')u00"
reject package-lock.json "$(npm_lock "res${u}6flved" https://unlisted.example/x.tgz)"
reject pnpm-lock.yaml '    "res'"${u}6f"'lution": {tarball: https://unlisted.example/x.tgz}'
reject Cargo.lock '"sour'"${u}63"'e" = "git+https://unlisted.example/crate#abc"'

cd "$temporary"
unset DF_CI_GO_MODULE_CACHE
export GONOPROXY=example.com GOINSECURE=example.com
df_locked_dependency_env
# The real tools resolve only the public registries, without lifecycle scripts.
[ "$(npm config get registry | sed 's#/$##')" = https://registry.npmjs.org ] && [ "$(npm config get ignore-scripts)" = true ] ||
    fail "npm resolves $(npm config get registry) with ignore-scripts=$(npm config get ignore-scripts)"
case " $(go env GOFLAGS) " in *" -mod=readonly "*) ;; *) fail "go resolves GOFLAGS=$(go env GOFLAGS)" ;; esac
[ "$(go env GOPROXY GOSUMDB GOPRIVATE GONOPROXY GONOSUMDB GOINSECURE | tr '\n' ' ')" = "https://proxy.golang.org sum.golang.org     " ] ||
    fail "go resolves $(go env GOPROXY GOSUMDB GOPRIVATE GONOPROXY GONOSUMDB GOINSECURE | tr '\n' ' ')"

# A clean Corepack fetches the committed pnpm from npmjs despite a project .corepack.env.
pnpm_version=$(sed -n 's/.*"packageManager": "pnpm@\([^"]*\)".*/\1/p' "$repository_root/web/package.json")
mkdir -p corepack
printf '{"packageManager":"pnpm@%s"}\n' "$pnpm_version" >corepack/package.json
printf 'COREPACK_NPM_REGISTRY=https://unlisted.example\n' >corepack/.corepack.env
[ "$(cd corepack && COREPACK_HOME="$temporary/corepack-home" corepack pnpm --version)" = "$pnpm_version" ] ||
    fail "Corepack did not fetch pnpm $pnpm_version from the public registry"

# Real go mod download from a local module proxy: a missing go.sum hash is drift.
mkdir -p proxy/example.com/dep/@v zip/example.com/dep@v1.0.0 module
printf 'module example.com/dep\n' | tee proxy/example.com/dep/@v/v1.0.0.mod >zip/example.com/dep@v1.0.0/go.mod
printf 'v1.0.0\n' >proxy/example.com/dep/@v/list
printf '{"Version":"v1.0.0"}\n' >proxy/example.com/dep/@v/v1.0.0.info
(cd zip && zip -qrD ../proxy/example.com/dep/@v/v1.0.0.zip example.com)
printf 'module example.com/m\n\ngo 1.22\n\nrequire example.com/dep v1.0.0\n' >module/go.mod
: >module/go.sum
export GOPROXY="file://$temporary/proxy" GOSUMDB=off GOMODCACHE="$temporary/mod" GOCACHE="$temporary/cache" GOFLAGS="$GOFLAGS -modcacherw" GOTOOLCHAIN=local
drift=$(cd module && df_go_mod_download go 2>&1) && fail "go.sum drift accepted"
case "$drift" in *"go.sum drifted"*) ;; *) fail "go.sum drift not detected: $drift" ;; esac
[ ! -s module/go.sum ] || fail "rejected go.sum additions were kept"
! (cd module && df_go_mod_download go 2>/dev/null) || fail "go.sum drift accepted on retry"
(cd module && go mod download) # stands in for committing the hash
(cd module && df_go_mod_download go) || fail "committed go.sum rejected"

# A dependency declaring a newer go version raises go.mod's: manifest drift.
mkdir -p zip2/example.com/dep@v1.1.0 module2
printf 'module example.com/dep\n\ngo 1.23\n' | tee proxy/example.com/dep/@v/v1.1.0.mod >zip2/example.com/dep@v1.1.0/go.mod
printf 'v1.0.0\nv1.1.0\n' >proxy/example.com/dep/@v/list
printf '{"Version":"v1.1.0"}\n' >proxy/example.com/dep/@v/v1.1.0.info
(cd zip2 && zip -qrD ../proxy/example.com/dep/@v/v1.1.0.zip example.com)
printf 'module example.com/m2\n\ngo 1.22\n\nrequire example.com/dep v1.1.0\n' >module2/go.mod.committed
cp module2/go.mod.committed module2/go.mod && : >module2/go.sum
(cd module2 && go mod download && cp go.mod.committed go.mod) # commits only the hashes
drift=$(cd module2 && df_go_mod_download go 2>&1) && fail "go.mod drift accepted"
case "$drift" in *"go.mod drifted"*) ;; *) fail "go.mod drift not detected: $drift" ;; esac
cmp -s module2/go.mod module2/go.mod.committed || fail "rejected go.mod changes were kept"

# A worker's read-only shared module cache serves downloads into its private GOPATH.
chmod -R a-w mod
(
    export DF_CI_GO_MODULE_CACHE="$temporary/mod" GOMODCACHE="$temporary/mod" GOPATH="$temporary/gopath"
    df_locked_dependency_env
    export GOSUMDB=off
    cd module && df_go_mod_download go && [ -f "$GOPATH/pkg/mod/example.com/dep@v1.0.0/go.mod" ]
) || fail "read-only shared module cache unusable"
