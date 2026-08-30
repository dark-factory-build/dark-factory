#!/bin/sh
set -eu

target_commit=3631e4712068c01e25d8327318e2e1b2d8b7c16d
worker=dark-factory-control-plane
hostname=maintainer.darkfactory.build
revision=maintainer-operations-v2
failed_v2=5391280b-5840-4c9f-9ec2-55d37c4a0022
stable_v1=47c98fa9-62ef-432d-8445-e2a7f4c83e85

mode=bootstrap
if test "$#" = 1 && test "$1" = recover-v1; then
    mode=recover-v1
elif test "$#" != 0; then
    echo "usage: scripts/bootstrap-maintainer-v2.sh [recover-v1]" >&2
    exit 1
fi

script_dir=$(CDPATH='' cd -- "$(dirname "$0")" && pwd -P)
repository_root=$(CDPATH='' cd -- "$script_dir/.." && pwd -P)
cd "$repository_root"
git=/usr/bin/git

"$git" diff --quiet "$target_commit" HEAD -- \
    control-plane/src control-plane/migrations \
    control-plane/Cargo.toml control-plane/Cargo.lock \
    control-plane/package.json control-plane/package-lock.json || {
    echo "bootstrap: control-plane runtime is not the reviewed v2 main runtime" >&2
    exit 1
}
test -z "$("$git" status --porcelain=v1 --untracked-files=all -- scripts/bootstrap-maintainer-v2.sh control-plane)" || {
    echo "bootstrap: reviewed bootstrap or control-plane source is dirty" >&2
    exit 1
}

# Build and test before any process reads the credential file.
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

run_wrangler() {
    # The single-quoted program is intentional: the isolated child, not this
    # credential-free parent shell, expands its fixed environment.
    # shellcheck disable=SC2016
    /usr/bin/env -i \
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

promoted=0
verified=0
previous=''
cleanup() {
    status=$?
    rollback_failed=0
    trap - EXIT HUP INT TERM
    if test "$promoted" = 1 && test "$verified" = 0 && test -n "$previous"; then
        current=$(run_wrangler deployments status --name "$worker" --json || true)
        current_version=$(printf '%s' "$current" | "$node" -e '
            let input=""; process.stdin.on("data", c => input += c).on("end", () => {
              try { const d=JSON.parse(input).versions; if (Array.isArray(d) && d.length === 1 && d[0].percentage === 100) process.stdout.write(d[0].version_id || ""); } catch {}
            });')
        if test "$current_version" = "$version"; then
            echo "bootstrap: restoring $previous" >&2
            if ! run_wrangler versions rollback "$previous" --name "$worker" --yes \
                --message 'rollback failed Maintainer v2 activation'; then
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

if test "$mode" = recover-v1; then
    test "$previous" = "$failed_v2" || {
        echo "bootstrap: refusing recovery because the failed v2 version is not live" >&2
        exit 1
    }
    run_wrangler versions rollback "$stable_v1" --name "$worker" --yes \
        --message 'restore stable Maintainer v1 after failed v2 readiness'
    ready=0
    for attempt in 1 2 3 4 5 6; do
        body=$(curl -sS --max-time 30 "https://$hostname/readyz" || true)
        if printf '%s' "$body" | grep -Fq '"status":"ready"' \
            && printf '%s' "$body" | grep -Fq '"maintainer_operations":"mcp_six_tools_operator_and_headless"'; then
            ready=1
            break
        fi
        test "$attempt" = 6 || sleep 5
    done
    test "$ready" = 1 || { echo "bootstrap: stable v1 recovery readiness failed" >&2; exit 1; }
    printf '{"outcome":"recovered","version":"%s"}\n' "$stable_v1"
    exit 0
fi

upload=$(run_wrangler versions upload --name "$worker" --strict \
    --secrets-file "$secret_file" \
    --message "reviewed v2 main $target_commit")
version=$(printf '%s\n' "$upload" | sed -n 's/^Worker Version ID: *//p' | tail -1)
test -n "$version" || { echo "bootstrap: upload returned no version ID" >&2; exit 1; }
echo "bootstrap: staged $version"

view=$(run_wrangler versions view "$version" --name "$worker" --json)
printf '%s' "$view" | "$node" -e '
  const required=["DARK_FACTORY_MAINTAINER_WEBHOOK_SECRET","DARK_FACTORY_MAINTAINER_WEBHOOK_SECRET_REVISION","DARK_FACTORY_MAINTAINER_APP_ID","DARK_FACTORY_MAINTAINER_PRIVATE_KEY_PKCS8","DARK_FACTORY_MAINTAINER_PERMISSION_REVISION","DARK_FACTORY_MAINTAINER_REPOSITORY","DARK_FACTORY_MAINTAINER_REPOSITORY_OWNER_ID","DARK_FACTORY_MAINTAINER_REPOSITORY_ID","DARK_FACTORY_MAINTAINER_OPERATOR_EMAIL_SHA256","DARK_FACTORY_CLOUDFLARE_ACCESS_TEAM_DOMAIN","DARK_FACTORY_CLOUDFLARE_ACCESS_AUD","DARK_FACTORY_CLOUDFLARE_ACCESS_SERVICE_TOKEN_ID"];
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
    echo "bootstrap: live version changed while v2 was staged; refusing promotion" >&2
    exit 1
}

# A failed response can be ambiguous after Cloudflare accepted the promotion;
# cleanup re-reads the live version before deciding whether rollback is safe.
promoted=1
run_wrangler versions deploy --name "$worker" --version-id "$version" --yes \
    --message "activate reviewed maintainer v2 $target_commit"

ready=0
for attempt in 1 2 3 4 5 6 7 8 9 10 11 12; do
    health=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 15 "https://$hostname/healthz" || true)
    body=$(curl -sS --max-time 30 "https://$hostname/readyz" || true)
    if test "$health" = 200 \
        && printf '%s' "$body" | grep -Fq '"status":"ready"' \
        && printf '%s' "$body" | grep -Fq '"maintainer_operations":"mcp_repository_bound_operator_and_headless"'; then
        ready=1
        break
    fi
    test "$attempt" = 12 || sleep 5
done
test "$ready" = 1 || { echo "bootstrap: live v2 readiness failed" >&2; exit 1; }

verified=1
printf '{"outcome":"promoted","version":"%s","previous_version":"%s"}\n' "$version" "$previous"
