#!/usr/bin/env python3
"""Drain, reinstall an exact runtime revision, verify, and restore dispatch."""
import argparse
import json
import os
from pathlib import Path
import re
import socket
import sqlite3
import subprocess
import sys
import tempfile
import time

# reinstall-service.sh refuses a run it cannot adopt with this status, and the
# release controller reads the same number back as "no effect; retryable".
REFUSED = 75


def state(home):
    with sqlite3.connect((home / 'factory.sqlite3').as_uri() + '?mode=ro', uri=True) as connection:
        # Controls and non-terminal runs are read from the same SQLite snapshot.
        connection.execute('BEGIN')
        enabled, revision = connection.execute('SELECT dispatch_enabled, revision FROM factory WHERE singleton=1').fetchone()
        runs = connection.execute("SELECT phase, lower(hex(id)) FROM runs WHERE phase <> 'terminal'").fetchall()
    # A run still running behind its runner's takeover endpoint is adopted by
    # the next daemon and survives the restart, so it does not have to drain;
    # every other non-terminal run still does.
    return enabled, revision, sum(1 for phase, run in runs if not adoptable(home, phase, run))


def adoptable(home, phase, run):
    return phase == 'running' and (home / 'runtimes' / run / 'takeover.sock').is_socket()


def service_reachable(home):
    """A connect, the same probe reinstall-service.sh waits on.

    A readable store proves only that the file is there. After a failed
    install the daemon may be gone while its store still reads perfectly, so
    the receipt's reachability has to come from something that serves.
    """
    with socket.socket(socket.AF_UNIX) as probe:
        probe.settimeout(5)
        try:
            probe.connect(str(home / 'runtimes' / 'factory.sock'))
        except OSError:
            return False
    return True


def failure_receipt(sha, reachable, dispatch_enabled):
    directory = Path.home() / '.dark-factory-backups'
    directory.mkdir(mode=0o700, exist_ok=True)
    target = directory / ('deploy-' + sha + '.json')
    fd, temporary = tempfile.mkstemp(prefix='.deploy-', dir=directory)
    try:
        with os.fdopen(fd, 'w', encoding='utf-8') as stream:
            json.dump({'sha': sha, 'healthy': False, 'dispatch_enabled': dispatch_enabled,
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
    enabled, original_revision, _ = state(home)
    def paused_state():
        current_enabled, revision, active = state(home)
        expected = original_revision + 1
        if current_enabled or revision != expected:
            raise ValueError('operator changed factory controls during deployment drain')
        return revision, active

    def restore_dispatch(revision):
        subprocess.run([str(control), 'dispatch', 'on', '--revision', str(revision)], env=env, check=True, timeout=15,
                       stdout=subprocess.DEVNULL)

    def recover(exc, owned_revision):
        """Report what the factory was left with, undoing only a proven pause.

        The daemon advances its control revision on every accepted command, so
        landing on ``original_revision + 1`` is not proof that this deployment
        paused the factory: a concurrent operator ``dispatch off`` lands on the
        same number. ``owned_revision`` is set only once the daemon accepted
        ours, and stays None otherwise, which keeps a refused or unanswered
        pause from undoing an operator's. The receipt states the store and the
        socket as observed; a read that failed is unknown, never a paused
        factory nobody saw.
        """
        try:
            current_enabled, revision, active = state(home)
            store_read = active >= 0
        except (OSError, sqlite3.Error):
            current_enabled, revision, store_read = None, None, False
        if owned_revision is not None and store_read and enabled and not current_enabled and revision == owned_revision:
            try:
                restore_dispatch(owned_revision)
            except (OSError, subprocess.SubprocessError):
                pass
        # The evidence is what the factory was left with, never what this
        # process attempted: an operator may have re-enabled dispatch during
        # the failed installation, and a timed-out restore may still have been
        # applied. A receipt that contradicts the live factory is worse than none.
        try:
            dispatching = bool(state(home)[0]) if store_read else None
        except (OSError, sqlite3.Error):
            dispatching = None
        reachable = service_reachable(home)
        failure_receipt(sha, reachable, dispatching)
        if dispatching is None:
            note = 'state is unreadable; run factoryctl status before deciding whether to resume'
        elif dispatching:
            note = 'is on so the factory keeps working on the old build'
        elif owned_revision is not None or not enabled:
            note = 'remains off'
        else:
            # The pause was never accepted, so it and an operator's are the
            # same revision from here and guessing would undo theirs. A
            # non-zero exit is no better evidence than no answer at all: the
            # CLI can also fail after the daemon committed the change.
            note = 'is off and this deployment cannot prove whose pause it is; run factoryctl status, then decide whether to resume (factoryctl dispatch on)'
        error = ValueError('deployment failed; dispatch ' + note + '; service_reachable=' + str(reachable).lower())
        # A refusal over a run it could not adopt installed nothing, so a later
        # attempt may simply proceed once the factory is dispatching again. An
        # unknown dispatch state is never that proof.
        error.retryable = dispatching is not None and (dispatching or not enabled) and getattr(exc, 'returncode', None) == REFUSED
        return error

    # One compensation scope: the pause, the drain reads that depend on it, and
    # the installation. Ownership is earned, not assumed -- only an accepted
    # `dispatch off` gives this deployment a revision of its own to undo.
    owned_revision = None
    try:
        subprocess.run([str(control), 'dispatch', 'off', '--revision', str(original_revision)], env=env, check=True, timeout=15, stdout=subprocess.DEVNULL)
        owned_revision = original_revision + 1
        paused_state()
        while True:
            active = paused_state()[1]
            if active == 0:
                break
            time.sleep(1)
        subprocess.run(['/bin/sh', str(scripts / 'reinstall-service.sh'), '--home', str(home), '--install-prepared', sha], env=env, check=True, timeout=600,
                       capture_output=True, text=True)
        observed = json.loads(subprocess.run([sys.executable, str(scripts / 'verify-live-runtime.py'), '--home', str(home), sha], env=env, capture_output=True, text=True, check=True, timeout=60).stdout)
        if observed.get('sha') != sha or observed.get('healthy') is not True:
            raise ValueError('runtime verification failed')
    except (OSError, ValueError, json.JSONDecodeError, sqlite3.Error, subprocess.SubprocessError) as exc:
        raise recover(exc, owned_revision) from exc
    current_enabled, revision, active = state(home)
    # A subsequent explicit operator dispatch change wins over our restoration.
    if enabled and not current_enabled and active == 0 and revision == owned_revision:
        restore_dispatch(owned_revision)
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
        cause = error.__cause__ or error
        if isinstance(cause, subprocess.SubprocessError):
            # The failing stage and its output are the only evidence of why. The
            # stage goes last and the output uncut: the release controller
            # redacts whole labels and then keeps the tail.
            print(str(cause.stderr or '') + '\nstage: ' + ' '.join(Path(str(arg)).name for arg in cause.cmd[:5])
                  + ' exit=' + str(getattr(cause, 'returncode', 'timeout')), file=sys.stderr)
        elif cause is not error:
            # The compensation summary says what the factory was left with; the
            # cause says what went wrong. An operator needs both.
            print('cause: ' + str(cause), file=sys.stderr)
        # 75 tells the release controller the failure left no effect to undo:
        # the non-destructive preparation, or a refusal to restart over a run
        # it could not adopt, after which the factory is working as before.
        raise SystemExit(REFUSED if getattr(error, 'retryable', False)
                         or (isinstance(cause, subprocess.SubprocessError) and '--prepare' in cause.cmd) else 1)
