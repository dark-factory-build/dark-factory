#!/bin/sh
set -eu

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
gate="$repository_root/scripts/local-ci.sh"
contributing="$repository_root/CONTRIBUTING.md"
ci="$repository_root/.github/workflows/ci.yml"
runner_manifest="$repository_root/crates/factory-runner/Cargo.toml"
runner_library="$repository_root/crates/factory-runner/src/lib.rs"
stale_control_plane_workflow="$repository_root/control-plane/.github/workflows/ci.yml"

fail() {
    echo "local-ci mode test failed: $*" >&2
    exit 1
}

grep -Fq 'cargo +1.88.0 fmt --all -- --check' "$gate" \
    || fail "legacy/Linux source mode lost rustfmt"
grep -Fq 'cargo +1.88.0 clippy --locked --workspace --all-targets --all-features -- -D warnings' "$gate" \
    || fail "legacy/Linux source mode lost clippy"
grep -Fq 'cargo +1.88.0 test --locked --workspace -- --test-threads=1' "$gate" \
    || fail "legacy/Linux source mode lost workspace tests"
grep -Fq 'git diff --check' "$gate" || fail "source gate lost diff check"

# The authoritative macOS gate is the Go gate; the retired Rust stages live
# only behind the explicit legacy flag and the Linux source preview.
final_gate=$(sed -n '/^# The shared shell-fixture gate/,$p' "$gate")
# This is a TEXT-SHAPE guard, not an execution guard: it establishes that the
# macOS arm still contains the invocation line, and nothing more. It rejects a
# bare mention of the name (the line commented out) but cannot detect the line
# being neutralized by surrounding control flow — `if false; then ... fi` still
# matches. Proving the stage actually runs needs local-ci executed against a
# recording stub, which this fixture-level test deliberately does not do.
printf '%s\n' "$final_gate" \
    | sed -n '/^[[:space:]]*macos)/,/^[[:space:]]*;;/p' \
    | grep -Eq '^[[:space:]]*/bin/sh "\$script_dir/go-ci-owned\.sh"' \
    || fail "macOS mode lost the authoritative Go gate invocation line"
# Self-test for the one mutation this shape guard does catch: the invocation
# neutralized behind a comment while the name survives.
mutated_gate=$(printf '%s\n' "$final_gate" \
    | sed 's|^\([[:space:]]*\)/bin/sh "\$script_dir/go-ci-owned\.sh"|\1: # /bin/sh "$script_dir/go-ci-owned.sh" disabled today|')
if printf '%s\n' "$mutated_gate" \
    | sed -n '/^[[:space:]]*macos)/,/^[[:space:]]*;;/p' \
    | grep -Eq '^[[:space:]]*/bin/sh "\$script_dir/go-ci-owned\.sh"'; then
    fail "the go-ci-owned invocation guard failed its own mutation self-test"
fi
if printf '%s\n' "$final_gate" \
    | sed -n '/^[[:space:]]*macos)/,/^[[:space:]]*;;/p' | grep -Fq 'cargo '; then
    fail "macOS default mode still runs cargo outside the legacy flag"
fi
printf '%s\n' "$final_gate" \
    | sed -n '/--legacy-rust | --linux-source)/,/^[[:space:]]*;;/p' \
    | grep -Fq 'cargo +1.88.0 test' \
    || fail "legacy flag lost the retired Rust stages"
legacy_fixture_mode=$(sed -n '/^[[:space:]]*--legacy-rust)/,/^[[:space:]]*;;/p' "$gate")
for mac_fixture in test-prepare-release-source.sh test-publish-release.sh \
    test-package-release.sh test-macos-launchd-release-proof.sh; do
    printf '%s\n' "$legacy_fixture_mode" | grep -Fq "$mac_fixture" \
        || fail "legacy Rust mode lost fixture $mac_fixture"
done

