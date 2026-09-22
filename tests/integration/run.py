#!/usr/bin/env python3
"""Exercise the public migration commands against disposable, real MySQL."""
from pathlib import Path
import concurrent.futures
import json
import os
import secrets
import shutil
import socket
import subprocess
import sys
import tempfile

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from env import DATABASES, Environment, ROOT, execute


def rejected(result, message):
    assert result.returncode != 0, f'command should reject {message}'
    assert message in result.stderr, f'wrong rejection: {result.stderr.strip()}'


def redis_ping(env):
    with socket.create_connection(('127.0.0.1', env.port('redis', 6379)), timeout=5) as conn:
        conn.settimeout(5)
        stream = conn.makefile('rb')
        for command in [('AUTH', env.state['redis_password']), ('PING',)]:
            args = [value.encode() for value in command]
            conn.sendall(b'*' + str(len(args)).encode() + b'\r\n' + b''.join(
                b'$' + str(len(arg)).encode() + b'\r\n' + arg + b'\r\n' for arg in args))
            assert stream.readline() == (b'+OK\r\n' if command[0] == 'AUTH' else b'+PONG\r\n')
    print('PASS: isolated Redis authenticates and responds through its loopback port', flush=True)


def rpc_regressions(env):
    config = env.credentials()
    config.update(user='grpctest', password=env.state['app_password'])
    with tempfile.TemporaryDirectory(prefix='grpctest-rpc-test-') as directory:
        path = Path(directory) / 'connection.json'
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, 'w') as handle:
            json.dump(config, handle)
        subprocess.run(['go', 'test', '-mod=readonly', '-race', '-tags=integration',
                        './tests/integration/...', '-count=1', '-timeout=90s'],
                       env=dict(os.environ, GRPCTEST_TEST_CONFIG=str(path), GOWORK='off'), check=True)
    print('PASS: four RPC handler/protocol regressions against real MySQL', flush=True)


def resource_collision():
    candidate = Environment('grpctest-test-collision-' + secrets.token_hex(4))
    name = candidate.project + '_mysql-data'
    execute(candidate.docker + ['volume', 'create', '--label', 'io.grpctest.collision-fixture=true', name])
    try:
        try:
            candidate.up()
        except ValueError as error:
            assert 'not owned' in str(error)
        else:
            raise AssertionError('must refuse an existing same-name volume without test ownership')
        assert execute(candidate.docker + ['volume', 'inspect', name], check=False).returncode == 0
    finally:
        # This exact empty volume was created above solely for this negative case.
        execute(candidate.docker + ['volume', 'rm', name])
    print('PASS: refuses to reuse or delete an unrelated same-name Docker volume', flush=True)


def legacy_upgrade():
    # Exercise the exact production adoption/backup tool only on owned local data.
    env = Environment('grpctest-test-adoption-' + secrets.token_hex(4))
    try:
        env.up(); env.build_tool()
        with tempfile.TemporaryDirectory(prefix='grpctest-baseline-') as directory:
            shutil.copy2(ROOT / 'deploy/mysql/migrations/0001_baseline.sql', directory)
            env.dbtool('up', migrations=Path(directory))
            env.dbtool('seed', migrations=Path(directory), seed=ROOT / 'tests/seed.sql')
        env.sql('DROP TABLE grpctest_migrations.schema_migrations; UPDATE grpctest_inventory_srv.inventory SET stocks=17 WHERE goods=421;')
        command = [sys.executable, str(ROOT / 'deploy/scripts/migrate-business.py')]
        options = ['--container', env.container('mysql'), '--state-dir', str(env.directory / 'migration-state')]
        env.sql('CREATE TABLE grpctest_goods_srv.unexpected(id INT);')
        rejected(execute(command + ['check'] + options, check=False), 'does not match')
        env.sql('DROP TABLE grpctest_goods_srv.unexpected;')
        print(execute(command + ['check'] + options).stdout.strip(), flush=True)
        print(execute(command + ['up'] + options).stdout.strip(), flush=True)
        execute(command + ['up'] + options)
        assert env.sql('SELECT stocks FROM grpctest_inventory_srv.inventory WHERE goods=421; SELECT COUNT(*) FROM grpctest_migrations.schema_migrations WHERE dirty=0;') == '17\n2'
        backups=list((env.directory / 'migration-state').glob('before-migration-*.sql'))
        assert len(backups)==1 and backups[0].stat().st_size>0
        assert backups[0].stat().st_mode & 0o077 == 0
        env.sql('UPDATE grpctest_migrations.schema_migrations SET dirty=1 WHERE version=2;')
        rejected(execute(command + ['up'] + options, check=False), 'dirty migration history')
        print('PASS: production migrator refuses drift/dirty history, backs up privately, adopts baseline and preserves stock on repeat', flush=True)
    finally:
        if env.state: env.down()


