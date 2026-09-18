#!/usr/bin/env python3
"""Run the operator-configured intake and local attention checks under launchd."""
import argparse
import contextlib
import pwd
import stat
import fcntl
import importlib.util
import hashlib
import json
import os
from pathlib import Path
import plistlib
import subprocess
import sys
import tempfile
import time


def validate_controller_config(config):
    home, journal = config.get('factory_home'), config.get('journal')
    if not isinstance(home, str) or not isinstance(journal, str) or not Path(home).is_absolute() or not Path(journal).is_absolute():
        raise ValueError('factory_home and journal must be absolute paths')
    resolved_home, resolved_journal = Path(home).resolve(), Path(journal).resolve()
    if resolved_journal == resolved_home or resolved_home in resolved_journal.parents:
        raise ValueError('journal must be outside factory_home')
    releases = config.get('release_configs', [])
    if not isinstance(releases, list) or any(not isinstance(item, str) or not Path(item).is_absolute() for item in releases):
        raise ValueError('release_configs must be absolute config paths')
    for path in releases:
        release_journal = json.loads(Path(path).read_text()).get('journal')
        if not isinstance(release_journal, str) or not Path(release_journal).is_absolute():
            raise ValueError('release journal must be absolute')
        resolved_release = Path(release_journal).resolve()
        if resolved_release == resolved_home or resolved_home in resolved_release.parents:
            raise ValueError('release journal must be outside factory_home')


class ControllerSourceError(ValueError):
    pass


def controller_checkout(scripts):
    # A release archive is immutable and has no source checkout to refresh.
    # Only the developer/release tree's conventional scripts directory owns
    # the post-release fast-forward described by the controller contract.
    return scripts.parent if scripts.name == 'scripts' and (scripts.parent / '.git').exists() else None


def refresh_controller(checkout, release_config, receipt):
    """Advance this existing checkout only after an exact verified release."""
    sha = receipt.get('sha', '')
    repository, branch = release_config['repository'], release_config['base']
    if receipt.get('state') != 'verified' or len(sha) != 40 or any(c not in '0123456789abcdef' for c in sha):
        raise ControllerSourceError('controller source refresh requires an exact verified release')

    def git(*args):
        result = subprocess.run(['git', '-C', str(checkout), '-c', 'core.hooksPath=/dev/null', *args], capture_output=True, text=True, timeout=120)
        if result.returncode:
            raise ControllerSourceError('controller source refresh refused at git ' + args[0] + '; inspect the checkout before the next release pass')
        return result.stdout.strip()

    if git('rev-parse', '--show-toplevel') != str(checkout.resolve()):
        raise ControllerSourceError('controller source must be the checkout root')
    if git('symbolic-ref', '--short', 'HEAD') != branch:
        raise ControllerSourceError('controller source must be on the configured release branch')
    remote = git('remote', 'get-url', 'origin')
    if remote.removesuffix('.git') not in ('https://github.com/' + repository,
                                          'git@github.com:' + repository, 'ssh://git@github.com/' + repository):
        raise ControllerSourceError('controller origin must match the configured release repository')
    if git('status', '--porcelain', '--untracked-files=no'):
        raise ControllerSourceError('controller source has tracked edits; preserve them before refreshing')
    git('fetch', '--no-tags', '--no-write-fetch-head', '--refmap=', 'origin', sha)
    git('merge-base', '--is-ancestor', 'HEAD', sha)
    git('merge', '--ff-only', sha)


