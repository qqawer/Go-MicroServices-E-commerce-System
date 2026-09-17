#!/usr/bin/env python3
"""Create the Mac Web config group from cloud dev; never change the cloud group."""
from pathlib import Path
import json
import os
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
NAMESPACES = {'user': 'b8cbecc7-537f-4507-b2d2-3e18dbd787c1',
              'goods': '80f74565-a2d2-4cb1-9820-cebc8d97f58e',
              'order': 'ce4b3f78-1db4-4418-9cb9-a3e1bd406c88'}

def main():
    settings = dict(line.split('=', 1) for line in (ROOT / '.env').read_text().splitlines()
                    if line and not line.startswith('#'))
    token = None

    def request(path, values, method='GET'):
        values = dict(values)
        if token:
            values['accessToken'] = token
        encoded = urllib.parse.urlencode(values)
        url = 'http://127.0.0.1:8848/nacos' + path + ('?' + encoded if method == 'GET' else '')
        req = urllib.request.Request(url, data=encoded.encode() if method != 'GET' else None, method=method)
        with urllib.request.urlopen(req, timeout=15) as response:
            return json.load(response)

    token = request('/v1/auth/login', {'username': 'nacos', 'password': settings['NACOS_PASSWORD']}, 'POST')['accessToken']

    def read(group, namespace, dataid):
        result = request('/v2/cs/config', {'namespaceId': namespace, 'group': group, 'dataId': dataid})
        if result['code'] != 0:
            raise RuntimeError('Read Nacos config failed')
        return json.loads(result['data'])

    os.umask(0o077)
    folder = ROOT / 'runtime/nacos-local'
    folder.mkdir(exist_ok=True)
    for domain, namespace in NAMESPACES.items():
        dataid = domain + '-web.json'
        source = read('dev', namespace, dataid)
        desired = json.loads(json.dumps(source))
        desired['host'] = '127.0.0.1'
        desired['consul'] = {'host': '127.0.0.1', 'port': 18500}
        for name, port in [('user_srv', 15051), ('goods_srv', 15052), ('inventory_srv', 15053), ('order_srv', 15054)]:
            if name in desired:
                desired[name].update(target=f'passthrough:///127.0.0.1:{port}', host='127.0.0.1', port=port)
        if 'redis' in desired:
            desired['redis'].update(host='127.0.0.1', port=16379)
        try:
            previous = read('local', namespace, dataid)
        except urllib.error.HTTPError as error:
            if error.code != 404:
                raise
            previous = None
        if previous is not None and previous != desired:
            raise RuntimeError('Existing local config differs; refusing overwrite: ' + dataid)
        if previous is None:
            result = request('/v2/cs/config', {'namespaceId': namespace, 'group': 'local', 'dataId': dataid,
                                              'content': json.dumps(desired), 'type': 'json'}, 'POST')
            if result['code'] != 0:
                raise RuntimeError('Publish Nacos config failed')
        for attempt in range(20):
            try:
                if read('local', namespace, dataid) == desired:
                    break
            except urllib.error.HTTPError as error:
                if error.code != 404:
                    raise
            time.sleep(0.5)
        else:
            raise RuntimeError('Timed out verifying local config: ' + dataid)
        if read('dev', namespace, dataid) != source:
            raise RuntimeError('Cloud config changed during local setup: ' + dataid)
        (folder / dataid).write_text(json.dumps(desired, indent=2) + '\n')
        print(dataid + ': local group verified; dev unchanged')

if __name__ == '__main__':
    main()
