#!/usr/bin/env python3
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import socket
import sqlite3
import subprocess
import sys
import tempfile
import time
import threading
import unittest
from unittest.mock import Mock, patch


def module(name):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(name + '.py'))
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


autonomy = module('factory-autonomy')
runtime = module('verify-live-runtime')
deploy = module('deploy-runtime')


class AutonomyTest(unittest.TestCase):
    def test_production_uses_existing_release_lane(self):
        config = {'repository': 'example/factory', 'project_id': 'a' * 32}
        with patch.object(autonomy.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, '{}', '')) as run:
            results = autonomy.tick(Path('/config.json'), config, release_only=True)
        self.assertEqual([{'component': 'factory-production', 'ok': True}], results)
        self.assertEqual('factory-production.py', Path(run.call_args.args[0][1]).name)
        self.assertEqual('--record', run.call_args.args[0][-1])

    def test_installed_controller_does_not_refresh_an_archive_parent(self):
        with tempfile.TemporaryDirectory() as directory:
            installed = Path(directory) / 'libexec' / 'dark-factory'
            installed.mkdir(parents=True)
            self.assertIsNone(autonomy.controller_checkout(installed))

    def test_controller_source_refresh_preserves_edits_and_requires_matching_ancestry(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            remote, checkout = root / 'remote', root / 'checkout'
            def git(path, *args):
                return subprocess.check_output(['git', '-C', str(path), *args], text=True, stderr=subprocess.DEVNULL).strip()
            remote.mkdir()
            git(remote, 'init', '-b', 'main')
            git(remote, 'config', 'user.name', 'Fixture')
            git(remote, 'config', 'user.email', 'fixture@example.invalid')
            (remote / 'source').write_text('before')
            git(remote, 'add', 'source')
            git(remote, 'commit', '-m', 'before')
            subprocess.run(['git', 'clone', str(remote), str(checkout)], check=True, capture_output=True)
            original = git(checkout, 'rev-parse', 'HEAD')
            url = 'https://github.com/fixture/controller.git'
            git(checkout, 'remote', 'set-url', 'origin', url)
            git(checkout, 'config', 'url.' + str(remote) + '.insteadOf', url)
            # get-url expands insteadOf: fake only this identity read, retaining
            # real Git fetch, ancestry, merge, dirty and branch behaviour.
            run = subprocess.run
            def transport(argv, **kwargs):
                if argv[-3:] == ['remote', 'get-url', 'origin']:
                    return subprocess.CompletedProcess(argv, 0, url, '')
                return run(argv, **kwargs)
            (remote / 'source').write_text('after')
            git(remote, 'commit', '-am', 'after')
            head = git(remote, 'rev-parse', 'HEAD')
            config = {'repository': 'fixture/controller', 'base': 'main'}
            receipt = {'state': 'verified', 'sha': head}
            with patch.object(autonomy.subprocess, 'run', side_effect=transport):
                (checkout / 'source').write_text('operator edits')
                with self.assertRaisesRegex(ValueError, 'tracked edits'):
                    autonomy.refresh_controller(checkout, config, receipt)
                self.assertEqual(original, git(checkout, 'rev-parse', 'HEAD'))
                self.assertEqual('operator edits', (checkout / 'source').read_text())
                git(checkout, 'restore', 'source')
                (checkout / 'untracked').write_text('keep')
                autonomy.refresh_controller(checkout, config, receipt)
                self.assertEqual(head, git(checkout, 'rev-parse', 'HEAD'))
                self.assertEqual('after', (checkout / 'source').read_text())
                self.assertEqual('keep', (checkout / 'untracked').read_text())
                for prefix in ('https://github.com/', 'git@github.com:', 'ssh://git@github.com/'):
                    for suffix in ('', '.git'):
                        url = prefix + 'fixture/controller' + suffix
                        git(checkout, 'remote', 'set-url', 'origin', url)
                        git(checkout, 'config', 'url.' + str(remote) + '.insteadOf', url)
                        autonomy.refresh_controller(checkout, config, receipt)
                with self.assertRaisesRegex(ValueError, 'merge-base'):
                    autonomy.refresh_controller(checkout, config, {'state': 'verified', 'sha': original})
                with self.assertRaisesRegex(ValueError, 'configured release repository'):
                    autonomy.refresh_controller(checkout, dict(config, repository='other/repository'), receipt)
                git(checkout, 'checkout', '-b', 'operator')
                with self.assertRaisesRegex(ValueError, 'release branch'):
                    autonomy.refresh_controller(checkout, config, receipt)
                self.assertEqual(head, git(checkout, 'rev-parse', 'HEAD'))

    def test_verified_release_refreshes_source_and_reports_refusal_without_redeploying(self):
        with tempfile.TemporaryDirectory() as directory:
            release = Path(directory) / 'release.json'
            release.write_text(json.dumps({'repository': 'fixture/controller', 'base': 'main'}))
            config = {'release_configs': [str(release)], 'factory_home': str(Path(directory) / 'home')}
            for state in ('planned', 'verified'):
                receipt = {'state': state, 'sha': 'a' * 40}
                responses = [subprocess.CompletedProcess([], 0, json.dumps(receipt), '')]
                with patch.object(autonomy.subprocess, 'run', side_effect=responses) as run, \
                     patch.object(autonomy.importlib.util, 'spec_from_file_location'), \
                     patch.object(autonomy.importlib.util, 'module_from_spec', return_value=Mock()), \
                     patch.object(autonomy, 'refresh_controller', side_effect=autonomy.ControllerSourceError('controller source has tracked edits')) as refresh:
                    result = autonomy.tick(Path(directory) / 'config', config, release_only=True)
                self.assertEqual(1, run.call_count)
                self.assertEqual(1, len(result))
                if state == 'verified':
                    refresh.assert_called_once()
                    self.assertFalse(result[-1]['ok'])
                    self.assertIn('tracked edits', result[-1]['error'])
                else:
                    refresh.assert_not_called()
                    self.assertTrue(result[-1]['ok'])

    def test_controller_excludes_another_job_for_the_same_factory(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            home = root / 'home'
            home.mkdir()
            other = root / 'other-config.json'
            other.write_text(json.dumps({'factory_home': str(home), 'journal': str(root / 'other-journal')}))
            with Path(str(home.resolve()) + '.autonomy.lock').open('a+') as lock:
                autonomy.fcntl.flock(lock, autonomy.fcntl.LOCK_EX | autonomy.fcntl.LOCK_NB)
                with patch.object(autonomy.sys, 'argv', ['factory-autonomy', str(other), '--once']), patch.object(autonomy, 'tick') as tick:
                    with self.assertRaisesRegex(ValueError, 'another controller'):
                        autonomy.main()
                    tick.assert_not_called()

    def test_external_controller_state_leaves_runtime_home_unchanged(self):
        with tempfile.TemporaryDirectory() as directory:
            root, home = Path(directory), Path(directory) / 'home'
            home.mkdir()
            config = root / 'config.json'
            journal = root / 'state' / 'journal.json'
            config.write_text(json.dumps({'factory_home': str(home), 'journal': str(journal)}))
            with patch.object(autonomy.sys, 'argv', ['factory-autonomy', str(config), '--once']), patch.object(autonomy, 'tick', return_value=[]):
                self.assertEqual(0, autonomy.main())
            self.assertEqual([], list(home.iterdir()))
            self.assertTrue(Path(str(home.resolve()) + '.autonomy.lock').is_file())
            self.assertTrue(Path(str(journal) + '.autonomy.json').is_file())

    def test_runtime_home_journal_and_release_journal_are_refused_before_controller_writes(self):
        with tempfile.TemporaryDirectory() as directory:
            root, home = Path(directory), Path(directory) / 'home'
            home.mkdir()
            release = root / 'release.json'
            release.write_text(json.dumps({'journal': str(home / 'release-journal.json')}))
            for config_value in ({'factory_home': str(home), 'journal': str(home / 'journal.json')}, {'factory_home': str(home), 'journal': str(root / 'journal.json'), 'release_configs': [str(release)]}):
                config = root / ('config-' + str(len(list(root.glob('config-*')))) + '.json')
                config.write_text(json.dumps(config_value))
                with patch.object(autonomy.sys, 'argv', ['factory-autonomy', str(config), '--once']), patch.object(autonomy, 'tick') as tick:
                    with self.assertRaisesRegex(ValueError, 'outside factory_home'):
                        autonomy.main()
                    tick.assert_not_called()
            self.assertEqual([], list(home.iterdir()))
            self.assertFalse(Path(str(home.resolve()) + '.autonomy.lock').exists())

    def test_intake_failure_is_retained_in_health(self):
        config = {'factory_home': '/private/tmp/factory', 'journal': '/private/tmp/journal'}
        with patch.object(autonomy.subprocess, 'run', return_value=subprocess.CompletedProcess([], 1, '', 'GitHub unavailable')) as run:
            result = autonomy.tick(Path('/private/tmp/config'), config)
        self.assertFalse(result[0]['ok'])
        self.assertEqual(1, len(result))
        self.assertIn('factory-intake.py', run.call_args_list[0].args[0][1])

    def test_launchd_results_do_not_retain_child_output(self):
        config = {'factory_home': '/private/tmp/factory', 'journal': '/private/tmp/journal'}
        secret = 'token=should-not-appear'
        with patch.object(autonomy.subprocess, 'run', return_value=subprocess.CompletedProcess([], 1, secret, 'gate refused: ' + secret)), \
             contextlib.redirect_stderr(io.StringIO()) as log:
            result = autonomy.tick(Path('/private/tmp/config'), config)
        self.assertEqual([{'component': 'factory-intake', 'ok': False, 'error': 'exit_1'}], result)
        # A bare status named no cause for four hours of identical ticks: the
        # controller's own log carries the redacted diagnostic the receipt must not.
        self.assertEqual('factory-intake exit_1: gate refused: token=***', log.getvalue().strip())
        self.assertNotIn('should-not-appear', log.getvalue())

    def test_silent_component_failure_still_names_itself_on_the_controller_log(self):
        config = {'factory_home': '/private/tmp/factory', 'journal': '/private/tmp/journal'}
        with patch.object(autonomy.subprocess, 'run', return_value=subprocess.CompletedProcess([], 2, '', '')), \
             contextlib.redirect_stderr(io.StringIO()) as log:
            autonomy.tick(Path('/private/tmp/config'), config)
        self.assertEqual('factory-intake exit_2: no stderr diagnostic', log.getvalue().strip())

    def test_health_receipt_is_private_and_finite(self):
        with tempfile.TemporaryDirectory() as directory:
            config = {'journal': str(Path(directory) / 'journal.json')}
            autonomy.write_health(config, [{'component': 'factory-intake', 'ok': False, 'error': 'exit_1'}])
            receipt = Path(config['journal'] + '.autonomy.json')
            self.assertEqual(oct(receipt.stat().st_mode & 0o777), '0o600')
            self.assertEqual(json.loads(receipt.read_text())['components'][0]['error'], 'exit_1')

    def test_source_refresh_waits_for_normal_pass_lock(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            config = {'factory_home': str(root / 'home'), 'release_configs': [str(root / 'release.json')]}
            (root / 'release.json').write_text('{}')
            receipt = {'state': 'verified', 'sha': 'a' * 40}
            requested, refreshed = threading.Event(), threading.Event()
            flock = autonomy.fcntl.flock
            def lock(fd, mode):
                requested.set()
                return flock(fd, mode)
            with (root / 'home.autonomy.lock').open('a+') as held:
                flock(held, autonomy.fcntl.LOCK_EX)
                with patch.object(autonomy.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, json.dumps(receipt), '')), \
                     patch.object(autonomy.importlib.util, 'spec_from_file_location'), \
                     patch.object(autonomy.importlib.util, 'module_from_spec', return_value=Mock()), \
                     patch.object(autonomy.fcntl, 'flock', side_effect=lock), \
                     patch.object(autonomy, 'refresh_controller', side_effect=lambda *_args: refreshed.set()):
                    worker = threading.Thread(target=autonomy.tick, args=(root / 'config', config, True))
                    worker.start()
                    try:
                        self.assertTrue(requested.wait(5))
                        self.assertFalse(refreshed.is_set())
                    finally:
                        flock(held, autonomy.fcntl.LOCK_UN)
                        worker.join(5)
                    self.assertFalse(worker.is_alive())
                    self.assertTrue(refreshed.is_set())

    def test_waiting_release_does_not_block_intake_review_or_allow_duplicate(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            script = root / 'factory-autonomy.py'
            script.write_text(Path(autonomy.__file__).read_text())
            release_config = root / 'release.json'
            release_config.write_text(json.dumps({'journal': str(root / 'release-journal')}))
            config = root / 'config.json'
            config.write_text(json.dumps({'factory_home': str(root / 'home'), 'journal': str(root / 'journal'),
                                          'release_configs': [str(release_config)], 'review_mirror_root': str(root / 'mirror')}))
            for name in ('factory-intake', 'factory-review-intake'):
                (root / (name + '.py')).write_text('print("{}")')
            (root / 'factory-release.py').write_text(
                'from pathlib import Path\nimport time\n'
                'root = Path(__file__).parent\n(root / "started").touch()\n'
                'while not (root / "finish").exists(): time.sleep(.01)\n'
                "print('{\"state\":\"planned\"}')\n")
            release = subprocess.Popen([sys.executable, str(script), str(config), '--once', '--release-only'],
                                       stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            try:
                deadline = time.monotonic() + 5
                while not (root / 'started').exists():
                    self.assertIsNone(release.poll())
                    self.assertLess(time.monotonic(), deadline)
                    time.sleep(.01)
                normal = subprocess.run([sys.executable, str(script), str(config), '--once'], capture_output=True, text=True, timeout=5)
                self.assertEqual(0, normal.returncode, normal.stderr)
                self.assertEqual(['factory-intake', 'factory-review-intake'],
                                 [item['component'] for item in json.loads(normal.stdout)['components']])
                duplicate = subprocess.run([sys.executable, str(script), str(config), '--once', '--release-only'], capture_output=True, text=True, timeout=5)
                self.assertNotEqual(0, duplicate.returncode)
                self.assertIn('another controller owns', duplicate.stderr)
                self.assertIsNone(release.poll())
            finally:
                (root / 'finish').touch()
                stdout, stderr = release.communicate(timeout=5)
            self.assertEqual(0, release.returncode, stderr)
            self.assertEqual(['factory-release'], [item['component'] for item in json.loads(stdout)['components']])

    def test_private_review_wakeup_is_optional(self):
        config = {'factory_home': '/private/tmp/factory', 'journal': '/private/tmp/journal', 'review_mirror_root': '/private/tmp/mirror'}
        with patch.object(autonomy.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, '{}', '')) as run:
            autonomy.tick(Path('/private/tmp/config'), config)
        self.assertTrue(any('factory-review-intake.py' in call.args[0][1] for call in run.call_args_list))
        self.assertIn('factory-review-intake.py', run.call_args_list[-1].args[0][1])
        self.assertIsNone(run.call_args_list[-1].kwargs['timeout'])

    def test_mixed_installed_binaries_cannot_prove_health(self):
        identities = ['a' * 40, 'b' * 40, 'a' * 40]
        last_sha = []
        def observe(argv, **kwargs):
            if '--build-identity' in argv:
                return subprocess.CompletedProcess(argv, 0, json.dumps({'source': last_sha[0], 'release': True}), '')
            sha = identities.pop(0)
            last_sha[:] = [sha]
            return subprocess.CompletedProcess(argv, 0, 'vcs.revision=' + sha + '\nvcs.modified=false\n', '')
        with patch.object(runtime.shutil, 'which', return_value='/usr/local/bin/go'), patch.object(runtime.subprocess, 'run', side_effect=observe):
            with self.assertRaisesRegex(ValueError, 'different revisions'):
                runtime.observe(Path('/private/tmp/factory'))

    def test_runtime_installer_is_bounded(self):
        states = iter([(True, 4, 0), (False, 5, 0), (False, 5, 0), (False, 5, 0), (True, 6, 0)])
        def command(argv, **kwargs):
            if Path(argv[1]).name == 'verify-live-runtime.py':
                return subprocess.CompletedProcess(argv, 0, json.dumps({'sha': 'a' * 40, 'healthy': True}), '')
            return subprocess.CompletedProcess(argv, 0, '', '')
        with patch.object(deploy, 'state', side_effect=lambda _home: next(states)), patch.object(deploy.subprocess, 'run', side_effect=command) as run:
            deploy.deploy('a' * 40)
        install = next(call for call in run.call_args_list if Path(call.args[0][1]).name == 'reinstall-service.sh' and '--prepare' not in call.args[0])
        self.assertEqual(600, install.kwargs['timeout'])
        self.assertIn([str(Path.home() / '.dark-factory.service/bin/current/factoryctl'), 'dispatch', 'off', '--revision', '4'], [call.args[0] for call in run.call_args_list])
        self.assertIn([str(Path.home() / '.dark-factory.service/bin/current/factoryctl'), 'dispatch', 'on', '--revision', '5'], [call.args[0] for call in run.call_args_list])

    def test_custom_home_prepares_before_pausing_and_is_used_throughout(self):
        home = Path('/private/tmp/alternate-factory')
        events = []
        states = iter([(True, 4, 0), (False, 5, 0), (False, 5, 0), (False, 5, 0), (True, 6, 0)])
        def state(actual_home):
            self.assertEqual(home, actual_home)
            events.append('state')
            return next(states)
        def command(argv, **kwargs):
            events.append(argv)
            self.assertEqual(str(home / 'runtimes/factory.sock'), kwargs['env']['DARK_FACTORY_SOCKET'])
            self.assertEqual(str(home / 'operator.token'), kwargs['env']['DARK_FACTORY_OPERATOR_TOKEN_FILE'])
            if 'dispatch' in argv:
                self.assertEqual(str(home) + '.service/bin/current/factoryctl', argv[0])
            else:
                self.assertEqual(str(home), argv[argv.index('--home') + 1])
            return subprocess.CompletedProcess(argv, 0, json.dumps({'sha': 'a' * 40, 'healthy': True}), '')
        with patch.object(deploy, 'state', side_effect=state), patch.object(deploy.subprocess, 'run', side_effect=command):
            deploy.deploy('a' * 40, home)
        self.assertIn('--prepare', events[0])
        self.assertEqual('state', events[1])

    def test_preparation_failure_does_not_pause_or_write_failure_receipt(self):
        with patch.object(deploy, 'state') as state, patch.object(deploy, 'failure_receipt') as receipt, \
             patch.object(deploy.subprocess, 'run', side_effect=subprocess.CalledProcessError(1, ['prepare'])) as run:
            with self.assertRaises(subprocess.CalledProcessError):
                deploy.deploy('a' * 40, Path('/private/tmp/alternate-factory'))
        state.assert_not_called()
        receipt.assert_not_called()
        self.assertEqual(1, run.call_count)
        self.assertIn('--prepare', run.call_args.args[0])

    def test_runtime_build_receipt_must_match_source(self):
        def command(argv, **kwargs):
            output = json.dumps({'source': 'b' * 40, 'release': True}) if '--build-identity' in argv else 'vcs.revision=' + 'a' * 40 + '\nvcs.modified=false\n'
            return subprocess.CompletedProcess(argv, 0, output, '')
        with patch.object(runtime.shutil, 'which', return_value='/usr/local/bin/go'), patch.object(runtime.subprocess, 'run', side_effect=command):
            with self.assertRaisesRegex(ValueError, 'receipt disagrees'):
                runtime.observe(Path('/private/tmp/alternate-factory'))

    def test_real_deploy_process_emits_one_receipt_without_replaying_commands(self):
        for initially_enabled, failing_action, settling in ((False, '', False), (True, '', False), (True, 'off', False), (True, 'on', False), (True, '', True), (False, '', True)):
            with self.subTest(initially_enabled=initially_enabled, failing_action=failing_action), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                home = root / 'factory'
                home.mkdir()
                with sqlite3.connect(home / 'factory.sqlite3') as connection:
                    connection.executescript('CREATE TABLE factory(singleton INTEGER, dispatch_enabled INTEGER, revision INTEGER); CREATE TABLE runs(phase TEXT, id BLOB);')
                    connection.execute('INSERT INTO factory VALUES(1, ?, 4)', (initially_enabled,))
                    if settling:
                        connection.execute("INSERT INTO runs VALUES('running', X'0123456789ABCDEF0123456789ABCDEF')")
                (root / 'deploy-runtime.py').write_text(Path(deploy.__file__).read_text())
                (root / 'reinstall-service.sh').write_text('printf "%s\\n" "$*" >>"$DARK_FACTORY_SOCKET.install-calls"\necho installer-output\n')
                (root / 'verify-live-runtime.py').write_text('import json, sys\nprint(json.dumps({"sha": sys.argv[-1], "healthy": True}))\n')
                control = Path(str(home) + '.service/bin/current/factoryctl')
                control.parent.mkdir(parents=True)
                (home / 'runtimes').mkdir()
                control.write_text('#!' + sys.executable + '\n' + r"""
import json, os, pathlib, sqlite3, sys
home = pathlib.Path(os.environ['DARK_FACTORY_OPERATOR_TOKEN_FILE']).parent
with (home / 'dispatch-calls').open('a') as stream:
    stream.write(sys.argv[2] + '\n')
if sys.argv[2] == FAILING_ACTION:
    print('fixture dispatch refused', file=sys.stderr)
    raise SystemExit(7)
with sqlite3.connect(home / 'factory.sqlite3') as connection:
    enabled, revision = connection.execute('SELECT dispatch_enabled, revision FROM factory').fetchone()
    assert revision == int(sys.argv[-1])
    target = int(sys.argv[2] == 'on')
    revision += 1
    connection.execute('UPDATE factory SET dispatch_enabled=?, revision=?', (target, revision))
    if sys.argv[2] == 'off':
        connection.execute("UPDATE runs SET phase='terminal' WHERE phase <> 'terminal'")
print(json.dumps({'enabled': bool(target), 'revision': revision}))
""".replace('FAILING_ACTION', repr(failing_action)))
                control.chmod(0o700)
                # Its own HOME: the compensation path writes a failure receipt
                # under it, which must never land in the operator's backups.
                result = subprocess.run([sys.executable, str(root / 'deploy-runtime.py'), '--home', str(home), 'a' * 40],
                                        env=dict(os.environ, HOME=str(root)), capture_output=True, text=True, timeout=15)
                calls = (home / 'dispatch-calls').read_text().splitlines()
                self.assertEqual(['off', 'on'] if initially_enabled and failing_action != 'off' else ['off'], calls)
                install_calls = (home / 'runtimes/factory.sock.install-calls').read_text().splitlines()
                self.assertEqual(1 if failing_action == 'off' else 2, len(install_calls))
                self.assertIn('--prepare', install_calls[0])
                if len(install_calls) == 2:
                    self.assertIn('--install-prepared', install_calls[1])
                receipt = root / '.dark-factory-backups' / ('deploy-' + 'a' * 40 + '.json')
                if failing_action:
                    # A stage after preparation never claims that nothing started.
                    self.assertEqual(1, result.returncode)
                    self.assertEqual('', result.stdout)
                    self.assertIn('fixture dispatch refused', result.stderr)
                    self.assertIn('stage: factoryctl dispatch ' + failing_action + ' --revision', result.stderr)
                    self.assertIn(' exit=7', result.stderr)
                    # A refused pause is inside the compensation scope and is
                    # recorded; a refused restore is after a healthy install.
                    if failing_action == 'off':
                        self.assertEqual(initially_enabled, json.loads(receipt.read_text())['dispatch_enabled'])
                    else:
                        self.assertFalse(receipt.exists())
                else:
                    self.assertFalse(receipt.exists())
                    self.assertEqual(0, result.returncode, result.stderr)
                    self.assertEqual({'sha': 'a' * 40, 'healthy': True, 'dispatch_enabled': initially_enabled}, json.loads(result.stdout))
                    self.assertEqual(1, len(result.stdout.splitlines()))

    def test_runtime_drain_counts_only_runs_a_new_daemon_cannot_adopt(self):
        # Short root: sockaddr_un's sun_path is 104 bytes, and the default
        # temporary directory plus a runtime name already crowds it.
        with tempfile.TemporaryDirectory(dir='/private/tmp') as directory:
            home = Path(directory) / 'f'
            (home / 'runtimes').mkdir(parents=True)
            with sqlite3.connect(home / 'factory.sqlite3') as connection:
                connection.executescript('CREATE TABLE factory(singleton INTEGER, dispatch_enabled INTEGER, revision INTEGER); CREATE TABLE runs(phase TEXT, id BLOB);')
                connection.execute('INSERT INTO factory VALUES(1, 0, 4)')
                connection.execute("INSERT INTO runs VALUES('running', X'0123456789ABCDEF0123456789ABCDEF')")
                connection.execute("INSERT INTO runs VALUES('finalizing', X'FEDCBA9876543210FEDCBA9876543210')")
            self.assertEqual(2, deploy.state(home)[2])
            for name in ('0123456789abcdef0123456789abcdef', 'fedcba9876543210fedcba9876543210'):
                (home / 'runtimes' / name).mkdir()
            with socket.socket(socket.AF_UNIX) as running, socket.socket(socket.AF_UNIX) as finalizing:
                running.bind(str(home / 'runtimes/0123456789abcdef0123456789abcdef/takeover.sock'))
                finalizing.bind(str(home / 'runtimes/fedcba9876543210fedcba9876543210/takeover.sock'))
                # Only the running one is adoptable; a finalizing run drains
                # whatever its runner still publishes.
                self.assertEqual(1, deploy.state(home)[2])

    def test_runtime_operator_change_after_pause_never_installs(self):
        changed = (False, 6, 0)
        states = iter([(True, 4, 0), changed])
        with patch.object(deploy, 'state', side_effect=lambda _home: next(states, changed)), \
             patch.object(deploy, 'service_reachable', return_value=True), \
             patch.object(deploy, 'failure_receipt'), \
             patch.object(deploy.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, '', '')) as run:
            with self.assertRaisesRegex(ValueError, 'deployment failed'):
                deploy.deploy('a' * 40)
        self.assertFalse(any(Path(call.args[0][1]).name == 'reinstall-service.sh' and '--prepare' not in call.args[0] for call in run.call_args_list))

    def test_runtime_settlements_during_pause_and_drain_restore_current_revision(self):
        states = iter([(True, 4, 4), (False, 5, 3), (False, 5, 2), (False, 5, 0), (False, 5, 0), (True, 6, 0)])
        def command(argv, **kwargs):
            return subprocess.CompletedProcess(argv, 0, json.dumps({'sha': 'a' * 40, 'healthy': True}), '')
        with patch.object(deploy, 'state', side_effect=lambda _home: next(states)), \
             patch.object(deploy.subprocess, 'run', side_effect=command) as run, patch.object(deploy.time, 'sleep'):
            deploy.deploy('a' * 40)
        self.assertTrue(any(call.args[0][-3:] == ['on', '--revision', '5'] for call in run.call_args_list))

    def test_runtime_operator_change_during_drain_never_installs_or_restores(self):
        # A revision past the one this invocation owns is the operator's, so
        # nothing is restored; the receipt still states what they left behind.
        for changed, dispatching in [((False, 7, 3), False), ((True, 6, 4), True), ((False, 6, 5), False)]:
            with self.subTest(changed=changed):
                states = iter([(True, 4, 4), (False, 5, 4), changed])
                with patch.object(deploy, 'state', side_effect=lambda _home: next(states, changed)), \
                     patch.object(deploy, 'service_reachable', return_value=True), \
                     patch.object(deploy.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, '', '')) as run, \
                     patch.object(deploy, 'failure_receipt') as receipt:
                    with self.assertRaisesRegex(ValueError, 'deployment failed'):
                        deploy.deploy('a' * 40)
                self.assertFalse(any('--install-prepared' in call.args[0] or 'on' in call.args[0] for call in run.call_args_list))
                receipt.assert_called_once_with('a' * 40, True, dispatching)

    def test_runtime_waits_past_old_deadline_without_replaying_pause(self):
        for enabled in (False, True):
            with self.subTest(enabled=enabled):
                states = iter([(enabled, 4, 2), (False, 5, 2), (False, 5, 1),
                               (False, 5, 1), (False, 5, 0), (False, 5, 0), (enabled, 6, 0)])
                with patch.object(deploy, 'state', side_effect=lambda _home: next(states)), \
                     patch.object(deploy.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, json.dumps({'sha': 'a' * 40, 'healthy': True}), '')) as run, \
                     patch.object(deploy.time, 'monotonic', side_effect=[0, 301, 1301]), \
                     patch.object(deploy.time, 'sleep') as sleep:
                    deploy.deploy('a' * 40)
                self.assertEqual(2, sleep.call_count)
                self.assertEqual(1, sum('off' in call.args[0] for call in run.call_args_list))
                self.assertEqual(int(enabled), sum('on' in call.args[0] for call in run.call_args_list))
                self.assertEqual(1, sum('--install-prepared' in call.args[0] for call in run.call_args_list))

    def test_runtime_restore_never_retries_a_refusal_or_uncertain_result(self):
        cases = [
            ('operator', 'factoryctl: dispatch revision is stale\n', [5], subprocess.CalledProcessError),
            ('opaque', 'factoryctl: dispatch was not accepted\n', [5], subprocess.CalledProcessError),
            ('timeout', None, [5], subprocess.TimeoutExpired),
        ]
        for name, error, expected, failure in cases:
            with self.subTest(name=name):
                states = iter([(True, 4, 2), (False, 5, 2), (False, 5, 0), (False, 5, 0)])
                attempts = []
                def command(argv, **kwargs):
                    if 'on' in argv:
                        attempts.append(int(argv[-1]))
                        if len(attempts) == 1:
                            if error is None:
                                raise subprocess.TimeoutExpired(argv, 15)
                            raise subprocess.CalledProcessError(1, argv, stderr=error)
                    return subprocess.CompletedProcess(argv, 0, json.dumps({'sha': 'a' * 40, 'healthy': True}), '')
                with patch.object(deploy, 'state', side_effect=lambda _home: next(states)), \
                     patch.object(deploy.subprocess, 'run', side_effect=command), \
                     patch.object(deploy.time, 'monotonic', side_effect=[0, 301]):
                    with self.assertRaises(failure):
                        deploy.deploy('a' * 40)
                self.assertEqual(expected, attempts)

    def test_runtime_operator_change_after_install_wins_over_restore(self):
        states = iter([(True, 4, 2), (False, 5, 2), (False, 5, 0), (False, 6, 0), (False, 6, 0)])
        with patch.object(deploy, 'state', side_effect=lambda _home: next(states)), \
             patch.object(deploy.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, json.dumps({'sha': 'a' * 40, 'healthy': True}), '')) as run:
            deploy.deploy('a' * 40)
        self.assertFalse(any('on' in call.args[0] for call in run.call_args_list))

    def test_runtime_failure_records_the_dispatch_state_it_actually_left(self):
        def installer(refuses_restore):
            def command(argv, **_kwargs):
                if Path(argv[1]).name == 'reinstall-service.sh' and '--prepare' not in argv:
                    raise subprocess.TimeoutExpired(argv, 600)
                if refuses_restore and 'on' in argv:
                    raise subprocess.CalledProcessError(1, argv, stderr='factoryctl: dispatch revision is stale\n')
                return subprocess.CompletedProcess(argv, 0, '', '')
            return command
        # The fourth state is the read that decides the restore, the fifth the
        # read the receipt states. Dispatch this process did not turn on still
        # counts: the receipt says what the factory was left with, not what it
        # attempted.
        for name, tail, refuses_restore, dispatching, restores in (
                ('restored', [(False, 5, 0), (True, 6, 0)], False, True, 1),
                ('restore refused', [(False, 5, 0), (False, 5, 0)], True, False, 1),
                ('operator turned it on first', [(True, 6, 0), (True, 6, 0)], False, True, 0)):
            with self.subTest(name=name):
                note = 'is on so the factory keeps working on the old build' if dispatching else 'remains off'
                with patch.object(deploy, 'state', side_effect=[(True, 4, 0), (False, 5, 0), (False, 5, 0)] + tail), \
                     patch.object(deploy, 'service_reachable', return_value=True), \
                     patch.object(deploy.subprocess, 'run', side_effect=installer(refuses_restore)) as run, \
                     patch.object(deploy, 'failure_receipt') as receipt:
                    with self.assertRaisesRegex(ValueError, 'dispatch ' + note + '; service_reachable=true'):
                        deploy.deploy('a' * 40)
                receipt.assert_called_once_with('a' * 40, True, dispatching)
                self.assertEqual(restores, sum('on' in call.args[0] for call in run.call_args_list))

    def test_runtime_failure_leaves_an_untouched_factory_dispatching_and_retryable(self):
        # The live failure of PR #1045's release: the restart was refused over
        # a run it could not adopt, after which nothing had been installed.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            home = root / 'factory'
            (home / 'runtimes').mkdir(parents=True)
            with sqlite3.connect(home / 'factory.sqlite3') as connection:
                connection.executescript('CREATE TABLE factory(singleton INTEGER, dispatch_enabled INTEGER, revision INTEGER); CREATE TABLE runs(phase TEXT, id BLOB);')
                connection.execute('INSERT INTO factory VALUES(1, 1, 4)')
            (root / 'deploy-runtime.py').write_text(Path(deploy.__file__).read_text())
            (root / 'reinstall-service.sh').write_text(
                'case "$*" in *--prepare*) exit 0 ;; esac\n'
                'echo "refusing: 1 non-adoptable non-terminal run(s)" >&2\nexit 75\n')
            control = Path(str(home) + '.service/bin/current/factoryctl')
            control.parent.mkdir(parents=True)
            control.write_text('#!' + sys.executable + '\n' + r"""
import os, pathlib, sqlite3, sys
home = pathlib.Path(os.environ['DARK_FACTORY_OPERATOR_TOKEN_FILE']).parent
with (home / 'dispatch-calls').open('a') as stream:
    stream.write(sys.argv[2] + '\n')
with sqlite3.connect(home / 'factory.sqlite3') as connection:
    revision = connection.execute('SELECT revision FROM factory').fetchone()[0]
    assert revision == int(sys.argv[-1])
    connection.execute('UPDATE factory SET dispatch_enabled=?, revision=?', (int(sys.argv[2] == 'on'), revision + 1))
""")
            control.chmod(0o700)
            result = subprocess.run([sys.executable, str(root / 'deploy-runtime.py'), '--home', str(home), 'a' * 40],
                                    env=dict(os.environ, HOME=str(root)), capture_output=True, text=True, timeout=15)
            # 75: no effect to undo, so the release lane may simply try again.
            self.assertEqual(75, result.returncode, result.stderr)
            self.assertIn('dispatch is on so the factory keeps working on the old build', result.stderr)
            self.assertEqual(['off', 'on'], (home / 'dispatch-calls').read_text().split())
            with sqlite3.connect(home / 'factory.sqlite3') as connection:
                self.assertEqual(1, connection.execute('SELECT dispatch_enabled FROM factory').fetchone()[0])
            receipt = json.loads((root / '.dark-factory-backups' / ('deploy-' + 'a' * 40 + '.json')).read_text())
            # The fixture has a readable store and no daemon, which is exactly
            # the pair the receipt must not conflate.
            self.assertEqual({'sha': 'a' * 40, 'healthy': False, 'dispatch_enabled': True,
                              'service_reachable': False, 'error': 'deployment_failed'}, receipt)

    def test_only_an_accepted_pause_is_this_deployment_s_to_undo(self):
        # Every accepted control command advances the revision, so landing on
        # original+1 proves nothing: a concurrent operator pause lands there
        # too. Only an accepted `dispatch off` is compensated. A non-zero exit
        # is no better evidence than a lost response -- the CLI can fail after
        # the daemon committed -- so both leave the pause alone and say so.
        unproven = ('is off and this deployment cannot prove whose pause it is;'
                    ' run factoryctl status, then decide whether to resume (factoryctl dispatch on)')
        for name, outcome, bump, left_on, restores, said in (
                ('accepted then the install fails', None, 1, True, 1,
                 'is on so the factory keeps working on the old build'),
                ('operator pause collides at +1',
                 subprocess.CalledProcessError(1, ['factoryctl', 'dispatch', 'off'], stderr='dispatch revision is stale\n'),
                 1, False, 0, unproven),
                # Same non-zero exit, opposite cause: our own pause committed
                # and the CLI lost the connection reporting it. Indistinguishable
                # from the collision above, and it must stay that way.
                ('non-zero exit after transport loss',
                 subprocess.CalledProcessError(1, ['factoryctl', 'dispatch', 'off'], stderr='connection reset by peer\n'),
                 1, False, 0, unproven),
                ('response lost at +1', subprocess.TimeoutExpired(['factoryctl', 'dispatch', 'off'], 15), 1, False, 0,
                 unproven)):
            with self.subTest(name=name):
                store = {'enabled': True, 'revision': 4}
                def command(argv, **_kwargs):
                    if 'dispatch' in argv and 'off' in argv:
                        # The operator's pause, or ours: the store cannot say.
                        store.update(enabled=False, revision=store['revision'] + bump)
                        if outcome is not None:
                            raise outcome
                    elif 'dispatch' in argv and 'on' in argv:
                        store.update(enabled=True, revision=store['revision'] + 1)
                    elif Path(argv[1]).name == 'reinstall-service.sh' and '--prepare' not in argv:
                        raise subprocess.TimeoutExpired(argv, 600)
                    return subprocess.CompletedProcess(argv, 0, '', '')
                with patch.object(deploy, 'state', side_effect=lambda _home: (store['enabled'], store['revision'], 0)), \
                     patch.object(deploy, 'service_reachable', return_value=True), \
                     patch.object(deploy.subprocess, 'run', side_effect=command) as run, \
                     patch.object(deploy, 'failure_receipt') as receipt:
                    with self.assertRaises(ValueError) as caught:
                        deploy.deploy('a' * 40)
                self.assertEqual(left_on, bool(store['enabled']))
                self.assertEqual(restores, sum('on' in call.args[0] for call in run.call_args_list))
                self.assertIn(said, str(caught.exception))
                receipt.assert_called_once_with('a' * 40, True, left_on)

    def test_unreadable_store_records_an_unknown_dispatch_state_not_a_false_off(self):
        # The last read is the receipt's only authority. When it fails, the
        # state is unknown: claiming "off" would send an operator to restart a
        # factory that may be running. An initially paused factory is the case
        # that used to slip through as proof of a no-effect refusal.
        for initially_enabled in (True, False):
            with self.subTest(initially_enabled=initially_enabled):
                reads = iter([(initially_enabled, 4, 0), (False, 5, 0), (False, 5, 0), (False, 5, 0)])
                def store(_home):
                    try:
                        return next(reads)
                    except StopIteration:
                        raise sqlite3.OperationalError('disk I/O error')
                def command(argv, **_kwargs):
                    if Path(argv[1]).name == 'reinstall-service.sh' and '--prepare' not in argv:
                        # The exit status a refusal over an unadoptable run uses.
                        raise subprocess.CalledProcessError(deploy.REFUSED, argv, stderr='refusing: 1 non-adoptable non-terminal run(s)\n')
                    return subprocess.CompletedProcess(argv, 0, '', '')
                with patch.object(deploy, 'state', side_effect=store), \
                     patch.object(deploy, 'service_reachable', return_value=True), \
                     patch.object(deploy.subprocess, 'run', side_effect=command), \
                     patch.object(deploy, 'failure_receipt') as receipt:
                    with self.assertRaises(ValueError) as caught:
                        deploy.deploy('a' * 40)
                self.assertIn('dispatch state is unreadable; run factoryctl status before deciding whether to resume',
                              str(caught.exception))
                receipt.assert_called_once_with('a' * 40, True, None)
                # Unknown is never the proof a no-effect refusal needs.
                self.assertFalse(getattr(caught.exception, 'retryable', False))

    def test_an_unreadable_store_during_the_drain_is_compensated_not_escaped(self):
        # sqlite3.Error is not an OSError: a drain read that fails has to reach
        # the same compensation as any other lost deployment.
        reads = iter([(True, 4, 0), (False, 5, 0)])
        def store(_home):
            try:
                return next(reads)
            except StopIteration:
                raise sqlite3.OperationalError('database is locked')
        with patch.object(deploy, 'state', side_effect=store), \
             patch.object(deploy, 'service_reachable', return_value=False), \
             patch.object(deploy.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, '', '')) as run, \
             patch.object(deploy, 'failure_receipt') as receipt:
            with self.assertRaises(ValueError) as caught:
                deploy.deploy('a' * 40)
        self.assertIn('deployment failed', str(caught.exception))
        receipt.assert_called_once_with('a' * 40, False, None)
        self.assertFalse(any('--install-prepared' in call.args[0] for call in run.call_args_list))

    def test_service_reachability_is_a_connect_not_a_readable_store(self):
        # Short root: sockaddr_un's sun_path is 104 bytes, and the default
        # temporary directory plus a runtime name already crowds it.
        with tempfile.TemporaryDirectory(dir='/private/tmp') as directory:
            home = Path(directory) / 'f'
            (home / 'runtimes').mkdir(parents=True)
            with sqlite3.connect(home / 'factory.sqlite3') as connection:
                connection.executescript('CREATE TABLE factory(singleton INTEGER, dispatch_enabled INTEGER, revision INTEGER); CREATE TABLE runs(phase TEXT, id BLOB);')
                connection.execute('INSERT INTO factory VALUES(1, 1, 4)')
            # A perfectly readable store is not a service.
            self.assertEqual(0, deploy.state(home)[2])
            self.assertFalse(deploy.service_reachable(home))
            with socket.socket(socket.AF_UNIX) as daemon:
                daemon.bind(str(home / 'runtimes/factory.sock'))
                # A socket file nothing listens on is still not reachable.
                self.assertFalse(deploy.service_reachable(home))
                daemon.listen(1)
                self.assertTrue(deploy.service_reachable(home))


