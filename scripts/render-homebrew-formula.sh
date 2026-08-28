#!/bin/sh
# Render the exact Formula/dark-factory.rb candidate for the custom tap.
# Publishing that candidate to the public tap remains a reviewed handoff.
set -eu

tag="${1:-}"
checksums="${2:-}"
repository="${3:-dark-factory-build/dark-factory}"
if [ -z "$tag" ] || [ -z "$checksums" ] || [ "$#" -gt 3 ]; then
    echo "usage: scripts/render-homebrew-formula.sh <tag> <SHA256SUMS> [<owner/repo>]" >&2
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

# Homebrew bootstrap for the Dark Factory runtime.
class DarkFactory < Formula
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
      running factory. Run \`factoryctl init --home ABSOLUTE\`, then use the
      explicit \`factoryctl service\` commands to manage the launchd job.
      Do not use \`brew services\` for Dark Factory.

      \`brew upgrade\` replaces these commands but never mutates a running home.
      There is no in-runtime updater or rollback-version store.

      \`brew uninstall dark-factory\` removes only the commands. Use the explicit
      service uninstall operation before removing a retained factory home.
    EOS
  end

  test do
    %w[factoryd factory-runner factoryctl].each do |name|
      assert_equal "#{name} #{version}", shell_output("#{bin}/#{name} --version").strip
    end
  end
end
RUBY
