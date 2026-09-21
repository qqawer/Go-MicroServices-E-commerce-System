#!/usr/bin/env python3
"""Install the RPC repository's deployment-only runner; registration token on stdin."""
import hashlib
import json
import os
from pathlib import Path
import pwd
import subprocess
import sys
import tarfile
import tempfile
import urllib.request

ROOT = Path('/opt/grpctest/actions-runner')
USER = 'actionsrunner'
REPOSITORY = 'https://github.com/qqawer/grpctest'
VERSION = '2.337.0'
SHA256 = '70920811a4f8ad4328818682bca5c6469c1c942fab52448868071d0063816613'


def main():
    if os.geteuid() != 0:
        raise ValueError('Run as root; pass the short-lived registration token on stdin')
    try:
        account = pwd.getpwnam(USER)
        if account.pw_dir != str(ROOT):
            raise ValueError('Existing runner account has a different home; refusing to reuse it')
    except KeyError:
        subprocess.run(['useradd', '--system', '--create-home', '--home-dir', str(ROOT),
                        '--shell', '/usr/sbin/nologin', USER], check=True)
        account = pwd.getpwnam(USER)
    ROOT.mkdir(parents=True, exist_ok=True)
    ROOT.chmod(0o700)
    os.chown(ROOT, account.pw_uid, account.pw_gid)
    registered = ROOT / '.runner'
    if registered.exists():
        if json.loads(registered.read_text()).get('gitHubUrl', '').rstrip('/') != REPOSITORY:
            raise ValueError('Existing runner belongs to a different repository')
    else:
        token = sys.stdin.readline().strip()
        if not token:
            raise ValueError('Missing short-lived registration token')
        if not (ROOT / 'config.sh').exists():
            url = f'https://github.com/actions/runner/releases/download/v{VERSION}/actions-runner-linux-x64-{VERSION}.tar.gz'
            with tempfile.TemporaryDirectory(prefix='grpctest-runner-') as directory:
                archive = Path(directory) / 'runner.tar.gz'
                digest = hashlib.sha256()
                with urllib.request.urlopen(url, timeout=60) as response, archive.open('wb') as output:
                    while chunk := response.read(1024 * 1024):
                        digest.update(chunk)
                        output.write(chunk)
                if digest.hexdigest() != SHA256:
                    raise ValueError('Runner archive checksum mismatch')
                with tarfile.open(archive) as source:
                    source.extractall(ROOT, filter='data')
            subprocess.run(['chown', '-R', USER + ':' + USER, str(ROOT)], check=True)
        environment = dict(os.environ, ACTIONS_RUNNER_INPUT_TOKEN=token)
        subprocess.run(['runuser', '-u', USER, '--', './config.sh', '--unattended',
                        '--url', REPOSITORY, '--name', 'aws-grpctest-deploy',
                        '--labels', 'grpctest-deploy', '--work', '_work'],
                       cwd=ROOT, env=environment, check=True)
    if not (ROOT / '.service').exists():
        subprocess.run(['./svc.sh', 'install', USER], cwd=ROOT, check=True)
    subprocess.run(['./svc.sh', 'start'], cwd=ROOT, check=True)
    print('Deployment runner installed as an unprivileged service (no sudo or Docker group).')


if __name__ == '__main__':
    main()
