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
        sources = ['a' * 40, 'b' * 40, 'a' * 40]
        def observe(argv, **kwargs):
            return subprocess.CompletedProcess(argv, 0, json.dumps({'source': sources.pop(0), 'release': True}), '')
        with patch.object(runtime.subprocess, 'run', side_effect=observe):
            with self.assertRaisesRegex(ValueError, 'different build receipts'):
                runtime.observe(Path('/private/tmp/factory'))


class DeployShimTest(unittest.TestCase):
    def test_shim_reports_factoryctl_release_status(self):
        for status, healthy in ((0, True), (75, False), (1, False)):
            with tempfile.TemporaryDirectory() as directory:
                home = Path(directory) / 'factory'
                factoryctl = Path(str(home) + '.service') / 'bin' / 'current' / 'factoryctl'
                factoryctl.parent.mkdir(parents=True)
                factoryctl.write_text('#!/bin/sh\necho "$* $DARK_FACTORY_SOCKET"\nexit ' + str(status) + '\n')
                factoryctl.chmod(0o755)
                result = subprocess.run([sys.executable, str(Path(__file__).with_name('deploy-runtime.py')), '--home', str(home), 'a' * 40],
                                        capture_output=True, text=True, timeout=15)
                self.assertEqual(status, result.returncode)
                self.assertEqual({'sha': 'a' * 40, 'healthy': healthy}, json.loads(result.stdout))
                self.assertIn('release ' + 'a' * 40 + ' --wait ' + str(home / 'runtimes' / 'factory.sock'), result.stderr)


if __name__ == '__main__':
    unittest.main()
