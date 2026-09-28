"""Deployment gates must reject a healthy-but-unwritable broker before rollout."""
import datetime
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
import xml.etree.ElementTree as ET
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[1] / 'deploy/scripts'
sys.path.insert(0, str(SCRIPTS))
spec = importlib.util.spec_from_file_location('release_capacity_test', SCRIPTS / 'release.py')
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class ReleaseGateTests(unittest.TestCase):
    def exercise(self, disk_before=70, disk_after=70, send='SEND_OK'):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            runtime = root / 'runtime'
            state = runtime / 'state'
            state.mkdir(parents=True)
            (root / '.env').write_text(f'GHCR_OWNER=qqawer\nRUNTIME_DIR={runtime}\n')
            images = {s: f'ghcr.io/qqawer/grpctest_web-{s}@sha256:' + 'b'*64
                      for s in ('user-web', 'goods-web', 'order-web')}
            for service in images:
                config = runtime / 'config' / service
                config.mkdir(parents=True)
                (config / 'config-debug.yaml').touch()
            previous = {s.upper().replace('-', '_') + '_IMAGE': image.replace('b'*64, 'a'*64)
                        for s, image in images.items()}
            original = ''.join(f'{k}={v}\n' for k, v in previous.items())
            (state / 'images.env').write_text(original)
            calls = []
            pulled = False
            def run(args, capture=False):
                nonlocal pulled
                calls.append(args)
                out = ''
                if 'pull' in args: pulled = True
                if 'ps' in args: out = 'container-id\n'
                elif '--format' in args and 'inspect' in args: out = 'healthy\n'
                elif any('SELECT DISTINCT' in a for a in args):
                    out = '\n'.join('grpctest_' + s + '_srv' for s in ('user', 'goods', 'inventory', 'orders'))
                elif 'df' in args:
                    percent = disk_after if pulled else disk_before
                    out = f'Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/root 20000000 14000000 5000000 {percent}% /\n'
                elif 'sendMessage' in args:
                    out = f'#Broker Name #QID #Send Result #MsgId\ngrpctest-broker 0 {send} fixture-id\n'
                return SimpleNamespace(stdout=out)
            failed = False
            with patch.object(release, 'ROOT', root), patch.object(release, 'run', run), \
                    patch.object(release, 'prune'), \
                    patch.dict(os.environ, {'IMAGES_JSON': json.dumps(images)}), \
                    patch.object(sys, 'argv', ['release', 'web', 'c'*40]):
                try: release.main()
                except (ValueError, RuntimeError): failed = True
            return failed, calls, (state / 'images.env').read_text() == original

    def test_success_checks_twice_and_commits_desired_release(self):
        failed, calls, unchanged = self.exercise()
        self.assertFalse(failed)
        self.assertFalse(unchanged)
        self.assertEqual(sum("sendMessage" in c for c in calls), 2)
        self.assertEqual(sum("df" in c for c in calls), 2)
        self.assertTrue(any("up" in c for c in calls))

    def test_disk_pressure_rejects_before_pull(self):
        failed, calls, unchanged = self.exercise(disk_before=91)
        self.assertTrue(failed, 'release accepted 91% disk use')
        self.assertFalse(any('pull' in c or 'up' in c for c in calls))
        self.assertTrue(unchanged)

    def test_zero_exit_mq_error_rejects_before_rollout(self):
        failed, calls, unchanged = self.exercise(send='FLUSH_DISK_TIMEOUT')
        self.assertTrue(failed, 'healthy TCP port incorrectly accepted as writable MQ')
        self.assertFalse(any('up' in c for c in calls))
        self.assertTrue(unchanged)

    def test_pull_crossing_disk_limit_does_not_replace_containers(self):
        failed, calls, unchanged = self.exercise(disk_after=86)
        self.assertTrue(failed, 'disk not rechecked after downloading images')
        self.assertTrue(any('pull' in c for c in calls))
        self.assertFalse(any('up' in c for c in calls))
        self.assertTrue(unchanged)

from capacity import check_disk, check_mq_write
from image_retention import candidates


