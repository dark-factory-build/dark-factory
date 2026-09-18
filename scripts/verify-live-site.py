#!/usr/bin/env python3
"""Observe the production alias's Vercel revision and HTTP health."""
import json
import re
from pathlib import Path
import subprocess
import sys
import urllib.request


def deployment_healthy(deployment, expected_sha, observed_sha, http_ok, browser_ok):
    return observed_sha == expected_sha and deployment.get('readyState') == 'READY' and deployment.get('target') == 'production' and http_ok and browser_ok


if __name__ == '__main__':
    try:
        if len(sys.argv) != 2 or re.fullmatch('[0-9a-f]{40}', sys.argv[1]) is None:
            raise ValueError('usage: verify-live-site.py EXPECTED_SHA')
        # Resolve the live alias itself, not the last deployment in a list.
        result = subprocess.run(['vercel', 'api', '/v13/deployments/app.darkfactory.build', '--raw'], check=True, capture_output=True, text=True, timeout=30)
        deployment = json.loads(result.stdout)
        sha = deployment.get('meta', {}).get('gitCommitSha', '')
        if re.fullmatch('[0-9a-f]{40}', sha) is None:
            raise ValueError('production deployment has no exact source revision')
        with urllib.request.urlopen('https://app.darkfactory.build/factory', timeout=20) as response:
            http_ok = response.status == 200
        browser = subprocess.run(['node', str(Path(__file__).with_name('verify-live-browser.mjs'))], capture_output=True, text=True, timeout=120)
        browser_ok = browser.returncode == 0 and json.loads(browser.stdout).get('healthy') is True
        healthy = deployment_healthy(deployment, sys.argv[1], sha, http_ok, browser_ok)
        print(json.dumps({'sha': sha, 'healthy': healthy, 'deployment_id': deployment.get('id')}))
        if not healthy:
            raise SystemExit(1)
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        print('verify-live-site: verification unavailable', file=sys.stderr)
        raise SystemExit(1)