def main():
    os.chdir(ROOT)
    resource_collision()
    legacy_upgrade()
    env = Environment('grpctest-test-ci-' + secrets.token_hex(4))
    try:
        env.up()
        env.build_tool()
        # Refuse a database that has existing data but no registered baseline.
        env.sql('CREATE TABLE grpctest_user_srv.sentinel(id INT PRIMARY KEY); INSERT INTO grpctest_user_srv.sentinel VALUES (7);')
        rejected(env.dbtool('up', check=False), 'without migration history')
        assert env.sql('SELECT id FROM grpctest_user_srv.sentinel;') == '7'
        env.sql('DROP TABLE grpctest_user_srv.sentinel;')
        wrong_marker = dict(env.credentials(), marker='not-this-environment')
        rejected(execute([str(env.binary), 'up'], stdin=json.dumps(wrong_marker), check=False), 'marker mismatch')
        remote_target = dict(env.credentials(), host='192.0.2.1')
        rejected(execute([str(env.binary), 'up'], stdin=json.dumps(remote_target), check=False), 'loopback host')
        print('PASS: refuses unregistered business tables, wrong marker and non-local target', flush=True)
        env.dbtool('up')
        rows = env.sql("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA IN (" +
                       ','.join("'" + db + "'" for db in DATABASES) + ');')
        assert rows == '13', f'current schema must create 13 business tables; got {rows}'
        print('PASS: empty environment applies baseline + inventory initialization migration (13 tables)', flush=True)
        with tempfile.TemporaryDirectory(prefix='grpctest-bad-seed-') as directory:
            bad_seed = Path(directory) / 'seed.sql'
            source = (ROOT / 'tests/seed.sql').read_text()
            bad_seed.write_text(source + '\nINSERT INTO grpctest_user_srv.user (id,mobile,password) VALUES (1,\'13000000002\',\'test-only\');\n')
            rejected(env.dbtool('seed', seed=bad_seed, check=False), 'rolled back')
            assert env.sql('SELECT COUNT(*) FROM grpctest_goods_srv.goods; SELECT COUNT(*) FROM grpctest_migrations.seed_history;') == '0\n0'
        print('PASS: failing seed rolls back both fixture rows and its completion record', flush=True)
        env.dbtool('seed', seed=ROOT / 'tests/seed.sql')
        actual = env.sql('SELECT COUNT(*) FROM grpctest_goods_srv.goods; SELECT stocks FROM grpctest_inventory_srv.inventory WHERE goods=421;')
        assert actual == '1\n100', f'fixture must contain one product with 100 stock; got {actual}'
        redis_ping(env)
        rpc_regressions(env)
        env.sql('UPDATE grpctest_inventory_srv.inventory SET stocks=17 WHERE goods=421;')
        env.dbtool('up')
        env.dbtool('seed', seed=ROOT / 'tests/seed.sql')
        assert env.sql('SELECT stocks FROM grpctest_inventory_srv.inventory WHERE goods=421;') == '17'
        print('PASS: repeat migration/seed preserves changed stock and does not duplicate fixture', flush=True)
        with tempfile.TemporaryDirectory(prefix='grpctest-migration-cases-') as directory:
            migrations = Path(directory)
            for source in (ROOT / 'deploy/mysql/migrations').glob('*.sql'):
                shutil.copy2(source, migrations / source.name)
            baseline = migrations / '0001_baseline.sql'
            original = baseline.read_text()
            baseline.write_text(original + '\n-- modified history\n')
            rejected(env.dbtool('up', migrations=migrations, check=False), 'checksum changed')
            baseline.write_text(original)
            changed_seed = migrations / 'changed-seed.txt'
            changed_seed.write_text((ROOT / 'tests/seed.sql').read_text() + '\n-- modified seed\n')
            rejected(env.dbtool('seed', seed=changed_seed, check=False), 'seed checksum changed')
            env.sql("INSERT INTO grpctest_migrations.schema_migrations VALUES (99,'unknown.sql',REPEAT('0',64),0,NOW());")
            rejected(env.dbtool('up', check=False), 'unknown or nonconsecutive')
            env.sql('DELETE FROM grpctest_migrations.schema_migrations WHERE version=99;')
            print('PASS: changed applied SQL/seed and unknown migration versions are rejected', flush=True)
            # A test-only upgrade demonstrates serialization without altering the baseline.
            (migrations / '0003_test_upgrade.sql').write_text('DO SLEEP(1); ALTER TABLE grpctest_orders_srv.shoppingcart ADD COLUMN integration_note VARCHAR(20) NULL;')
            with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
                results = list(pool.map(lambda _: env.dbtool('up', migrations=migrations), range(2)))
            assert all(result.returncode == 0 for result in results)
            assert env.sql('SELECT COUNT(*) FROM grpctest_migrations.schema_migrations;') == '3'
            assert env.sql('SELECT stocks FROM grpctest_inventory_srv.inventory WHERE goods=421;') == '17'
            print('PASS: concurrent incremental migration applies once and preserves data', flush=True)
            (migrations / '0004_test_failure.sql').write_text('CREATE TABLE grpctest_orders_srv.migration_probe(id INT); INSERT INTO grpctest_orders_srv.does_not_exist VALUES (1);')
            rejected(env.dbtool('up', migrations=migrations, check=False), 'history remains dirty')
            assert env.sql('SELECT dirty FROM grpctest_migrations.schema_migrations WHERE version=4;') == '1'
            assert env.sql("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA='grpctest_orders_srv' AND TABLE_NAME='migration_probe';") == '1'
            rejected(env.dbtool('up', migrations=migrations, check=False), 'dirty migration history')
            print('PASS: partial DDL remains marked dirty and blocks automatic retries', flush=True)
        env.down()
        (env.directory / 'init.sql').unlink()
        env.up()
        env.dbtool('up')
        env.dbtool('seed', seed=ROOT / 'tests/seed.sql')
        assert env.sql('SELECT COUNT(*) FROM grpctest_migrations.schema_migrations; SELECT stocks FROM grpctest_inventory_srv.inventory WHERE goods=421;') == '2\n100'
        print('PASS: explicit test-only reset recovers a clean baseline and missing init file', flush=True)
    finally:
        if env.state:
            env.down()


if __name__ == '__main__':
    main()
