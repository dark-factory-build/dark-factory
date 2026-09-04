#!/bin/sh

# This file is sourced at every local-CI entry boundary. Resolve the exact
# Node/Corepack pair from the caller's toolchain before replacing the
# environment with the small, credential-free set the gate needs.
ci_original_path=${PATH-}
ci_node=${DF_CI_NODE-}
ci_corepack=${DF_CI_COREPACK-}
ci_old_ifs=$IFS
IFS=:
ci_probe() {
    /usr/bin/env -i \
        PATH=/usr/bin:/bin HOME=/dev/null TMPDIR=/tmp \
        GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null \
        GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_COUNT=0 "$@"
}
if [ -n "$ci_node" ] && [ -n "$ci_corepack" ] \
    && [ -x "$ci_node" ] && [ -f "$ci_corepack" ] && [ -x "$ci_corepack" ]; then
    ci_existing_pair_valid=1
    ci_node_version=$(ci_probe "$ci_node" --version 2>/dev/null) || ci_node_version=
    ci_node_major=$(/usr/bin/printf '%s\n' "$ci_node_version" \
        | /usr/bin/sed -n 's/^v\([0-9][0-9]*\)\..*/\1/p')
    ci_corepack_version=$(ci_probe "$ci_node" "$ci_corepack" --version 2>/dev/null) || ci_corepack_version=
    case "$ci_node_major" in
        ''|*[!0-9]*) ci_existing_pair_valid=0 ;;
    esac
    [ "$ci_node_major" -ge 22 ] 2>/dev/null || ci_existing_pair_valid=0
    [ -n "$ci_corepack_version" ] || ci_existing_pair_valid=0
    [ "$ci_existing_pair_valid" -eq 1 ] || { ci_node=; ci_corepack=; }
else
    ci_node=
    ci_corepack=
fi
if [ -z "$ci_node" ]; then
for ci_path_entry in $ci_original_path; do
    [ -n "$ci_path_entry" ] || ci_path_entry=.
    case "$ci_path_entry" in
        /*) ;;
        *) continue ;;
    esac
    ci_candidate_node="$ci_path_entry/node"
    ci_candidate_corepack="$ci_path_entry/corepack"
    [ -x "$ci_candidate_node" ] && [ -f "$ci_candidate_corepack" ] \
        && [ -x "$ci_candidate_corepack" ] || continue
    ci_node_version=
    ci_node_version=$(ci_probe "$ci_candidate_node" --version 2>/dev/null) || ci_node_version=
    ci_node_major=$(/usr/bin/printf '%s\n' "$ci_node_version" \
        | /usr/bin/sed -n 's/^v\([0-9][0-9]*\)\..*/\1/p')
    case "$ci_node_major" in
        ''|*[!0-9]*) continue ;;
    esac
    [ "$ci_node_major" -ge 22 ] 2>/dev/null || continue
    ci_corepack_version=
    ci_corepack_version=$(ci_probe "$ci_candidate_node" "$ci_candidate_corepack" --version 2>/dev/null) || ci_corepack_version=
    [ -n "$ci_corepack_version" ] || continue
    ci_node=$ci_candidate_node
    ci_corepack=$ci_candidate_corepack
    break
done
fi
IFS=$ci_old_ifs
[ -n "$ci_node" ] && [ -n "$ci_corepack" ] || {
    echo "local-ci: no coherent Node >=22/Corepack pair found" >&2
    return 1
}

ci_saved_df_gate_fault=${DF_GATE_FAULT-}
ci_have_df_gate_fault=${DF_GATE_FAULT+yes}
ci_saved_local_ci_lease_held=${DARK_FACTORY_LOCAL_CI_LEASE_HELD-}
ci_have_local_ci_lease_held=${DARK_FACTORY_LOCAL_CI_LEASE_HELD+yes}
ci_saved_local_ci_wait=${DARK_FACTORY_LOCAL_CI_WAIT-}
ci_have_local_ci_wait=${DARK_FACTORY_LOCAL_CI_WAIT+yes}
ci_saved_local_ci_test_sentinel=${DARK_FACTORY_LOCAL_CI_TEST_SENTINEL-}
ci_have_local_ci_test_sentinel=${DARK_FACTORY_LOCAL_CI_TEST_SENTINEL+yes}
ci_saved_pause_before=${DARK_FACTORY_LOCAL_CI_TEST_PAUSE_BEFORE_GROUP_TRAPS-}
ci_have_pause_before=${DARK_FACTORY_LOCAL_CI_TEST_PAUSE_BEFORE_GROUP_TRAPS+yes}
ci_saved_pause_after=${DARK_FACTORY_LOCAL_CI_TEST_PAUSE_AFTER_LOCKF-}
ci_have_pause_after=${DARK_FACTORY_LOCAL_CI_TEST_PAUSE_AFTER_LOCKF+yes}
ci_saved_owner_marker=${DARK_FACTORY_LOCAL_CI_TEST_OWNER_MARKER-}
ci_have_owner_marker=${DARK_FACTORY_LOCAL_CI_TEST_OWNER_MARKER+yes}
ci_saved_holder_pid=${DARK_FACTORY_LOCAL_CI_TEST_HOLDER_PID_FILE-}
ci_have_holder_pid=${DARK_FACTORY_LOCAL_CI_TEST_HOLDER_PID_FILE+yes}
ci_saved_wrapper_pid=${DARK_FACTORY_LOCAL_CI_TEST_WRAPPER_PID_FILE-}
ci_have_wrapper_pid=${DARK_FACTORY_LOCAL_CI_TEST_WRAPPER_PID_FILE+yes}
ci_saved_fail_after=${DARK_FACTORY_LOCAL_CI_TEST_FAIL_AFTER_MARKER-}
ci_have_fail_after=${DARK_FACTORY_LOCAL_CI_TEST_FAIL_AFTER_MARKER+yes}

