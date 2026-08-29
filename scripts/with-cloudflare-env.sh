#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(/usr/bin/dirname "$0")" && pwd -P)
repository_root=$(CDPATH='' cd -- "$script_dir/.." && pwd -P)
trusted_path=/opt/homebrew/bin:/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin
go_command=$(PATH="$trusted_path" command -v go) || {
    echo "cloudflare-admin: pinned Go is unavailable" >&2
    exit 1
}
case "$*" in
    'dns status'|'dns publish-app') ;;
    *)
        echo "usage: scripts/with-cloudflare-env.sh dns status" >&2
        echo "       scripts/with-cloudflare-env.sh dns publish-app" >&2
        exit 2
        ;;
esac

clean_git() {
    /usr/bin/env -i \
        HOME=/var/empty \
        PATH=/usr/bin:/bin \
        LANG=C \
        LC_ALL=C \
        GIT_CONFIG_GLOBAL=/dev/null \
        GIT_CONFIG_SYSTEM=/dev/null \
        GIT_CONFIG_NOSYSTEM=1 \
        GIT_TERMINAL_PROMPT=0 \
        /usr/bin/git "$@"
}

reviewed_commit=$(clean_git -C "$repository_root" rev-parse --verify 'HEAD^{commit}') || {
    echo "cloudflare-admin: cannot resolve the reviewed helper commit" >&2
    exit 1
}

verify_reviewed_tree() {
    current_commit=$(clean_git -C "$repository_root" rev-parse --verify 'HEAD^{commit}') || {
        echo "cloudflare-admin: cannot verify the reviewed helper commit" >&2
        return 1
    }
    if [ "$current_commit" != "$reviewed_commit" ]; then
        echo "cloudflare-admin: repository HEAD changed during helper build" >&2
        return 1
    fi
    clean_git -C "$repository_root" \
        cat-file -e "$reviewed_commit:scripts/with-cloudflare-env.sh" || {
        echo "cloudflare-admin: helper is not committed at the reviewed commit" >&2
        return 1
    }
    clean_git -C "$repository_root" \
        diff --quiet --no-ext-diff "$reviewed_commit" -- \
        scripts/with-cloudflare-env.sh cmd/cloudflare-admin internal/cloudflareadmin || {
        echo "cloudflare-admin: helper source differs from the reviewed commit" >&2
        return 1
    }
}

verify_reviewed_tree

temporary=$(/usr/bin/mktemp -d "/tmp/dark-factory-cloudflare-admin.XXXXXX")
trap '/bin/rm -rf "$temporary"' EXIT HUP INT TERM
/bin/mkdir -m 700 "$temporary/source" "$temporary/home" "$temporary/go-cache"
archive="$temporary/source.tar"
clean_git -C "$repository_root" \
    archive --format=tar --output="$archive" "$reviewed_commit" -- \
    go.mod go.sum cmd/cloudflare-admin internal/cloudflareadmin
/usr/bin/tar -xf "$archive" -C "$temporary/source"
/bin/rm "$archive"

/usr/bin/env -i \
    HOME="$temporary/home" \
    PATH="$trusted_path" \
    LANG=C \
    LC_ALL=C \
    GOCACHE="$temporary/go-cache" \
    GOENV=off \
    GOWORK=off \
    GOPROXY=off \
    GOSUMDB=off \
    GOTOOLCHAIN=local \
    "$go_command" -C "$temporary/source" build -buildvcs=false -trimpath \
    -ldflags=-X=main.wrapperReceipt=dark-factory-reviewed-wrapper-v1 \
    -o "$temporary/cloudflare-admin" ./cmd/cloudflare-admin

verify_reviewed_tree
cd "$repository_root"
/usr/bin/env -i LANG=C LC_ALL=C "$temporary/cloudflare-admin" "$@"