class RetentionTests(unittest.TestCase):
    def test_only_old_known_registry_copies_are_selected(self):
        now = datetime.datetime(2026, 9, 28, tzinfo=datetime.timezone.utc)
        repo = 'ghcr.io/qqawer/grpctest-order_srv'
        def image(n, revision=None, repository=repo):
            digest = f'{n:064x}'
            return {'Id': 'sha256:' + digest, 'Created': f'2026-09-{n:02d}T00:00:00Z',
                    'RepoDigests': [repository + '@sha256:' + digest], 'RepoTags': [],
                    'Config': {'Labels': {'org.opencontainers.image.revision': revision or f'{n:040x}',
                                          'org.opencontainers.image.source': 'https://github.com/qqawer/grpctest'}}}
        images = [image(n) for n in (19, 20, 21, 22, 23, 24, 25, 26)]
        # Another manifest for the newest revision must not count as a new version.
        images += [image(27, revision=f'{26:040x}')]
        unknown = image(18); unknown['RepoDigests'] = []
        external = image(17, repository='ghcr.io/someone/another-project')
        local_alias = image(16); local_alias['RepoTags'] = ['manual-backup:latest']
        images += [unknown, external, local_alias]
        protected = {images[0]['Id'], images[1]['RepoDigests'][0], images[2]['Id']}
        picked = candidates(images, protected, 'qqawer', now)
        self.assertEqual({x['Id'] for x in picked}, {image(22)['Id'], image(23)['Id']})

    def test_never_remove_recently_created_builds(self):
        now = datetime.datetime(2026, 9, 28, tzinfo=datetime.timezone.utc)
        repo = 'ghcr.io/qqawer/grpctest-order_srv'
        images = [{'Id': f'sha256:{n:064x}', 'Created': f'2026-09-27T{n:02d}:00:00Z',
                   'RepoDigests': [repo + f'@sha256:{n:064x}'], 'RepoTags': [],
                   'Config': {'Labels': {'org.opencontainers.image.revision': f'{n:040x}',
                                         'org.opencontainers.image.source': 'https://github.com/qqawer/grpctest'}}}
                  for n in range(1, 6)]
        self.assertEqual(candidates(images, set(), 'qqawer', now), [])

    def test_cleanup_rechecks_container_references_and_never_forces_removal(self):
        import image_retention
        image = {'Id': 'sha256:' + 'd'*64, 'Created': '2026-09-20T00:00:00Z',
                 'RepoDigests': ['ghcr.io/qqawer/grpctest-order_srv@sha256:' + 'd'*64]}
        for in_use in (False, True):
            with self.subTest(in_use=in_use), tempfile.TemporaryDirectory() as directory:
                calls = []
                def docker(*args):
                    calls.append(args)
                    if args[:3] == ('image', 'ls', '-q'): return image['Id']
                    if args[:2] == ('image', 'inspect'): return json.dumps([image])
                    if args[:2] == ('ps', '-aq'): return 'new-container' if '--filter' in args and in_use else ''
                    return ''
                with patch.object(image_retention, 'docker', docker), \
                        patch.object(image_retention, 'candidates', return_value=[image]):
                    report = image_retention.prune('qqawer', Path(directory), apply=True)
                removals = [c for c in calls if c[:2] == ('image', 'rm')]
                self.assertEqual(removals, [] if in_use else [('image', 'rm', image['Id'])])
                self.assertEqual(len(report['deleted']), 0 if in_use else 1)
                self.assertEqual(json.loads((Path(directory) / 'image-retention-last.json').read_text()), report)

    def test_native_log_policies_preserve_destinations_and_levels(self):
        spec = importlib.util.spec_from_file_location('nacos_retention', SCRIPTS / 'prepare-nacos-logging.py')
        module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module)
        policies = ''.join('<appender name="log%d"><file>/home/nacos/logs/test%d.log</file>'
                           '<rollingPolicy class="ch.qos.logback.core.rolling.SizeAndTimeBasedRollingPolicy">'
                           '<fileNamePattern>test.log.%%d{yyyy-MM-dd}.%%i</fileNamePattern>'
                           '<maxFileSize>1GB</maxFileSize><maxHistory>7</maxHistory>'
                           '<totalSizeCap>3GB</totalSizeCap><cleanHistoryOnStart>true</cleanHistoryOnStart>'
                           '</rollingPolicy></appender>' % (i, i) for i in range(32))
        source = ('<configuration>' + policies + '<root level="INFO"/></configuration>').encode()
        with patch.object(module, 'SOURCE_SHA256', hashlib.sha256(source).hexdigest()):
            result = ET.fromstring(module.bounded_config(source))
            self.assertEqual(result.find('root').get('level'), 'INFO')
            self.assertEqual(len(result.findall('.//file')), 32)
            for policy in result.findall('.//rollingPolicy'):
                self.assertEqual({k: policy.findtext(k) for k in module.LIMITS}, module.LIMITS)
        with self.assertRaisesRegex(ValueError, 'source changed'):
            module.bounded_config(source)

    def test_mq_missing_result_and_zero_exit_error_are_not_success(self):
        for output in ('', 'Exception SEND_OK is not a send result',
                       'grpctest-broker 0 FLUSH_DISK_TIMEOUT id',
                       '#Send Result SEND_OK', 'other-broker 0 SEND_OK id'):
            with self.subTest(output=output), self.assertRaises(ValueError):
                check_mq_write(lambda *a, **k: SimpleNamespace(stdout=output))

    def test_disk_unparseable_or_low_bytes_rejects(self):
        for output in ('', 'unavailable',
                       'Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/root 900000 600000 300000 67% /'):
            with self.subTest(output=output), self.assertRaises(ValueError):
                check_disk(lambda *a, **k: SimpleNamespace(stdout=output))


if __name__ == "__main__":
    unittest.main()
