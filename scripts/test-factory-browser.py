import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import subprocess

spec = importlib.util.spec_from_file_location('factory_browser', Path(__file__).with_name('dark-factory-browser-mcp.py'))
browser = importlib.util.module_from_spec(spec)
spec.loader.exec_module(browser)


class BrowserTest(unittest.TestCase):
    def test_failed_attempt_authentication_never_starts_browser(self):
        with tempfile.TemporaryDirectory() as temporary:
            runtime = Path(temporary).resolve()
            with patch.object(browser.sys, 'argv', ['bridge', '--runtime-dir', str(runtime)]), \
                 patch.object(browser, 'settings', return_value={}), \
                 patch.dict(os.environ, {'DARK_FACTORY_FACTORYCTL': '/factoryctl'}), \
                 patch.object(browser.subprocess, 'run', side_effect=subprocess.CalledProcessError(1, 'factoryctl')), \
                 patch.object(browser.os, 'execve') as execute:
                with self.assertRaises(subprocess.CalledProcessError):
                    browser.main()
                execute.assert_not_called()
                self.assertEqual(list(runtime.iterdir()), [])

    def test_sessions_are_separate_and_do_not_inherit_credentials(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            executable = root / 'tool'
            executable.write_text('#!/bin/sh\nexit 0\n')
            executable.chmod(0o700)
            config = dict(command=[str(executable), str(executable)], executable=str(executable),
                          origins=['http://127.0.0.1:5192'], headless=True)
            path = root / 'config.json'
            path.write_text(json.dumps(config))
            loaded = browser.settings(path)
            first, env, workspace = browser.prepare(loaded, root)
            second, other, _ = browser.prepare(loaded, root)
            self.assertNotEqual(env['HOME'], other['HOME'])
            self.assertEqual(workspace, Path(env['HOME']))
            self.assertEqual(set(env), {'HOME', 'TMPDIR', 'PATH', 'LANG', 'LC_ALL', 'PWTEST_SOCKETS_DIR'})
            self.assertIn('--isolated', first)
            self.assertIn('--sandbox', first)
            self.assertIn('--save-session', first)
            self.assertNotIn('--cdp-endpoint', first)
            self.assertNotIn('--storage-state', first)
            self.assertTrue(Path(first[first.index('--output-dir') + 1]).is_dir())
            self.assertNotEqual(first, second)
            for origin in ['http://127.0.0.1:43123', 'https://app.darkfactory.build',
                           'http://localhost:5192', 'http://127.0.0.1:5192/path',
                           'http://127.0.0.1:5192?x=1', 'http://user@127.0.0.1:5192', '*']:
                path.write_text(json.dumps(dict(config, origins=[origin])))
                with self.subTest(origin=origin), self.assertRaises(ValueError):
                    browser.settings(path)
            path.write_text(json.dumps(config))
            path.chmod(0o666)
            with self.assertRaises(ValueError):
                browser.settings(path)
            link = root / 'link'
            link.symlink_to(root, target_is_directory=True)
            with self.assertRaises(ValueError):
                browser.prepare(config, link)
            with self.assertRaises(FileNotFoundError):
                browser.settings(root / 'missing')


if __name__ == '__main__':
    unittest.main()
