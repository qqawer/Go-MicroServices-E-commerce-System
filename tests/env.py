#!/usr/bin/env python3
"""Local-only disposable MySQL/Redis environment; never reads deploy/.env."""
import argparse
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
DATABASES = ('grpctest_user_srv', 'grpctest_goods_srv', 'grpctest_inventory_srv', 'grpctest_orders_srv')


def execute(args, *, env=None, stdin=None, timeout=180, check=True):
    result = subprocess.run(args, input=stdin, text=True, capture_output=True, env=env, timeout=timeout)
    if check and result.returncode:
        # Docker may include environment values in diagnostic messages.
        raise RuntimeError(f'{Path(args[0]).name} command failed (exit {result.returncode})')
    return result


class Environment:
    def __init__(self, project='grpctest-test'):
        if not re.fullmatch(r'grpctest-test(?:-[a-z0-9]+)*', project):
            raise ValueError('project must be grpctest-test or grpctest-test-<suffix>')
        if os.environ.get('DOCKER_HOST', '').startswith(('tcp:', 'ssh:')):
            raise ValueError('only a local Docker Unix socket is allowed')
        self.context = execute(['docker', 'context', 'show']).stdout.strip()
        info = json.loads(execute(['docker', 'context', 'inspect', self.context]).stdout)[0]
        if not info['Endpoints']['docker']['Host'].startswith('unix://'):
            raise ValueError('only a local Docker Unix socket is allowed')
        self.project = project
        self.directory = ROOT / '.local' / 'testing' / project
        self.state_path = self.directory / 'state.json'
        self.docker = ['docker', '--context', self.context]
        self.compose = self.docker + ['compose', '--env-file', '/dev/null', '-p', project,
                                     '-f', str(ROOT / 'tests/compose.yaml')]
        self.state = json.loads(self.state_path.read_text()) if self.state_path.exists() else None
        if self.state is not None and (set(self.state) != {'root_password', 'app_password', 'redis_password', 'marker'} or
                                      any(not isinstance(value, str) or not re.fullmatch(r'[0-9a-f]{48}', value)
                                          for value in self.state.values())):
            raise ValueError('invalid private test state; refusing to reuse it')
        self.binary = ROOT / '.local' / 'tools' / 'test-dbtool'

    def variables(self):
        if not self.state:
            raise ValueError('test environment not prepared; run up first')
        return dict(os.environ, TEST_MYSQL_ROOT_PASSWORD=self.state['root_password'],
                    TEST_MYSQL_PASSWORD=self.state['app_password'], TEST_REDIS_PASSWORD=self.state['redis_password'],
                    TEST_MARKER=self.state['marker'], TEST_INIT_SQL=str(self.directory / 'init.sql'))

    def ensure_owned(self):
        marker = self.state['marker'] if self.state else None
        def verify(obj, kind):
            labels = obj['Config']['Labels'] if kind == 'container' else obj.get('Labels', {})
            labels = labels or {}
            if (not marker or labels.get('io.grpctest.test.marker') != marker or
                    labels.get('com.docker.compose.project') != self.project):
                raise ValueError('existing Docker resources are not owned by this test state; refusing changes')

        # Compose can reuse a same-name resource even if it lacks project labels.
        exact_names = [('volume', self.project + '_mysql-data'), ('volume', self.project + '_redis-data'),
                       ('network', self.project + '_test'), ('container', self.project + '-mysql-1'),
                       ('container', self.project + '-redis-1')]
        for kind, name in exact_names:
            result = execute(self.docker + [kind, 'inspect', name], check=False)
            if result.returncode:
                if not any(message in result.stderr.lower() for message in ('no such', 'not found')):
                    raise RuntimeError('cannot verify existing Docker resource')
                continue
            for obj in json.loads(result.stdout):
                verify(obj, kind)
        queries = [(['volume', 'ls', '-q'], 'volume'), (['network', 'ls', '-q'], 'network'),
                   (['ps', '-aq'], 'container')]
        for command, kind in queries:
            ids = execute(self.docker + command + ['--filter', 'label=com.docker.compose.project=' + self.project]).stdout.split()
            if not ids:
                continue
            objects = json.loads(execute(self.docker + [kind, 'inspect', *ids]).stdout)
            for obj in objects:
                verify(obj, kind)

    def up(self):
        self.ensure_owned()
        if not self.state:
            self.directory.mkdir(parents=True, mode=0o700, exist_ok=True)
            self.state = {key: secrets.token_hex(24) for key in ('root_password', 'app_password', 'redis_password', 'marker')}
            fd = os.open(self.state_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(fd, 'w') as handle:
                json.dump(self.state, handle)
        # Regenerate atomically even after interruption between state/init creation.
        sql = '\n'.join(f'CREATE DATABASE IF NOT EXISTS `{db}` CHARACTER SET utf8mb4;\n'
                        f"GRANT ALL ON `{db}`.* TO 'grpctest'@'%';" for db in DATABASES)
        sql += '\nCREATE DATABASE grpctest_migrations;\n'
        sql += 'CREATE TABLE grpctest_migrations.test_environment(marker VARCHAR(128) NOT NULL);\n'
        sql += "INSERT INTO grpctest_migrations.test_environment VALUES ('" + self.state['marker'] + "');\n"
        sql += "GRANT SELECT ON grpctest_migrations.test_environment TO 'grpctest'@'%';\n"
        temporary = self.directory / ('init-' + secrets.token_hex(4) + '.sql')
        fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, 'w') as handle:
            handle.write(sql)
            # The MySQL entrypoint reads this bind mount as its own UID on Linux.
            # It contains no passwords; the host parent directory stays private.
            os.fchmod(handle.fileno(), 0o644)
        temporary.replace(self.directory / 'init.sql')
        print(f'Preparing {self.project}: local MySQL + Redis...', flush=True)
        execute(self.compose + ['up', '-d', '--wait', '--wait-timeout', '150'], env=self.variables(), timeout=240)
        self.ensure_owned()

    def container(self, service):
        self.ensure_owned()
        identifier = execute(self.compose + ['ps', '-q', service], env=self.variables()).stdout.strip()
        if not identifier:
            raise ValueError(service + ' test container is not running')
        return identifier

    def port(self, service, container_port):
        obj = json.loads(execute(self.docker + ['inspect', self.container(service)]).stdout)[0]
        bindings = obj['NetworkSettings']['Ports'].get(str(container_port) + '/tcp')
        if not bindings:
            raise ValueError('Docker did not publish the test port')
        binding = bindings[0]
        if binding['HostIp'] != '127.0.0.1':
            raise ValueError('test port must bind only to loopback')
        return int(binding['HostPort'])

    def credentials(self):
        return dict(host='127.0.0.1', port=self.port('mysql', 3306), user='root',
                    password=self.state['root_password'], marker=self.state['marker'])

    def sql(self, sql):
        return execute(self.docker + ['exec', '-i', self.container('mysql'), 'sh', '-ec',
                       'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -uroot -N -B --default-character-set=utf8mb4'],
                       stdin=sql).stdout.strip()

    def build_tool(self):
        self.binary.parent.mkdir(parents=True, exist_ok=True)
        execute(['go', 'build', '-mod=readonly', '-o', str(self.binary), './tests/dbtool'],
                env=dict(os.environ, GOWORK='off'), timeout=180)

    def dbtool(self, action, migrations=None, seed=None, check=True):
        args = [str(self.binary), action, '--migrations', str(migrations or ROOT / 'deploy/mysql/migrations')]
        if seed:
            args += ['--seed', str(seed)]
        result = execute(args, stdin=json.dumps(self.credentials()), check=False)
        if check and result.returncode:
            raise RuntimeError(result.stderr.strip() or 'database command failed')
        return result

    def down(self):
        if not self.state:
            raise ValueError('no local test state; refusing cleanup')
        self.ensure_owned()
        execute(self.compose + ['down', '--volumes', '--remove-orphans'], env=self.variables())
        print(f'Removed only {self.project} containers/network/volumes.', flush=True)

    def info(self):
        print(f'MySQL: 127.0.0.1:{self.port("mysql", 3306)}; Redis: 127.0.0.1:{self.port("redis", 6379)}')
        print(f'Test credentials (ignored by Git): {self.state_path}')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['up', 'migrate', 'seed', 'status', 'down', 'reset'])
    parser.add_argument('--project', default='grpctest-test')
    args = parser.parse_args()
    os.chdir(ROOT)
    env = Environment(args.project)
    if args.action == 'down':
        env.down()
        return
    if args.action == 'reset':
        env.down()
    if args.action in ['up', 'reset']:
        env.up()
    env.build_tool()
    if args.action in ['up', 'reset', 'migrate']:
        print(env.dbtool('up').stdout.strip())
    if args.action in ['up', 'reset', 'seed']:
        print(env.dbtool('seed', seed=ROOT / 'tests/seed.sql').stdout.strip())
    print(env.dbtool('status').stdout.strip())
    env.info()


if __name__ == '__main__':
    try:
        main()
    except (ValueError, RuntimeError, OSError, subprocess.TimeoutExpired) as error:
        sys.exit(str(error))
