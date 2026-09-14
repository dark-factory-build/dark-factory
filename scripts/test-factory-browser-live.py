#!/usr/bin/env python3
"""Exercise two isolated browsers against the actual labelled dev fixture."""
import argparse
import importlib.util
import json
from pathlib import Path
import shutil
import subprocess
import tempfile

spec = importlib.util.spec_from_file_location('factory_browser', Path(__file__).with_name('dark-factory-browser-mcp.py'))
browser = importlib.util.module_from_spec(spec)
spec.loader.exec_module(browser)


def descendants(pid):
    rows = []
    for line in subprocess.check_output(['ps', '-axo', 'pid=,ppid=,comm='], text=True).splitlines():
        parts = line.strip().split(None, 2)
        if len(parts) == 3:
            rows.append((int(parts[0]), int(parts[1]), parts[2]))
    family = {pid}
    for _ in range(16):
        family.update(child for child, parent, _ in rows if parent in family)
    return {child: command for child, _, command in rows if child in family and child != pid}


def call(process, method, params):
    process.stdin.write(json.dumps({'jsonrpc': '2.0', 'id': 1, 'method': method, 'params': params}) + '\n')
    process.stdin.flush()
    while True:
        line = process.stdout.readline()
        if not line:
            raise RuntimeError('browser server exited before response')
        result = json.loads(line)
        if result.get('id') == 1:
            assert 'error' not in result, result
            return result['result']


def tool(process, name, arguments):
    return call(process, 'tools/call', {'name': name, 'arguments': arguments})


def text(result):
    return '\n'.join(part.get('text', '') for part in result.get('content', []))


parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('config')
parser.add_argument('url')
parser.add_argument('output')
args = parser.parse_args()
config = browser.settings(Path(args.config).resolve())
assert args.url.startswith(tuple(origin + '/' for origin in config['origins'])) and '?fixture' in args.url
output = Path(args.output).resolve()
output.mkdir(parents=True, exist_ok=True)
processes = []
children = {}
with tempfile.TemporaryDirectory(prefix='factory-browser-proof-') as temporary:
    runtime = Path(temporary).resolve()
    try:
        directories = []
        for _ in range(2):
            argv, env, workspace = browser.prepare(config, runtime)
            directories.append(Path(argv[argv.index('--output-dir') + 1]))
            process = subprocess.Popen(argv, cwd=workspace, env=env, stdin=subprocess.PIPE,
                                       stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
            processes.append(process)
            call(process, 'initialize', {'protocolVersion': '2024-11-05', 'capabilities': {},
                                        'clientInfo': {'name': 'factory-browser-proof', 'version': '1'}})
            call(process, 'tools/list', {})
            assert not descendants(process.pid), 'browser helpers started before a browser action'
            assert not list(directories[-1].glob('*.png'))
            result = tool(process, 'browser_navigate', {'url': args.url})
            assert not result.get('isError'), text(result)
            assert 'fixture' in text(result).lower(), 'actual labelled fixture was not rendered'
        for process in processes:
            children.update(descendants(process.pid))
        assert children, 'real browser processes were not observed'
        first, second = processes
        marker = 'factory_browser_isolation_probe'
        tool(first, 'browser_evaluate', {'function': f'() => localStorage.setItem("{marker}", "first")'})
        result = tool(second, 'browser_evaluate', {'function': f'() => localStorage.getItem("{marker}")'})
        assert 'null' in text(result) and '"first"' not in text(result), 'browser storage leaked across runs'
        rejected = tool(second, 'browser_navigate', {'url': 'http://127.0.0.1:43123/'})
        assert rejected.get('isError') or 'not allowed' in text(rejected).lower(), 'direct unlisted navigation was not refused'
        for width, height, name in [(1280, 720, 'desktop.png'), (390, 844, 'phone.png')]:
            tool(first, 'browser_resize', {'width': width, 'height': height})
            result = tool(first, 'browser_take_screenshot', {'filename': str(directories[0] / name), 'scale': 'css'})
            assert not result.get('isError'), text(result)
            shutil.copyfile(directories[0] / name, output / name)
        tool(first, 'browser_close', {})
        assert not tool(second, 'browser_navigate', {'url': args.url}).get('isError'), 'closing one run affected another'
        (output / 'proof.json').write_text(json.dumps({'fixture': args.url, 'sessions': 2,
            'storage_isolated': True, 'direct_unlisted_origin_refused': True,
            'independent_close': True, 'viewports': [[1280, 720], [390, 844]]}, indent=2))
    finally:
        for process in processes:
            process.stdin.close()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.terminate()
                process.wait(timeout=10)
alive = subprocess.check_output(['ps', '-axo', 'pid=,comm='], text=True)
for line in alive.splitlines():
    parts = line.strip().split(None, 1)
    if len(parts) == 2:
        assert children.get(int(parts[0])) != parts[1], 'browser descendant survived server shutdown'
proof_path = output / 'proof.json'
proof = json.loads(proof_path.read_text())
proof.update(lazy_start=True, descendants_released=True)
proof_path.write_text(json.dumps(proof, indent=2))
print('factory browser live fixture proof passed')
