#!/usr/bin/env python3
import importlib.util
import json
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
        with patch.object(autonomy.subprocess, 'run', return_value=subprocess.CompletedProcess([], 1, secret, secret)):
            result = autonomy.tick(Path('/private/tmp/config'), config)
        self.assertEqual([{'component': 'factory-intake', 'ok': False, 'error': 'exit_1'}], result)

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
                result = subprocess.run([sys.executable, str(root / 'deploy-runtime.py'), '--home', str(home), 'a' * 40], capture_output=True, text=True, timeout=15)
                calls = (home / 'dispatch-calls').read_text().splitlines()
                self.assertEqual(['off', 'on'] if initially_enabled and failing_action != 'off' else ['off'], calls)
                install_calls = (home / 'runtimes/factory.sock.install-calls').read_text().splitlines()
                self.assertEqual(1 if failing_action == 'off' else 2, len(install_calls))
                self.assertIn('--prepare', install_calls[0])
                if len(install_calls) == 2:
                    self.assertIn('--install-prepared', install_calls[1])
                if failing_action:
                    self.assertNotEqual(0, result.returncode)
                    self.assertEqual('', result.stdout)
                    self.assertIn('fixture dispatch refused', result.stderr)
                else:
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
        states = iter([(True, 4, 0), (False, 6, 0)])
        with patch.object(deploy, 'state', side_effect=lambda _home: next(states)), patch.object(deploy.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, '', '')) as run:
            with self.assertRaisesRegex(ValueError, 'operator changed'):
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
        for changed in [(False, 7, 3), (True, 6, 4), (False, 6, 5)]:
            with self.subTest(changed=changed):
                states = iter([(True, 4, 4), (False, 5, 4), changed])
                with patch.object(deploy, 'state', side_effect=lambda _home: next(states, changed)), \
                     patch.object(deploy.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, '', '')) as run, \
                     patch.object(deploy, 'failure_receipt') as receipt, patch.object(deploy.time, 'monotonic', side_effect=[0, 301]):
                    with self.assertRaisesRegex(ValueError, 'operator changed'):
                        deploy.deploy('a' * 40)
                self.assertFalse(any('--install-prepared' in call.args[0] or 'on' in call.args[0] for call in run.call_args_list))
                receipt.assert_not_called()

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

    def test_runtime_failure_records_a_safe_receipt(self):
        def timeout_install(argv, **_kwargs):
            if Path(argv[1]).name == 'reinstall-service.sh' and '--prepare' not in argv:
                raise subprocess.TimeoutExpired(argv, 600)
            return subprocess.CompletedProcess(argv, 0, '', '')
        with patch.object(deploy, 'state', side_effect=[(True, 4, 0), (False, 5, 0), (False, 5, 0), (False, 5, 0)]), \
             patch.object(deploy.subprocess, 'run', side_effect=timeout_install), \
             patch.object(deploy, 'failure_receipt') as receipt:
            with self.assertRaisesRegex(ValueError, 'dispatch remains off; service_reachable=true'):
                deploy.deploy('a' * 40)
        receipt.assert_called_once_with('a' * 40, True)


if __name__ == '__main__':
    unittest.main()
