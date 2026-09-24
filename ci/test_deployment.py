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
from types import SimpleNamespace
from unittest.mock import patch
from urllib.parse import parse_qs

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('trigger', ROOT / 'ci/trigger_jenkins.py')
trigger = importlib.util.module_from_spec(spec)
spec.loader.exec_module(trigger)
runner_spec = importlib.util.spec_from_file_location('install_runner', ROOT / 'deploy/scripts/install-deploy-runner.py')
install_runner = importlib.util.module_from_spec(runner_spec)
runner_spec.loader.exec_module(install_runner)


class DeploymentTests(unittest.TestCase):
    def test_deployment_runner_registration_keeps_repositories_isolated(self):
        for arguments, group, user, repository, name, label in (
                ([], 'rpc', 'actionsrunner', 'https://github.com/qqawer/grpctest', 'aws-grpctest-deploy', 'grpctest-deploy'),
                (['web'], 'web', 'actionsrunnerweb', 'https://github.com/qqawer/grpctest_web', 'aws-grpctest-web-deploy', 'grpctest-web-deploy')):
            with self.subTest(group=group), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                (root / 'config.sh').touch()  # A verified runner download is already present.
                target = install_runner.TARGETS[group]
                account = SimpleNamespace(pw_dir=str(root), pw_uid=1001, pw_gid=1001)
                output = io.StringIO()
                with patch.dict(install_runner.TARGETS, {group: (root, *target[1:])}), \
                        patch.object(sys, 'argv', ['install-runner', *arguments]), \
                        patch.object(sys, 'stdin', io.StringIO('registration-test-only\n')), \
                        patch.object(install_runner.os, 'geteuid', return_value=0), \
                        patch.object(install_runner.os, 'chown'), \
                        patch.object(install_runner.pwd, 'getpwnam', return_value=account) as get_account, \
                        patch.object(install_runner.subprocess, 'run') as run, \
                        contextlib.redirect_stdout(output):
                    install_runner.main()
                get_account.assert_called_once_with(user)
                self.assertEqual(run.call_args_list[0].args[0],
                                 ['runuser', '-u', user, '--', './config.sh', '--unattended',
                                  '--url', repository, '--name', name, '--labels', label, '--work', '_work'])
                self.assertEqual(run.call_args_list[0].kwargs['cwd'], root)
                self.assertEqual(run.call_args_list[0].kwargs['env']['ACTIONS_RUNNER_INPUT_TOKEN'], 'registration-test-only')
                self.assertEqual(run.call_args_list[1].args[0], ['./svc.sh', 'install', user])
                self.assertEqual(run.call_args_list[2].args[0], ['./svc.sh', 'start'])
                self.assertNotIn('registration-test-only', output.getvalue())
                for call in run.call_args_list:
                    self.assertNotIn('registration-test-only', ' '.join(call.args[0]))

    def test_deployment_runner_refuses_existing_registration_for_another_repository(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / '.runner').write_text(json.dumps({'gitHubUrl': 'https://github.com/qqawer/grpctest'}))
            target = install_runner.TARGETS['web']
            account = SimpleNamespace(pw_dir=str(root), pw_uid=1001, pw_gid=1001)
            with patch.dict(install_runner.TARGETS, {'web': (root, *target[1:])}), \
                    patch.object(sys, 'argv', ['install-runner', 'web']), \
                    patch.object(install_runner.os, 'geteuid', return_value=0), \
                    patch.object(install_runner.os, 'chown'), \
                    patch.object(install_runner.pwd, 'getpwnam', return_value=account), \
                    patch.object(install_runner.subprocess, 'run') as run:
                with self.assertRaisesRegex(ValueError, 'different repository'):
                    install_runner.main()
                run.assert_not_called()

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
