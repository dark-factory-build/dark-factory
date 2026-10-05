#!/bin/sh
# Package both supported macOS release targets in one all-or-nothing step:
#
#   scripts/package-release.sh <tag> <source-sha> <out-dir> <owner/repo> \
#     aarch64-apple-darwin <arm-bin-dir> \
#     x86_64-apple-darwin <intel-bin-dir>
#
# The output directory must not exist. Everything is built in a sibling
# staging directory and renamed into place only after both archives, their
# checksums, the shared update manifest, and the tap formula candidate exist.
# Archive members have one fixed order, mode, owner, and timestamp so rebuilding
# byte-identical binaries can resume the same partially published release.
# The result is two `dark-factory-<tag>-<target>.tar.gz` files plus
# `SHA256SUMS`, `latest.json`, and `dark-factory.rb`.
set -eu

tag="${1:-}"
source_sha="${2:-}"
out_dir="${3:-}"
repository="${4:-}"
if [ "$#" -ne 8 ] || [ -z "$tag" ] || [ -z "$source_sha" ] || [ -z "$out_dir" ] || [ -z "$repository" ]; then
    echo "usage: scripts/package-release.sh <tag> <source-sha> <out-dir> <owner/repo> <target> <bin-dir> <target> <bin-dir>" >&2
    exit 1
fi
shift 4

case "$tag" in
    v[0-9]*) ;;
    *) echo "release tag must start with v and a digit: $tag" >&2; exit 1 ;;
esac
case "$tag" in
    *[!A-Za-z0-9._-]*) echo "release tag has unsupported characters: $tag" >&2; exit 1 ;;
esac
case "$source_sha" in
    *[!0-9a-f]*) echo "release source must be one lowercase Git SHA-1" >&2; exit 1 ;;
esac
[ "${#source_sha}" -eq 40 ] && [ "$source_sha" != 0000000000000000000000000000000000000000 ] || {
    echo "release source must be one lowercase Git SHA-1" >&2
    exit 1
}
case "$repository" in
    */*) ;;
    *) echo "repository must be owner/repo" >&2; exit 1 ;;
esac
repository_owner=${repository%%/*}
repository_name=${repository#*/}
case "$repository_owner" in
    "" | *[!A-Za-z0-9_.-]*)
        echo "repository must be owner/repo" >&2
        exit 1
        ;;