# A sourced file cannot use env -i for its caller. Enumerate and clear the
# inherited environment, then restore only the explicit test controls above.
ci_environment_names=$(/usr/bin/env | /usr/bin/sed -n 's/=.*//p')
IFS='
'
for ci_environment_name in $ci_environment_names; do
    unset "$ci_environment_name"
done
IFS=$ci_old_ifs

ci_script_dir=$(CDPATH= cd -- "$(/usr/bin/dirname "$0")" && pwd -P)
ci_repository_root=$(CDPATH= cd -- "$ci_script_dir/.." && pwd -P)
ci_tools_root="$ci_repository_root/.tools"
if [ -L "$ci_tools_root" ] || { [ -e "$ci_tools_root" ] && [ ! -d "$ci_tools_root" ]; }; then
    echo "local-ci: refusing unsafe .tools path" >&2
    return 1
fi
[ -d "$ci_tools_root" ] || /bin/mkdir "$ci_tools_root"
ci_cache_root="$ci_tools_root/local-ci"
if [ -L "$ci_cache_root" ] || { [ -e "$ci_cache_root" ] && [ ! -d "$ci_cache_root" ]; }; then
    echo "local-ci: refusing unsafe .tools/local-ci path" >&2
    return 1
fi
[ -d "$ci_cache_root" ] || /bin/mkdir "$ci_cache_root"
ci_cache_children='corepack go-build go-mod go cache data state'
for ci_cache_child in $ci_cache_children; do
    ci_cache_path="$ci_cache_root/$ci_cache_child"
    if [ -L "$ci_cache_path" ] || { [ -e "$ci_cache_path" ] && [ ! -d "$ci_cache_path" ]; }; then
        echo "local-ci: refusing unsafe .tools/local-ci/$ci_cache_child path" >&2
        return 1
    fi
done
/bin/chmod 700 "$ci_cache_root"
for ci_cache_child in $ci_cache_children; do
    ci_cache_path="$ci_cache_root/$ci_cache_child"
    [ -d "$ci_cache_path" ] || /bin/mkdir "$ci_cache_path"
done

export DF_CI_NODE="$ci_node" DF_CI_COREPACK="$ci_corepack"
export DARK_FACTORY_E2E_NODE="$ci_node" DARK_FACTORY_E2E_COREPACK="$ci_corepack"
export DF_CI_CACHE_ROOT="$ci_cache_root"
export PATH=/opt/homebrew/bin:/usr/bin:/bin
export HOME=/var/empty TMPDIR=/tmp GOENV=off
export XDG_CONFIG_HOME=/var/empty XDG_CACHE_HOME="$ci_cache_root/cache"
export XDG_DATA_HOME="$ci_cache_root/data" XDG_STATE_HOME="$ci_cache_root/state"
export COREPACK_HOME="$ci_cache_root/corepack"
export npm_config_userconfig=/var/empty/.npmrc NPM_CONFIG_USERCONFIG=/var/empty/.npmrc
export npm_config_globalconfig=/var/empty/.npmrc-global NPM_CONFIG_GLOBALCONFIG=/var/empty/.npmrc-global
export NETRC=/dev/null
export GOPATH="$ci_cache_root/go" GOCACHE="$ci_cache_root/go-build" GOMODCACHE="$ci_cache_root/go-mod"
export LC_ALL=C
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_COUNT=0
if [ -n "$ci_have_df_gate_fault" ]; then export DF_GATE_FAULT="$ci_saved_df_gate_fault"; fi
if [ -n "$ci_have_local_ci_lease_held" ]; then export DARK_FACTORY_LOCAL_CI_LEASE_HELD="$ci_saved_local_ci_lease_held"; fi
if [ -n "$ci_have_local_ci_wait" ]; then export DARK_FACTORY_LOCAL_CI_WAIT="$ci_saved_local_ci_wait"; fi
if [ -n "$ci_have_local_ci_test_sentinel" ]; then export DARK_FACTORY_LOCAL_CI_TEST_SENTINEL="$ci_saved_local_ci_test_sentinel"; fi
if [ -n "$ci_have_pause_before" ]; then export DARK_FACTORY_LOCAL_CI_TEST_PAUSE_BEFORE_GROUP_TRAPS="$ci_saved_pause_before"; fi
if [ -n "$ci_have_pause_after" ]; then export DARK_FACTORY_LOCAL_CI_TEST_PAUSE_AFTER_LOCKF="$ci_saved_pause_after"; fi
if [ -n "$ci_have_owner_marker" ]; then export DARK_FACTORY_LOCAL_CI_TEST_OWNER_MARKER="$ci_saved_owner_marker"; fi
if [ -n "$ci_have_holder_pid" ]; then export DARK_FACTORY_LOCAL_CI_TEST_HOLDER_PID_FILE="$ci_saved_holder_pid"; fi
if [ -n "$ci_have_wrapper_pid" ]; then export DARK_FACTORY_LOCAL_CI_TEST_WRAPPER_PID_FILE="$ci_saved_wrapper_pid"; fi
if [ -n "$ci_have_fail_after" ]; then export DARK_FACTORY_LOCAL_CI_TEST_FAIL_AFTER_MARKER="$ci_saved_fail_after"; fi