def tick(config_path, config, release_only=False):
    scripts = Path(__file__).resolve().parent
    calls = []
    if not release_only:
        calls.append([sys.executable, str(scripts / 'factory-intake.py'), str(config_path), '--once'])
    releases = config.get('release_configs', []) if release_only else []
    for release_config in releases:
        calls.append([sys.executable, str(scripts / 'factory-release.py'), release_config, '--latest', '--once'])
    if not release_only and 'review_mirror_root' in config:
        if not isinstance(config['review_mirror_root'], str) or not Path(config['review_mirror_root']).is_absolute():
            raise ValueError('review_mirror_root must be an absolute path')
        calls.append([sys.executable, str(scripts / 'factory-review-intake.py'), str(config_path), '--once'])
    results = []
    for argv in calls:
        try:
            completed = subprocess.run(argv, capture_output=True, text=True, timeout=1300 if Path(argv[1]).name == 'factory-intake.py' else None)
            result = {'component': Path(argv[1]).stem, 'ok': completed.returncode == 0}
            if completed.returncode:
                result['error'] = 'exit_' + str(completed.returncode)
            results.append(result)
            if completed.returncode == 0 and Path(argv[1]).name == 'factory-release.py':
                receipt = json.loads(completed.stdout)
                if receipt.get('state') == 'verified':
                    spec = importlib.util.spec_from_file_location('factory_delivery', scripts / 'factory-delivery.py')
                    delivery = importlib.util.module_from_spec(spec)
                    spec.loader.exec_module(delivery)
                    release_config = json.loads(Path(argv[2]).read_text())
                    delivery.deliver(config, release_config, receipt)
                    # Checkout replacement must not overlap readers in the normal pass.
                    refresh_lock = Path(str(Path(config['factory_home']).resolve()) + '.autonomy.lock')
                    with refresh_lock.open('a+') as lock:
                        fcntl.flock(lock, fcntl.LOCK_EX)
                        checkout = controller_checkout(scripts)
                        if checkout is not None:
                            refresh_controller(checkout, release_config, receipt)

        except ControllerSourceError as error:
            result.update({'ok': False, 'error': str(error)})
        except subprocess.TimeoutExpired:
            results.append({'component': Path(argv[1]).stem, 'ok': False, 'error': 'timeout'})
        except Exception:
            results.append({'component': Path(argv[1]).stem, 'ok': False, 'error': 'exception'})
    return results


def atomic_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix='.' + path.name + '.', dir=path.parent)
    try:
        with os.fdopen(fd, 'w', encoding='utf-8') as stream:
            json.dump(value, stream, sort_keys=True)
            stream.write('\n')
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
        os.chmod(path, 0o600)
        directory = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass


def write_health(config, results, release_only=False):
    path = Path(config['journal'] + ('.release-autonomy.json' if release_only else '.autonomy.json'))
    atomic_json(path, {'at': int(time.time()), 'components': results})
# Managed intake uses the same one-shot controller and launchd scheduling. The
# daemon owns source policy, acceptance, and tasks; this file alone owns cursors.
def managed_paths(home):
    home = Path(home).resolve(strict=True)
    proof = home.stat()
    if not home.is_dir() or proof.st_uid != os.geteuid():
        raise ValueError('factory home is not owned by this operator')
    state = Path(str(home) + '.intake')
    state.mkdir(mode=0o700, exist_ok=True)
    metadata = state.lstat()
    if not stat.S_ISDIR(metadata.st_mode) or metadata.st_uid != os.geteuid() or stat.S_IMODE(metadata.st_mode) != 0o700:
        raise ValueError('managed intake directory must be a private operator-owned directory')
    return home, state, [proof.st_dev, proof.st_ino]


def managed_read(path, default=None, maximum=1 << 20, private=True, decode=json.loads):
    try:
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    except FileNotFoundError:
        return default
    with os.fdopen(descriptor, 'rb') as stream:
        value = os.fstat(stream.fileno())
        if not stat.S_ISREG(value.st_mode) or value.st_uid != os.geteuid() or (stat.S_IMODE(value.st_mode) != 0o600 if private else value.st_mode & 0o022) or value.st_nlink != 1 or value.st_size > maximum:
            raise ValueError('managed intake file is not private or is oversized')
        data = stream.read(maximum + 1)
        if len(data) > maximum:
            raise ValueError('managed intake file grew past its bound')
        return decode(data)


