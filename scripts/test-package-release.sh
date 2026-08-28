#!/bin/sh
set -eu

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
packager="$repository_root/scripts/package-release.sh"
renderer="$repository_root/scripts/render-homebrew-formula.sh"
temporary=$(mktemp -d "${TMPDIR:-/tmp}/dark-factory-package-test.XXXXXX")
trap 'rm -rf "$temporary"' EXIT HUP INT TERM
source_sha=1234567890abcdef1234567890abcdef12345678

fail() {
    echo "package-release test failed: $*" >&2
    exit 1
}

release_tool="$temporary/release-artifact"
CGO_ENABLED=0 GOENV=off GOAUTH=off GOTOOLCHAIN=local \
    go build -trimpath -buildvcs=false -o "$release_tool" \
    ./internal/buildinfo/cmd/release-artifact
if "$release_tool" target-bounds 201326593 >/dev/null 2>&1; then
    fail "oversized aggregate input passed before archiving"
fi
if "$release_tool" bounds 65 1 >/dev/null 2>&1; then
    fail "unreasonable archive compression passed"
fi
if "$release_tool" release-bounds 268435456 1 >/dev/null 2>&1; then
    fail "oversized aggregate release passed"
fi

build_binary() {
    build_component=$1
    build_architecture=$2
    build_receipt=$3
    build_output=$4
    CGO_ENABLED=0 GOOS=darwin GOARCH="$build_architecture" GOENV=off GOAUTH=off GOTOOLCHAIN=local \
        go build -trimpath -buildvcs=false \
        -ldflags "-s -w -X github.com/dark-factory-build/dark-factory/internal/buildinfo.receipt=$build_receipt" \
        -o "$build_output" "./cmd/$build_component"
}

make_binaries() {
    architecture=$1
    target=$2
    directory=$3
    mkdir -p "$directory"
    target_receipt=$("$release_tool" receipt 1.2.3 "$source_sha" "$target")
    for binary in factoryd factory-runner factoryctl; do
        build_binary "$binary" "$architecture" "$target_receipt" "$directory/$binary"
    done
}

arm_dir="$temporary/arm"
intel_dir="$temporary/intel"
make_binaries arm64 darwin/arm64 "$arm_dir"
make_binaries amd64 darwin/amd64 "$intel_dir"
arm_receipt=$("$release_tool" receipt 1.2.3 "$source_sha" darwin/arm64)
intel_receipt=$("$release_tool" receipt 1.2.3 "$source_sha" darwin/amd64)
arm_build_id=${arm_receipt##*|}
intel_build_id=${intel_receipt##*|}
case "$(uname -m)" in
    arm64) native_dir=$arm_dir; native_target=darwin/arm64; native_build_id=$arm_build_id ;;
    x86_64) native_dir=$intel_dir; native_target=darwin/amd64; native_build_id=$intel_build_id ;;
    *) fail "unsupported native macOS architecture" ;;
esac
for binary in factoryd factory-runner factoryctl; do
    "$native_dir/$binary" --build-identity | ruby -rjson -e '
      value = JSON.parse(STDIN.read)
      abort "version" unless value.fetch("version") == "1.2.3"
      abort "source" unless value.fetch("source") == ARGV.fetch(0)
      abort "target" unless value.fetch("target") == ARGV.fetch(1)
      abort "build ID" unless value.fetch("build_id") == ARGV.fetch(2)
      abort "release" unless value.fetch("release") == true
    ' "$source_sha" "$native_target" "$native_build_id" \
        || fail "$binary did not report its exact release identity"
done
chmod 0700 "$arm_dir/factoryd"
chmod 0711 "$intel_dir/factoryctl"
output="$temporary/dist"
if "$packager" v1.2.3 "$source_sha" "$output" example/project \
    x86_64-apple-darwin "$intel_dir" \
    aarch64-apple-darwin "$arm_dir" >"$temporary/mode.out" 2>"$temporary/mode.err"
then
    fail "non-0755 input binaries were packaged"
fi
[ ! -e "$output" ] || fail "invalid source modes exposed a partial output"
chmod 0755 "$arm_dir/factoryd" "$intel_dir/factoryctl"

hardlink="$temporary/factory-runner-hardlink"
ln "$arm_dir/factory-runner" "$hardlink"
if "$packager" v1.2.3 "$source_sha" "$output" example/project \
    aarch64-apple-darwin "$arm_dir" \
    x86_64-apple-darwin "$intel_dir" >"$temporary/hardlink.out" 2>"$temporary/hardlink.err"
