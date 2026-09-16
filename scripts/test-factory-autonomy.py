#!/usr/bin/env python3
import importlib.util
import json
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch


def module(name):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(name + '.py'))
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


autonomy = module('factory-autonomy')
runtime = module('verify-live-runtime')
deploy = module('deploy-runtime')


class AutonomyTest(unittest.TestCase):
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

    def test_private_review_wakeup_is_optional(self):
        config = {'factory_home': '/private/tmp/factory', 'journal': '/private/tmp/journal', 'review_mirror_root': '/private/tmp/mirror'}
        with patch.object(autonomy.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, '{}', '')) as run:
            autonomy.tick(Path('/private/tmp/config'), config)
        self.assertTrue(any('factory-review-intake.py' in call.args[0][1] for call in run.call_args_list))

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
                    connection.executescript('CREATE TABLE factory(singleton INTEGER, dispatch_enabled INTEGER, revision INTEGER); CREATE TABLE runs(phase TEXT);')
                    connection.execute('INSERT INTO factory VALUES(1, ?, 4)', (initially_enabled,))
                    if settling:
                        connection.execute("INSERT INTO runs VALUES('running')")
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
        settled = connection.execute("UPDATE runs SET phase='terminal' WHERE phase <> 'terminal'").rowcount
        connection.execute('UPDATE factory SET revision=revision+?', (settled,))
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

    def test_runtime_operator_change_after_pause_never_installs(self):
        states = iter([(True, 4, 0), (False, 6, 0)])
        with patch.object(deploy, 'state', side_effect=lambda _home: next(states)), patch.object(deploy.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, '', '')) as run:
            with self.assertRaisesRegex(ValueError, 'operator changed'):
                deploy.deploy('a' * 40)
        self.assertFalse(any(Path(call.args[0][1]).name == 'reinstall-service.sh' and '--prepare' not in call.args[0] for call in run.call_args_list))

    def test_runtime_settlements_during_pause_and_drain_restore_current_revision(self):
        states = iter([(True, 4, 4), (False, 6, 3), (False, 7, 2), (False, 9, 0), (False, 9, 0), (True, 10, 0)])
        def command(argv, **kwargs):
            return subprocess.CompletedProcess(argv, 0, json.dumps({'sha': 'a' * 40, 'healthy': True}), '')
        with patch.object(deploy, 'state', side_effect=lambda _home: next(states)), \
             patch.object(deploy.subprocess, 'run', side_effect=command) as run, patch.object(deploy.time, 'sleep'):
            deploy.deploy('a' * 40)
        self.assertTrue(any(call.args[0][-3:] == ['on', '--revision', '9'] for call in run.call_args_list))

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

    def test_runtime_drain_timeout_restores_only_the_owned_pause(self):
        for enabled in (False, True):
            with self.subTest(enabled=enabled):
                paused = 5
                states = iter([(enabled, 4, 2), (False, paused, 2), (False, paused + 1, 1)])
                with patch.object(deploy, 'state', side_effect=lambda _home: next(states, (False, paused + 1, 1))), \
                     patch.object(deploy.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, '', '')) as run, \
                     patch.object(deploy.time, 'monotonic', side_effect=[0, 301]), \
                     patch.object(deploy, 'failure_receipt') as receipt:
                    with self.assertRaisesRegex(ValueError, 'runs did not drain'):
                        deploy.deploy('a' * 40)
                self.assertFalse(any('--install-prepared' in call.args[0] for call in run.call_args_list))
                restore = [call.args[0] for call in run.call_args_list if 'on' in call.args[0]]
                self.assertEqual(int(enabled), len(restore))
                if enabled:
                    self.assertEqual(['on', '--revision', str(paused + 1)], restore[0][-3:])
                receipt.assert_not_called()

    def test_runtime_restore_retries_only_a_refused_cas_with_proven_settlement(self):
        cases = [
            ('settlement', 'factoryctl: dispatch revision is stale\n', (False, 6, 1), [5, 6], ValueError),
            ('operator', 'factoryctl: dispatch revision is stale\n', (False, 7, 1), [5], ValueError),
            ('no progress', 'factoryctl: dispatch revision is stale\n', (False, 5, 2), [5], subprocess.CalledProcessError),
            ('opaque', 'factoryctl: dispatch was not accepted\n', None, [5], subprocess.CalledProcessError),
            ('timeout', None, None, [5], subprocess.TimeoutExpired),
        ]
        for name, error, raced, expected, failure in cases:
            with self.subTest(name=name):
                states = iter([(True, 4, 2), (False, 5, 2), (False, 5, 2), raced])
                attempts = []
                def command(argv, **kwargs):
                    if 'on' in argv:
                        attempts.append(int(argv[-1]))
                        if len(attempts) == 1:
                            if error is None:
                                raise subprocess.TimeoutExpired(argv, 15)
                            raise subprocess.CalledProcessError(1, argv, stderr=error)
                    return subprocess.CompletedProcess(argv, 0, '', '')
                with patch.object(deploy, 'state', side_effect=lambda _home: next(states)), \
                     patch.object(deploy.subprocess, 'run', side_effect=command), \
                     patch.object(deploy.time, 'monotonic', side_effect=[0, 301]):
                    with self.assertRaises(failure):
                        deploy.deploy('a' * 40)
                self.assertEqual(expected, attempts)

    def test_runtime_operator_change_after_install_wins_over_restore(self):
        states = iter([(True, 4, 2), (False, 5, 2), (False, 7, 0), (False, 9, 0), (False, 9, 0)])
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