class DeployStageEvidence(unittest.TestCase):
    def test_failed_stage_output_is_passed_up_uncut_with_the_stage_last(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'factory').mkdir()
            (root / 'deploy-runtime.py').write_text(Path(deploy.__file__).read_text())
            (root / 'reinstall-service.sh').write_text('printf "first line\\n%03000d\\nlast line\\n" 0 >&2\nexit 3\n')
            result = subprocess.run([sys.executable, str(root / 'deploy-runtime.py'), '--home', str(root / 'factory'), 'a' * 40], capture_output=True, text=True, timeout=15)
        self.assertEqual(75, result.returncode)
        self.assertIn('first line\n', result.stderr)
        self.assertTrue(result.stderr.endswith('last line\n\nstage: sh reinstall-service.sh --home factory --prepare exit=3\n'), result.stderr[-120:])


class ManagedIntakeTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.home = self.root / 'factory'
        self.home.mkdir(mode=0o700)
        self.factoryctl = self.root / 'release/factoryctl'
        self.factoryctl.parent.mkdir()
        self.factoryctl.write_text('fixture')
        self.factoryctl.chmod(0o755)
        self.script = self.factoryctl.parent / 'libexec/dark-factory/factory-autonomy.py'
        self.script.parent.mkdir(parents=True)
        self.script.write_text('fixture')
        self.script.chmod(0o755)
        self.plists = self.root / 'plists'
        self.sources = [{'id': '1'*32, 'enabled': True, 'revision': 1, 'poll_seconds': 60},
                        {'id': '2'*32, 'enabled': True, 'revision': 1, 'poll_seconds': 60}]

    def test_per_source_errors_persist_and_paused_sources_reconcile(self):
        replies = [{'state':'ok', 'sources': self.sources}, {'state':'ok','imported_tasks':['a'*32]}, {'state':'unavailable'}]
        with patch.object(autonomy, 'managed_api', side_effect=replies) as api, patch.object(autonomy.time, 'time', return_value=100):
            result = autonomy.managed_tick(self.home, self.factoryctl)
        self.assertEqual('ok', result['sources']['1'*32]['state'])
        self.assertEqual(100, result['sources']['1'*32]['last_success_at'])
        self.assertEqual('unavailable', result['sources']['2'*32]['error'])
        with patch.object(autonomy, 'managed_api', return_value={'state':'ok','sources':self.sources}) as api, patch.object(autonomy.time, 'time', return_value=105):
            result = autonomy.managed_tick(self.home, self.factoryctl)
        self.assertEqual(1, api.call_count)
        self.assertEqual('unavailable', result['sources']['2'*32]['error'])
        self.assertEqual(100, result['sources']['1'*32]['last_success_at'])
        self.sources[1].update(enabled=False, revision=2)
        with patch.object(autonomy, 'managed_api', side_effect=[{'state':'ok','sources':self.sources},{'state':'paused'}]) as api, patch.object(autonomy.time, 'time', return_value=106):
            result = autonomy.managed_tick(self.home, self.factoryctl)
        self.assertEqual(['tick','--source','2'*32,'--page','1'],api.call_args.args[2])
        self.assertEqual('paused', result['sources']['2'*32]['state'])
        self.assertEqual(106, result['sources']['2'*32]['last_success_at'])

    def test_cursor_survives_restart_and_ambiguous_import(self):
        self.sources = self.sources[:1]
        for at, next_page, expected in [(100,2,'1'),(105,'failure','2'),(165,None,'2')]:
            reply = {'state':'unavailable'} if next_page == 'failure' else {'state':'ok','next_page':next_page,'imported_tasks':['a'*32]}
            with patch.object(autonomy, 'managed_api', side_effect=[{'state':'ok','sources':self.sources},reply]) as api, patch.object(autonomy.time, 'time', return_value=at):
                autonomy.managed_tick(self.home,self.factoryctl)
                self.assertEqual(expected,api.call_args.args[2][-1])
        journal = json.loads(Path(str(self.home)+'.intake/journal.json').read_text())
        self.assertEqual(1,journal['sources']['1'*32]['next_page'])
        self.assertNotIn('task_id',journal['sources']['1'*32])

    def test_acceptance_cursor_survives_restart_and_lost_response_then_wraps(self):
        self.sources = self.sources[:1]
        for at, reply, cursor in [(100, {'state':'ok','acceptance_cursor':'b'*32}, ''),
                                  (105, {'state':'unavailable'}, 'b'*32),
                                  (165, {'state':'ok'}, 'b'*32),
                                  (225, {'state':'ok'}, '')]:
            with patch.object(autonomy, 'managed_api', side_effect=[{'state':'ok','sources':self.sources},reply]) as api, patch.object(autonomy.time, 'time', return_value=at):
                autonomy.managed_tick(self.home,self.factoryctl)
                expected = ['tick','--source','1'*32,'--page','1']
                if cursor:
                    expected += ['--acceptance-cursor',cursor]
                self.assertEqual(expected,api.call_args.args[2])
        journal = json.loads(Path(str(self.home)+'.intake/journal.json').read_text())
        self.assertEqual('',journal['sources']['1'*32]['acceptance_cursor'])

    def test_partial_receipt_progress_keeps_error_then_wraps_without_success(self):
        self.sources = self.sources[:1]
        for at, reply, expected, due in [(100, {'state':'unavailable','acceptance_progress':True,'acceptance_cursor':'b'*32}, '', 160),
                                         (105, {'state':'unavailable'}, '', 160),
                                         (160, {'state':'unavailable','acceptance_progress':True,'acceptance_cursor':'c'*32}, 'b'*32, 220),
                                         (220, {'state':'ok'}, 'c'*32, 280)]:
            with patch.object(autonomy, 'managed_api', side_effect=[{'state':'ok','sources':self.sources},reply]) as api, patch.object(autonomy.time, 'time', return_value=at):
                autonomy.managed_tick(self.home,self.factoryctl)
                arguments = api.call_args.args[2]
                if at not in (105,):
                    self.assertEqual(expected, arguments[-1] if '--acceptance-cursor' in arguments else '')
            journal = json.loads(Path(str(self.home)+'.intake/journal.json').read_text())
            record = journal['sources']['1'*32]
            self.assertEqual(due, record['next_due'])
            if reply['state'] != 'ok':
                self.assertEqual('unavailable',record['error'])
                self.assertEqual(0,record.get('last_success_at',0))
        self.assertEqual(220,record['last_success_at'])
        self.assertEqual('',record['error'])

    def test_overflow_and_factory_replacement_fail_closed(self):
        with patch.object(autonomy,'managed_api',return_value={'state':'ok','sources':self.sources*101}):
            result = autonomy.managed_tick(self.home,self.factoryctl)
        self.assertEqual('overflow',result['error'])
        old = self.home.with_name('old')
        self.home.rename(old)
        self.home.mkdir(mode=0o700)
        with patch.object(autonomy,'managed_api') as api, self.assertRaisesRegex(ValueError,'different factory'):
            autonomy.managed_tick(self.home,self.factoryctl)
        api.assert_not_called()

    def test_api_child_receives_only_local_operator_environment(self):
        response = subprocess.CompletedProcess([],0,'{"state":"ok"}','')
        with patch.object(autonomy.subprocess,'run',return_value=response) as run:
            autonomy.managed_api(self.factoryctl,self.home,['config'])
        self.assertEqual({'PATH','DARK_FACTORY_SOCKET','DARK_FACTORY_OPERATOR_TOKEN_FILE'},set(run.call_args.kwargs['env']))
        self.assertEqual([str(self.factoryctl),'intake','config'],run.call_args.args[0])

    def test_service_install_upgrade_status_uninstall_retains_journal(self):
        jobs, calls = {}, []
        def launchctl(*args):
            calls.append(args)
            if args[0] == 'print':
                path = jobs.get(args[1])
                return subprocess.CompletedProcess(args,0 if path else 113,'path = '+str(path)+'\n' if path else '','')
            if args[0] == 'bootstrap':
                value = autonomy.plistlib.loads(Path(args[2]).read_bytes())
                jobs[args[1]+'/'+value['Label']] = args[2]
            elif args[0] == 'bootout':
                del jobs[args[1]]
            return subprocess.CompletedProcess(args,0,'','')
        with patch.object(autonomy,'__file__',str(self.script)), patch.object(autonomy,'managed_plist_root',return_value=self.plists), patch.object(autonomy,'managed_launchctl',side_effect=launchctl):
            self.assertEqual('scheduled',autonomy.managed_service(self.home,self.factoryctl,'install')['state'])
            self.assertEqual('scheduled',autonomy.managed_service(self.home,self.factoryctl,'install')['state'])
            self.assertEqual(1,sum(call[0]=='bootstrap' for call in calls))
            self.assertEqual('scheduled',autonomy.managed_service(self.home,self.factoryctl,'status')['state'])
            journal = Path(str(self.home)+'.intake/journal.json')
            autonomy.atomic_json(journal,{'keep':'cutoff'})
            self.script.write_text('upgraded fixture')
            autonomy.managed_service(self.home,self.factoryctl,'install')
            self.assertEqual(2,sum(call[0]=='bootstrap' for call in calls))
            autonomy.managed_service(self.home,self.factoryctl,'uninstall')
            self.assertEqual({'keep':'cutoff'},json.loads(journal.read_text()))
            self.assertFalse(jobs)

    def test_install_refuses_same_home_legacy_schedule_without_touching_it(self):
        self.plists.mkdir()
        config = self.root / 'legacy.json'
        config.write_text(json.dumps({'factory_home': str(self.home), 'journal': str(self.root / 'legacy-journal.json')}))
        config.chmod(0o600)
        # The operator may save the generated plist under a custom filename.
        plist = self.plists / 'custom-intake.plist'
        job = {'Label': 'build.darkfactory.autonomy.fixture', 'ProgramArguments': ['/usr/bin/python3', '/old/release/factory-autonomy.py', str(config), '--once']}
        plist.write_bytes(autonomy.plistlib.dumps(job))
        plist.chmod(0o600)
        original = plist.read_bytes()
        with patch.object(autonomy, 'managed_plist_root', return_value=self.plists), patch.object(autonomy, 'managed_launchctl', return_value=subprocess.CompletedProcess([], 0, '', '')) as launchctl:
            with self.assertRaisesRegex(ValueError, 'legacy intake already schedules'):
                autonomy.managed_service(self.home, self.factoryctl, 'install')
            self.assertEqual([('list',)], [call.args for call in launchctl.call_args_list])
        self.assertEqual(original, plist.read_bytes())
        self.assertFalse(Path(str(self.home)+'.intake/service.json').exists())
        # Release-only work and another factory retain their existing jobs.
        job['ProgramArguments'].append('--release-only')
        plist.write_bytes(autonomy.plistlib.dumps(job))
        with patch.object(autonomy, 'managed_plist_root', return_value=self.plists), patch.object(autonomy, 'managed_launchctl', return_value=subprocess.CompletedProcess([], 0, '', '')):
            autonomy.refuse_legacy_intake_service(self.home)
            job['ProgramArguments'].pop()
            plist.write_bytes(autonomy.plistlib.dumps(job))
            config.write_text(json.dumps({'factory_home': str(self.root / 'another-home')}))
            autonomy.refuse_legacy_intake_service(self.home)

    def test_install_refuses_uninspectable_loaded_legacy_job_and_overflow(self):
        label = 'build.darkfactory.autonomy.fixture'
        replies = [subprocess.CompletedProcess([], 0, '-\t0\t'+label+'\n', ''), subprocess.CompletedProcess([], 0, 'path = '+str(self.root/'missing.plist')+'\n', '')]
        with patch.object(autonomy, 'managed_plist_root', return_value=self.plists), patch.object(autonomy, 'managed_launchctl', side_effect=replies), self.assertRaisesRegex(ValueError, 'arguments unavailable'):
            autonomy.refuse_legacy_intake_service(self.home)
        self.plists.mkdir()
        for index in range(201):
            (self.plists / (str(index)+'.plist')).touch()
        with patch.object(autonomy, 'managed_plist_root', return_value=self.plists), patch.object(autonomy, 'managed_launchctl', return_value=subprocess.CompletedProcess([], 0, '', '')), self.assertRaisesRegex(ValueError, 'exceeds 200'):
            autonomy.refuse_legacy_intake_service(self.home)

    def test_foreign_plist_is_never_stopped_and_missing_owned_plist_can_uninstall(self):
        absent = subprocess.CompletedProcess([],113,'','')
        success = subprocess.CompletedProcess([],0,'','')
        with patch.object(autonomy,'__file__',str(self.script)), patch.object(autonomy,'managed_plist_root',return_value=self.plists), patch.object(autonomy,'managed_launchctl',side_effect=[success,absent,success]):
            autonomy.managed_service(self.home,self.factoryctl,'install')
        plist = next(self.plists.iterdir())
        plist.write_text('foreign')
        with patch.object(autonomy,'managed_plist_root',return_value=self.plists), patch.object(autonomy,'managed_launchctl') as launchctl, self.assertRaisesRegex(ValueError,'foreign'):
            autonomy.managed_service(self.home,self.factoryctl,'uninstall')
        launchctl.assert_not_called()
        plist.unlink()
        with patch.object(autonomy,'managed_plist_root',return_value=self.plists), patch.object(autonomy,'managed_launchctl',return_value=absent):
            self.assertEqual('absent',autonomy.managed_service(self.home,self.factoryctl,'uninstall')['state'])

    @unittest.skipUnless(autonomy.os.environ.get('DARK_FACTORY_INTAKE_SERVICE_E2E') == '1', 'disposable launchd gate only')
    def test_real_disposable_service_lifecycle(self):
        self.script.write_bytes(Path(autonomy.__file__).read_bytes())
        self.factoryctl.write_text('#!/bin/sh\nprintf \'{"state":"ok","sources":[]}\\n\'\n')
        label = 'com.dark-factory.intake.' + autonomy.hashlib.sha256(str(self.home).encode()).hexdigest()[:12]
        target = 'gui/' + str(autonomy.os.geteuid()) + '/' + label
        status = Path(str(self.home) + '.intake/status.json')
        with patch.object(autonomy, '__file__', str(self.script)), patch.object(autonomy, 'managed_plist_root', return_value=self.plists):
            try:
                self.assertEqual('scheduled', autonomy.managed_service(self.home, self.factoryctl, 'install')['state'])
                deadline = time.monotonic() + 20
                while not status.exists() and time.monotonic() < deadline:
                    time.sleep(0.1)
                self.assertEqual('ok', autonomy.managed_read(status)['state'])
                self.assertEqual('scheduled', autonomy.managed_service(self.home, self.factoryctl, 'status')['state'])
                self.assertEqual('scheduled', autonomy.managed_service(self.home, self.factoryctl, 'install')['state'])
                self.assertEqual('absent', autonomy.managed_service(self.home, self.factoryctl, 'uninstall')['state'])
                self.assertEqual(113, autonomy.managed_launchctl('print', target).returncode)
                self.assertTrue(status.exists())
                self.assertFalse(list(self.plists.iterdir()))
            finally:
                autonomy.managed_launchctl('bootout', target)


