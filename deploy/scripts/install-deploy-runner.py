#!/usr/bin/env python3
"""Install a repository's deployment-only runner; registration token on stdin."""
import argparse
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

TARGETS = {
    'rpc': (Path('/opt/grpctest/actions-runner'), 'actionsrunner',
            'https://github.com/qqawer/grpctest', 'aws-grpctest-deploy', 'grpctest-deploy'),
    'web': (Path('/opt/grpctest/actions-runner-web'), 'actionsrunnerweb',
            'https://github.com/qqawer/grpctest_web', 'aws-grpctest-web-deploy', 'grpctest-web-deploy'),
}
VERSION = '2.337.0'
SHA256 = '70920811a4f8ad4328818682bca5c6469c1c942fab52448868071d0063816613'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('group', nargs='?', choices=TARGETS, default='rpc')
    args = parser.parse_args()
    root, user, repository, name, label = TARGETS[args.group]
    if os.geteuid() != 0:
        raise ValueError('Run as root; pass the short-lived registration token on stdin')
    try:
        account = pwd.getpwnam(user)
        if account.pw_dir != str(root):
            raise ValueError('Existing runner account has a different home; refusing to reuse it')
    except KeyError:
        subprocess.run(['useradd', '--system', '--create-home', '--home-dir', str(root),
                        '--shell', '/usr/sbin/nologin', user], check=True)
        account = pwd.getpwnam(user)
    root.mkdir(parents=True, exist_ok=True)
    root.chmod(0o700)
    os.chown(root, account.pw_uid, account.pw_gid)
    registered = root / '.runner'
    if registered.exists():
        if json.loads(registered.read_text()).get('gitHubUrl', '').rstrip('/') != repository:
            raise ValueError('Existing runner belongs to a different repository')
    else:
        token = sys.stdin.readline().strip()
        if not token:
            raise ValueError('Missing short-lived registration token')
        if not (root / 'config.sh').exists():
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
                    source.extractall(root, filter='data')
            subprocess.run(['chown', '-R', user + ':' + user, str(root)], check=True)
        environment = dict(os.environ, ACTIONS_RUNNER_INPUT_TOKEN=token)
        subprocess.run(['runuser', '-u', user, '--', './config.sh', '--unattended',
                        '--url', repository, '--name', name,
                        '--labels', label, '--work', '_work'],
                       cwd=root, env=environment, check=True)
    if not (root / '.service').exists():
        subprocess.run(['./svc.sh', 'install', user], cwd=root, check=True)
    subprocess.run(['./svc.sh', 'start'], cwd=root, check=True)
    print(f'{args.group} deployment runner installed as an unprivileged service (no sudo or Docker group).')


if __name__ == '__main__':
    main()
