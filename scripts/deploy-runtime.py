#!/usr/bin/env python3
"""Release an exact merged commit through factoryd (`factoryctl release SHA --wait`)."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--home', type=Path, default=Path.home() / '.dark-factory')
parser.add_argument('sha')
args = parser.parse_args()
env = dict(os.environ, DARK_FACTORY_SOCKET=str(args.home / 'runtimes' / 'factory.sock'), DARK_FACTORY_OPERATOR_TOKEN_FILE=str(args.home / 'operator.token'))
factoryctl = Path(str(args.home) + '.service') / 'bin' / 'current' / 'factoryctl'
result = subprocess.run([str(factoryctl), 'release', args.sha, '--wait'], env=env, capture_output=True, text=True)
sys.stderr.write(result.stderr + result.stdout)
print(json.dumps({'sha': args.sha, 'healthy': result.returncode == 0}))
# factoryctl's own status: 75 is "refused, nothing swapped", which the release
# controller retries; any other failure keeps its deployment barrier.
sys.exit(result.returncode)
