#!/usr/bin/env python3
"""Run the operator-configured intake and local attention checks under launchd."""
import argparse
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


def tick(config_path, config):
    scripts = Path(__file__).resolve().parent
    calls = []
    calls.append([sys.executable, str(scripts / 'factory-intake.py'), str(config_path), '--once'])
    releases = config.get('release_configs', [])
    for release_config in releases:
        calls.append([sys.executable, str(scripts / 'factory-release.py'), release_config, '--latest', '--once'])
    if 'review_mirror_root' in config:
        if not isinstance(config['review_mirror_root'], str) or not Path(config['review_mirror_root']).is_absolute():
            raise ValueError('review_mirror_root must be an absolute path')
        calls.append([sys.executable, str(scripts / 'factory-review-intake.py'), str(config_path), '--once'])
    results = []
    for argv in calls:
        try:
            completed = subprocess.run(argv, capture_output=True, text=True, timeout=None if Path(argv[1]).name == 'factory-review-intake.py' else 1300)
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
                    refresh_controller(scripts.parent, release_config, receipt)

        except ControllerSourceError as error:
            result.update({'ok': False, 'error': str(error)})
        except subprocess.TimeoutExpired:
            results.append({'component': Path(argv[1]).stem, 'ok': False, 'error': 'timeout'})
        except Exception:
            results.append({'component': Path(argv[1]).stem, 'ok': False, 'error': 'exception'})
    return results


def write_health(config, results):
    path = Path(config['journal'] + '.autonomy.json')
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix='.' + path.name + '.', dir=path.parent)
    try:
        with os.fdopen(fd, 'w', encoding='utf-8') as stream:
            json.dump({'at': int(time.time()), 'components': results}, stream, sort_keys=True)
            stream.write('\n')
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
        os.chmod(path, 0o600)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('config', type=Path)
    parser.add_argument('--once', action='store_true')
    parser.add_argument('--plist', action='store_true', help='Print a launchd plist; does not install or start it.')
    args = parser.parse_args()
    config_path = args.config.resolve(strict=True)
    config = json.loads(config_path.read_text())
    validate_controller_config(config)
    interval = config.get('poll_seconds', 120)
    if type(interval) is not int or not 5 <= interval <= 86400:
        raise ValueError('poll_seconds must be 5..86400')
    # Use launchd StartInterval rather than keeping a second polling daemon.
    if args.plist:
        log = str(Path(config['journal']).with_suffix('.service.log'))
        plist = {'Label': 'build.darkfactory.autonomy.' + hashlib.sha256(str(config_path).encode()).hexdigest()[:12], 'ProgramArguments': [sys.executable, str(Path(__file__).resolve()), str(config_path), '--once'],
                 'StartInterval': interval, 'RunAtLoad': True, 'ProcessType': 'Background',
                 'StandardOutPath': log, 'StandardErrorPath': log,
                 'EnvironmentVariables': {'PATH': os.environ.get('PATH', '/usr/bin:/bin:/usr/sbin:/sbin')}}
        sys.stdout.buffer.write(plistlib.dumps(plist))
        return 0
    # ponytail: one host controller at a time. A final cold review can occupy
    # this job/lock for its 20-minute deadline; separate existing review
    # scheduling only if intake/release latency justifies that additional job.
    descriptor = os.open(Path(str(Path(config['factory_home']).resolve()) + '.autonomy.lock'), os.O_CREAT | os.O_RDWR, 0o600)
    with os.fdopen(descriptor, 'a+') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise ValueError('another controller owns this factory') from error
        results = tick(config_path, config)
        write_health(config, results)
    print(json.dumps({'at': int(time.time()), 'components': results}), flush=True)
    return 0 if all(result['ok'] for result in results) else 1


if __name__ == '__main__':
    try:
        raise SystemExit(main())
    except (OSError, ValueError, KeyError) as error:
        print('factory-autonomy: ' + str(error), file=sys.stderr)
        raise SystemExit(1)
