#!/usr/bin/env python3
"""Initialize EMPTY demo databases from reviewed SQL; never drop or replace tables."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[1]
GOODS_SHA256 = '392a36f5aad6fb8a74f7a1886ff0a99d7825a471f27c855e3ff7595fea9e6f7b'
TABLES = {
    'grpctest_user_srv': ['user'],
    'grpctest_goods_srv': ['banner', 'brands', 'category', 'goods', 'goods_category_brand'],
    'grpctest_inventory_srv': ['inventory', 'stockselldetail'],
    'grpctest_orders_srv': ['shoppingcart', 'orderinfo', 'ordergoods'],
}

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--container', default='grpctest-infra-mysql-1')
    parser.add_argument('--goods-sql', type=Path, required=True)
    parser.add_argument('--marker', type=Path, default=ROOT / 'runtime/state/demo-db-bootstrap.json')
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    source = args.goods_sql.read_bytes()
    if hashlib.sha256(source).hexdigest() != GOODS_SHA256:
        raise ValueError('Goods SQL differs from the reviewed repository snapshot; review it before importing.')
    goods_sql = source.decode('utf-8')
    if re.findall(r'CREATE TABLE `([^`]+)`', goods_sql) != TABLES['grpctest_goods_srv']:
        raise ValueError('Unexpected goods tables')
    goods_sql, removed = re.subn(r'^DROP TABLE IF EXISTS `[^`]+`;\s*$', '', goods_sql, flags=re.M)
    if removed != 5:
        raise ValueError('Unexpected DROP statements in goods SQL')
    schema = (ROOT / 'mysql/02-business-schema.sql').read_text()
    plan_hash = hashlib.sha256((goods_sql + schema).encode()).hexdigest()

    def mysql(sql):
        result = subprocess.run(
            ['docker', 'exec', '-i', args.container, 'sh', '-ec',
             'MYSQL_PWD="$MYSQL_PASSWORD" exec mysql -ugrpctest --default-character-set=utf8mb4 -N -B'],
            input=sql, text=True, capture_output=True, check=False, timeout=90)
        if result.returncode:
            # SQL output can contain row data. Preserve it locally only when debugging deliberately.
            raise RuntimeError('MySQL operation failed; existing data was not dropped. Inspect schema before retrying.')
        return result.stdout

    db_list = ','.join("'" + name + "'" for name in TABLES)
    databases = set(mysql('SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME IN (' + db_list + ');').split())
    if databases != set(TABLES):
        raise ValueError('The four demo databases must already exist')
    existing = mysql('SELECT TABLE_SCHEMA,TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA IN (' + db_list + ');').splitlines()
    expected = {db + '\t' + table for db, tables in TABLES.items() for table in tables}
    if args.marker.exists():
        marker = json.loads(args.marker.read_text())
        if marker.get('plan_sha256') != plan_hash or not expected.issubset(set(existing)):
            raise ValueError('Bootstrap marker/schema mismatch; refusing to overwrite data')
        print('Demo bootstrap already completed; left all data unchanged.')
        return
    if existing:
        raise ValueError('Business databases are not empty; refusing to overwrite or drop any table')
    print('Preflight passed: four empty databases; five DROP statements removed; 420 source goods.')
    if not args.apply:
        return
    mysql('USE grpctest_goods_srv;\n' + goods_sql)
    mysql(schema)
    mysql('INSERT INTO grpctest_inventory_srv.inventory (add_time,update_time,is_deleted,goods,stocks,version) '
          'SELECT NOW(3),NOW(3),0,id,100,0 FROM grpctest_goods_srv.goods;')
    counts = {}
    for db, tables in TABLES.items():
        for table in tables:
            counts[db + '.' + table] = int(mysql('SELECT COUNT(*) FROM `' + db + '`.`' + table + '`;').strip())
    if counts['grpctest_goods_srv.goods'] != 420 or counts['grpctest_inventory_srv.inventory'] != 420:
        raise RuntimeError('Imported row counts differ; inspect database before retrying')
    args.marker.parent.mkdir(parents=True, exist_ok=True)
    args.marker.write_text(json.dumps({'plan_sha256': plan_hash, 'source_sha256': GOODS_SHA256, 'initial_counts': counts}, indent=2) + '\n')
    args.marker.chmod(0o600)
    print(json.dumps(counts, indent=2))

if __name__ == '__main__':
    try:
        main()
    except (ValueError, RuntimeError, OSError, subprocess.TimeoutExpired) as error:
        raise SystemExit(str(error))
