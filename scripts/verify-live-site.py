#!/usr/bin/env python3
"""Observe the production alias's Vercel revision and HTTP health."""
import json
import re
from pathlib import Path
import subprocess
import sys
import urllib.request


def valid_identity(deployment):
    identity = deployment.get('id')
    return isinstance(identity, str) and identity != ''


def observe_deployment():
    result = subprocess.run(['vercel', 'api', '/v13/deployments/app.darkfactory.build', '--raw'], check=True, capture_output=True, text=True, timeout=30)
    deployment = json.loads(result.stdout)
    if not isinstance(deployment, dict) or not isinstance(deployment.get('meta'), dict) or not valid_identity(deployment):
        raise ValueError('production deployment has no valid identity')
    sha = deployment['meta'].get('gitCommitSha')
    if not isinstance(sha, str) or re.fullmatch('[0-9a-f]{40}', sha) is None:
        raise ValueError('production deployment has no exact source revision')
    return deployment, sha


def deployment_healthy(before, after, expected_sha, http_ok, browser_ok):
    return valid_identity(before) and valid_identity(after) and before.get('id') == after.get('id') and before.get('meta', {}).get('gitCommitSha') == expected_sha and after.get('meta', {}).get('gitCommitSha') == expected_sha and after.get('readyState') == 'READY' and after.get('target') == 'production' and http_ok and browser_ok


if __name__ == '__main__':
    try:
        if len(sys.argv) != 2 or re.fullmatch('[0-9a-f]{40}', sys.argv[1]) is None:
            raise ValueError('usage: verify-live-site.py EXPECTED_SHA')
        # Resolve the live alias itself, not the last deployment in a list.
        deployment, sha = observe_deployment()
        with urllib.request.urlopen('https://app.darkfactory.build/factory', timeout=20) as response:
            http_ok = response.status == 200
        browser = subprocess.run(['node', str(Path(__file__).with_name('verify-live-browser.mjs'))], capture_output=True, text=True, timeout=120)
        browser_ok = browser.returncode == 0 and json.loads(browser.stdout).get('healthy') is True
        observed, _ = observe_deployment()
        healthy = deployment_healthy(deployment, observed, sys.argv[1], http_ok, browser_ok)
        print(json.dumps({'sha': sha, 'healthy': healthy, 'deployment_id': deployment.get('id')}))
        if not healthy:
            raise SystemExit(1)
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        print('verify-live-site: verification unavailable', file=sys.stderr)
        raise SystemExit(1)
