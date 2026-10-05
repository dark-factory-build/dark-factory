#!/bin/sh
set -eu

event_name=${GITHUB_EVENT_NAME:-}
event_ref=${GITHUB_REF:-}
event_sha=${GITHUB_SHA:-}
environment_file=${GITHUB_ENV:-}
runner_temp=${RUNNER_TEMP:-}

if [ -z "$environment_file" ] || [ -z "$runner_temp" ]; then
    echo "GITHUB_ENV and RUNNER_TEMP are required" >&2
    exit 1
fi
case "$event_sha" in
    *[!0-9a-f]* | "")
        echo "GITHUB_SHA must be a full lowercase Git commit SHA" >&2
        exit 1
        ;;
esac
if [ "${#event_sha}" -ne 40 ] || [ "$(git rev-parse --verify HEAD)" != "$event_sha" ]; then
    echo "GITHUB_SHA must be the checked-out commit" >&2
    exit 1
fi

case "$event_name:$event_ref" in
    push:refs/tags/*) tag=${event_ref#refs/tags/} ;;
    *) echo "releases must run from a tag push" >&2; exit 1 ;;
esac

case "$tag" in
    v[0-9]*) ;;
    *) echo "release tag must start with v and a digit" >&2; exit 1 ;;
esac
case "$tag" in
    *[!A-Za-z0-9._-]*)
        echo "release tag has unsupported characters: $tag" >&2
        exit 1
        ;;
esac

git fetch --depth=1 origin "refs/tags/$tag"
source_sha=$(git rev-parse --verify 'FETCH_HEAD^{commit}')
if [ "$source_sha" != "$event_sha" ]; then
    echo "tag $tag points to $source_sha, expected $event_sha" >&2
    exit 1
fi

# The release tool is built from the workflow's own commit, kept outside the
# source tree it packages.
tool_source=$(mktemp -d "$runner_temp/dark-factory-release-tool.XXXXXX")
git archive "$event_sha" | tar -x -C "$tool_source"
release_artifact="$tool_source/release"
printf '#!/bin/sh\nset -e\ngo build -C %s -o %s/release-artifact ./internal/buildinfo/cmd/release-artifact\nexec %s/release-artifact "$@"\n' \
    "$tool_source" "$tool_source" "$tool_source" >"$release_artifact"
chmod 0700 "$release_artifact"

printf 'TAG=%s\n' "$tag" >>"$environment_file"
printf 'SOURCE_SHA=%s\n' "$source_sha" >>"$environment_file"
printf 'RELEASE_ARTIFACT=%s\n' "$release_artifact" >>"$environment_file"
