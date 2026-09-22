#!/bin/sh
set -eu

usage() {
    echo "usage: scripts/gh-comment.sh [--repo OWNER/REPO] [--body-file FILE|-] ISSUE_OR_PR" >&2
    exit 2
}

repo=
body_file=-
issue_or_pr=

while [ "$#" -gt 0 ]; do
    case "$1" in
        --repo)
            [ "$#" -ge 2 ] || usage
            repo=$2
            shift 2
            ;;
        --body-file)
            [ "$#" -ge 2 ] || usage
            body_file=$2
            shift 2
            ;;
        --)
            shift
            [ "$#" -eq 1 ] || usage
            issue_or_pr=$1
            shift
            ;;
        -*)
            usage
            ;;
        *)
            [ -z "$issue_or_pr" ] || usage
            issue_or_pr=$1
            shift
            ;;
    esac
done

case "$issue_or_pr" in
    ''|*[!0-9]*) usage ;;
    0*) usage ;;
esac

if [ -n "$repo" ]; then
    case "$repo" in
        */*/*|/*|*/|*' '*|*'	'*) usage ;;
    esac
fi

if [ "$body_file" != - ] && [ ! -f "$body_file" ]; then
    echo "gh-comment: body file does not exist: $body_file" >&2
    usage
fi

gh=$(command -v gh) || {
    echo "gh-comment: gh is unavailable" >&2
    exit 1
}

set -- issue comment "$issue_or_pr"
if [ -n "$repo" ]; then
    set -- "$@" --repo "$repo"
fi
set -- "$@" --body-file "$body_file"
exec "$gh" "$@"
