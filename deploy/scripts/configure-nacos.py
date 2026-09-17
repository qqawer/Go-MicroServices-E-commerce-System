#!/usr/bin/env python3
"""Create cloud demo configs; publish only missing configs, never replace existing ones."""
import argparse
import json
import os
from pathlib import Path
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
BASE = 'http://127.0.0.1:8848/nacos'
NAMESPACES = {
    'user': 'b8cbecc7-537f-4507-b2d2-3e18dbd787c1',
    'goods': '80f74565-a2d2-4cb1-9820-cebc8d97f58e',
    'inventory': 'e2f59a3b-5bcc-470b-ad61-8b2f0264525b',
    'order': 'ce4b3f78-1db4-4418-9cb9-a3e1bd406c88',
}

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--apply', action='store_true', help='Publish to server-local Nacos')
    args = parser.parse_args()
    settings = dict(line.split('=', 1) for line in (ROOT / '.env').read_text().splitlines()
                    if line and not line.startswith('#'))
    os.umask(0o077)
    folder = Path(settings['RUNTIME_DIR']) / 'nacos'
    folder.mkdir(exist_ok=True)
    entries = []
    for domain in NAMESPACES:
        name = 'orders-srv' if domain == 'order' else domain + '-srv'
        db = 'grpctest_orders_srv' if domain == 'order' else f'grpctest_{domain}_srv'
        config = {'name': name, 'host': domain + '-srv', 'tags': ['srv', domain],
                  'mysql': {'host': 'mysql', 'port': '3306', 'name': db, 'user': 'grpctest', 'password': settings['MYSQL_APP_PASSWORD']},
                  'consul': {'host': 'consul', 'port': 8500}}
        if domain == 'order':
            config.update(goods_srv={'name': 'goods-srv'}, inventory_srv={'name': 'inventory-srv'})
        entries.append((domain, name + '.json', config))
    for domain, port in [('user', 8021), ('goods', 8022), ('order', 8023)]:
        config = {'name': domain + '-web', 'host': domain + '-web', 'port': port, 'mode': 'release',
                  'tag': ['web', domain], 'consul': {'host': 'consul', 'port': 8500},
                  'auth': {'jwt_expire': 24, 'key': settings['JWT_KEY']},
                  'log': {'level': 'info', 'filename': domain + '_web.log', 'max_size': 20, 'maxi_size': 20,
                          'max_age': 7, 'max_backups': 3, 'nax_backups': 3}}
        if domain == 'user':
            config.update(user_srv={'name': 'user-srv', 'host': 'user-srv', 'port': 50051},
                          redis={'host': 'redis', 'port': 6379, 'password': settings['REDIS_PASSWORD'], 'db': 0, 'pool_size': 10},
                          ali_sms={'key': '', 'secret': '', 'expire': 300})
        else:
            config['goods_srv'] = {'name': 'goods-srv'}
        if domain == 'order':
            config.update(order_srv={'name': 'orders-srv'}, inventory_srv={'name': 'inventory-srv'},
                          alipay={key: '' for key in ('appid', 'private_key', 'alipaypublic_key', 'notice_url', 'return_url')})
        entries.append((domain, domain + '-web.json', config))
    for _, data_id, config in entries:
        target = folder / data_id
        if not target.exists():
            target.write_text(json.dumps(config, ensure_ascii=False, indent=2) + '\n')
    print('Prepared seven configs under runtime/nacos. SMS/payment credentials remain unconfigured.')
    if not args.apply:
        return

    token = None
    def request(path, values=None, method='GET'):
        values = dict(values or {})
        if token:
            values['accessToken'] = token
        encoded = urllib.parse.urlencode(values)
        url = BASE + path + ('?' + encoded if method == 'GET' else '')
        req = urllib.request.Request(url, data=encoded.encode() if method != 'GET' else None, method=method)
        with urllib.request.urlopen(req, timeout=15) as response:
            return json.load(response)
    def api(path, values=None, method='GET'):
        result = request(path, values, method)
        if result.get('code') != 0:
            raise RuntimeError('Nacos operation failed: ' + path)
        return result.get('data')
    try:
        request('/v1/auth/users/admin', {'password': settings['NACOS_PASSWORD']}, 'POST')
    except urllib.error.HTTPError as exc:
        if exc.code not in (400, 403, 409):
            raise
    token = request('/v1/auth/login', {'username': 'nacos', 'password': settings['NACOS_PASSWORD']}, 'POST')['accessToken']
    existing = {ns['namespace'] for ns in api('/v2/console/namespace/list')}
    for domain, namespace in NAMESPACES.items():
        if namespace not in existing:
            api('/v2/console/namespace', {'namespaceId': namespace, 'namespaceName': 'grpctest-' + domain}, 'POST')
    # Check all existing content before any config write.
    missing = []
    for domain, data_id, _ in entries:
        content = (folder / data_id).read_text()
        desired = json.loads(content)
        params = {'namespaceId': NAMESPACES[domain], 'group': 'dev', 'dataId': data_id}
        try:
            actual = api('/v2/cs/config', params)
        except urllib.error.HTTPError as exc:
            if exc.code != 404:
                raise
            actual = None
        if actual is None:
            missing.append((params, content))
        elif json.loads(actual) != desired:
            raise RuntimeError('Existing config differs; edit deliberately in Nacos: ' + data_id)
    for params, content in missing:
        api('/v2/cs/config', {**params, 'content': content, 'type': 'json'}, 'POST')
        for attempt in range(20):
            try:
                actual = api('/v2/cs/config', params)
                if json.loads(actual) == json.loads(content):
                    break
            except urllib.error.HTTPError as exc:
                if exc.code != 404:
                    raise
            time.sleep(0.5)
        else:
            raise RuntimeError('Nacos read-back verification failed: ' + params['dataId'])
    print('Nacos configuration verified. Database tables and business integrations still need initialization.')

if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, KeyError, RuntimeError) as exc:
        print('Nacos setup failed: ' + str(exc))
        raise SystemExit(1)