esac
case "$repository_name" in
    "" | */* | *[!A-Za-z0-9_.-]*)
        echo "repository must be owner/repo" >&2
        exit 1
        ;;
esac

arm_dir=
intel_dir=
while [ "$#" -gt 0 ]; do
    target=$1
    bin_dir=$2
    shift 2
    case "$target" in
        aarch64-apple-darwin)
            [ -z "$arm_dir" ] || { echo "duplicate release target: $target" >&2; exit 1; }
            arm_dir=$bin_dir
            ;;
        x86_64-apple-darwin)
            [ -z "$intel_dir" ] || { echo "duplicate release target: $target" >&2; exit 1; }
            intel_dir=$bin_dir
            ;;
        *) echo "unsupported release target: $target" >&2; exit 1 ;;
    esac
done
[ -n "$arm_dir" ] && [ -n "$intel_dir" ] || {
    echo "both aarch64-apple-darwin and x86_64-apple-darwin are required" >&2
    exit 1
}

if [ -e "$out_dir" ] || [ -L "$out_dir" ]; then
    echo "release output already exists: $out_dir" >&2
    exit 1
fi
out_parent=$(dirname "$out_dir")
mkdir -p "$out_parent"
staging=$(mktemp -d "$out_parent/.dark-factory-package.XXXXXX")
cleanup=$staging
trap '[ -z "$cleanup" ] || rm -rf "$cleanup"' EXIT HUP INT TERM
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(dirname "$script_dir")
release_tool="$staging/.release-artifact"
(cd "$repository_root" && \
    CGO_ENABLED=0 GOENV=off GOAUTH=off GOTOOLCHAIN="${GOTOOLCHAIN:-local}" \
        go build -trimpath -buildvcs=false -o "$release_tool" \
        ./internal/buildinfo/cmd/release-artifact)

version=${tag#v}
arm_build_id=
intel_build_id=
arm_unpacked_bytes=
intel_unpacked_bytes=
arm_archive_bytes=
intel_archive_bytes=

package_target() {
    package_target_name=$1
    package_bin_dir=$2
    case "$package_target_name" in
        aarch64-apple-darwin) package_go_target=darwin/arm64 ;;
        x86_64-apple-darwin) package_go_target=darwin/amd64 ;;
        *) echo "unsupported release target: $package_target_name" >&2; exit 1 ;;
    esac
    package_archive="dark-factory-$tag-$package_target_name.tar.gz"
    package_payload="$staging/.payload-$package_target_name"
    mkdir "$package_payload"
    package_unpacked_bytes=0
    package_build_id=
    # The verifier opens each source once without following a final symlink,
    # snapshots from that retained descriptor, and validates the private copy.
    # Tar never reopens the caller-controlled build directory.
    for binary in factoryd factory-runner factoryctl; do
        verified_build_id=$("$release_tool" snapshot \
            "$package_bin_dir/$binary" "$package_payload/$binary" "$binary" \
            "$version" "$source_sha" "$package_go_target")
        if [ -z "$package_build_id" ]; then
            package_build_id=$verified_build_id
        else
            [ "$package_build_id" = "$verified_build_id" ] || {
                echo "release target has inconsistent build identities: $package_target_name" >&2
                exit 1
            }
        fi
        snapshot_size=$(/usr/bin/stat -f '%z' "$package_payload/$binary")
        package_unpacked_bytes=$((package_unpacked_bytes + snapshot_size))
        TZ=UTC0 touch -t 200001010000.00 "$package_payload/$binary"
    done
    "$release_tool" target-bounds "$package_unpacked_bytes"
    # `ustar` fixes member order, mode, owner and timestamp. The payload is
    # already the exact verified snapshot, not another copy of the input path.
    COPYFILE_DISABLE=1 tar --format ustar --uid 0 --gid 0 --uname root --gname wheel \
        --no-acls --no-xattrs --no-fflags --options gzip:!timestamp \
        -czf "$staging/$package_archive" -C "$package_payload" \
        factoryd factory-runner factoryctl
    rm -r "$package_payload"
    package_archive_bytes=$(/usr/bin/stat -f '%z' "$staging/$package_archive")
    "$release_tool" bounds "$package_unpacked_bytes" "$package_archive_bytes"
    package_sha=$(shasum -a 256 "$staging/$package_archive" | cut -d' ' -f1)
    printf '%s  %s\n' "$package_sha" "$package_archive" >>"$staging/SHA256SUMS"
    case "$package_target_name" in
        aarch64-apple-darwin)
            arm_build_id=$package_build_id
            arm_unpacked_bytes=$package_unpacked_bytes
            arm_archive_bytes=$package_archive_bytes
            ;;
        x86_64-apple-darwin)
            intel_build_id=$package_build_id
            intel_unpacked_bytes=$package_unpacked_bytes
            intel_archive_bytes=$package_archive_bytes
            ;;
    esac
}

: >"$staging/SHA256SUMS"
package_target aarch64-apple-darwin "$arm_dir"
package_target x86_64-apple-darwin "$intel_dir"
"$release_tool" release-bounds "$arm_archive_bytes" "$intel_archive_bytes"

arm_archive="dark-factory-$tag-aarch64-apple-darwin.tar.gz"
intel_archive="dark-factory-$tag-x86_64-apple-darwin.tar.gz"
arm_sha=$(awk -v name="$arm_archive" '$2 == name { print $1 }' "$staging/SHA256SUMS")
intel_sha=$(awk -v name="$intel_archive" '$2 == name { print $1 }' "$staging/SHA256SUMS")
cat >"$staging/latest.json" <<JSON
{
  "version": "$version",
  "tag": "$tag",
  "source": "$source_sha",
  "assets": {
    "aarch64-apple-darwin": {
      "url": "https://github.com/$repository/releases/download/$tag/$arm_archive",
      "sha256": "$arm_sha",
      "bytes": $arm_archive_bytes,
      "unpacked_bytes": $arm_unpacked_bytes,
      "build_id": "$arm_build_id"
    },
    "x86_64-apple-darwin": {
      "url": "https://github.com/$repository/releases/download/$tag/$intel_archive",
      "sha256": "$intel_sha",
      "bytes": $intel_archive_bytes,
      "unpacked_bytes": $intel_unpacked_bytes,
      "build_id": "$intel_build_id"
    }
  }
}
JSON
manifest_sha=$(shasum -a 256 "$staging/latest.json" | cut -d' ' -f1)
printf '%s  %s\n' "$manifest_sha" latest.json >>"$staging/SHA256SUMS"

"$script_dir/render-homebrew-formula.sh" "$tag" "$source_sha" "$arm_build_id" \
    "$intel_build_id" "$staging/SHA256SUMS" "$repository" \
    >"$staging/dark-factory.rb"
rm "$release_tool"

[ ! -e "$out_dir" ] && [ ! -L "$out_dir" ] || {
    echo "release output appeared while packaging: $out_dir" >&2
    exit 1
}
mv "$staging" "$out_dir"
cleanup=
echo "packaged $out_dir for aarch64-apple-darwin and x86_64-apple-darwin"
