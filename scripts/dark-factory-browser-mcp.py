#!/usr/bin/env python3
"""Run the operator-installed Playwright MCP inside one factory runtime."""
import argparse
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
from urllib.parse import urlsplit


def owned(path, directory=False):
    path = Path(path)
    info = path.lstat()
    if not path.is_absolute() or path.resolve() != path or info.st_uid != os.getuid() or info.st_mode & 0o022:
        raise ValueError('unsafe browser installation or runtime path')
    if not (stat.S_ISDIR(info.st_mode) if directory else stat.S_ISREG(info.st_mode)):
        raise ValueError('unexpected browser path type')
    return path


def settings(path):
    path = owned(path)
    if path.stat().st_size > 8192:
        raise ValueError('browser configuration too large')
    config = json.loads(path.read_text())
    if set(config) != {'command', 'executable', 'origins', 'headless'}:
        raise ValueError('unexpected browser configuration')
    command = config['command']
    if not isinstance(command, list) or len(command) != 2 or not all(isinstance(x, str) for x in command):
        raise ValueError('command must name Node and the installed MCP entry point')
    for item in command + [config['executable']]:
        owned(item)
    if not os.access(command[0], os.X_OK) or not os.access(config['executable'], os.X_OK):
        raise ValueError('browser executable unavailable')
    if type(config['headless']) is not bool or not isinstance(config['origins'], list) or not 1 <= len(config['origins']) <= 8:
        raise ValueError('browser requires explicit local development origins')
    for origin in config['origins']:
        if not isinstance(origin, str):
            raise ValueError('invalid browser origin')
        url = urlsplit(origin)
        if url.scheme != 'http' or url.hostname != '127.0.0.1' or not url.port or url.port == 43123 or origin != f'http://127.0.0.1:{url.port}':
            raise ValueError('only exact development loopback origins are supported')
    return config


def prepare(config, runtime):
    runtime = owned(runtime, directory=True)
    session = Path(tempfile.mkdtemp(prefix='browser-', dir=runtime))
    output = session / 'output'
    output.mkdir(mode=0o700)
    argv = config['command'] + ['--isolated', '--sandbox', '--block-service-workers',
        '--executable-path', config['executable'], '--allowed-origins', ';'.join(config['origins']),
        '--output-dir', str(output), '--output-max-size', '16777216', '--save-session']
    if config['headless']:
        argv.append('--headless')
    # Browser helpers receive no provider login, operator or attempt credentials.
    env = {'HOME': str(session), 'TMPDIR': str(session), 'PATH': os.defpath, 'LANG': 'C', 'LC_ALL': 'C', 'PWTEST_SOCKETS_DIR': '.'}
    return argv, env, session


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--runtime-dir', required=True)
    args = parser.parse_args()
    config = settings(Path(__file__).resolve().with_suffix('.json'))
    # Authentication is checked by the existing API, never by reading the token.
    subprocess.run([os.environ['DARK_FACTORY_FACTORYCTL'], 'attempt', 'task'],
        check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=15)
    argv, env, session = prepare(config, args.runtime_dir)
    # Relative Unix sockets avoid macOS sun_path limits without leaving the run.
    os.chdir(session)
    os.execve(argv[0], argv, env)


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError):
        print('factory browser unavailable: check the installed configuration and live attempt', file=sys.stderr)
        sys.exit(1)
