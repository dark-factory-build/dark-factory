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
                    result = autonomy.tick(config)
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
            with Path(str(home.resolve()) + '.release.autonomy.lock').open('a+') as lock:
                autonomy.fcntl.flock(lock, autonomy.fcntl.LOCK_EX | autonomy.fcntl.LOCK_NB)
                with patch.object(autonomy.sys, 'argv', ['factory-autonomy', str(other), '--once', '--release-only']), patch.object(autonomy, 'tick') as tick:
                    with self.assertRaisesRegex(ValueError, 'another controller'):
                        autonomy.main()
                    tick.assert_not_called()

    def test_intake_pass_is_refused_before_any_write(self):
        # The live intake job still runs this script without --release-only
        # until it is unloaded; it must neither run nor write anything.
        with tempfile.TemporaryDirectory() as directory:
            root, home = Path(directory), Path(directory) / 'home'
            home.mkdir()
            config = root / 'config.json'
            config.write_text(json.dumps({'factory_home': str(home), 'journal': str(root / 'journal.json')}))
            with patch.object(autonomy.sys, 'argv', ['factory-autonomy', str(config), '--once']), patch.object(autonomy, 'tick') as tick, \
                 contextlib.redirect_stderr(io.StringIO()) as log, self.assertRaises(SystemExit) as raised:
                autonomy.main()
            self.assertEqual(2, raised.exception.code)
            self.assertIn('factoryd polls intake itself', log.getvalue())
            tick.assert_not_called()
            self.assertEqual(['config.json', 'home'], sorted(path.name for path in root.iterdir()))

    def test_external_controller_state_leaves_runtime_home_unchanged(self):
        with tempfile.TemporaryDirectory() as directory:
            root, home = Path(directory), Path(directory) / 'home'
            home.mkdir()
            config = root / 'config.json'
            journal = root / 'state' / 'journal.json'
            config.write_text(json.dumps({'factory_home': str(home), 'journal': str(journal)}))
            with patch.object(autonomy.sys, 'argv', ['factory-autonomy', str(config), '--once', '--release-only']), patch.object(autonomy, 'tick', return_value=[]):
                self.assertEqual(0, autonomy.main())
            self.assertEqual([], list(home.iterdir()))
            self.assertTrue(Path(str(home.resolve()) + '.release.autonomy.lock').is_file())
            self.assertTrue(Path(str(journal) + '.release-autonomy.json').is_file())

    def test_runtime_home_journal_and_release_journal_are_refused_before_controller_writes(self):
        with tempfile.TemporaryDirectory() as directory:
            root, home = Path(directory), Path(directory) / 'home'
            home.mkdir()
            release = root / 'release.json'
            release.write_text(json.dumps({'journal': str(home / 'release-journal.json')}))
            for config_value in ({'factory_home': str(home), 'journal': str(home / 'journal.json')}, {'factory_home': str(home), 'journal': str(root / 'journal.json'), 'release_configs': [str(release)]}):
                config = root / ('config-' + str(len(list(root.glob('config-*')))) + '.json')
                config.write_text(json.dumps(config_value))
                with patch.object(autonomy.sys, 'argv', ['factory-autonomy', str(config), '--once', '--release-only']), patch.object(autonomy, 'tick') as tick:
                    with self.assertRaisesRegex(ValueError, 'outside factory_home'):
                        autonomy.main()
                    tick.assert_not_called()
            self.assertEqual([], list(home.iterdir()))
            self.assertFalse(Path(str(home.resolve()) + '.release.autonomy.lock').exists())

    def test_component_failure_is_retained_in_health(self):
        config = {'factory_home': '/private/tmp/factory', 'journal': '/private/tmp/journal', 'release_configs': ['/private/tmp/release.json']}
        with patch.object(autonomy.subprocess, 'run', return_value=subprocess.CompletedProcess([], 1, '', 'GitHub unavailable')) as run, \
             contextlib.redirect_stderr(io.StringIO()):
            result = autonomy.tick(config)
        self.assertFalse(result[0]['ok'])
        self.assertEqual(1, len(result))
        self.assertIn('factory-release.py', run.call_args_list[0].args[0][1])

    def test_launchd_results_do_not_retain_child_output(self):
        config = {'factory_home': '/private/tmp/factory', 'journal': '/private/tmp/journal', 'release_configs': ['/private/tmp/release.json']}
        secret = 'token=should-not-appear'
        with patch.object(autonomy.subprocess, 'run', return_value=subprocess.CompletedProcess([], 1, secret, 'gate refused: ' + secret)), \
             contextlib.redirect_stderr(io.StringIO()) as log:
            result = autonomy.tick(config)
        self.assertEqual([{'component': 'factory-release', 'ok': False, 'error': 'exit_1'}], result)
        # A bare status named no cause for four hours of identical ticks: the
        # controller's own log carries the redacted diagnostic the receipt must not.
        self.assertEqual('factory-release exit_1: gate refused: token=***', log.getvalue().strip())
        self.assertNotIn('should-not-appear', log.getvalue())

    def test_silent_component_failure_still_names_itself_on_the_controller_log(self):
        config = {'factory_home': '/private/tmp/factory', 'journal': '/private/tmp/journal', 'release_configs': ['/private/tmp/release.json']}
        with patch.object(autonomy.subprocess, 'run', return_value=subprocess.CompletedProcess([], 2, '', '')), \
             contextlib.redirect_stderr(io.StringIO()) as log:
            autonomy.tick(config)
        self.assertEqual('factory-release exit_2: no stderr diagnostic', log.getvalue().strip())

    def test_health_receipt_is_private_and_finite(self):
        with tempfile.TemporaryDirectory() as directory:
            config = {'journal': str(Path(directory) / 'journal.json')}
            autonomy.write_health(config, [{'component': 'factory-release', 'ok': False, 'error': 'exit_1'}])
            receipt = Path(config['journal'] + '.release-autonomy.json')
            self.assertEqual(oct(receipt.stat().st_mode & 0o777), '0o600')
            self.assertEqual(json.loads(receipt.read_text())['components'][0]['error'], 'exit_1')

    def test_waiting_release_refuses_a_duplicate(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            script = root / 'factory-autonomy.py'
            script.write_text(Path(autonomy.__file__).read_text())
            release_config = root / 'release.json'
            release_config.write_text(json.dumps({'journal': str(root / 'release-journal')}))
            config = root / 'config.json'
            config.write_text(json.dumps({'factory_home': str(root / 'home'), 'journal': str(root / 'journal'),
                                          'release_configs': [str(release_config)]}))
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
                duplicate = subprocess.run([sys.executable, str(script), str(config), '--once', '--release-only'], capture_output=True, text=True, timeout=5)
                self.assertNotEqual(0, duplicate.returncode)
                self.assertIn('another controller owns', duplicate.stderr)
                self.assertIsNone(release.poll())
            finally:
                (root / 'finish').touch()
                stdout, stderr = release.communicate(timeout=5)
            self.assertEqual(0, release.returncode, stderr)
            self.assertEqual(['factory-release'], [item['component'] for item in json.loads(stdout)['components']])

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

    def test_runtime_lookup_closes_connection_on_success_and_error(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            for valid in (True, False):
                connection = sqlite3.connect(':memory:')
                if valid:
                    connection.executescript('CREATE TABLE factory(singleton, dispatch_enabled, revision); INSERT INTO factory VALUES(1, 1, 4); CREATE TABLE runs(phase, id);')
                with patch.object(deploy.sqlite3, 'connect', return_value=connection):
                    if valid:
                        self.assertEqual((1, 4, 0), deploy.state(home))
                    else:
                        with self.assertRaises(sqlite3.OperationalError):
                            deploy.state(home)
                # Holding a reference deliberately prevents garbage collection
                # from hiding a lookup that only ends its transaction.
                with self.assertRaises(sqlite3.ProgrammingError):
                    connection.execute('SELECT 1')

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
                              'service_reachable': False, 'error': 'deployment_failed',
                              'install_output': 'stdout:\n\nstderr:\nrefusing: 1 non-adoptable non-terminal run(s)\n'}, receipt)

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
                receipt.assert_called_once_with('a' * 40, True, None,
                                                'stdout:\n\nstderr:\nrefusing: 1 non-adoptable non-terminal run(s)\n')
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

    def test_failed_install_receipt_keeps_bounded_redacted_output(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            home = root / 'factory'
            (home / 'runtimes').mkdir(parents=True)
            with sqlite3.connect(home / 'factory.sqlite3') as connection:
                connection.executescript('CREATE TABLE factory(singleton INTEGER, dispatch_enabled INTEGER, revision INTEGER); CREATE TABLE runs(phase TEXT, id BLOB);')
                connection.execute('INSERT INTO factory VALUES(1, 1, 4)')
            (root / 'deploy-runtime.py').write_text(Path(deploy.__file__).read_text())
            control = Path(str(home) + '.service/bin/current/factoryctl')
            control.parent.mkdir(parents=True)
            control.write_text('#!/bin/sh\nexit 0\n')
            control.chmod(0o700)
            calls = []
            def command(argv, **kwargs):
                calls.append(argv)
                if '--install-prepared' in argv:
                    raise subprocess.CalledProcessError(3, argv, output='stdout token=private\n', stderr='stderr bearer secret\n')
                return subprocess.CompletedProcess(argv, 0, '', '')
            states = iter([(True, 4, 0), (False, 5, 0), (False, 5, 0), (False, 5, 0), (True, 6, 0)])
            with patch.dict(os.environ, {'HOME': str(root)}), patch.object(deploy, 'state', side_effect=lambda _home: next(states)), patch.object(deploy.subprocess, 'run', side_effect=command):
                with self.assertRaises(ValueError):
                    deploy.deploy('a' * 40, home)
            receipt = root / '.dark-factory-backups' / ('deploy-' + 'a' * 40 + '.json')
            value = json.loads(receipt.read_text())
            self.assertEqual('stdout:\nstdout token=***\n\nstderr:\nstderr bearer ***\n', value['install_output'])
            self.assertNotIn('private', value['install_output'])
            self.assertNotIn('secret', value['install_output'])


if __name__ == '__main__':
    unittest.main()
