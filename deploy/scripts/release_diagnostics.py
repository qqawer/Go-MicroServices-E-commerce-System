"""Save bounded, private evidence before rollback removes failed containers."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time


def save_failure_diagnostics(compose, services, state, group, revision):
    directory = Path(tempfile.mkdtemp(prefix=f'failed-{group}-{revision[:12]}-', dir=state))
    deadline = time.monotonic() + 20

    def write(name, text):
        with open(directory / name, 'w', opener=lambda path, flags: os.open(path, flags, 0o600)) as f:
            f.write(text)

    def capture(name, args):
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            write(name, 'Diagnostic time budget exhausted.\n')
            return ''
        try:
            result = subprocess.run(args, capture_output=True, text=True, errors='replace',
                                    timeout=min(3, remaining), check=True)
            output = result.stdout + result.stderr
            write(name, output[-262144:])
            return result.stdout
        except (OSError, subprocess.SubprocessError) as error:
            # Neither command output nor private log contents go to Jenkins.
            write(name, f'Capture failed: {type(error).__name__}\n')
            return ''

    write('release.json', json.dumps({'group': group, 'revision': revision, 'services': services}, indent=2) + '\n')
    for name in ('meminfo', 'vmstat'):
        try:
            write(name + '.txt', Path('/proc', name).read_text())
        except OSError:
            write(name + '.txt', 'Host counters unavailable.\n')
    ids = capture('containers.txt', compose + ['ps', '-aq', *services]).split()
    for index, container in enumerate(ids):
        # Explicit fields exclude Config.Env, mounted configuration and credentials.
        fields = '{{json .Id}} {{json .Name}} {{json .Image}} {{.RestartCount}} {{json .State}}'
        capture(f'{index}-state.txt', ['docker', 'inspect', '--format', fields, container])
        capture(f'{index}-logs.txt', ['docker', 'logs', '--timestamps', '--tail', '200', container])
    capture('stats.txt', ['docker', 'stats', '--no-stream', '--format', '{{json .}}'])
    return directory
