#!/usr/bin/env python3
"""Fail on known OSV advisories for what the release ships: the Go standard
library at go.mod's go line (release builds use exactly that toolchain), the
modules go.mod requires (since Go 1.17 that is every module the build uses),
and web/pnpm-lock.yaml. scripts/advisory-allowlist accepts an advisory with a
reason, one "ID reason" per line."""
import json
import re
import sys
import urllib.request

go_mod = open('go.mod').read()
queries = [('Go', 'stdlib', re.search(r'(?m)^go (\S+)$', go_mod).group(1))]
queries += [('Go', name, version) for name, version in
            re.findall(r'(?m)^\s*(?:require\s+)?([a-z0-9.-]+\.[a-z]+/\S+) (v\S+)', go_mod)]
packages = open('web/pnpm-lock.yaml').read().split('\npackages:\n', 1)[1].split('\nsnapshots:\n', 1)[0]
queries += [('npm',) + tuple(key.strip("'").rsplit('@', 1)) for key in re.findall(r'(?m)^  (\S.*):$', packages)]

allowed = {}
for line in open('scripts/advisory-allowlist'):
    entry = line.split('#', 1)[0].split(None, 1)
    if len(entry) == 1:
        sys.exit(f'check-advisories: allowlist entry {entry[0]} has no reason')
    if entry:
        allowed[entry[0]] = entry[1].strip()

found = []
# ponytail: one querybatch of at most 1000 queries without paging; split the
# batch if the lockfiles outgrow it.
assert len(queries) <= 1000, len(queries)
request = urllib.request.Request('https://api.osv.dev/v1/querybatch', json.dumps({'queries': [
    {'package': {'ecosystem': ecosystem, 'name': name}, 'version': version}
    for ecosystem, name, version in queries]}).encode(), {'Content-Type': 'application/json'})
with urllib.request.urlopen(request, timeout=60) as response:
    results = json.load(response)['results']
for (ecosystem, name, version), result in zip(queries, results):
    for vuln in result.get('vulns', []):
        if vuln['id'] not in allowed:
            found.append(f'{ecosystem} {name} {version}: https://osv.dev/vulnerability/{vuln["id"]}')

print(f'check-advisories: {len(queries)} packages, {len(allowed)} allowlisted advisories', flush=True)
if found:
    print('\n'.join(found), file=sys.stderr)
    sys.exit('check-advisories: upgrade, or add "ID reason" to scripts/advisory-allowlist')