then
    fail "multiply linked input binary was packaged"
fi
[ ! -e "$output" ] || fail "multiply linked source exposed a partial output"
rm "$hardlink"

saved_factoryd="$temporary/intel-factoryd"
mv "$intel_dir/factoryd" "$saved_factoryd"
ln -s "$saved_factoryd" "$intel_dir/factoryd"
if "$packager" v1.2.3 "$source_sha" "$output" example/project \
    aarch64-apple-darwin "$arm_dir" \
    x86_64-apple-darwin "$intel_dir" >"$temporary/symlink.out" 2>"$temporary/symlink.err"
then
    fail "symbolic-link input binary was packaged"
fi
[ ! -e "$output" ] || fail "symbolic-link source exposed a partial output"
rm "$intel_dir/factoryd"
mv "$saved_factoryd" "$intel_dir/factoryd"

# The static artifact verifier rejects identity or architecture mutations even
# when the file remains one executable Mach-O with the expected name and mode.
valid_arm_factoryd="$temporary/valid-arm-factoryd"
cp "$arm_dir/factoryd" "$valid_arm_factoryd"
wrong_version_receipt=$("$release_tool" receipt 1.2.4 "$source_sha" darwin/arm64)
build_binary factoryd arm64 "$wrong_version_receipt" "$arm_dir/factoryd"
if "$packager" v1.2.3 "$source_sha" "$output" example/project \
    aarch64-apple-darwin "$arm_dir" x86_64-apple-darwin "$intel_dir" \
    >"$temporary/wrong-version.out" 2>"$temporary/wrong-version.err"; then
    fail "wrong embedded release was packaged"
fi
mv "$valid_arm_factoryd" "$arm_dir/factoryd"

valid_arm_factoryd="$temporary/valid-arm-factoryd"
cp "$arm_dir/factoryd" "$valid_arm_factoryd"
wrong_source_receipt=$("$release_tool" receipt 1.2.3 2234567890abcdef1234567890abcdef12345678 darwin/arm64)
build_binary factoryd arm64 "$wrong_source_receipt" "$arm_dir/factoryd"
if "$packager" v1.2.3 "$source_sha" "$output" example/project \
    aarch64-apple-darwin "$arm_dir" x86_64-apple-darwin "$intel_dir" \
    >"$temporary/wrong-source.out" 2>"$temporary/wrong-source.err"; then
    fail "wrong embedded source was packaged"
fi
mv "$valid_arm_factoryd" "$arm_dir/factoryd"

valid_arm_factoryd="$temporary/valid-arm-factoryd"
cp "$arm_dir/factoryd" "$valid_arm_factoryd"
bad_build_id=$(printf '%064d' 1)
bad_receipt="1.2.3|$source_sha|darwin/arm64|$bad_build_id"
build_binary factoryd arm64 "$bad_receipt" "$arm_dir/factoryd"
if "$packager" v1.2.3 "$source_sha" "$output" example/project \
    aarch64-apple-darwin "$arm_dir" x86_64-apple-darwin "$intel_dir" \
    >"$temporary/wrong-build-id.out" 2>"$temporary/wrong-build-id.err"; then
    fail "wrong embedded build ID was packaged"
fi
mv "$valid_arm_factoryd" "$arm_dir/factoryd"

valid_intel_factoryd="$temporary/valid-intel-factoryd"
mv "$intel_dir/factoryd" "$valid_intel_factoryd"
cp "$arm_dir/factoryd" "$intel_dir/factoryd"
if "$packager" v1.2.3 "$source_sha" "$output" example/project \
    aarch64-apple-darwin "$arm_dir" x86_64-apple-darwin "$intel_dir" \
    >"$temporary/wrong-arch.out" 2>"$temporary/wrong-arch.err"; then
    fail "wrong Mach-O architecture was packaged"
fi
mv "$valid_intel_factoryd" "$intel_dir/factoryd"

# Unrelated source-directory residue is not an archive member.
printf '%s\n' obsolete >"$arm_dir/factory-tui"
"$packager" v1.2.3 "$source_sha" "$output" example/project \
    x86_64-apple-darwin "$intel_dir" \
    aarch64-apple-darwin "$arm_dir"

