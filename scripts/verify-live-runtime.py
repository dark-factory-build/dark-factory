#!/usr/bin/env python3
"""Observe installed runtime identity and API health for a release receipt."""
import argparse
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys


def runtime_status_matches_revision(status, revision):
    build = status.get('build') if isinstance(status, dict) else None
    return (isinstance(build, dict) and build.get('source') == revision
            and status.get('ready') is True)


def observe(home, expected_sha=None):
    binary_root = Path(str(home) + '.service') / 'bin' / 'current'
    go = shutil.which('go')
    if go is None:
        raise ValueError('go tool is unavailable in PATH')
    revisions = set()
    receipts = set()
    for name in ('factoryctl', 'factoryd', 'factory-runner'):
        metadata = subprocess.run([go, 'version', '-m', str(binary_root / name)], check=True, capture_output=True, text=True, timeout=15).stdout
        revision = re.search(r'vcs\.revision=([0-9a-f]{40})', metadata)
        if revision is None or 'vcs.modified=false' not in metadata:
            raise ValueError('installed binary identity is missing or modified')
        identity = json.loads(subprocess.run([str(binary_root / name), '--build-identity'], check=True, capture_output=True, text=True, timeout=15).stdout)
        if identity.get('source') != revision.group(1) or identity.get('release') is not True:
            raise ValueError('installed build receipt disagrees with source identity')
        revisions.add(revision.group(1))
        receipts.add(tuple(identity.get(field) for field in ('version', 'source', 'target', 'build_id')))
    if len(revisions) != 1:
        raise ValueError('installed binaries have different revisions')
    if len(receipts) != 1:
        raise ValueError('installed binaries have different build receipts')
    env = dict(os.environ, DARK_FACTORY_SOCKET=str(home / 'runtimes' / 'factory.sock'), DARK_FACTORY_OPERATOR_TOKEN_FILE=str(home / 'operator.token'))
    status = json.loads(subprocess.run([str(binary_root / 'factoryctl'), 'web', 'status'], env=env, check=True, capture_output=True, text=True, timeout=15).stdout)
    revision = revisions.pop()
    if not runtime_status_matches_revision(status, revision):
        raise ValueError('running daemon build identity disagrees with installed source')
    return {'sha': revision, 'healthy': True, 'daemon_source': revision}


if __name__ == '__main__':
    try:
        parser = argparse.ArgumentParser(description=__doc__)
        parser.add_argument('--home', type=Path, default=Path.home() / '.dark-factory')
        parser.add_argument('sha')
        args = parser.parse_args()
        if re.fullmatch('[0-9a-f]{40}', args.sha) is None or not args.home.is_absolute():
            raise ValueError('expected full SHA and absolute factory home')
        print(json.dumps(observe(args.home, args.sha)))
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        print('verify-live-runtime: ' + str(error), file=sys.stderr)
        raise SystemExit(1)
