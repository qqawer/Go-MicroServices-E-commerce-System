#!/usr/bin/env python3
"""Tunnel to private Jenkins, enqueue a release and wait for its final result."""
import base64
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

BASE = 'http://127.0.0.1:18080'

def main():
    if len(sys.argv) != 4 or sys.argv[1] not in ('rpc', 'web') or not re.fullmatch(r'[0-9a-f]{40}', sys.argv[2]):
        raise ValueError('Usage: trigger_jenkins.py rpc|web FULL_SHA MANIFEST_DIR')
    required = ('DEPLOY_HOST', 'DEPLOY_USER', 'DEPLOY_SSH_KEY', 'DEPLOY_KNOWN_HOSTS', 'JENKINS_API_TOKEN')
    if any(not os.environ.get(key) for key in required):
        raise ValueError('Missing AWS environment secrets; see deploy/README.md')
    host, user = os.environ['DEPLOY_HOST'], os.environ['DEPLOY_USER']
    if not re.fullmatch(r'[a-zA-Z0-9][a-zA-Z0-9.-]*', host) or not re.fullmatch(r'[a-z_][a-z0-9_-]*', user):
        raise ValueError('Invalid SSH host or user')
    images = {}
    for path in sorted(Path(sys.argv[3]).glob('*.json')):
        part = json.loads(path.read_text())
        if not isinstance(part, dict) or images.keys() & part.keys():
            raise ValueError('Invalid or duplicate image manifests')
        images.update(part)
    if not images:
        raise ValueError('No image manifests')
    raw_auth = os.environ.get('JENKINS_USER', 'actions') + ':' + os.environ['JENKINS_API_TOKEN']
    auth = 'Basic ' + base64.b64encode(raw_auth.encode()).decode()
    # Do not follow redirects: never forward the API token to another origin.
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, req, fp, code, msg, headers, newurl):
            return None
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())

    def request(path, data=None):
        req = urllib.request.Request(BASE + path, data=data, headers={'Authorization': auth})
        with opener.open(req, timeout=15) as response:
            return response.headers, response.read()

    def get(path):
        return json.loads(request(path)[1])

    with tempfile.TemporaryDirectory(prefix='jenkins-ssh-') as tmp:
        key = Path(tmp) / 'key'
        known = Path(tmp) / 'known_hosts'
        key.write_text(os.environ['DEPLOY_SSH_KEY'].strip() + '\n')
        key.chmod(0o600)
        known.write_text(os.environ['DEPLOY_KNOWN_HOSTS'].strip() + '\n')
        args = ['ssh', '-N', '-i', str(key), '-o', 'BatchMode=yes', '-o', 'IdentitiesOnly=yes',
                '-o', 'StrictHostKeyChecking=yes', '-o', f'UserKnownHostsFile={known}',
                '-o', 'ExitOnForwardFailure=yes', '-o', 'ConnectTimeout=15',
                '-o', 'ServerAliveInterval=15', '-o', 'ServerAliveCountMax=3',
                '-L', '127.0.0.1:18080:127.0.0.1:8080', f'{user}@{host}']
        tunnel = subprocess.Popen(args)
        try:
            for attempt in range(30):
                if tunnel.poll() is not None:
                    raise RuntimeError('SSH tunnel exited; check host key, security group and SSH credentials')
                try:
                    get('/api/json')
                    break
                except urllib.error.HTTPError:
                    raise
                except (urllib.error.URLError, TimeoutError):
                    time.sleep(1)
            else:
                raise RuntimeError('Jenkins was not reachable through the tunnel')
            data = urllib.parse.urlencode({'GROUP': sys.argv[1], 'REVISION': sys.argv[2],
                                           'IMAGES_JSON': json.dumps(images)}).encode()
            headers, _ = request('/job/grpctest-deploy/buildWithParameters', data)
            queue = urllib.parse.urlsplit(headers.get('Location', '')).path
            if not re.fullmatch(r'/queue/item/\d+/', queue):
                raise RuntimeError('Jenkins did not return a valid queue item')
            deadline = time.monotonic() + 1200
            build = None
            while time.monotonic() < deadline:
                if tunnel.poll() is not None:
                    raise RuntimeError('SSH connection lost; inspect Jenkins before retrying')
                if build is None:
                    queued = get(queue + 'api/json')
                    if queued.get('cancelled'):
                        raise RuntimeError('Jenkins queue item was cancelled')
                    if queued.get('executable'):
                        number = queued['executable']['number']
                        if not isinstance(number, int) or number < 1:
                            raise RuntimeError('Invalid Jenkins build number')
                        build = f'/job/grpctest-deploy/{number}/'
                        print(f'Jenkins deployment #{number} started.', flush=True)
                else:
                    result = get(build + 'api/json')
                    if not result.get('building') and result.get('result'):
                        if result['result'] != 'SUCCESS':
                            raise RuntimeError('Jenkins deployment result: ' + result['result'])
                        print('Jenkins deployment completed successfully.')
                        return
                time.sleep(5)
            raise RuntimeError('Timed out waiting for Jenkins; inspect the job before retrying')
        finally:
            tunnel.terminate()
            try:
                tunnel.wait(timeout=10)
            except subprocess.TimeoutExpired:
                tunnel.kill()
                tunnel.wait()

if __name__ == '__main__':
    try:
        main()
    except (ValueError, KeyError, OSError, RuntimeError) as exc:
        print(f'Deployment trigger failed: {exc}', file=sys.stderr)
        sys.exit(1)
