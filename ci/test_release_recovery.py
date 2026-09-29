"""Keep failed-release evidence before rollback and respect RPC startup dependencies."""
import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest
from types import SimpleNamespace
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[1] / 'deploy/scripts'
sys.path.insert(0, str(SCRIPTS))
spec = importlib.util.spec_from_file_location('release_recovery_test', SCRIPTS / 'release.py')
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)
import release_diagnostics


class ReleaseRecoveryTests(unittest.TestCase):
    def exercise(self, fail_wave=None, diagnostic_error=False, existing=True):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            state = root / 'runtime/state'
            state.mkdir(parents=True)
            (root / '.env').write_text(f'GHCR_OWNER=qqawer\nRUNTIME_DIR={root}/runtime\n')
            migration = root / 'mysql/migrations/0001.sql'
            migration.parent.mkdir(parents=True)
            migration.write_text('-- fixture')
            services = release.GROUPS['rpc'][1]
            images = {s: 'ghcr.io/qqawer/grpctest-' + s.replace('-', '_') + '@sha256:' + 'b'*64 for s in services}
            previous = ''.join(f"{s.upper().replace('-', '_')}_IMAGE={v.replace('b'*64, 'a'*64)}\n" for s, v in images.items()) if existing else ''
            (state / 'images.env').write_text(previous)
            for s in services:
                p = root / 'runtime/config' / s.replace('-', '_')
                p.mkdir(parents=True)
                (p / 'config-debug.yaml').touch()
            events, wave = [], 0
            error = subprocess.CalledProcessError(1, ['docker', 'compose', 'up'])
            def run(args, capture=False):
                nonlocal wave
                out = ''
                if 'up' in args:
                    wave += 1
                    events.append(('up', [s for s in services if s in args]))
                    if wave == fail_wave:
                        raise error
                elif 'rm' in args:
                    events.append(('rm', [s for s in services if s in args]))
                elif 'ps' in args:
                    out = 'container-id' if existing or 'compose.yaml' in ' '.join(args) else ''
                elif 'inspect' in args:
                    out = 'healthy'
                elif any('SELECT DISTINCT' in a for a in args):
                    out = ' '.join('grpctest_' + s + '_srv' for s in ('user', 'goods', 'inventory', 'orders'))
                elif any('schema_migrations ORDER' in a for a in args):
                    out = f'1\t0001.sql\t{hashlib.sha256(migration.read_bytes()).hexdigest()}\t0'
                return SimpleNamespace(stdout=out)
            def diagnostics(*args):
                events.append(('diagnostics', []))
                if diagnostic_error:
                    raise OSError('fixture disk unavailable')
            output = io.StringIO()
            old_umask = os.umask(0o077)
            try:
                with patch.object(release, 'ROOT', root), patch.object(release, 'run', run), \
                        patch.object(release, 'check_disk'), patch.object(release, 'check_mq_write'), \
                        patch.object(release, 'prune'), patch.object(release, 'save_failure_diagnostics', diagnostics, create=True), \
                        patch.dict(os.environ, {'IMAGES_JSON': json.dumps(images)}), \
                        patch.object(sys, 'argv', ['release', 'rpc', 'c'*40]), \
                        contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
                    if fail_wave:
                        with self.assertRaises(subprocess.CalledProcessError) as caught:
                            release.main()
                        self.assertIs(caught.exception, error)
                    else:
                        release.main()
                unchanged = (state / 'images.env').read_text() == previous
                self.assertEqual(list(state.glob('release-*.env')), [])
                return events, unchanged
            finally:
                os.umask(old_umask)

    def test_order_starts_only_after_required_rpc_wave_is_healthy(self):
        events, unchanged = self.exercise()
        self.assertEqual(events, [('up', ['user-srv', 'goods-srv', 'inventory-srv']), ('up', ['order-srv'])])
        self.assertFalse(unchanged)

    def test_failure_is_captured_before_dependency_ordered_rollback(self):
        for wave in (1, 2):
            with self.subTest(wave=wave):
                events, unchanged = self.exercise(fail_wave=wave)
                before = [('up', ['user-srv', 'goods-srv', 'inventory-srv']), ('up', ['order-srv'])][:wave]
                self.assertEqual(events, before + [('diagnostics', []), ('up', ['user-srv', 'goods-srv', 'inventory-srv']), ('up', ['order-srv'])])
                self.assertTrue(unchanged)

    def test_diagnostic_failure_does_not_prevent_rollback(self):
        events, unchanged = self.exercise(fail_wave=1, diagnostic_error=True)
        self.assertEqual(events[-2:], [('up', ['user-srv', 'goods-srv', 'inventory-srv']), ('up', ['order-srv'])])
        self.assertTrue(unchanged)

    def test_first_deployment_captures_before_removing_new_containers(self):
        events, unchanged = self.exercise(fail_wave=1, existing=False)
        self.assertEqual(events[-2:], [('diagnostics', []), ('rm', release.GROUPS['rpc'][1])])
        self.assertTrue(unchanged)


class DiagnosticTests(unittest.TestCase):
    def test_private_bounded_snapshot_excludes_environment_and_keeps_probe_results(self):
        with tempfile.TemporaryDirectory() as folder:
            calls = []
            def run(args, **kwargs):
                calls.append(args)
                self.assertLessEqual(kwargs['timeout'], 3)
                out = 'fixture-id\n' if 'ps' in args else ''
                if 'inspect' in args:
                    self.assertIn('--format', args)
                    self.assertNotIn('.Config', args[args.index('--format') + 1])
                    out = '{"OOMKilled":false,"ExitCode":1,"Health":{"Status":"unhealthy"}}'
                if 'logs' in args:
                    self.assertEqual(args[-3:], ['--tail', '200', 'fixture-id'])
                    out = 'x'*300000 + '\nfixture startup error'
                return SimpleNamespace(stdout=out, stderr='')
            output = io.StringIO()
            with patch.object(release_diagnostics.subprocess, 'run', run), contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
                directory = release_diagnostics.save_failure_diagnostics(['docker', 'compose'], ['order-srv'], Path(folder), 'rpc', 'a'*40)
            self.assertEqual(stat.S_IMODE(directory.stat().st_mode), 0o700)
            for path in directory.iterdir():
                self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
            self.assertIn('"OOMKilled":false', (directory / '0-state.txt').read_text())
            self.assertLessEqual(len((directory / '0-logs.txt').read_text()), 262144)
            self.assertIn('fixture startup error', (directory / '0-logs.txt').read_text())
            self.assertEqual(output.getvalue(), '')

    def test_failed_command_and_exhausted_budget_leave_partial_evidence(self):
        with tempfile.TemporaryDirectory() as folder:
            def run(args, **kwargs):
                if 'ps' in args:
                    return SimpleNamespace(stdout='fixture-id', stderr='')
                raise subprocess.TimeoutExpired(args, kwargs['timeout'], output='private fixture')
            with patch.object(release_diagnostics.subprocess, 'run', run), \
                    patch.object(release_diagnostics.time, 'monotonic', side_effect=[0, 1, 2, 21, 22]):
                directory = release_diagnostics.save_failure_diagnostics(['docker', 'compose'], ['order-srv'], Path(folder), 'rpc', 'a'*40)
            self.assertIn('TimeoutExpired', (directory / '0-state.txt').read_text())
            self.assertIn('budget exhausted', (directory / '0-logs.txt').read_text())
            self.assertNotIn('private fixture', ''.join(p.read_text() for p in directory.iterdir()))


if __name__ == '__main__':
    unittest.main()
