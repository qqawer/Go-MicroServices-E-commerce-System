#!/usr/bin/env python3
"""Deploy one complete repository release; restore previous containers on failure."""
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
GROUPS = {
    'rpc': ('grpctest', ['user-srv', 'goods-srv', 'inventory-srv', 'order-srv']),
    'web': ('grpctest_web', ['user-web', 'goods-web', 'order-web']),
}

def env_file(path):
    return dict(line.split('=', 1) for line in path.read_text().splitlines()
                if line and not line.startswith('#'))

def run(args, capture=False):
    return subprocess.run(args, check=True, text=True, stdout=subprocess.PIPE if capture else None)

def main():
    preloaded = len(sys.argv) == 4 and sys.argv[3] == '--preloaded'
    if (len(sys.argv) != 3 and not preloaded) or sys.argv[1] not in GROUPS or not re.fullmatch(r'[0-9a-f]{40}', sys.argv[2]):
        raise ValueError('Usage: release.py rpc|web FULL_COMMIT_SHA [--preloaded]; set IMAGES_JSON')
    group, revision = sys.argv[1:3]
    repo, services = GROUPS[group]
    settings = env_file(ROOT / '.env')
    owner = settings['GHCR_OWNER']
    if not re.fullmatch(r'[a-z0-9][a-z0-9-]*', owner):
        raise ValueError('Invalid GHCR_OWNER')
    images = json.loads(os.environ['IMAGES_JSON'])
    if not isinstance(images, dict) or set(images) != set(services):
        raise ValueError('Manifest must contain exactly all services for this repository')
    for service, image in images.items():
        directory = service.replace('-srv', '_srv') if group == 'rpc' else service
        prefix = f'ghcr.io/{owner}/{repo}-{directory}@sha256:'
        if preloaded:
            if not isinstance(image, str) or not re.fullmatch(r'sha256:[0-9a-f]{64}', image):
                raise ValueError('Preloaded releases require immutable local image IDs')
            inspected = json.loads(run(['docker', 'image', 'inspect', image], capture=True).stdout)[0]
            labels = inspected.get('Config', {}).get('Labels') or {}
            if (inspected.get('Id') != image or
                    labels.get('org.opencontainers.image.source') != f'https://github.com/{owner}/{repo}' or
                    labels.get('org.opencontainers.image.revision') != revision or
                    labels.get('io.grpctest.service') != directory):
                raise ValueError('Preloaded image does not match the requested repository, revision and service')
        elif not isinstance(image, str) or not re.fullmatch(re.escape(prefix) + r'[0-9a-f]{64}', image):
            raise ValueError('Manifest contains an unexpected repository or digest')
    runtime = Path(settings['RUNTIME_DIR'])
    state = runtime / 'state'
    state.mkdir(parents=True, exist_ok=True)
    os.umask(0o077)
    with (state / 'release.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        release_file = state / 'images.env'
        infra = ['docker', 'compose', '--env-file', str(ROOT / '.env'), '-f', str(ROOT / 'compose.yaml')]
        for dependency in ('mysql', 'redis', 'nacos', 'consul'):
            ids = run(infra + ['ps', '-q', dependency], capture=True).stdout.split()
            if len(ids) != 1:
                raise ValueError(f'Infrastructure service is not running: {dependency}')
            health = run(['docker', 'inspect', '--format', '{{.State.Health.Status}}', ids[0]], capture=True).stdout.strip()
            if health != 'healthy':
                raise ValueError(f'Infrastructure service is not healthy: {dependency}')
        sql = "SELECT DISTINCT TABLE_SCHEMA FROM information_schema.TABLES WHERE TABLE_SCHEMA LIKE 'grpctest_%'"
        schemas = set(run(infra + ['exec', '-T', 'mysql', 'sh', '-ec',
                          'MYSQL_PWD="$MYSQL_PASSWORD" exec mysql -ugrpctest -Nse "$1"', 'sh', sql], capture=True).stdout.split())
        expected = {'grpctest_user_srv', 'grpctest_goods_srv', 'grpctest_inventory_srv', 'grpctest_orders_srv'}
        if not expected.issubset(schemas):
            raise ValueError('Business database tables are missing: ' + ', '.join(sorted(expected - schemas)))
        if group == 'rpc':
            # Additive schema changes are applied and backed up explicitly before
            # rollout. Never restart RPC containers against missing/dirty tables.
            expected_history = ['\t'.join((str(index), path.name, hashlib.sha256(path.read_bytes()).hexdigest(), '0'))
                                for index, path in enumerate(sorted((ROOT / 'mysql/migrations').glob('*.sql')), 1)]
            history_sql = 'SELECT version,filename,sha256,dirty FROM grpctest_migrations.schema_migrations ORDER BY version'
            history = run(infra + ['exec', '-T', 'mysql', 'sh', '-ec',
                          'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -uroot -N -B -e "$1"', 'sh', history_sql], capture=True).stdout.strip().splitlines()
            if not expected_history or history != expected_history:
                raise ValueError('Business schema is not at the reviewed migration version; run migrate-business.py check/up before deployment')
        previous = env_file(release_file) if release_file.exists() else {}
        candidate = dict(previous)
        for service, image in images.items():
            candidate[service.upper().replace('-', '_') + '_IMAGE'] = image
            directory = service.replace('-srv', '_srv') if group == 'rpc' else service
            if not (runtime / 'config' / directory / 'config-debug.yaml').is_file():
                raise ValueError(f'Missing Nacos bootstrap file for {service}')

        def write_env(values):
            f = tempfile.NamedTemporaryFile(mode='w', prefix='release-', suffix='.env', dir=state, delete=False)
            with f:
                for key, value in values.items():
                    f.write(f'{key}={value}\n')
            return Path(f.name)

        new_env, old_env = write_env(candidate), write_env(previous)
        def compose(env):
            return ['docker', 'compose', '--env-file', str(ROOT / '.env'), '--env-file', str(env),
                    '-f', str(ROOT / 'compose.apps.yaml'), '--profile', group]
        old_services = []
        try:
            run(compose(new_env) + ['config', '--quiet'])
            # Pull all images before touching any running container.
            if not preloaded:
                run(compose(new_env) + ['pull', *services])
            for service in services:
                ids = run(compose(old_env) + ['ps', '-aq', service], capture=True).stdout.split()
                if ids:
                    if service.upper().replace('-', '_') + '_IMAGE' not in previous:
                        raise ValueError('Unmanaged existing application containers found; adopt them explicitly first')
                    old_services.append(service)
            try:
                run(compose(new_env) + ['up', '-d', '--no-deps', '--pull', 'never', '--wait', '--wait-timeout', '180', *services])
            except subprocess.CalledProcessError:
                print('Deployment failed. Restoring the previous release.', file=sys.stderr)
                new_services = [service for service in services if service not in old_services]
                if new_services:
                    run(compose(new_env) + ['rm', '-sf', *new_services])
                if old_services:
                    run(compose(old_env) + ['up', '-d', '--no-deps', '--wait', '--wait-timeout', '180', *old_services])
                raise
            # Commit desired state only after Compose reports all services healthy.
            os.replace(new_env, release_file)
            record = {'group': group, 'revision': revision, 'images': images, 'source': 'preloaded' if preloaded else 'ghcr'}
            (state / f'{group}-{revision}.json').write_text(json.dumps(record, indent=2) + '\n')
            print(f'{group} {revision}: containers passed startup health checks (not business tests).')
        finally:
            new_env.unlink(missing_ok=True)
            old_env.unlink(missing_ok=True)

if __name__ == '__main__':
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.CalledProcessError) as exc:
        print(f'Deployment failed: {exc}', file=sys.stderr)
        sys.exit(1)
