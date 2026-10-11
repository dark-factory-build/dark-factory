#!/bin/sh
set -eu

script=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/hotspots.sh
temporary=$(mktemp -d "${TMPDIR:-/tmp}/dark-factory-hotspots-test.XXXXXX")
trap 'rm -rf "$temporary"' EXIT HUP INT TERM
cd "$temporary"
git init -q
cat >complex.go <<'EOF'
package p
func F(x bool) {
	if x {
	}
}
EOF
commit() { for f in "$@"; do case "$f" in *.go) echo '// x' >>"$f";; *) echo x >>"$f";; esac; done; git add . && git -c user.name=t -c user.email=t@t commit -qm c; }
commit complex.go; commit complex.go
commit a b; commit a b; commit a b c; commit a
expected=$(printf 'commits\tchurn\tfile\n4\t4\ta\n3\t3\tb\n2\t7\tcomplex.go\n1\t1\tc\n\nscore\tcommits\tmax complexity\tfile\tfunction\n2\t2\t1\tcomplex.go\tF\n\ntogether\tshare%%\tfile pair\n3\t100\ta  b')
[ "$("$script")" = "$expected" ] || { echo "hotspots test failed:" >&2; "$script" >&2; exit 1; }