@contextlib.contextmanager
def managed_lock(path):
    descriptor = os.open(path, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, 'a+') as stream:
        metadata = os.fstat(stream.fileno())
        if not stat.S_ISREG(metadata.st_mode) or metadata.st_uid != os.geteuid() or stat.S_IMODE(metadata.st_mode) != 0o600 or metadata.st_nlink != 1:
            raise ValueError('managed intake lock is not private')
        try:
            fcntl.flock(stream, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise ValueError('another controller owns this factory') from error
        yield


def managed_api(factoryctl, home, arguments):
    environment = {'PATH': '/usr/bin:/bin:/usr/sbin:/sbin',
                   'DARK_FACTORY_SOCKET': str(home / 'runtimes/factory.sock'),
                   'DARK_FACTORY_OPERATOR_TOKEN_FILE': str(home / 'operator.token')}
    try:
        response = subprocess.run([str(factoryctl), 'intake'] + arguments, env=environment,
                                  capture_output=True, text=True, timeout=125)
        result = json.loads(response.stdout)
        if response.returncode != 0 or not isinstance(result, dict) or len(response.stdout) > 1 << 20:
            return {'state': 'unavailable'}
        return result
    except (OSError, subprocess.TimeoutExpired, json.JSONDecodeError):
        return {'state': 'unavailable'}


def managed_tick(home, factoryctl):
    home, state, identity = managed_paths(home)
    with managed_lock(Path(str(home) + '.autonomy.lock')), managed_lock(state / 'journal.lock'):
        now = int(time.time())
        journal = managed_read(state / 'journal.json', {'version': 1, 'home_identity': identity, 'sources': {}, 'last_success_at': 0})
        if not isinstance(journal, dict) or journal.get('version') != 1 or journal.get('home_identity') != identity or not isinstance(journal.get('sources'), dict):
            raise ValueError('managed intake journal belongs to a different factory')
        result = managed_api(factoryctl, home, ['config'])
        sources = result.get('sources', [])
        error = ''
        imported, did_sync = 0, False
        if result.get('state') != 'ok':
            error = result.get('state', 'unavailable')
        elif not isinstance(sources, list):
            error = 'invalid'
        elif len(sources) > 200:
            error = 'overflow'
        else:
            for source in sources:
                if not isinstance(source, dict):
                    error = 'invalid'
                    break
                identifier, interval, revision = source.get('id', ''), source.get('poll_seconds'), source.get('revision')
                if not isinstance(identifier, str) or len(identifier) != 32 or any(c not in '0123456789abcdef' for c in identifier) or type(interval) is not int or not 5 <= interval <= 86400 or type(revision) is not int or revision < 1 or type(source.get('enabled')) is not bool:
                    error = 'invalid'
                    break
                record = journal['sources'].setdefault(identifier, {'next_page': 1, 'next_due': 0, 'revision': revision})
                if not isinstance(record, dict) or type(record.get('next_due')) is not int:
                    raise ValueError('managed intake journal schedule is invalid')
                if record.get('revision') != revision:
                    record.update(next_page=1, acceptance_cursor="", next_due=0, revision=revision)
                if record['next_due'] > now:
                    if record.get('error'):
                        error = record['error']
                    continue
                page = record['next_page']
                if type(page) is not int or not 1 <= page <= 1000:
                    raise ValueError('managed intake journal cursor is invalid')
                cursor = record.get('acceptance_cursor', '')
                if not isinstance(cursor, str) or (cursor and (len(cursor) != 32 or any(c not in '0123456789abcdef' for c in cursor))):
                    raise ValueError('managed intake acceptance cursor is invalid')
                record.update(last_attempt_at=now, imported_tasks=0)
                arguments = ['tick', '--source', identifier, '--page', str(page)]
                if cursor:
                    arguments += ['--acceptance-cursor', cursor]
                reply = managed_api(factoryctl, home, arguments)
                status = reply.get('state', 'unavailable')
                if status not in ('ok', 'paused', 'denied', 'unavailable', 'invalid', 'stale', 'conflict'):
                    status = 'unavailable'
                next_page, tasks = reply.get('next_page'), reply.get('imported_tasks', [])
                next_cursor = reply.get('acceptance_cursor', '')
                if type(reply.get('acceptance_progress', False)) is not bool or not isinstance(next_cursor, str) or (next_cursor and (len(next_cursor) != 32 or any(c not in '0123456789abcdef' for c in next_cursor))):
                    status = 'invalid'
                if status in ('ok', 'paused') and ((next_page is not None and (type(next_page) is not int or not page < next_page <= 1000)) or not isinstance(tasks, list) or len(tasks) > 200 or any(not isinstance(task, str) or len(task) != 32 or any(c not in '0123456789abcdef' for c in task) for task in tasks)):
                    status = 'invalid'
                if status in ('ok', 'paused'):
                    record.update(next_page=next_page or 1, acceptance_cursor=next_cursor, next_due=now + (5 if next_page or next_cursor else interval), last_success_at=now, error='', state=status, imported_tasks=len(tasks))
                    imported += len(tasks)
                    did_sync = True
                else:
                    error = status
                    record.update(next_due=now + min(interval, 60), error=status, state='error')
                    if status in ('unavailable', 'denied', 'conflict') and reply.get('acceptance_progress') is True:
                        record.update(acceptance_cursor=next_cursor, next_due=now + 5)
                        if isinstance(tasks, list) and len(tasks) <= 200 and all(isinstance(task, str) and len(task) == 32 and all(c in '0123456789abcdef' for c in task) for task in tasks):
                            record['imported_tasks'] = len(tasks)
                            imported += len(tasks)
                # A confirmed partial response advances only receipt scanning; the
                # source error stays visible. A lost response keeps the page for the daemon's idempotent
                # acceptance/import path. Never infer a task from a timeout.
                atomic_json(state / 'journal.json', journal)
        if error not in ('', 'denied', 'unavailable', 'invalid', 'stale', 'conflict', 'paused', 'overflow'):
            error = 'unavailable'
        if result.get('state') != 'ok':
            for record in journal['sources'].values():
                if not isinstance(record, dict):
                    raise ValueError('managed intake journal source is invalid')
                record.update(last_attempt_at=now, state='error', error=error, next_due=0, imported_tasks=0)
        if not error and (did_sync or not sources):
            journal['last_success_at'] = now
        atomic_json(state / 'journal.json', journal)
        summaries = {}
        for identifier, record in journal['sources'].items():
            if len(summaries) >= 200:
                error = 'overflow'
                break
            if len(identifier) != 32 or any(c not in '0123456789abcdef' for c in identifier) or not isinstance(record, dict):
                raise ValueError('managed intake journal source is invalid')
            summary = {key: record.get(key, 0) for key in ('last_attempt_at', 'last_success_at', 'imported_tasks')}
            if any(type(value) is not int or value < 0 or value > 2**63-1 for value in summary.values()):
                raise ValueError('managed intake journal status is invalid')
            summary.update(state=record.get('state', 'error'), error=record.get('error', 'unavailable'))
            if summary['state'] not in ('ok', 'paused', 'error') or summary['error'] not in ('', 'denied', 'unavailable', 'invalid', 'stale', 'conflict'):
                raise ValueError('managed intake journal error is invalid')
            if result.get('state') != 'ok' or error == 'overflow':
                summary.update(last_attempt_at=now, state='error', error=error)
            summaries[identifier] = summary
        summary = {'version': 1, 'state': 'error' if error else 'ok', 'error': error, 'sources': summaries}
        if len(json.dumps(summary).encode()) > 64 << 10:
            raise ValueError('managed intake status exceeds its bound')
        atomic_json(state / 'status.json', summary)
        return summary



def managed_plist_root():
    return Path(pwd.getpwuid(os.geteuid()).pw_dir) / 'Library/LaunchAgents'


def managed_plist(home, state, label, script, factoryctl, python):
    return {'Label': label, 'ProgramArguments': [str(python), str(script), '--managed', '--factory-home', str(home), '--factoryctl', str(factoryctl), '--once'],
            'StartInterval': 5, 'RunAtLoad': True, 'ProcessType': 'Background', 'Umask': 63,
            'StandardOutPath': str(state / 'controller.log'), 'StandardErrorPath': str(state / 'controller.log'),
            'EnvironmentVariables': {'PATH': '/usr/bin:/bin:/usr/sbin:/sbin'}}


def managed_launchctl(*arguments):
    return subprocess.run(['/bin/launchctl', *arguments], capture_output=True, text=True, timeout=30)


def refuse_legacy_intake_service(home):
    # The per-pass lock only serializes controllers; independent schedules
    # still create different task IDs from the same issue on successive ticks.
    paths = set(managed_plist_root().glob('*.plist'))
    loaded_paths = set()
    loaded = managed_launchctl('list')
    if loaded.returncode != 0 or len(loaded.stdout) > 1 << 20:
        raise ValueError('legacy intake service discovery unavailable; refusing install')
    for line in loaded.stdout.splitlines():
        fields = line.split()
        if not fields or not fields[-1].startswith('build.darkfactory.autonomy.') or fields[-1].endswith('.release'):
            continue
        job = managed_launchctl('print', 'gui/' + str(os.geteuid()) + '/' + fields[-1])
        locations = [line.strip()[7:] for line in job.stdout.splitlines() if line.strip().startswith('path = ')]
        if job.returncode != 0 or len(locations) != 1 or not Path(locations[0]).is_absolute():
            raise ValueError('legacy intake service ownership unavailable; refusing install')
        loaded_paths.add(Path(locations[0]))
    paths.update(loaded_paths)
    # ponytail: native schedule discovery is bounded to 200 plists; larger
    # installations need explicit indexed service ownership before expansion.
    if len(paths) > 200:
        raise ValueError('intake service discovery exceeds 200 plists; refusing install')
    for path in paths:
        try:
            job = managed_read(path, maximum=64 << 10, private=False, decode=plistlib.loads)
        except (OSError, ValueError, plistlib.InvalidFileException):
            if path in loaded_paths or path.name.startswith('build.darkfactory.autonomy.'):
                raise ValueError('legacy intake service ownership unavailable; refusing install')
            continue
        args = job.get('ProgramArguments', []) if isinstance(job, dict) else []
        if path in loaded_paths and (not isinstance(args, list) or len(args) < 3 or not all(isinstance(arg, str) for arg in args)):
            raise ValueError('legacy intake service arguments unavailable; refusing install')
        if not isinstance(args, list) or len(args) < 3 or not all(isinstance(arg, str) for arg in args) or Path(args[1]).name not in ('factory-autonomy.py', 'factory-intake.py') or '--release-only' in args or '--managed' in args:
            continue
        if not Path(args[2]).is_absolute():
            raise ValueError('legacy intake configuration identity unavailable; refusing install')
        config = managed_read(Path(args[2]), maximum=64 << 10, private=False)
        if not isinstance(config, dict) or not isinstance(config.get('factory_home'), str) or not Path(config['factory_home']).is_absolute():
            raise ValueError('legacy intake configuration identity unavailable; refusing install')
        if Path(config['factory_home']).resolve() == home:
            raise ValueError('legacy intake already schedules this factory; migrate its configuration and journal before installing managed intake')


def managed_service(home, factoryctl, action):
    home, state, identity = managed_paths(home)
    if action == 'install':
        refuse_legacy_intake_service(home)
    label = 'com.dark-factory.intake.' + hashlib.sha256(str(home).encode()).hexdigest()[:12]
    domain = 'gui/' + str(os.geteuid())
    target = domain + '/' + label
    plist_path = managed_plist_root() / (label + '.plist')
    receipt_path = state / 'service.json'
    with managed_lock(state / 'service.lock'):
        receipt = managed_read(receipt_path, maximum=16384)
        present = plist_path.exists() or plist_path.is_symlink()
        if receipt is not None:
            if not isinstance(receipt, dict) or receipt.get('version') != 1 or receipt.get('home_identity') != identity or receipt.get('label') != label or receipt.get('plist_path') != str(plist_path):
                raise ValueError('managed service receipt belongs to another factory')
            expected = plistlib.dumps(managed_plist(home, state, label, receipt['script'], receipt['factoryctl'], receipt['python']))
            if present:
                metadata = plist_path.lstat()
                if not stat.S_ISREG(metadata.st_mode) or metadata.st_uid != os.geteuid() or stat.S_IMODE(metadata.st_mode) != 0o600 or metadata.st_nlink != 1 or plist_path.read_bytes() != expected:
                    raise ValueError('managed service plist is foreign; refusing launchctl')
        elif present:
            raise ValueError('managed service plist has no ownership receipt; refusing launchctl')
        loaded = managed_launchctl('print', target)
        if loaded.returncode == 0:
            paths = [line.strip()[7:] for line in loaded.stdout.splitlines() if line.strip().startswith('path = ')]
            if receipt is None or not present or paths != [str(plist_path)]:
                raise ValueError('managed service label is already owned; refusing launchctl')
        elif loaded.returncode != 113:
            raise ValueError('launchd service status is unavailable; refusing mutation')
        if action == 'status':
            return {'state': 'scheduled' if receipt and loaded.returncode == 0 else 'stopped' if receipt else 'absent',
                    'sync': managed_read(state / 'status.json', {}, maximum=64 << 10)}
        if action not in ('install', 'uninstall'):
            raise ValueError('unknown managed service action')
        value, body = None, None
        if action == 'install':
            script, factoryctl, python = Path(__file__).resolve(), Path(factoryctl).resolve(strict=True), Path(sys.executable).resolve(strict=True)
            expected_prefix = factoryctl.parent.parent if factoryctl.parent.name == 'bin' else factoryctl.parent
            if script != expected_prefix / 'libexec/dark-factory/factory-autonomy.py':
                raise ValueError('managed intake must be installed from the release or Homebrew libexec directory')
            plist_path.parent.mkdir(parents=True, exist_ok=True)
            parent = plist_path.parent.lstat()
            if not stat.S_ISDIR(parent.st_mode) or parent.st_uid != os.geteuid() or parent.st_mode & 0o022:
                raise ValueError('LaunchAgents directory authority is unsafe')
            log = state / 'controller.log'
            descriptor = os.open(log, os.O_WRONLY | os.O_CREAT | os.O_APPEND | os.O_NOFOLLOW, 0o600)
            with os.fdopen(descriptor, 'a') as stream:
                metadata = os.fstat(stream.fileno())
                if not stat.S_ISREG(metadata.st_mode) or metadata.st_uid != os.geteuid() or stat.S_IMODE(metadata.st_mode) != 0o600 or metadata.st_nlink != 1:
                    raise ValueError('managed intake log is not private')
            body = plistlib.dumps(managed_plist(home, state, label, script, factoryctl, python))
            value = {'version': 1, 'label': label, 'home_identity': identity, 'plist_path': str(plist_path),
                     'script': str(script), 'script_digest': hashlib.sha256(script.read_bytes()).hexdigest(),
                     'factoryctl': str(factoryctl), 'python': str(python)}
            if value == receipt and loaded.returncode == 0:
                return {'state': 'scheduled'}
        if receipt and loaded.returncode == 0:
            if managed_launchctl('bootout', target).returncode != 0:
                raise ValueError('could not stop managed intake; ownership artifacts retained')
        # Remove only proven old artifacts before writing a replacement receipt.
        # A crash at either boundary is recoverable by install or uninstall.
        if receipt:
            if present:
                plist_path.unlink()
            receipt_path.unlink()
        if action == 'uninstall':
            return {'state': 'absent', 'journal_retained': True}
        atomic_json(receipt_path, value)
        descriptor, temporary = tempfile.mkstemp(prefix='.' + label + '.', dir=plist_path.parent)
        try:
            with os.fdopen(descriptor, 'wb') as stream:
                stream.write(body)
                stream.flush()
                os.fsync(stream.fileno())
            os.replace(temporary, plist_path)
        finally:
            if os.path.exists(temporary):
                os.unlink(temporary)
        if managed_launchctl('bootstrap', domain, str(plist_path)).returncode != 0:
            raise ValueError('could not start managed intake; retry service install')
        return {'state': 'scheduled'}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('config', type=Path, nargs='?')
    parser.add_argument('--managed', action='store_true')
    parser.add_argument('--factory-home', type=Path)
    parser.add_argument('--factoryctl', type=Path)
    parser.add_argument('--service', choices=('install', 'uninstall', 'status'))
    parser.add_argument('--once', action='store_true')
    parser.add_argument('--release-only', action='store_true', help='Run the separately scheduled release and delivery pass.')
    parser.add_argument('--plist', action='store_true', help='Print a launchd plist; does not install or start it.')
    args = parser.parse_args()
    if args.managed:
        if args.config is not None or args.release_only or args.plist or args.factory_home is None or args.factoryctl is None or not args.factory_home.is_absolute() or not args.factoryctl.is_absolute():
            parser.error('managed intake requires --factory-home and --factoryctl only')
        if sys.version_info < (3, 9):
            raise ValueError('managed intake requires Python 3.9 or newer')
        result = managed_service(args.factory_home, args.factoryctl, args.service) if args.service else managed_tick(args.factory_home, args.factoryctl)
        if args.service:
            print(json.dumps(result), flush=True)
        return 0 if result['state'] != 'error' else 1
    if args.config is None or args.factory_home is not None or args.factoryctl is not None or args.service is not None:
        parser.error('legacy controller requires its configuration file')
    config_path = args.config.resolve(strict=True)
    config = json.loads(config_path.read_text())
    validate_controller_config(config)
    interval = config.get('poll_seconds', 120)
    if type(interval) is not int or not 5 <= interval <= 86400:
        raise ValueError('poll_seconds must be 5..86400')
    suffix = '.release' if args.release_only else ''
    # Use launchd StartInterval rather than keeping a second polling daemon.
    if args.plist:
        log = str(Path(config['journal']).with_suffix(suffix + '.service.log'))
        plist = {'Label': 'build.darkfactory.autonomy.' + hashlib.sha256(str(config_path).encode()).hexdigest()[:12] + suffix, 'ProgramArguments': [sys.executable, str(Path(__file__).resolve()), str(config_path), '--once'] + (['--release-only'] if args.release_only else []),
                 'StartInterval': interval, 'RunAtLoad': True, 'ProcessType': 'Background',
                 'StandardOutPath': log, 'StandardErrorPath': log,
                 'EnvironmentVariables': {'PATH': os.environ.get('PATH', '/usr/bin:/bin:/usr/sbin:/sbin')}}
        sys.stdout.buffer.write(plistlib.dumps(plist))
        return 0
    # Release waits must not hold the intake/review scheduler's lock.
    descriptor = os.open(Path(str(Path(config['factory_home']).resolve()) + suffix + '.autonomy.lock'), os.O_CREAT | os.O_RDWR, 0o600)
    with os.fdopen(descriptor, 'a+') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise ValueError('another controller owns this factory') from error
        results = tick(config_path, config, args.release_only)
        write_health(config, results, args.release_only)
    print(json.dumps({'at': int(time.time()), 'components': results}), flush=True)
    return 0 if all(result['ok'] for result in results) else 1


if __name__ == '__main__':
    try:
        raise SystemExit(main())
    except (OSError, ValueError, KeyError) as error:
        print('factory-autonomy: ' + str(error), file=sys.stderr)
        raise SystemExit(1)
