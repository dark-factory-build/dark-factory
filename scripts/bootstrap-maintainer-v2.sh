#!/bin/sh
set -eu

worker=dark-factory-control-plane
hostname=maintainer.darkfactory.build

# The reviewed control-plane tree is stated by the operator, not pinned here.
# A constant asserting "this is the reviewed runtime" is a declaration nothing
# observes: it rotted three times in one pull request, and each time the first
# thing to notice was a refused activation at the moment it was needed.
# `deploy-control-plane.yml` takes `expected_tree` as a dispatch input and
# proves the checkout matches it. This is the same shape of contract for the
# same decision, but deliberately not the same value: the workflow proves
# `HEAD^{tree}`, the whole repository, while this proves the `control-plane`
# subtree that is all it ships. Crossing the two fails closed either way; do
# not "unify" them.
#
# A subtree reference needs no reachability check -- it resolves through HEAD,
# so no merge strategy, branch deletion, or shallow clone can put it out of
# reach, which is what forced the old pinned commit to carry one. It is not
# self-evidently a tree, though: `HEAD:control-plane` resolves to a blob if
# that path is a symlink and to an absent gitlink if it is a submodule, and in
# both cases the bytes that would ship sit outside the object being proven.
#
# This path exists because `dispatch_control_plane_deploy` observes the
# repository, so a defect there disables the App's ability to deploy its own
# repair, and the workflow requires the App as `github.actor` so no human
# dispatch can substitute. It is not one-time: it shipped that exact repair on
# 30 Aug 2026. Routine deployments remain the App's.
test "$#" = 1 || {
    echo "usage: scripts/bootstrap-maintainer-v2.sh <reviewed-control-plane-tree>" >&2
    echo "the reviewed tree is git rev-parse <reviewed-head>:control-plane" >&2
    exit 1
}
reviewed_tree=$1
case "$reviewed_tree" in
    ????????????????????????????????????????) ;;
    *) echo "bootstrap: reviewed tree must be a full 40-character SHA-1" >&2; exit 1 ;;
esac
case "$reviewed_tree" in
    *[!0-9a-f]*) echo "bootstrap: reviewed tree must be lowercase hex" >&2; exit 1 ;;
esac

script_dir=$(CDPATH='' cd -- "$(dirname "$0")" && pwd -P)
repository_root=$(CDPATH='' cd -- "$script_dir/.." && pwd -P)
cd "$repository_root"
git=/usr/bin/git

# The script is source too: a half-applied local edit to the break-glass path
# is exactly the honest mistake this catches, and it is the one that happens
# under pressure. It cannot stop a hostile edit -- that edit would delete this
# line -- but that is not the case it is here for.
test -z "$("$git" status --porcelain=v1 --untracked-files=all \
    -- scripts/bootstrap-maintainer-v2.sh control-plane)" || {
    echo "bootstrap: the activation script or control-plane is dirty, so HEAD is not what would ship" >&2
    exit 1
}
actual_tree=$("$git" rev-parse HEAD:control-plane)
test "$("$git" cat-file -t "$actual_tree" 2>/dev/null)" = tree || {
    echo "bootstrap: HEAD:control-plane is $actual_tree, which is not a tree object" >&2
    exit 1
}
test "$actual_tree" = "$reviewed_tree" || {
    echo "bootstrap: control-plane at HEAD is $actual_tree, not the reviewed $reviewed_tree" >&2
    echo "bootstrap: check out the reviewed head, or pass the tree that head carries" >&2
    exit 1
}
# Derived from the tree just proven, never restated here. While one pinned
# commit was the only admissible source this could not drift; now that any
# reviewed tree is admissible, a restated constant would silently disagree with
# `PERMISSION_REVISION` the moment a tree bumped it, and the activation would
# promote a Worker its own authority check rejects.
revision=$("$git" show "HEAD:control-plane/src/github_app.rs" | /usr/bin/sed -n \
    's/^pub(crate) const PERMISSION_REVISION: &str = "\([a-z0-9-]*\)";$/\1/p')
case "$revision" in
    ''|*[!a-z0-9-]*)
        echo "bootstrap: could not read exactly one PERMISSION_REVISION from the proven tree" >&2
        exit 1
        ;;
esac
echo "bootstrap: permission revision $revision"

./control-plane/scripts/local-ci.sh

