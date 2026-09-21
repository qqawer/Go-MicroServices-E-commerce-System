"""Exercise the Jenkins HTTP boundary and disposable Docker credentials locally."""
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest.mock import patch
from urllib.parse import parse_qs

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('trigger', ROOT / 'ci/trigger_jenkins.py')
trigger = importlib.util.module_from_spec(spec)
spec.loader.exec_module(trigger)


class DeploymentTests(unittest.TestCase):
    def test_jenkins_success_and_failure_are_propagated(self):
        for result in ('SUCCESS', 'FAILURE'):
            with self.subTest(result=result), tempfile.TemporaryDirectory() as directory:
                received = {}
                class Jenkins(BaseHTTPRequestHandler):
                    def log_message(self, *args): pass
                    def do_GET(self):
                        body = ({'executable': {'number': 4}} if self.path.startswith('/queue/') else
                                {'building': False, 'result': result} if self.path.startswith('/job/') else {})
                        self.send_response(200)
                        self.end_headers()
                        self.wfile.write(json.dumps(body).encode())
                    def do_POST(self):
                        received.update(parse_qs(self.rfile.read(int(self.headers['Content-Length'])).decode()))
                        received['authorization'] = self.headers.get('Authorization')
                        self.send_response(201)
                        self.send_header('Location', '/queue/item/7/')
                        self.end_headers()
                server = ThreadingHTTPServer(('127.0.0.1', 0), Jenkins)
                thread = threading.Thread(target=server.serve_forever, daemon=True)
                thread.start()
                try:
                    Path(directory, 'goods.json').write_text(json.dumps({'goods-srv': 'fixture-digest'}))
                    environment = {'JENKINS_URL': f'http://127.0.0.1:{server.server_port}',
                                   'JENKINS_API_TOKEN': 'jenkins-test-only', 'GHCR_USER': 'qqawer',
                                   'GHCR_TOKEN': 'workflow-test-only'}
                    output = io.StringIO()
                    with patch.dict(os.environ, environment, clear=True), patch.object(sys, 'argv', ['trigger', 'rpc', 'a'*40, directory]), \
                            patch.object(trigger.time, 'sleep', lambda _: None), contextlib.redirect_stdout(output):
                        if result == 'SUCCESS': trigger.main()
                        else:
                            with self.assertRaisesRegex(RuntimeError, 'FAILURE'): trigger.main()
                    self.assertEqual(received['GHCR_TOKEN'], ['workflow-test-only'])
                    self.assertEqual(received['PRELOADED'], ['false'])
                    self.assertEqual(received['REVISION'], ['a'*40])
                    self.assertTrue(received['authorization'].startswith('Basic '))
                    self.assertNotIn('workflow-test-only', output.getvalue())
                    self.assertNotIn('jenkins-test-only', output.getvalue())
                finally:
                    server.shutdown()
                    server.server_close()
                    thread.join()

    def test_direct_mode_rejects_nonlocal_origins(self):
        for url in ('http://example.invalid:8080', 'https://127.0.0.1:8080', 'http://user@127.0.0.1:8080', 'http://127.0.0.1:8080/path'):
            with self.subTest(url=url), patch.dict(os.environ, {'JENKINS_URL': url}, clear=True):
                with self.assertRaises(ValueError), trigger.jenkins_connection(): pass

    def test_registry_credentials_are_temporary_and_not_in_child_environment(self):
        for login_status in (0, 1):
            with self.subTest(login_status=login_status), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                shutil.copy(ROOT / 'deploy/scripts/release-with-registry.py', root)
                # Real subprocess boundaries with disposable Docker/release fixtures.
                (root / 'docker').write_text(f'''#!{sys.executable}
import os,sys
from pathlib import Path
assert sys.argv[1:] == ['login','ghcr.io','--username','qqawer','--password-stdin']
assert sys.stdin.read().strip() == 'workflow-test-only'
assert 'GHCR_TOKEN' not in os.environ
directory=Path(os.environ['DOCKER_CONFIG'])
Path(os.environ['TEST_CONFIG_RECORD']).write_text(str(directory))
(directory/'config.json').write_text('test-only credential')
sys.exit({login_status})
''')
                (root / 'docker').chmod(0o755)
                (root / 'release.py').write_text('''import os
from pathlib import Path
assert 'GHCR_TOKEN' not in os.environ
assert Path(os.environ['DOCKER_CONFIG'],'config.json').is_file()
print('fixture release finished')
''')
                environment = dict(os.environ, PATH=str(root)+os.pathsep+os.environ['PATH'], GHCR_TOKEN='workflow-test-only',
                                   GHCR_USER='qqawer', TEST_CONFIG_RECORD=str(root / 'record'))
                result = subprocess.run([sys.executable, str(root / 'release-with-registry.py'), 'rpc', 'a'*40],
                                        env=environment, capture_output=True, text=True)
                self.assertEqual(result.returncode, login_status)
                self.assertFalse(Path((root / 'record').read_text()).exists())
                self.assertNotIn('workflow-test-only', result.stdout + result.stderr)


if __name__ == '__main__':
    unittest.main()
