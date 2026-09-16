#!/usr/bin/env python3
"""Drain, reinstall an exact runtime revision, verify, and restore dispatch."""
import argparse
import json
import os
from pathlib import Path
import re
import sqlite3
import subprocess
import sys
import tempfile
import time


def state(home):
    with sqlite3.connect((home / 'factory.sqlite3').as_uri() + '?mode=ro', uri=True) as connection:
        # One statement gives both values the same SQLite snapshot; settlement
        # increments the factory revision and removes one active run atomically.
        return connection.execute("SELECT dispatch_enabled, revision, (SELECT count(*) FROM runs WHERE phase <> 'terminal') FROM factory WHERE singleton=1").fetchone()


def failure_receipt(sha, reachable):
    directory = Path.home() / '.dark-factory-backups'
    directory.mkdir(mode=0o700, exist_ok=True)
    target = directory / ('deploy-' + sha + '.json')
    fd, temporary = tempfile.mkstemp(prefix='.deploy-', dir=directory)
    try:
        with os.fdopen(fd, 'w', encoding='utf-8') as stream:
            json.dump({'sha': sha, 'healthy': False, 'dispatch_enabled': False,
                       'service_reachable': reachable, 'error': 'deployment_failed'}, stream, sort_keys=True)
            stream.write('\n')
        os.replace(temporary, target)
        os.chmod(target, 0o600)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass


def deploy(sha, home=None):
    if re.fullmatch('[0-9a-f]{40}', sha) is None:
        raise ValueError('expected a full merge SHA')
    scripts = Path(__file__).resolve().parent
    home = Path(home) if home is not None else Path.home() / '.dark-factory'
    if not home.is_absolute():
        raise ValueError('factory home must be absolute')
    control = Path(str(home) + '.service') / 'bin' / 'current' / 'factoryctl'
    env = dict(os.environ, DARK_FACTORY_SOCKET=str(home / 'runtimes' / 'factory.sock'), DARK_FACTORY_OPERATOR_TOKEN_FILE=str(home / 'operator.token'))
    # Preparation is non-destructive and leaves working agents running.
    subprocess.run(['/bin/sh', str(scripts / 'reinstall-service.sh'), '--home', str(home), '--prepare', sha], env=env, check=True, timeout=600, capture_output=True, text=True)
    enabled, original_revision, original_active = state(home)
    subprocess.run([str(control), 'dispatch', 'off', '--revision', str(original_revision)], env=env, check=True, timeout=15, stdout=subprocess.DEVNULL)
    def paused_state():
        current_enabled, revision, active = state(home)
        expected = original_revision + 1 + original_active - active
        if current_enabled or not 0 <= active <= original_active or revision != expected:
            raise ValueError('operator changed factory controls during deployment drain')
        return revision, active

    def restore_dispatch(revision):
        # Only a definite refused CAS is retryable. Each fresh owned snapshot
        # must prove another settlement, so at most the original runs can race.
        for _ in range(original_active + 1):
            try:
                subprocess.run([str(control), 'dispatch', 'on', '--revision', str(revision)], env=env, check=True, timeout=15,
                               stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
                return
            except subprocess.CalledProcessError as exc:
                if exc.returncode != 1 or exc.stderr != 'factoryctl: dispatch revision is stale\n':
                    sys.stderr.write(exc.stderr or '')
                    raise
                next_revision, _ = paused_state()
                if next_revision <= revision:
                    raise
                revision = next_revision
        raise ValueError('dispatch restoration did not converge after known settlements')

    paused_state()
    deadline = time.monotonic() + 300
    while True:
        drained_revision, active = paused_state()
        if active == 0:
            break
        if time.monotonic() >= deadline:
            # Installation has not started. Restore only this proven owned pause;
            # the revision CAS refuses a racing operator change or settlement.
            if enabled:
                restore_dispatch(drained_revision)
            raise ValueError('runs did not drain')
        time.sleep(1)
    try:
        subprocess.run(['/bin/sh', str(scripts / 'reinstall-service.sh'), '--home', str(home), '--install-prepared', sha], env=env, check=True, timeout=600,
                       capture_output=True, text=True)
        observed = json.loads(subprocess.run([sys.executable, str(scripts / 'verify-live-runtime.py'), '--home', str(home), sha], env=env, capture_output=True, text=True, check=True, timeout=60).stdout)
        if observed.get('sha') != sha or observed.get('healthy') is not True:
            raise ValueError('runtime verification failed')
    except (OSError, ValueError, json.JSONDecodeError, subprocess.SubprocessError) as exc:
        try:
            reachable = state(home)[2] >= 0
        except (OSError, sqlite3.Error):
            reachable = False
        failure_receipt(sha, reachable)
        raise ValueError('deployment failed; dispatch remains off; service_reachable=' + str(reachable).lower()) from exc
    current_enabled, revision, active = state(home)
    # A subsequent explicit operator dispatch change wins over our restoration.
    if enabled and not current_enabled and active == 0 and revision == drained_revision:
        restore_dispatch(drained_revision)
    print(json.dumps({'sha': sha, 'healthy': True, 'dispatch_enabled': bool(state(home)[0])}))


if __name__ == '__main__':
    try:
        parser = argparse.ArgumentParser(description=__doc__)
        parser.add_argument('--home', type=Path, default=Path.home() / '.dark-factory')
        parser.add_argument('sha')
        args = parser.parse_args()
        deploy(args.sha, args.home)
    except (OSError, ValueError, sqlite3.Error, subprocess.SubprocessError) as error:
        print('deploy-runtime: ' + str(error), file=sys.stderr)
        raise SystemExit(1)
