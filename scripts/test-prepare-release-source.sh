#!/bin/sh
set -eu

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
resolver="$repository_root/scripts/prepare-release-source.sh"
temporary=$(mktemp -d "${TMPDIR:-/tmp}/dark-factory-release-source-test.XXXXXX")
trap 'rm -rf "$temporary"' EXIT HUP INT TERM
remote="$temporary/remote.git"
seed="$temporary/seed"

git init --bare "$remote" >/dev/null
git init -b main "$seed" >/dev/null
git -C "$seed" config user.name fixture
git -C "$seed" config user.email fixture@example.com
printf 'tagged tool\n' >"$seed/marker"
git -C "$seed" add marker
git -C "$seed" commit -m tagged >/dev/null
tagged_sha=$(git -C "$seed" rev-parse HEAD)
git -C "$seed" tag v1.2.3
printf 'main tool\n' >"$seed/marker"
git -C "$seed" commit -am main >/dev/null
main_sha=$(git -C "$seed" rev-parse HEAD)
git -C "$seed" remote add origin "$remote"
git -C "$seed" push origin main v1.2.3 >/dev/null

fail() {
    echo "prepare-release-source test failed: $*" >&2
    exit 1
}

value() {
    key=$1
    file=$2
    sed -n "s/^$key=//p" "$file"
}

fresh_workspace() {
    name=$1
    workspace="$temporary/$name"
    git clone --quiet --branch main "$remote" "$workspace"
    environment_file="$temporary/$name.env"
    runner_temp="$temporary/$name-runner"
    mkdir -p "$runner_temp"
}

run_resolver() {
    run_event=$1
    run_ref=$2
    run_sha=$3
    (
        cd "$workspace"
        GITHUB_EVENT_NAME="$run_event" \
            GITHUB_REF="$run_ref" \
            GITHUB_SHA="$run_sha" \
            GITHUB_ENV="$environment_file" \
            RUNNER_TEMP="$runner_temp" \
            "$resolver"
    )
}

# A tag push builds the release tool committed with that tag.
fresh_workspace push
git -C "$workspace" checkout --quiet --detach "$tagged_sha"
run_resolver push refs/tags/v1.2.3 "$tagged_sha"
[ "$(value TAG "$environment_file")" = v1.2.3 ] || fail "tag"
[ "$(value SOURCE_SHA "$environment_file")" = "$tagged_sha" ] || fail "source SHA"
[ "$(git -C "$workspace" rev-parse HEAD)" = "$tagged_sha" ] || fail "checkout moved"
[ "$(cat "$(dirname "$(value RELEASE_ARTIFACT "$environment_file")")/marker")" = "tagged tool" ] \
    || fail "tag push did not use its tagged release tool"

# Only a tag push can release; a dispatch or branch push is refused before
# anything is built.
fresh_workspace dispatch
if run_resolver workflow_dispatch refs/heads/main "$main_sha"; then
    fail "dispatch was accepted"
fi
fresh_workspace branch
if run_resolver push refs/heads/main "$main_sha"; then
    fail "branch push was accepted"
fi

# The tag is one validated ref component; it cannot inject a refspec.
fresh_workspace injection
if run_resolver push 'refs/tags/v1.2.3:refs/heads/main' "$main_sha"; then
    fail "tag refspec injection was accepted"
fi

# A tag push must still resolve to the immutable event commit.
fresh_workspace moved
if run_resolver push refs/tags/v1.2.3 "$main_sha"; then
    fail "tag/event commit mismatch was accepted"
fi

echo "prepare-release-source tests passed"