node=$(node -p 'process.execPath')
case "$node" in /*) ;; *) echo "bootstrap: Node path is not absolute" >&2; exit 1 ;; esac
"$node" -e 'if (Number(process.versions.node.split(".")[0]) < 22) process.exit(1)'
wrangler="$repository_root/control-plane/node_modules/wrangler/bin/wrangler.js"
test -f "$wrangler" || { echo "bootstrap: pinned Wrangler is missing" >&2; exit 1; }
test "$("$node" "$wrangler" --version)" = '4.125.0' || {
    echo "bootstrap: Wrangler is not 4.125.0" >&2
    exit 1
}

common_directory=$("$git" rev-parse --path-format=absolute --git-common-dir)
env_file=$(dirname "$common_directory")/.env.txt
test -f "$env_file" && test ! -L "$env_file" || {
    echo "bootstrap: root .env.txt must be a regular file, not a symlink" >&2
    exit 1
}
test "$(stat -f '%Lp' "$env_file")" = 600 || {
    echo "bootstrap: root .env.txt must have mode 0600" >&2
    exit 1
}

temporary=$(mktemp -d /tmp/dark-factory-maintainer-v2.XXXXXX)
mkdir -m 700 "$temporary/home" "$temporary/tmp"

secret_file="$temporary/revision.env"
printf '%s=%s\n' DARK_FACTORY_MAINTAINER_PERMISSION_REVISION "$revision" >"$secret_file"
chmod 600 "$secret_file"

exec_wrangler() {
    # The single-quoted program is intentional: the isolated child, not this
    # credential-free parent shell, expands its fixed environment.
    # shellcheck disable=SC2016
    exec /usr/bin/env -i \
        PATH=/usr/bin:/bin \
        HOME="$temporary/home" \
        TMPDIR="$temporary/tmp" \
        NO_COLOR=1 \
        CI=1 \
        WRANGLER_SEND_METRICS=false \
        CLOUDFLARE_LOAD_DEV_VARS_FROM_DOT_ENV=false \
        DARK_FACTORY_WRANGLER_PREBUILT=1 \
        DARK_FACTORY_ENV_FILE="$env_file" \
        DARK_FACTORY_CONTROL_PLANE="$repository_root/control-plane" \
        DARK_FACTORY_NODE="$node" \
        DARK_FACTORY_WRANGLER="$wrangler" \
        /bin/sh -c '
            set -eu
            extract() {
                /usr/bin/awk -v key="$1" '\''
                    index($0, key "=") == 1 { count++; value = substr($0, length(key) + 2) }
                    END { if (count == 1 && value != "") print value; else exit 1 }
                '\'' "$DARK_FACTORY_ENV_FILE"
            }
            CLOUDFLARE_API_TOKEN=$(extract CLOUDFLARE_API_TOKEN) || {
                echo "bootstrap: .env.txt needs exactly one non-empty CLOUDFLARE_API_TOKEN" >&2
                exit 1
            }
            CLOUDFLARE_ACCOUNT_ID=$(extract CLOUDFLARE_ACCOUNT_ID) || {
                echo "bootstrap: .env.txt needs exactly one non-empty CLOUDFLARE_ACCOUNT_ID" >&2
                exit 1
            }
            case "$CLOUDFLARE_ACCOUNT_ID" in
                ????????????????????????????????) ;;
                *) echo "bootstrap: invalid Cloudflare account ID" >&2; exit 1 ;;
            esac
            case "$CLOUDFLARE_ACCOUNT_ID" in *[!0-9a-f]*) echo "bootstrap: invalid Cloudflare account ID" >&2; exit 1 ;; esac
            export CLOUDFLARE_API_TOKEN CLOUDFLARE_ACCOUNT_ID
            cd "$DARK_FACTORY_CONTROL_PLANE"
            exec "$DARK_FACTORY_NODE" "$DARK_FACTORY_WRANGLER" "$@"
        ' wrangler "$@"
}

run_wrangler() {
    (exec_wrangler "$@")
}

promoted=0
verified=0
previous=''
tail_pid=''
tail_log="$temporary/readiness-tail.log"
stop_tail() {
    if test -n "$tail_pid"; then
        kill -TERM "$tail_pid" 2>/dev/null || true
        wait "$tail_pid" 2>/dev/null || true
        tail_pid=''
    fi
}
cleanup() {
    status=$?
    rollback_failed=0
    trap - EXIT HUP INT TERM
    stop_tail
    if test "$promoted" = 1 && test "$verified" = 0 && test -n "$previous"; then
        /usr/bin/grep -E \
            'readiness: (journal unavailable|app authority unverified)|journal: (stub unavailable|ready fetch failed|ready returned)|app jwt signing failed|github request (could not be built|failed)|github rejected|installation rejected on:' \
            "$tail_log" >&2 || echo "bootstrap: no readiness diagnostic reached the tail" >&2
        current=$(run_wrangler deployments status --name "$worker" --json || true)
        current_version=$(printf '%s' "$current" | "$node" -e '
            let input=""; process.stdin.on("data", c => input += c).on("end", () => {
              try { const d=JSON.parse(input).versions; if (Array.isArray(d) && d.length === 1 && d[0].percentage === 100) process.stdout.write(d[0].version_id || ""); } catch {}
            });')
        if test "$current_version" = "$version"; then
            echo "bootstrap: restoring $previous" >&2
            if ! run_wrangler rollback "$previous" --name "$worker" --yes \
                --message 'rollback failed Maintainer v3 activation'; then
                echo "bootstrap: rollback failed" >&2
                rollback_failed=1
            elif test "$(curl -sS -o /dev/null -w '%{http_code}' --max-time 20 "https://$hostname/healthz" || true)" != 200; then
                echo "bootstrap: rollback health check failed" >&2
                rollback_failed=1
            fi
        else
            echo "bootstrap: live version changed; refusing to overwrite it with rollback" >&2
            rollback_failed=1
        fi
    fi
    rm -rf "$temporary"
    test "$rollback_failed" = 0 || status=1
    exit "$status"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

deployment=$(run_wrangler deployments status --name "$worker" --json)
previous=$(printf '%s' "$deployment" | "$node" -e '
    let input=""; process.stdin.on("data", c => input += c).on("end", () => {
      const live=JSON.parse(input); const d=live.versions;
      if (!Array.isArray(d) || d.length !== 1 || d[0].percentage !== 100 || !d[0].version_id) process.exit(1);
      process.stdout.write(d[0].version_id);
    });') || { echo "bootstrap: live deployment is not one version at 100%" >&2; exit 1; }
echo "bootstrap: rollback target $previous"

upload=$(run_wrangler versions upload --name "$worker" --strict \
    --secrets-file "$secret_file" \
    --message "reviewed control-plane tree $reviewed_tree")
version=$(printf '%s\n' "$upload" | sed -n 's/^Worker Version ID: *//p' | tail -1)
test -n "$version" || { echo "bootstrap: upload returned no version ID" >&2; exit 1; }
echo "bootstrap: staged $version"

