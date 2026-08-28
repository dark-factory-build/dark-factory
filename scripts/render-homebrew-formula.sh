#!/bin/sh
# Render the exact Formula/dark-factory.rb candidate for the custom tap.
# Publishing that candidate to the public tap remains a reviewed handoff.
set -eu

tag="${1:-}"
source_sha="${2:-}"
arm_build_id="${3:-}"
intel_build_id="${4:-}"
checksums="${5:-}"
repository="${6:-dark-factory-build/dark-factory}"
if [ -z "$tag" ] || [ -z "$source_sha" ] || [ -z "$arm_build_id" ] || [ -z "$intel_build_id" ] || [ -z "$checksums" ] || [ "$#" -gt 6 ]; then
    echo "usage: scripts/render-homebrew-formula.sh <tag> <source-sha> <arm-build-id> <intel-build-id> <SHA256SUMS> [<owner/repo>]" >&2
    exit 1
fi
[ -f "$checksums" ] || { echo "checksum file not found: $checksums" >&2; exit 1; }
case "$tag" in
    v[0-9]*) ;;
    *) echo "release tag must start with v and a digit: $tag" >&2; exit 1 ;;
esac
case "$tag" in
    *[!A-Za-z0-9._-]*) echo "release tag has unsupported characters: $tag" >&2; exit 1 ;;
esac
valid_lower_hex() {
    value=$1
    length=$2
    case "$value" in *[!0-9a-f]*) return 1 ;; esac
    [ "${#value}" -eq "$length" ] && [ "$value" != "$(printf '%*s' "$length" '' | tr ' ' 0)" ]
}
valid_lower_hex "$source_sha" 40 || { echo "invalid release source" >&2; exit 1; }
valid_lower_hex "$arm_build_id" 64 || { echo "invalid arm64 build ID" >&2; exit 1; }
valid_lower_hex "$intel_build_id" 64 || { echo "invalid Intel build ID" >&2; exit 1; }
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

checksum_for() {
    checksum_name=$1
    checksum_matches=$(awk -v name="$checksum_name" '$2 == name { print $1 }' "$checksums")
    [ "$(printf '%s\n' "$checksum_matches" | awk 'NF { count++ } END { print count + 0 }')" -eq 1 ] || {
        echo "checksum file must name $checksum_name exactly once" >&2
        exit 1
    }
    case "$checksum_matches" in
        *[!0-9a-f]*) echo "invalid SHA-256 for $checksum_name" >&2; exit 1 ;;
    esac
    [ "${#checksum_matches}" -eq 64 ] || {
        echo "invalid SHA-256 for $checksum_name" >&2
        exit 1
    }
    printf '%s\n' "$checksum_matches"
}

arm_archive="dark-factory-$tag-aarch64-apple-darwin.tar.gz"
intel_archive="dark-factory-$tag-x86_64-apple-darwin.tar.gz"
manifest="latest.json"
arm_sha=$(checksum_for "$arm_archive")
intel_sha=$(checksum_for "$intel_archive")
manifest_sha=$(checksum_for "$manifest")
version_stanza=
case "$tag" in
    *-*)
        version_stanza="  version \"${tag#v}\"
"
        ;;
esac

cat <<RUBY
# typed: strict
# frozen_string_literal: true

require "json"

# Homebrew bootstrap for the Dark Factory runtime.
class DarkFactory < Formula
  SOURCE_SHA = "$source_sha"
  BUILD_IDS = {
    "darwin/arm64" => "$arm_build_id",
    "darwin/amd64" => "$intel_build_id",
  }.freeze

  desc "Web-first local runtime for persistent coding-agent teams"
  homepage "https://github.com/$repository"
  url "https://github.com/$repository/releases/download/$tag/$manifest"
${version_stanza}  sha256 "$manifest_sha"
  license "MIT"

  depends_on :macos

  resource "binaries" do
    on_arm do
      url "https://github.com/$repository/releases/download/$tag/$arm_archive"
      sha256 "$arm_sha"
    end
    on_intel do
      url "https://github.com/$repository/releases/download/$tag/$intel_archive"
      sha256 "$intel_sha"
    end
  end

  def install
    resource("binaries").stage do
      bin.install "factoryd", "factory-runner", "factoryctl"
    end
  end

  def caveats
    <<~EOS
      Homebrew installs the three Dark Factory commands; it does not own the
      running factory. Run \`factoryctl init --home ABSOLUTE\` to create a fresh
      home. This formula does not install or remove a launchd job; do not use
      \`brew services\` for Dark Factory.

      \`brew upgrade\` replaces these commands but never mutates a running home.
      There is no in-runtime updater or rollback-version store.

      \`brew uninstall dark-factory\` removes only the commands. Stop any daemon
      started outside Homebrew before removing a retained factory home.
    EOS
  end

  test do
    %w[factoryd factory-runner factoryctl].each do |name|
      assert_equal "#{name} #{version}", shell_output("#{bin}/#{name} --version").strip
      identity = JSON.parse(shell_output("#{bin}/#{name} --build-identity"))
      assert_equal version.to_s, identity.fetch("version")
      assert_equal SOURCE_SHA, identity.fetch("source")
      assert_equal BUILD_IDS.fetch(identity.fetch("target")), identity.fetch("build_id")
      assert_equal true, identity.fetch("release")
    end
  end
end
RUBY
