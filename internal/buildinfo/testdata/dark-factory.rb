# typed: strict
# frozen_string_literal: true

require "json"

# Homebrew bootstrap for the Dark Factory runtime.
class DarkFactory < Formula
  SOURCE_SHA = "1234567890abcdef1234567890abcdef12345678"
  BUILD_IDS = {
    "darwin/arm64" => "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "darwin/amd64" => "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  }.freeze

  desc "Web-first local runtime for persistent coding-agent teams"
  homepage "https://github.com/example/project"
  url "https://github.com/example/project/releases/download/v1.2.3/latest.json"
  sha256 "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
  license "MIT"

  depends_on :macos

  resource "binaries" do
    on_arm do
      url "https://github.com/example/project/releases/download/v1.2.3/dark-factory-v1.2.3-aarch64-apple-darwin.tar.gz"
      sha256 "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
    end
    on_intel do
      url "https://github.com/example/project/releases/download/v1.2.3/dark-factory-v1.2.3-x86_64-apple-darwin.tar.gz"
      sha256 "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
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
      running factory. Run `factoryctl init --home ABSOLUTE` to create a fresh home.
      This formula does not install or remove a launchd job; do not use
      `brew services` for Dark Factory.

      `brew upgrade` replaces these commands but never mutates a running home.
      There is no in-runtime updater or rollback-version store.

      `brew uninstall dark-factory` removes commands. Stop or unload any daemon
      first; retained factory data is untouched.
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