view=$(run_wrangler versions view "$version" --name "$worker" --json)
printf '%s' "$view" | "$node" -e '
  const required=["DARK_FACTORY_MAINTAINER_WEBHOOK_SECRET","DARK_FACTORY_MAINTAINER_WEBHOOK_SECRET_REVISION","DARK_FACTORY_MAINTAINER_APP_ID","DARK_FACTORY_MAINTAINER_PRIVATE_KEY_PKCS8","DARK_FACTORY_MAINTAINER_PERMISSION_REVISION","DARK_FACTORY_MAINTAINER_OPERATOR_EMAIL_SHA256","DARK_FACTORY_CLOUDFLARE_ACCESS_TEAM_DOMAIN","DARK_FACTORY_CLOUDFLARE_ACCESS_AUD","DARK_FACTORY_CLOUDFLARE_ACCESS_SERVICE_TOKEN_ID"];
  let input=""; process.stdin.on("data", c => input += c).on("end", () => {
    const v=JSON.parse(input), bindings=v.resources?.bindings || [];
    const byName=new Map(bindings.map(b => [b.name,b]));
    if (required.some(name => byName.get(name)?.type !== "secret_text")) process.exit(1);
    const journal=byName.get("DARK_FACTORY_MAINTAINER_DELIVERIES");
    if (journal?.type !== "durable_object_namespace" || journal.class_name !== "MaintainerDeliveryJournal") process.exit(1);
  });' || { echo "bootstrap: staged bindings are incomplete" >&2; exit 1; }

before_promotion=$(run_wrangler deployments status --name "$worker" --json)
before_version=$(printf '%s' "$before_promotion" | "$node" -e '
    let input=""; process.stdin.on("data", c => input += c).on("end", () => {
      const d=JSON.parse(input).versions;
      if (!Array.isArray(d) || d.length !== 1 || d[0].percentage !== 100 || !d[0].version_id) process.exit(1);
      process.stdout.write(d[0].version_id);
    });')
test "$before_version" = "$previous" || {
    echo "bootstrap: live version changed while v3 was staged; refusing promotion" >&2
    exit 1
}

(exec_wrangler tail "$worker" --format pretty --version-id "$version" \
    --method GET) >"$tail_log" 2>&1 &
tail_pid=$!
tail_ready=0
for attempt in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
    if /usr/bin/grep -Fq 'waiting for logs...' "$tail_log"; then
        tail_ready=1
        break
    fi
    if ! kill -0 "$tail_pid" 2>/dev/null; then
        wait "$tail_pid" || true
        tail_pid=''
        break
    fi
    test "$attempt" = 20 || sleep 0.25
done
test "$tail_ready" = 1 || {
    echo "bootstrap: version-specific readiness tail did not connect" >&2
    exit 1
}

# A failed response can be ambiguous after Cloudflare accepted the promotion;
# cleanup re-reads the live version before deciding whether rollback is safe.
promoted=1
run_wrangler versions deploy --name "$worker" --version-id "$version" --yes \
    --message "activate reviewed control-plane tree $reviewed_tree"

ready=0
for attempt in 1 2 3 4 5 6 7 8 9 10 11 12; do
    health=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 15 "https://$hostname/healthz" || true)
    body=$(curl -sS --max-time 30 "https://$hostname/readyz" || true)
    if test "$health" = 200 \
        && printf '%s' "$body" | grep -Fq '"status":"ready"' \
        && printf '%s' "$body" | grep -Fq '"maintainer_operations":"mcp_installation_bound_operator_and_headless"'; then
        ready=1
        break
    fi
    test "$attempt" = 12 || sleep 5
done
test "$ready" = 1 || { echo "bootstrap: live v3 readiness failed" >&2; exit 1; }

stop_tail
verified=1
printf '{"outcome":"promoted","version":"%s","previous_version":"%s"}\n' "$version" "$previous"
