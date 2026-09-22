#!/usr/bin/env python3
"""Adopt the verified existing baseline, back up, and apply numbered business SQL."""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[1]
DATABASES = ('grpctest_user_srv', 'grpctest_goods_srv', 'grpctest_inventory_srv', 'grpctest_orders_srv')
SCHEMAS = ','.join("'" + name + "'" for name in DATABASES)


def snapshot(sql):
    return {
        'tables': sql(f"SELECT TABLE_SCHEMA,TABLE_NAME,ENGINE FROM information_schema.TABLES WHERE TABLE_SCHEMA IN ({SCHEMAS}) ORDER BY 1,2"),
        'columns': sql(f"SELECT TABLE_SCHEMA,TABLE_NAME,COLUMN_NAME,COLUMN_TYPE,IS_NULLABLE,COALESCE(COLUMN_DEFAULT,'<NULL>'),EXTRA FROM information_schema.COLUMNS WHERE TABLE_SCHEMA IN ({SCHEMAS}) ORDER BY TABLE_SCHEMA,TABLE_NAME,ORDINAL_POSITION"),
        'indexes': sql(f"SELECT TABLE_SCHEMA,TABLE_NAME,INDEX_NAME,NON_UNIQUE,SEQ_IN_INDEX,COLUMN_NAME FROM information_schema.STATISTICS WHERE TABLE_SCHEMA IN ({SCHEMAS}) ORDER BY 1,2,3,5"),
        'foreign_keys': sql(f"SELECT CONSTRAINT_SCHEMA,TABLE_NAME,CONSTRAINT_NAME,REFERENCED_TABLE_NAME,UPDATE_RULE,DELETE_RULE FROM information_schema.REFERENTIAL_CONSTRAINTS WHERE CONSTRAINT_SCHEMA IN ({SCHEMAS}) ORDER BY 1,2,3"),
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['check', 'up'])
    parser.add_argument('--container', default='grpctest-infra-mysql-1')
    parser.add_argument('--state-dir', type=Path, default=ROOT / 'runtime/state')
    args = parser.parse_args()
    os.umask(0o077)
    args.state_dir.mkdir(parents=True, exist_ok=True)

    def sql(statement):
        result = subprocess.run(['docker', 'exec', '-i', args.container, 'sh', '-ec',
            'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -uroot -N -B --raw --default-character-set=utf8mb4'],
            input=statement, text=True, capture_output=True, timeout=180)
        if result.returncode:
            raise RuntimeError('MySQL operation failed; inspect the private backup and dirty migration before retrying')
        return result.stdout.strip()

    migrations = []
    for index, path in enumerate(sorted((ROOT / 'mysql/migrations').glob('*.sql')), 1):
        if not path.name.startswith(f'{index:04d}_'):
            raise ValueError('Migration versions must be consecutive')
        migrations.append((index, path.name, hashlib.sha256(path.read_bytes()).hexdigest(), path.read_text()))
    with (args.state_dir / 'release.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        history_exists = sql("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA='grpctest_migrations' AND TABLE_NAME='schema_migrations'") == '1'
        applied = 0
        if history_exists:
            for row in sql('SELECT version,filename,sha256,dirty FROM grpctest_migrations.schema_migrations ORDER BY version').splitlines():
                version, filename, digest, dirty = row.split('\t')
                if int(version) != applied + 1 or int(version) > len(migrations):
                    raise ValueError('Unknown/nonconsecutive migration history')
                if (filename, digest) != migrations[applied][1:3] or dirty != '0':
                    raise ValueError('Changed checksum or dirty migration history; refusing automatic retry')
                applied += 1
        if applied == 0:
            expected = json.loads((ROOT / 'mysql/0001_schema.json').read_text())
            if snapshot(sql) != expected:
                raise ValueError('Existing business schema does not match the reviewed baseline; refusing adoption')
        if applied < 2:
            bad = sql('SELECT COUNT(*)-COUNT(DISTINCT goods)+COALESCE(SUM(goods IS NULL OR goods<=0 OR stocks IS NULL OR stocks<0),0) FROM grpctest_inventory_srv.inventory')
            missing = sql('SELECT COUNT(*) FROM grpctest_goods_srv.goods g LEFT JOIN grpctest_inventory_srv.inventory i ON i.goods=g.id AND i.deleted_at IS NULL WHERE g.deleted_at IS NULL AND i.id IS NULL')
            if bad != '0' or missing != '0':
                raise ValueError('Duplicate/invalid inventory or existing goods without inventory; reconcile explicitly first')
        if applied < 3 and len(migrations) >= 3:
            # Refuse ambiguous legacy history before creating any new schema objects.
            bad_orders = sql("SELECT COUNT(*)-COUNT(DISTINCT order_sn)+COALESCE(SUM(order_sn IS NULL OR order_sn NOT REGEXP '^[A-Za-z0-9_-]{1,200}$'),0) FROM grpctest_orders_srv.orderinfo")
            bad_stock = sql("SELECT COUNT(*)-COUNT(DISTINCT order_sn)+COALESCE(SUM(order_sn IS NULL OR order_sn NOT REGEXP '^[A-Za-z0-9_-]{1,200}$' OR status IS NULL OR status NOT IN ('1','2') OR detail IS NULL OR NOT JSON_VALID(detail)),0) FROM grpctest_inventory_srv.stockselldetail")
            if bad_orders != '0' or bad_stock != '0':
                raise ValueError('Invalid/duplicate legacy order or stock operation history; reconcile explicitly first')
            for row in sql('SELECT order_sn,detail FROM grpctest_inventory_srv.stockselldetail').splitlines():
                number, raw = row.split('\t', 1)
                detail = json.loads(raw)
                if not isinstance(detail, list) or not 1 <= len(detail) <= 1000:
                    raise ValueError('Invalid legacy stock operation snapshot')
                totals = {}
                for item in detail:
                    if not isinstance(item, dict) or type(item.get('Goods')) is not int or type(item.get('Num')) is not int:
                        raise ValueError('Invalid legacy stock operation snapshot')
                    goods, quantity = item['Goods'], item['Num']
                    totals[goods] = totals.get(goods, 0) + quantity
                    if not 1 <= goods <= 2147483647 or quantity <= 0 or totals[goods] > 2147483647:
                        raise ValueError('Invalid legacy stock operation quantity')
        print(f'Preflight passed: {applied}/{len(migrations)} migrations recorded')
        if args.action == 'check' or applied == len(migrations):
            return
        backup = args.state_dir / ('before-migration-' + time.strftime('%Y%m%dT%H%M%SZ', time.gmtime()) + '.sql')
        with backup.open('xb') as output:
            result = subprocess.run(['docker', 'exec', args.container, 'sh', '-ec',
                'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysqldump -uroot --single-transaction --quick --no-tablespaces --set-gtid-purged=OFF --hex-blob --databases "$@"',
                'sh', *DATABASES], stdout=output, stderr=subprocess.PIPE, timeout=180)
        if result.returncode or backup.stat().st_size == 0:
            raise RuntimeError('Backup failed; migrations were not applied')
        print('Private backup:', backup)
        sql('CREATE DATABASE IF NOT EXISTS grpctest_migrations; CREATE TABLE IF NOT EXISTS grpctest_migrations.schema_migrations (version INT PRIMARY KEY, filename VARCHAR(255) NOT NULL, sha256 CHAR(64) NOT NULL, dirty BOOLEAN NOT NULL, applied_at DATETIME(3) NULL);')
        if applied == 0:
            version, filename, digest, _ = migrations[0]
            sql(f"INSERT INTO grpctest_migrations.schema_migrations VALUES ({version},'{filename}','{digest}',0,NOW(3))")
            applied = 1
        for version, filename, digest, statement in migrations[applied:]:
            sql(f"INSERT INTO grpctest_migrations.schema_migrations VALUES ({version},'{filename}','{digest}',1,NULL)")
            sql(statement)
            sql(f'UPDATE grpctest_migrations.schema_migrations SET dirty=0,applied_at=NOW(3) WHERE version={version}')
            print(f'Applied {filename}')


if __name__ == '__main__':
    try:
        main()
    except (ValueError, RuntimeError, OSError, subprocess.TimeoutExpired) as error:
        sys.exit(str(error))