class LegacyCutoverTest(unittest.TestCase):
    setUp = ManagedIntakeTest.setUp

    def fixture(self):
        import shutil
        import hashlib
        import plistlib
        shutil.copyfile(Path(__file__).with_name('factory-intake.py'), self.script.with_name('factory-intake.py'))
        self.script.with_name('factory-review-intake.py').write_text('# fixture')
        self.config_path, journal = self.root / 'legacy.json', self.root / 'legacy-journal.json'
        self.config = {'repository':'fixture/issues','project_id':'1'*32,'overseer_agent_id':'2'*32,'label':'ready','allowed_authors':['owner'],'factory_home':str(self.home),'journal':str(journal),'priority_by_label':{'urgent':5,'later':-2},'review_mirror_root':str(self.root / 'reviews')}
        autonomy.atomic_json(self.config_path, self.config)
        legacy = module('factory-intake')
        autonomy.atomic_json(journal, {'version':2,'config_fingerprint':legacy.config_fingerprint(self.config),'issues':{}})
        label = 'build.darkfactory.autonomy.' + hashlib.sha256(str(self.config_path).encode()).hexdigest()[:12]
        self.plists.mkdir()
        self.legacy_plist = self.plists / (label + '.plist')
        self.legacy_plist.write_bytes(plistlib.dumps({'Label':label,'ProgramArguments':[sys.executable,str(self.script),str(self.config_path),'--once'],'EnvironmentVariables':{'PATH':'/legacy/bin:/usr/bin:/bin'}}))
        self.loaded = {label:str(self.legacy_plist)}
        self.calls, self.sources, self.commits = [], {}, 0
        self.plan = 'a'*64
        def launchctl(*args):
            self.calls.append(args)
            label = args[1].split('/')[-1] if len(args)>1 else ''
            if args[0] == 'list':
                return subprocess.CompletedProcess(args,0,'\n'.join(self.loaded),'')
            if args[0] == 'print':
                return subprocess.CompletedProcess(args,0,'path = '+self.loaded[label],'') if label in self.loaded else subprocess.CompletedProcess(args,113,'','')
            if args[0] == 'bootout':
                self.loaded.pop(label, None)
            if args[0] == 'bootstrap':
                value = plistlib.loads(Path(args[2]).read_bytes())
                self.loaded[value['Label']] = args[2]
            return subprocess.CompletedProcess(args,0,'','')
        def api(_binary,_home,args,value=None):
            if args[0] == 'legacy_preview':
                return {'state':'legacy_committed' if self.sources else 'legacy_preview','legacy':{'plan_hash': self.plan, 'target_repository_id':'b'*32, 'publication_repository':'fixture/publication', 'requires_policy_acknowledgement': bool(value['legacy'].get('manual_app_authors'))}}
            if args[0] == 'legacy_commit':
                if value['legacy']['plan_hash'] != self.plan:
                    return {'state':'stale','legacy':{'plan_hash':self.plan, 'target_repository_id':'b'*32}}
                if not self.sources:
                    self.commits += 1
                    self.sources[value['source_id']] = dict(value['configuration'],id=value['source_id'],enabled=False,revision=1)
                return {'state':'legacy_committed','legacy':{'plan_hash':self.plan, 'target_repository_id':'b'*32}}
            if args[0] == 'config':
                return {'state':'ok','sources':list(self.sources.values())}
            if args[0] == 'enable':
                self.sources[args[2]].update(enabled=True,revision=2)
                return {'state':'ok'}
            if args[0] == 'tick':
                return {'state':'ok'}
            raise AssertionError(args)
        self.api = api
        self.addCleanup(patch.stopall)
        patch.object(autonomy,'__file__',str(self.script)).start()
        self.review_ready = patch.object(autonomy,'legacy_review_ready').start()
        patch.object(autonomy,'managed_plist_root',return_value=self.plists).start()
        patch.object(autonomy,'managed_launchctl',side_effect=launchctl).start()
        patch.object(autonomy,'managed_api',side_effect=api).start()

    def test_review_readiness_is_checked_before_stopping_legacy(self):
        self.fixture()
        self.review_ready.side_effect=ValueError('review mirror unavailable')
        with self.assertRaisesRegex(ValueError,'review mirror unavailable'):
            autonomy.managed_migrate(self.home,self.factoryctl,self.config_path,self.plan)
        self.assertFalse(any(call[0]=='bootout' for call in self.calls))
        self.assertEqual(0,self.commits)
        self.review_ready.assert_called_once_with(self.config,'fixture/publication')

    def test_release_companion_refuses_cutover_before_schedule_or_baseline_changes(self):
        self.fixture()
        self.config['release_configs'] = [str(self.root / 'release.json')]
        autonomy.atomic_json(self.root / 'release.json', {'journal': str(self.root / 'release-journal.json')})
        autonomy.atomic_json(self.config_path, self.config)
        legacy = module('factory-intake')
        autonomy.atomic_json(Path(self.config['journal']), {'version': 2, 'config_fingerprint': legacy.config_fingerprint(self.config), 'issues': {}})
        original = self.legacy_plist.read_bytes()
        for plan in (None, self.plan):
            with self.assertRaisesRegex(ValueError, 'customer-scoped release path'):
                autonomy.managed_migrate(self.home, self.factoryctl, self.config_path, plan)
        self.assertEqual(original, self.legacy_plist.read_bytes())
        self.assertFalse(any(call[0] == 'bootout' for call in self.calls))
        self.assertEqual(0, self.commits)

    def test_processed_history_hash_is_proven_only_by_the_retained_matching_snapshot(self):
        self.fixture()
        legacy = module('factory-intake')
        desired = {'number':1,'title':'Original','body':'Bytes','author':'owner','labels':['ready'],'state':'OPEN','updated_at':'before','url':'https://github.com/fixture/issues/issues/1'}
        fingerprint = legacy.fingerprint(desired)
        record = {'number':1,'managed':True,'processed_fingerprint':fingerprint,'desired':desired,'desired_fingerprint':fingerprint}
        journal = {'version':2,'config_fingerprint':legacy.config_fingerprint(self.config),'issues':{'fixture/issues#1':record}}
        autonomy.atomic_json(Path(self.config['journal']),journal)
        request,_,_,_=autonomy.legacy_migration_input(self.home,self.config_path)
        self.assertIn('historical_content_hash',request['legacy']['history'][0])
        task=request['legacy']['history'][0]['task_id']
        desired['updated_at']='after'
        record['desired_fingerprint']=legacy.fingerprint(desired)
        autonomy.atomic_json(Path(self.config['journal']),journal)
        request,_,_,_=autonomy.legacy_migration_input(self.home,self.config_path)
        self.assertEqual(task,request['legacy']['history'][0]['task_id'])
        self.assertNotIn('historical_content_hash',request['legacy']['history'][0])
        journal['issues'].update({str(i):{} for i in range(201)})
        autonomy.atomic_json(Path(self.config['journal']),journal)
        with self.assertRaisesRegex(ValueError,'at most 200'):
            autonomy.legacy_migration_input(self.home,self.config_path)

    def test_preview_preserves_priority_and_makes_no_state_or_schedule_changes(self):
        self.fixture()
        before = self.legacy_plist.read_bytes()
        result = autonomy.managed_migrate(self.home,self.factoryctl,self.config_path)
        self.assertEqual('legacy_preview',result['state'])
        self.assertEqual(self.config['priority_by_label'],result['configuration']['priority_by_label'])
        self.assertEqual(['owner'],result['configuration']['trusted_authors'])
        self.assertEqual('b'*32,result['configuration']['target_repository_id'])
        self.assertFalse(Path(str(self.home)+'.intake').exists())
        self.assertEqual(before,self.legacy_plist.read_bytes())
        self.assertTrue(all(call[0] in ('print','list') for call in self.calls))

    def test_app_authors_require_reviewed_narrowing_before_any_stop_and_resume_keeps_ack(self):
        self.fixture()
        self.config['allowed_authors'] = ['owner', 'app/factory', 'automation[bot]']
        autonomy.atomic_json(self.config_path,self.config)
        legacy = module('factory-intake')
        journal_path = Path(self.config['journal'])
        autonomy.atomic_json(journal_path, {'version':2,'config_fingerprint':legacy.config_fingerprint(self.config),'issues':{}})
        before = journal_path.read_bytes()
        preview = autonomy.managed_migrate(self.home,self.factoryctl,self.config_path)
        self.assertTrue(preview['legacy']['requires_policy_acknowledgement'])
        self.assertEqual(['owner'], preview['configuration']['trusted_authors'])
        with self.assertRaisesRegex(ValueError, 'acknowledge-policy-narrowing'):
            autonomy.managed_migrate(self.home,self.factoryctl,self.config_path,self.plan)
        self.assertTrue(self.legacy_plist.exists())
        self.assertFalse(any(call[0] == 'bootout' for call in self.calls))
        write = autonomy.atomic_json
        def crash(path,value):
            write(path,value)
            if Path(path).name == 'migration.json' and value['phase'] == 'prepared':
                raise RuntimeError('phase crash')
        with patch.object(autonomy,'atomic_json',side_effect=crash):
            with self.assertRaisesRegex(RuntimeError,'phase crash'):
                autonomy.managed_migrate(self.home,self.factoryctl,self.config_path,self.plan,True)
        receipt = autonomy.managed_read(Path(str(self.home)+'.intake/migration.json'),maximum=4<<20)
        self.assertTrue(receipt['request']['legacy']['acknowledge_policy_narrowing'])
        self.assertEqual(['app/factory','automation[bot]'],receipt['request']['legacy']['manual_app_authors'])
        self.assertEqual('b'*32,receipt['request']['configuration']['target_repository_id'])
        result = autonomy.managed_migrate(self.home,self.factoryctl,self.config_path,self.plan)
        self.assertEqual('migrated',result['state'])
        self.assertEqual(before,journal_path.read_bytes())
        self.assertEqual(1,self.commits)

    def test_app_only_legacy_policy_becomes_manual(self):
        self.fixture()
        self.config['allowed_authors'] = ['app/factory']
        autonomy.atomic_json(self.config_path,self.config)
        legacy = module('factory-intake')
        autonomy.atomic_json(Path(self.config['journal']), {'version':2,'config_fingerprint':legacy.config_fingerprint(self.config),'issues':{}})
        request,_,_,_ = autonomy.legacy_migration_input(self.home,self.config_path)
        self.assertEqual('manual',request['configuration']['policy'])
        self.assertEqual([],request['configuration']['trusted_authors'])

    def test_every_durable_phase_resumes_once_and_preserves_history_and_companion(self):
        for phase in ('prepared','old_stopped','baseline_committed','managed_started','completed'):
            with self.subTest(phase=phase):
                self.setUp()
                self.fixture()
                before = Path(self.config['journal']).read_bytes()
                write = autonomy.atomic_json
                failed = False
                def crash(path,value):
                    nonlocal failed
                    write(path,value)
                    if path.name == 'migration.json' and value.get('phase') == phase and not failed:
                        failed = True
                        raise RuntimeError('simulated crash')
                with patch.object(autonomy,'atomic_json',side_effect=crash):
                    with self.assertRaisesRegex(RuntimeError,'simulated crash'):
                        autonomy.managed_migrate(self.home,self.factoryctl,self.config_path,self.plan)
                receipt = autonomy.managed_read(Path(str(self.home)+'.intake/migration.json'),maximum=4<<20)
                self.assertEqual(phase,receipt['phase'])
                self.assertEqual('migrated',autonomy.managed_migrate(self.home,self.factoryctl,self.config_path,self.plan)['state'])
                self.assertEqual('migrated',autonomy.managed_migrate(self.home,self.factoryctl,self.config_path,self.plan)['state'])
                self.assertEqual(1,self.commits)
                self.assertEqual(1,len(self.loaded))
                self.assertTrue(next(iter(self.loaded)).startswith('com.dark-factory.intake.'))
                self.assertEqual(1,sum(call[0]=='bootout' for call in self.calls))
                self.assertEqual(before,Path(self.config['journal']).read_bytes())
                self.assertEqual(before.decode(),receipt['journal'])
                with patch.object(autonomy,'tick',return_value=[{'ok':True}]) as companion:
                    autonomy.managed_tick(self.home,self.factoryctl)
                    self.assertTrue(companion.call_args.kwargs['skip_intake'])
                    self.assertEqual('/legacy/bin:/usr/bin:/bin',companion.call_args.kwargs['environment']['PATH'])
                    self.assertEqual(self.config,companion.call_args.args[1])
                    self.assertEqual(['--managed-migration',str(Path(str(self.home)+'.intake/migration.json')),'--factoryctl',str(self.factoryctl)],companion.call_args.kwargs['review_arguments'])
                patch.stopall()

    def test_lost_commit_response_never_restarts_legacy_or_duplicates_baseline(self):
        self.fixture()
        lost = False
        def api(binary,home,args,value=None):
            nonlocal lost
            reply = self.api(binary,home,args,value)
            if args[0] == 'legacy_commit' and not lost:
                lost = True
                return {'state':'unavailable'}
            return reply
        with patch.object(autonomy,'managed_api',side_effect=api):
            result = autonomy.managed_migrate(self.home,self.factoryctl,self.config_path,self.plan)
            self.assertEqual('old_stopped',result['cutover_phase'])
            self.assertEqual({},self.loaded)
            self.assertFalse(next(iter(self.sources.values()))['enabled'])
            self.assertEqual('migrated',autonomy.managed_migrate(self.home,self.factoryctl,self.config_path,self.plan)['state'])
        self.assertEqual(1,self.commits)

    def test_stale_remote_plan_requires_review_and_can_resume_while_legacy_stays_stopped(self):
        self.fixture()
        def api(binary,home,args,value=None):
            if args[0] == 'legacy_commit':
                self.plan='b'*64
            return self.api(binary,home,args,value)
        with patch.object(autonomy,'managed_api',side_effect=api):
            self.assertEqual('stale',autonomy.managed_migrate(self.home,self.factoryctl,self.config_path,'a'*64)['state'])
        self.assertEqual({},self.loaded)
        preview=autonomy.managed_migrate(self.home,self.factoryctl,self.config_path)
        self.assertEqual('b'*64,preview['legacy']['plan_hash'])
        self.assertEqual('migrated',autonomy.managed_migrate(self.home,self.factoryctl,self.config_path,'b'*64)['state'])
        self.assertEqual(1,self.commits)

    def test_unsettled_legacy_plan_and_second_same_home_job_refuse_before_stop(self):
        self.fixture()
        with patch.object(autonomy,'managed_api',return_value={'state':'legacy_blocked'}):
            self.assertEqual('legacy_blocked',autonomy.managed_migrate(self.home,self.factoryctl,self.config_path,self.plan)['state'])
        self.assertTrue(self.legacy_plist.exists())
        self.assertFalse(any(call[0]=='bootout' for call in self.calls))
        import shutil
        shutil.copyfile(self.legacy_plist,self.plists/'second.plist')
        with self.assertRaisesRegex(ValueError,'legacy intake already'):
            autonomy.managed_migrate(self.home,self.factoryctl,self.config_path,self.plan)
        self.assertFalse(any(call[0]=='bootout' for call in self.calls))


if __name__ == '__main__':
    unittest.main()
