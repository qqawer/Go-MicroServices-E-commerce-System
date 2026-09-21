#!/usr/bin/env python3
"""Use a workflow-scoped registry token only for this Jenkins release."""
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile


def main():
    environment = dict(os.environ)
    token = environment.pop('GHCR_TOKEN', '').strip()
    user = environment.pop('GHCR_USER', '').strip()
    command = [sys.executable, str(Path(__file__).with_name('release.py')), *sys.argv[1:]]
    if not token:
        # Preserve manual/preloaded releases and an existing registry login.
        return subprocess.run(command, env=environment).returncode
    if not re.fullmatch(r'[a-zA-Z0-9][a-zA-Z0-9-]*', user):
        raise ValueError('Invalid registry username')
    with tempfile.TemporaryDirectory(prefix='grpctest-registry-') as directory:
        environment['DOCKER_CONFIG'] = directory
        result = subprocess.run(['docker', 'login', 'ghcr.io', '--username', user, '--password-stdin'],
                                input=token + '\n', env=environment, text=True, capture_output=True)
        if result.returncode:
            raise ValueError('Cannot authenticate to GHCR with this workflow token')
        # The release and Docker subprocesses receive only the temporary config,
        # not the token environment variable; the config is deleted on exit.
        return subprocess.run(command, env=environment).returncode


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (ValueError, OSError) as error:
        print(f'Registry release failed: {error}', file=sys.stderr)
        sys.exit(1)
