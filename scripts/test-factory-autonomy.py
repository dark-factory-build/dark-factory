#!/usr/bin/env python3
import importlib.util
import json
from pathlib import Path
import subprocess
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

    def test_runtime_operator_change_after_pause_never_installs(self):
        states = iter([(True, 4, 0), (False, 6, 0)])
        with patch.object(deploy, 'state', side_effect=lambda _home: next(states)), patch.object(deploy.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, '', '')) as run:
            with self.assertRaisesRegex(ValueError, 'operator changed'):
                deploy.deploy('a' * 40)
        self.assertFalse(any(Path(call.args[0][1]).name == 'reinstall-service.sh' and '--prepare' not in call.args[0] for call in run.call_args_list))

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