runner_lib_section=$(awk '
    $0 == "[lib]" { in_lib = 1; next }
    /^\[/ { if (in_lib) exit }
    in_lib { print }
' "$runner_manifest")
if printf '%s\n' "$runner_lib_section" \
    | grep -Eq '^[[:space:]]*test[[:space:]]*=[[:space:]]*false'; then
    fail "factory-runner library unit tests are disabled"
fi
grep -Fq '#[cfg(test)]' "$runner_library" \
    || fail "factory-runner lost its substantive library unit tests"

linux_mode=$(sed -n '/^[[:space:]]*--linux-source)/,/^[[:space:]]*;;/p' "$gate")
printf '%s\n' "$linux_mode" | grep -Fq './scripts/check-toolchain-pins.sh' \
    || fail "Linux source mode lost toolchain pin validation"
for mac_fixture in test-prepare-release-source.sh test-publish-release.sh \
    test-package-release.sh test-macos-launchd-release-proof.sh; do
    if printf '%s\n' "$linux_mode" | grep -Fq "$mac_fixture"; then
        fail "Linux source mode invokes macOS fixture $mac_fixture"
    fi
done

macos_mode=$(sed -n '/^[[:space:]]*macos)/,/^[[:space:]]*;;/p' "$gate")
for mac_fixture in test-prepare-release-source.sh test-publish-release.sh \
    test-package-release.sh test-macos-launchd-release-proof.sh; do
    printf '%s\n' "$macos_mode" | grep -Fq "$mac_fixture" \
        || fail "macOS mode lost fixture $mac_fixture"
done
shared_gate=$(sed -n '/^# Measure after/,$p' "$gate")
e2e_tool_fixture_line=$(printf '%s\n' "$shared_gate" \
    | grep -n -F './scripts/test-go-e2e-tools.sh' \
    | head -1 | cut -d: -f1)
owned_gate_line=$(printf '%s\n' "$shared_gate" \
    | grep -n -F '/bin/sh "$script_dir/go-ci-owned.sh"' \
    | head -1 | cut -d: -f1)
[ -n "$e2e_tool_fixture_line" ] || fail "shared source gate lost the Go E2E tool fixture"
[ -n "$owned_gate_line" ] && [ "$e2e_tool_fixture_line" -lt "$owned_gate_line" ] \
    || fail "Go E2E tool fixture does not run before the heavy Go gate"
if printf '%s\n' "$shared_gate" \
    | grep -Fq './scripts/test-macos-launchd-release-proof.sh'; then
    fail "shared source gate invokes the macOS launchd fixture"
fi

linux_job=$(sed -n '/^  linux:/,$p' "$ci")
line_of() {
    printf '%s\n' "$linux_job" | grep -n -F "$1" | head -1 | cut -d: -f1
}
linux_gate_line=$(line_of 'name: Run the Linux authoritative gate')
linux_build_line=$(line_of 'name: Build the workspace binaries')
linux_smoke_line=$(line_of 'name: Run the source contributor smoke')
[ -n "$linux_gate_line" ] && [ -n "$linux_build_line" ] && [ -n "$linux_smoke_line" ] \
    || fail "Linux CI lost its gate, binary build, or smoke step"
[ "$linux_gate_line" -lt "$linux_build_line" ] \
    || fail "Linux CI rebuilds source-gate inputs after building smoke binaries"
[ "$linux_build_line" -lt "$linux_smoke_line" ] \
    || fail "Linux CI does not build its smoke binaries before using them"
printf '%s\n' "$linux_job" \
    | grep -Fq 'cargo +1.88.0 build --locked --workspace --bins' \
    || fail "Linux CI does not limit its post-gate build to smoke binaries"
if printf '%s\n' "$linux_job" | grep -Fq 'name: Check build headroom'; then
    fail "Linux CI duplicates the authoritative gate's build-headroom check"
fi

control_plane_job=$(sed -n '/^  control-plane:/,/^  linux:/p' "$ci")
control_line_of() {
    printf '%s\n' "$control_plane_job" | grep -n -F "$1" | head -1 | cut -d: -f1
}
node_runtime_line=$(control_line_of 'name: Verify the supported Node runtime')
control_gate_line=$(control_line_of 'run: ./control-plane/scripts/local-ci.sh')
[ -n "$node_runtime_line" ] && [ -n "$control_gate_line" ] \
    || fail "hosted control-plane CI lost its Node runtime check or gate"
[ "$node_runtime_line" -lt "$control_gate_line" ] \
    || fail "hosted control-plane CI runs its gate before checking Node"
[ ! -e "$stale_control_plane_workflow" ] \
    || fail "control-plane gate remains hidden in an undiscovered nested workflow"

grep -Fxq './scripts/local-ci.sh' "$contributing" \
    || fail "CONTRIBUTING lost the macOS gate command"
grep -Fxq './scripts/local-ci.sh --linux-source' "$contributing" \
    || fail "CONTRIBUTING does not use the Linux source gate"
grep -Fxq './scripts/linux-contributor-smoke.sh' "$contributing" \
    || fail "CONTRIBUTING lost the Linux source smoke"

sh -n "$gate"
echo "local-ci mode tests passed"
