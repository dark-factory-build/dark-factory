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


def tick(config_path, config, release_only=False, skip_intake=False, environment=None, review_arguments=(), controller_lock_fd=None):
    scripts = Path(__file__).resolve().parent
    calls = []
    if not release_only and not skip_intake:
        calls.append([sys.executable, str(scripts / 'factory-intake.py'), str(config_path), '--once'])
    releases = config.get('release_configs', []) if release_only else []
    for release_config in releases:
        calls.append([sys.executable, str(scripts / 'factory-release.py'), release_config, '--latest', '--once'])
    if not release_only and 'review_mirror_root' in config:
        if not isinstance(config['review_mirror_root'], str) or not Path(config['review_mirror_root']).is_absolute():
            raise ValueError('review_mirror_root must be an absolute path')
        calls.append([sys.executable, str(scripts / 'factory-review-intake.py'), str(config_path), '--once'] + list(review_arguments) + (['--controller-lock-fd', str(controller_lock_fd)] if controller_lock_fd is not None else []))
    results = []
    for argv in calls:
        try:
            completed = subprocess.run(argv, capture_output=True, text=True, env=environment, pass_fds=() if controller_lock_fd is None else (controller_lock_fd,), timeout=1300 if Path(argv[1]).name == 'factory-intake.py' else None)
            result = {'component': Path(argv[1]).stem, 'ok': completed.returncode == 0}
            if completed.returncode:
                result['error'] = 'legacy_review_customer_unsupported: use the installed customer publication workflow' if Path(argv[1]).name == 'factory-review-intake.py' and 'Legacy review is owner-only' in completed.stderr else 'exit_' + str(completed.returncode)
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
def managed_lock(path, private=True):
    descriptor = os.open(path, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, 'a+') as stream:
        metadata = os.fstat(stream.fileno())
        if not stat.S_ISREG(metadata.st_mode) or metadata.st_uid != os.geteuid() or (stat.S_IMODE(metadata.st_mode) != 0o600 if private else metadata.st_mode & 0o022) or metadata.st_nlink != 1:
            raise ValueError('managed intake lock is not private')
        try:
            fcntl.flock(stream, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise ValueError('another controller owns this factory') from error
        yield descriptor


def managed_api(factoryctl, home, arguments, value=None):
    environment = {'PATH': '/usr/bin:/bin:/usr/sbin:/sbin',
                   'DARK_FACTORY_SOCKET': str(home / 'runtimes/factory.sock'),
                   'DARK_FACTORY_OPERATOR_TOKEN_FILE': str(home / 'operator.token')}
    try:
        response = subprocess.run([str(factoryctl), 'intake'] + arguments, env=environment,
                                  input=json.dumps(value) if value is not None else None,
                                  capture_output=True, text=True, timeout=125)
        result = json.loads(response.stdout)
        if response.returncode != 0 or not isinstance(result, dict) or len(response.stdout) > 1 << 20:
            return {'state': 'unavailable'}
        return result
    except (OSError, subprocess.TimeoutExpired, json.JSONDecodeError):
        return {'state': 'unavailable'}


def managed_tick(home, factoryctl):
    home, state, identity = managed_paths(home)
    with managed_lock(Path(str(home) + '.autonomy.lock')) as controller_lock_fd, managed_lock(state / 'journal.lock'):
        migration = managed_read(state / 'migration.json', maximum=4 << 20)
        if migration and (migration.get('home_identity') != identity or migration.get('phase') != 'completed'):
            return {'state': 'migration_pending'}
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
                        record['acceptance_cursor'] = next_cursor
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
        review = journal.get('review', [])
        if migration and migration['job']['was_loaded'] and 'review_mirror_root' in json.loads(migration['config']) and now >= journal.get('review_next_due', 0):
            environment = dict(os.environ, PATH=migration['job']['path_environment'])
            review = tick(state / 'legacy-review.json', json.loads(migration['config']), skip_intake=True, environment=environment, review_arguments=['--managed-migration', str(state / 'migration.json'), '--factoryctl', str(factoryctl)], controller_lock_fd=controller_lock_fd)
            journal.update(review=review, review_next_due=now + int(json.loads(migration['config']).get('poll_seconds',120)))
            atomic_json(state / 'journal.json',journal)
        if any(not item['ok'] for item in review):
            error = 'review_unavailable'
        summary = {'version': 1, 'state': 'error' if error else 'ok', 'error': error, 'sources': summaries}
        if review:
            summary['review'] = review
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


def legacy_migration_input(home, config_path):
    spec = importlib.util.spec_from_file_location('legacy_intake', Path(__file__).with_name('factory-intake.py'))
    legacy = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(legacy)
    raw_config = managed_read(config_path, maximum=64 << 10, private=False, decode=lambda data: data)
    if raw_config is None:
        raise ValueError('legacy configuration is missing')
    config = json.loads(raw_config)
    try:
        legacy.validate_config(config)
        validate_controller_config(config)
    except legacy.IntakeError as error:
        raise ValueError(str(error)) from error
    if Path(config['factory_home']).resolve() != home:
        raise ValueError('legacy configuration belongs to another factory')
    known = {'repository', 'project_id', 'overseer_agent_id', 'label', 'allowed_authors', 'journal', 'factory_home', 'max_issues', 'poll_seconds', 'command_timeout', 'priority_default', 'priority_by_label', 'review_mirror_root', 'review_provider', 'base', 'release_configs'}
    if set(config) - known:
        raise ValueError('unsupported legacy configuration fields: ' + ', '.join(sorted(set(config) - known)))
    raw_journal = managed_read(Path(config['journal']), maximum=1 << 20, decode=lambda data: data)
    if raw_journal is None:
        raise ValueError('migration requires the existing legacy journal')
    journal = json.loads(raw_journal)
    if not isinstance(journal, dict) or journal.get('version') != 2 or not isinstance(journal.get('issues'), dict) or len(journal['issues']) > 200:
        raise ValueError('migration requires a version 2 journal with at most 200 issues')
    if set(journal) - {'version', 'updated_at', 'issues', 'config_fingerprint'}:
        raise ValueError('unsupported legacy journal fields require review before cutover')
    try:
        legacy.bind_journal(config, journal)
    except legacy.IntakeError as error:
        raise ValueError(str(error)) from error
    if config.get('review_provider', 'codex') not in ('codex', 'claude'):
        raise ValueError('review_provider must be codex or claude')
    if 'review_mirror_root' in config and (not isinstance(config['review_mirror_root'], str) or not Path(config['review_mirror_root']).is_absolute()):
        raise ValueError('review_mirror_root must be an absolute path')
    history = []
    for key, record in sorted(journal['issues'].items()):
        if not isinstance(record, dict) or type(record.get('number')) is not int or key != legacy.issue_key(config, record['number']) or record.get('managed') is not True or set(record) - {'number', 'managed', 'processed_fingerprint', 'desired', 'desired_fingerprint', 'operation', 'needs_operator_recovery'}:
            raise ValueError('legacy journal issue record is unsupported')
        desired = record.get('desired')
        if not isinstance(desired, dict) or legacy.fingerprint(desired) != record.get('desired_fingerprint') or desired.get('number') != record['number']:
            raise ValueError('legacy snapshot fingerprint is invalid')
        operation, recovery = record.get('operation'), record.get('needs_operator_recovery')
        fingerprint = (recovery or operation or {}).get('fingerprint') or record.get('processed_fingerprint', '')
        entry = {'number': record['number'], 'kind': 'observed'}
        if fingerprint:
            task = legacy.sha_id('source', legacy.source_marker(config, record), fingerprint)
            incarnation = legacy.sha_id('incarnation', task)
            if operation and (operation.get('task_id') != task or operation.get('incarnation_id') != incarnation) or recovery and recovery.get('task_id') != task:
                raise ValueError('legacy task receipt is invalid')
            entry.update(kind='recovery' if recovery else 'operation' if operation else 'processed', task_id=task, incarnation_id=incarnation, fingerprint=fingerprint)
            if fingerprint == record['desired_fingerprint']:
                entry['historical_content_hash'] = hashlib.sha256((desired['title'] + '\0' + desired['body']).encode()).hexdigest()
        history.append(entry)
    manual_apps = sorted(author for author in config['allowed_authors'] if author.startswith('app/') or author.endswith('[bot]'))
    humans = [author for author in config['allowed_authors'] if author not in manual_apps]
    configuration = {'priority_default': config.get('priority_default', 0), 'priority_by_label': config.get('priority_by_label', {}), 'repository': config['repository'], 'overseer_agent_id': config['overseer_agent_id'], 'label': config['label'], 'policy': 'trusted_authors' if humans else 'manual', 'trusted_authors': humans, 'poll_seconds': int(config.get('poll_seconds', 120)), 'admission_limit': int(config.get('max_issues', 25))}
    digest = lambda value: hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
    source_id = hashlib.sha256(('legacy-intake\0' + str(home) + '\0' + str(config_path)).encode()).hexdigest()[:32]
    request = {'action': 'legacy_preview', 'source_id': source_id, 'project_id': config['project_id'], 'configuration': configuration, 'legacy': {'review_companion': 'review_mirror_root' in config, 'manual_app_authors': manual_apps, 'config_hash': digest(config), 'journal_hash': digest(history), 'history': history}}
    return request, config, raw_config.decode(), raw_journal.decode()


def legacy_review_ready(config, publication):
    if 'review_mirror_root' not in config:
        return
    if not isinstance(publication, str) or not publication:
        raise ValueError('review publication repository is unbound; legacy schedule remains untouched')
    spec = importlib.util.spec_from_file_location('legacy_review_preflight', Path(__file__).with_name('factory-review-intake.py'))
    reviewer = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(reviewer)
    try:
        reviewer.mirror(dict(config, repository=publication))
    except (reviewer.ReviewError, reviewer.intake.IntakeError, OSError) as error:
        raise ValueError('prepare the configured Git review mirror for the publication repository before cutover; legacy schedule remains untouched') from error


def legacy_migration_job(config_path):
    label = 'build.darkfactory.autonomy.' + hashlib.sha256(str(config_path).encode()).hexdigest()[:12]
    target = 'gui/' + str(os.geteuid()) + '/' + label
    loaded = managed_launchctl('print', target)
    if loaded.returncode not in (0, 113):
        raise ValueError('legacy schedule status unavailable; refusing cutover')
    paths = []
    if loaded.returncode == 0:
        paths = [Path(line.strip()[7:]) for line in loaded.stdout.splitlines() if line.strip().startswith('path = ')]
    else:
        candidates = list(managed_plist_root().glob('*.plist'))
        if len(candidates) > 200:
            raise ValueError('legacy schedule discovery exceeds 200 plists')
        for path in candidates:
            try:
                value = managed_read(path, maximum=64 << 10, private=False, decode=plistlib.loads)
            except (OSError, ValueError, plistlib.InvalidFileException):
                continue
            if isinstance(value, dict) and value.get('Label') == label:
                paths.append(path)
    if len(paths) != 1 or not paths[0].is_absolute():
        raise ValueError('one exact generated legacy intake schedule is required for cutover')
    raw = managed_read(paths[0], maximum=64 << 10, private=False, decode=lambda data: data)
    value = plistlib.loads(raw)
    args = value.get('ProgramArguments', [])
    if value.get('Label') != label or not isinstance(args, list) or len(args) != 4 or not all(isinstance(arg, str) for arg in args) or Path(args[1]).name != 'factory-autonomy.py' or Path(args[2]).resolve() != config_path or args[3] != '--once' or set(value.get('EnvironmentVariables', {})) - {'PATH'}:
        raise ValueError('legacy schedule arguments or environment require explicit review')
    return {'label': label, 'target': target, 'path': str(paths[0]), 'plist': raw.hex(), 'was_loaded': loaded.returncode == 0, 'path_environment': value.get('EnvironmentVariables', {}).get('PATH', '/usr/bin:/bin:/usr/sbin:/sbin')}


def refuse_legacy_intake_service(home, permitted_path=None):
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
        if path == permitted_path:
            continue
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


def managed_install_assets(factoryctl):
    script, factoryctl, python = Path(__file__).resolve(), Path(factoryctl).resolve(strict=True), Path(sys.executable).resolve(strict=True)
    prefix = factoryctl.parent.parent if factoryctl.parent.name == 'bin' else factoryctl.parent
    if script != prefix / 'libexec/dark-factory/factory-autonomy.py':
        raise ValueError('managed intake must be installed from the release or Homebrew libexec directory')
    return script, factoryctl, python


def managed_service(home, factoryctl, action, migration=False):
    home, state, identity = managed_paths(home)
    if action == 'install':
        pending = managed_read(state / 'migration.json', maximum=4 << 20)
        if pending and pending.get('phase') != 'completed' and not migration:
            raise ValueError('resume the explicit legacy migration before installing intake')
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
            script, factoryctl, python = managed_install_assets(factoryctl)
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


def managed_migrate(home, factoryctl, config_path, plan_hash=None, acknowledge_policy_narrowing=False):
    home, config_path = Path(home).resolve(strict=True), Path(config_path).resolve(strict=True)
    metadata = home.stat()
    if not home.is_dir() or metadata.st_uid != os.geteuid():
        raise ValueError('factory home is not owned by this operator')
    identity, state = [metadata.st_dev, metadata.st_ino], Path(str(home) + '.intake')
    receipt_path = state / 'migration.json'
    receipt = managed_read(receipt_path, maximum=4 << 20)
    if receipt and (receipt.get('version') != 1 or receipt.get('home_identity') != identity or receipt.get('config_path') != str(config_path)):
        raise ValueError('migration receipt belongs to another factory or configuration')
    request, config, raw_config, raw_journal = legacy_migration_input(home, config_path)
    if config.get('release_configs'):
        raise ValueError('legacy release companion uses host GitHub authority; customer migration requires a customer-scoped release path, so the legacy schedules remain untouched')
    if receipt and receipt['phase'] == 'completed':
        if plan_hash is not None and plan_hash != receipt['plan_hash']:
            raise ValueError('migration already completed under a different plan')
        return {'state': 'migrated', 'source_id': receipt['request']['source_id'], 'plan_hash': receipt['plan_hash']}
    if plan_hash is None:
        # Preview never creates state or modifies either schedule.
        if receipt:
            request = dict(receipt['request'], action='legacy_preview')
        else:
            job = legacy_migration_job(config_path)
            refuse_legacy_intake_service(home, Path(job['path']))
        reply = managed_api(factoryctl, home, ['legacy_preview'], request)
        if reply.get('state') == 'legacy_preview':
            legacy_review_ready(config, reply.get('legacy', {}).get('publication_repository'))
        reply['configuration'] = dict(request['configuration'], target_repository_id=reply.get('legacy', {}).get('target_repository_id', request['configuration'].get('target_repository_id', '')))
        reply['companions'] = {key: config[key] for key in ('review_mirror_root','review_provider','base','release_configs','command_timeout') if key in config}
        reply['source_id'] = request['source_id']
        if receipt:
            reply['cutover_phase'] = receipt['phase']
        return reply
    if len(plan_hash) != 64 or any(c not in '0123456789abcdef' for c in plan_hash):
        raise ValueError('migration requires the exact reviewed plan hash')
    if not receipt and request['legacy']['manual_app_authors'] and not acknowledge_policy_narrowing:
        raise ValueError('review App/bot policy narrowing: imported work is preserved, future bot issues require manual acceptance; apply with --acknowledge-policy-narrowing')
    script, _, _ = managed_install_assets(factoryctl)  # Fail before stopping anything.
    if 'review_mirror_root' in config and managed_read(script.with_name('factory-review-intake.py'), private=False, maximum=1 << 20, decode=lambda data: data) is None:
        raise ValueError('installed review companion is missing; legacy schedule remains untouched')
    home, state, identity = managed_paths(home)
    with managed_lock(Path(str(home) + '.autonomy.lock')), managed_lock(Path(config['journal'] + '.lock'), private=False):
        receipt = managed_read(receipt_path, maximum=4 << 20)
        request, config, raw_config, raw_journal = legacy_migration_input(home, config_path)
        if receipt:
            if receipt.get('home_identity') != identity or receipt.get('config_path') != str(config_path):
                raise ValueError('resume migration with its recorded plan and configuration')
            if request['legacy']['config_hash'] != receipt['request']['legacy']['config_hash'] or request['legacy']['journal_hash'] != receipt['request']['legacy']['journal_hash']:
                raise ValueError('legacy configuration or task history changed; cutover remains stopped for review')
            request = receipt['request']
            if receipt['plan_hash'] != plan_hash:
                if receipt['phase'] != 'old_stopped':
                    raise ValueError('resume migration with its recorded plan')
                current = dict(request, action='legacy_preview')
                reply = managed_api(factoryctl, home, ['legacy_preview'], current)
                if reply.get('state') != 'legacy_preview' or reply.get('legacy', {}).get('plan_hash') != plan_hash:
                    return reply
                receipt['plan_hash'] = plan_hash
                legacy_review_ready(config, reply['legacy'].get('publication_repository'))
                request['legacy']['plan_hash'] = plan_hash
                request['configuration']['target_repository_id'] = reply['legacy']['target_repository_id']
                atomic_json(receipt_path, receipt)
        else:
            if request['legacy']['manual_app_authors'] and not acknowledge_policy_narrowing:
                raise ValueError('apply the reviewed App/bot policy with --acknowledge-policy-narrowing')
            job = legacy_migration_job(config_path)
            refuse_legacy_intake_service(home, Path(job['path']))
            reply = managed_api(factoryctl, home, ['legacy_preview'], request)
            if reply.get('state') != 'legacy_preview' or reply.get('legacy', {}).get('plan_hash') != plan_hash:
                return reply if reply.get('state') != 'legacy_preview' else dict(reply, state='stale')
            legacy_review_ready(config, reply['legacy'].get('publication_repository'))
            request['legacy']['plan_hash'] = plan_hash
            request['legacy']['acknowledge_policy_narrowing'] = bool(request['legacy']['manual_app_authors']) and acknowledge_policy_narrowing
            request['configuration']['target_repository_id'] = reply['legacy']['target_repository_id']
            receipt = {'version': 1, 'home_identity': identity, 'config_path': str(config_path), 'phase': 'prepared', 'plan_hash': plan_hash, 'request': request, 'job': job, 'config': raw_config, 'journal': raw_journal}
            atomic_json(receipt_path, receipt)
        job = receipt['job']
        if receipt['phase'] == 'prepared':
            raw = managed_read(Path(job['path']), maximum=64 << 10, private=False, decode=lambda data: data)
            if raw is not None and raw.hex() != job['plist']:
                raise ValueError('legacy schedule changed; refusing to stop a foreign job')
            loaded = managed_launchctl('print', job['target'])
            paths = [line.strip()[7:] for line in loaded.stdout.splitlines() if line.strip().startswith('path = ')]
            if loaded.returncode == 0:
                if paths != [job['path']]:
                    raise ValueError('legacy loaded schedule ownership changed')
                if managed_launchctl('bootout', job['target']).returncode != 0:
                    raise ValueError('legacy schedule stop failed; retry the same migration')
            elif loaded.returncode != 113:
                raise ValueError('legacy schedule status unavailable')
            if raw is not None:
                Path(job['path']).unlink()
                descriptor = os.open(Path(job['path']).parent, os.O_RDONLY)
                try:
                    os.fsync(descriptor)
                finally:
                    os.close(descriptor)
            receipt['phase'] = 'old_stopped'
            atomic_json(receipt_path, receipt)
        if receipt['phase'] == 'old_stopped':
            request['action'] = 'legacy_commit'
            reply = managed_api(factoryctl, home, ['legacy_commit'], request)
            if reply.get('state') != 'legacy_committed':
                # Do not restart legacy after an ambiguous reply: the atomic
                # baseline may already exist. An exact retry reads its receipt.
                return dict(reply, cutover_phase='old_stopped', resume_plan=plan_hash)
            receipt['phase'] = 'baseline_committed'
            atomic_json(receipt_path, receipt)
        if receipt['phase'] == 'baseline_committed':
            atomic_json(state / 'legacy-review.json', json.loads(receipt['config']))
            managed_service(home, factoryctl, 'install', migration=True)
            receipt['phase'] = 'managed_started'
            atomic_json(receipt_path, receipt)
        if receipt['phase'] == 'managed_started':
            if job['was_loaded']:
                sources = managed_api(factoryctl, home, ['config'])
                found = [source for source in sources.get('sources', []) if source.get('id') == request['source_id']]
                if sources.get('state') != 'ok' or len(found) != 1:
                    raise ValueError('migration source status unavailable; retry the same plan')
                source = found[0]
                if source.get('revision') == 1 and source.get('enabled') is False:
                    reply = managed_api(factoryctl, home, ['enable', '--source', request['source_id'], '--revision', '1', '--reviewed-revision', '1'])
                    if reply.get('state') != 'ok':
                        return dict(reply, cutover_phase='managed_started', resume_plan=plan_hash)
                elif source.get('revision') != 2 or source.get('enabled') is not True:
                    raise ValueError('operator changed the migration source; it remains under operator control')
            receipt['phase'] = 'completed'
            atomic_json(receipt_path, receipt)
        if receipt['phase'] != 'completed':
            raise ValueError('unknown migration phase; refusing controller activation')
        return {'state': 'migrated', 'source_id': request['source_id'], 'plan_hash': plan_hash}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('config', type=Path, nargs='?')
    parser.add_argument('--managed', action='store_true')
    parser.add_argument('--factory-home', type=Path)
    parser.add_argument('--factoryctl', type=Path)
    parser.add_argument('--service', choices=('install', 'uninstall', 'status', 'migrate'))
    parser.add_argument('--legacy-config', type=Path)
    parser.add_argument('--plan')
    parser.add_argument('--acknowledge-policy-narrowing', action='store_true')
    parser.add_argument('--once', action='store_true')
    parser.add_argument('--release-only', action='store_true', help='Run the separately scheduled release and delivery pass.')
    parser.add_argument('--plist', action='store_true', help='Print a launchd plist; does not install or start it.')
    args = parser.parse_args()
    if args.managed:
        if args.config is not None or args.release_only or args.plist or args.factory_home is None or args.factoryctl is None or not args.factory_home.is_absolute() or not args.factoryctl.is_absolute():
            parser.error('managed intake requires --factory-home and --factoryctl only')
        if sys.version_info < (3, 9):
            raise ValueError('managed intake requires Python 3.9 or newer')
        if args.service == 'migrate':
            if args.legacy_config is None or not args.legacy_config.is_absolute():
                parser.error('migration requires --legacy-config ABS')
            result = managed_migrate(args.factory_home, args.factoryctl, args.legacy_config, args.plan, args.acknowledge_policy_narrowing)
        else:
            if args.legacy_config is not None or args.plan is not None or args.acknowledge_policy_narrowing:
                parser.error('migration arguments require --service migrate')
            result = managed_service(args.factory_home, args.factoryctl, args.service) if args.service else managed_tick(args.factory_home, args.factoryctl)
        if args.service:
            print(json.dumps(result), flush=True)
        return 0 if result['state'] != 'error' else 1
    if args.config is None or args.factory_home is not None or args.factoryctl is not None or args.service is not None or args.legacy_config is not None or args.plan is not None or args.acknowledge_policy_narrowing:
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
        if not args.release_only and Path(str(Path(config['factory_home']).resolve()) + '.intake/migration.json').exists():
            raise ValueError('legacy intake was explicitly cut over; resume or use the managed service')
        results = tick(config_path, config, args.release_only, controller_lock_fd=lock.fileno())
        write_health(config, results, args.release_only)
    print(json.dumps({'at': int(time.time()), 'components': results}), flush=True)
    return 0 if all(result['ok'] for result in results) else 1


if __name__ == '__main__':
    try:
        raise SystemExit(main())
    except (OSError, ValueError, KeyError) as error:
        print('factory-autonomy: ' + str(error), file=sys.stderr)
        raise SystemExit(1)
