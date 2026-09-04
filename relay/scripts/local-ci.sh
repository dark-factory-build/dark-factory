#!/bin/sh
# The relay's whole gate. It mirrors control-plane/scripts/local-ci.sh: install
# from the lockfile, type-check, prove a dry-run deploy under a cleaned
# environment, and drive a real `wrangler dev --local` child from the tests.
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname "$0")" && pwd)
relay_root=$(CDPATH='' cd -- "$script_dir/.." && pwd)
cd "$relay_root"

if find . -maxdepth 1 -type f \( -name '.env*' -o -name '.dev.vars*' \) | grep -q .; then
    echo "local environment files are forbidden in the relay tree" >&2
    exit 1
fi

node -e "if (Number(process.versions.node.split('.')[0]) < 22) process.exit(1)"
node_executable=$(node -p 'process.execPath')
case "$node_executable" in
    /*) ;;
    *) echo "Node did not report an absolute executable path" >&2; exit 1 ;;
esac

npm ci --ignore-scripts
npx tsc --noEmit

dry_run_dir=$(mktemp -d "${TMPDIR:-/tmp}/df-relay-dry-run.XXXXXX")
cleanup() {
    rm -rf -- "$dry_run_dir"
}
trap cleanup EXIT HUP INT TERM
./scripts/with-clean-wrangler-env.sh "$node_executable" "$relay_root/node_modules/wrangler/bin/wrangler.js" \
    deploy --dry-run --outdir "$dry_run_dir"

node --test tests/*.test.mjs

# The relay carries no secret, but it does spawn Wrangler and write local state,
# so the ignore rules that keep both out of the tree are part of the gate.
for pattern in '.env' '.env.*' '.dev.vars' '.dev.vars.*' '.wrangler/' 'node_modules/'; do
    grep -Fqx -- "$pattern" ../.gitignore
done
for ignored_path in .env .dev.vars .wrangler/state node_modules/wrangler; do
    git check-ignore -q --no-index "$ignored_path"
done

git diff --check -- .