for target in aarch64-apple-darwin x86_64-apple-darwin; do
    archive="$output/dark-factory-v1.2.3-$target.tar.gz"
    [ -f "$archive" ] || fail "missing $target archive"
    listing=$(tar -tzf "$archive" | LC_ALL=C sort)
    [ "$listing" = "factory-runner
factoryctl
factoryd" ] || fail "$target archive has unexpected contents: $listing"
    gzip_mtime=$(od -An -tu1 -j4 -N4 "$archive" | tr -d '[:space:]')
    [ "$gzip_mtime" = "0000" ] || fail "$target archive embeds its packaging time"
    LC_ALL=C tar -tvzf "$archive" | awk '
      $1 != "-rwxr-xr-x" || $2 != 0 || $3 != "root" || $4 != "wheel" ||
        $6 != "Jan" || $7 != 1 || $8 != 2000 { exit 1 }
      END { exit NR == 3 ? 0 : 1 }
    ' || fail "$target archive metadata is not normalized"
done
(cd "$output" && shasum -a 256 -c SHA256SUMS >/dev/null) || fail "release checksums failed"

ruby -rjson -e '
  manifest = JSON.parse(File.read(ARGV.fetch(0)))
  abort "version" unless manifest["version"] == "1.2.3"
  abort "tag" unless manifest["tag"] == "v1.2.3"
  abort "source" unless manifest["source"] == ARGV.fetch(1)
  expected = %w[aarch64-apple-darwin x86_64-apple-darwin]
  abort "keys" unless manifest.fetch("assets").keys.sort == expected
  build_ids = {
    "aarch64-apple-darwin" => ARGV.fetch(2),
    "x86_64-apple-darwin" => ARGV.fetch(3),
  }
  expected.each do |target|
    asset = manifest.fetch("assets").fetch(target)
    abort "url" unless asset.fetch("url").end_with?("dark-factory-v1.2.3-#{target}.tar.gz")
    abort "sha" unless asset.fetch("sha256").match?(/\A[0-9a-f]{64}\z/)
    abort "build ID" unless asset.fetch("build_id") == build_ids.fetch(target)
    abort "bytes" unless asset.fetch("bytes").positive?
    abort "unpacked bytes" unless asset.fetch("unpacked_bytes") > asset.fetch("bytes")
  end
' "$output/latest.json" "$source_sha" "$arm_build_id" "$intel_build_id" \
    || fail "manifest is not the exact two-target identity shape"

formula="$output/dark-factory.rb"
ruby -c "$formula" >/dev/null || fail "formula is not valid Ruby"
for target in aarch64-apple-darwin x86_64-apple-darwin; do
    grep -Fq "dark-factory-v1.2.3-$target.tar.gz" "$formula" \
        || fail "formula omitted $target"
done
for binary in factoryd factory-runner factoryctl; do
    grep -Fq "$binary" "$formula" || fail "formula omitted $binary"
done
grep -Fq 'on_arm do' "$formula" || fail "formula omitted the arm architecture block"
grep -Fq 'on_intel do' "$formula" || fail "formula omitted the Intel architecture block"
grep -Fq 'url "https://github.com/example/project/releases/download/v1.2.3/latest.json"' "$formula" \
    || fail "formula has no stable top-level source"
if grep -Eq '^[[:space:]]+version ' "$formula"; then
    fail "stable formula has a redundant or stale explicit version"
fi
grep -Fq 'resource("binaries").stage' "$formula" \
    || fail "formula does not install its selected resource"
grep -Fq 'assert_equal "#{name} #{version}", shell_output("#{bin}/#{name} --version").strip' \
    "$formula" || fail "formula does not test the exact binary version"
grep -Fq "SOURCE_SHA = \"$source_sha\"" "$formula" || fail "formula omitted the exact source"
grep -Fq "\"darwin/arm64\" => \"$arm_build_id\"" "$formula" || fail "formula omitted the arm build ID"
grep -Fq "\"darwin/amd64\" => \"$intel_build_id\"" "$formula" || fail "formula omitted the Intel build ID"
grep -Fq 'identity.fetch("release")' "$formula" || fail "formula does not require a release identity"
if grep -Fq 'Hardware::CPU' "$formula"; then
    fail "formula bypasses Homebrew architecture blocks"
fi
if grep -Eq 'factory-tui|factoryctl update|rollback binaries' "$formula"; then
    fail "formula retained deleted TUI/updater behavior"
fi
grep -Fq '`brew services` for Dark Factory.' "$formula" || fail "formula permits competing service ownership"
grep -Fq '`brew uninstall dark-factory` removes only the commands' "$formula" \
    || fail "formula hides command-only uninstall behavior"
if grep -Eq 'factoryctl service|service uninstall operation' "$formula"; then
    fail "formula advertises a nonexistent service command"
fi
if grep -Eq '^[[:space:]]*service do' "$formula"; then
    fail "formula defines a Homebrew service"
fi

second_formula="$temporary/second.rb"
"$renderer" v1.2.3 "$source_sha" "$arm_build_id" "$intel_build_id" \
    "$output/SHA256SUMS" example/project >"$second_formula"
cmp -s "$formula" "$second_formula" || fail "formula rendering is not deterministic"

prerelease_checksums="$temporary/prerelease-SHA256SUMS"
sed 's/v1\.2\.3-/v1.2.3-rc.1-/' "$output/SHA256SUMS" >"$prerelease_checksums"
prerelease_formula="$temporary/prerelease.rb"
prerelease_arm_receipt=$("$release_tool" receipt 1.2.3-rc.1 "$source_sha" darwin/arm64)
prerelease_intel_receipt=$("$release_tool" receipt 1.2.3-rc.1 "$source_sha" darwin/amd64)
prerelease_arm_build_id=${prerelease_arm_receipt##*|}
prerelease_intel_build_id=${prerelease_intel_receipt##*|}
"$renderer" v1.2.3-rc.1 "$source_sha" "$prerelease_arm_build_id" "$prerelease_intel_build_id" \
    "$prerelease_checksums" example/project >"$prerelease_formula"
[ "$(grep -Ec '^[[:space:]]+version ' "$prerelease_formula")" -eq 1 ] &&
    grep -Fxq '  version "1.2.3-rc.1"' "$prerelease_formula" \
    || fail "prerelease formula does not declare its exact version once"
grep -Fq 'assert_equal "#{name} #{version}", shell_output("#{bin}/#{name} --version").strip' \
    "$prerelease_formula" || fail "prerelease formula does not test its exact binary version"

# Reversing target order and changing only every source mtime cannot change
# any published byte. The packager owns one canonical archive representation.
TZ=UTC0 touch -t 203001010000.00 "$arm_dir"/*
TZ=UTC0 touch -t 204001010000.00 "$intel_dir"/*
second_output="$temporary/second-dist"
"$packager" v1.2.3 "$source_sha" "$second_output" example/project \
    aarch64-apple-darwin "$arm_dir" \
    x86_64-apple-darwin "$intel_dir"
for artifact in \
    dark-factory-v1.2.3-aarch64-apple-darwin.tar.gz \
    dark-factory-v1.2.3-x86_64-apple-darwin.tar.gz \
    SHA256SUMS latest.json dark-factory.rb
do
    cmp -s "$output/$artifact" "$second_output/$artifact" \
        || fail "target order or source mtime changed $artifact"
done

# Validation finishes before the output transaction begins. A missing binary
# in either architecture leaves no partial output or staging directory.
rm "$intel_dir/factoryctl"
failed_output="$temporary/failed-dist"
if "$packager" v1.2.3 "$source_sha" "$failed_output" example/project \
    aarch64-apple-darwin "$arm_dir" \
    x86_64-apple-darwin "$intel_dir" >"$temporary/failed.out" 2>"$temporary/failed.err"
then
    fail "incomplete Intel build was packaged"
fi
[ ! -e "$failed_output" ] || fail "failed package exposed a partial output"
if find "$temporary" -maxdepth 1 -name '.dark-factory-package.*' | grep -q .; then
    fail "failed package left a staging directory"
fi

# A complete output is immutable input to release publication; a rerun must
# not replace it with different binaries under the same tag.
if "$packager" v1.2.3 "$source_sha" "$output" example/project \
    aarch64-apple-darwin "$arm_dir" \
    x86_64-apple-darwin "$arm_dir" >"$temporary/repack.out" 2>"$temporary/repack.err"
then
    fail "existing release output was overwritten"
fi
(cd "$output" && shasum -a 256 -c SHA256SUMS >/dev/null) \
    || fail "refused rerun changed the existing output"

# The local Rust workspace remains gated until deletion, but the Go-only local
# release must not depend on the obsolete Rust release workflow.
"$repository_root/scripts/check-toolchain-pins.sh" \
    || fail "toolchain pins rejected the Go release"
if grep -Eq 'check_pin \.github/workflows/release\.yml .*rustup|check_pin \.github/workflows/release\.yml .*cargo' \
    "$repository_root/scripts/check-toolchain-pins.sh"; then
    fail "toolchain pin check still requires a Rust local-runtime release"
fi
if grep -Eq 'rustup|cargo[[:space:]]+\+' "$repository_root/.github/workflows/release.yml"; then
    fail "release workflow still builds the replaced Rust local runtime"
fi

echo "package-release tests passed"
