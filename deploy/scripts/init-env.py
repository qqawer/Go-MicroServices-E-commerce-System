#!/usr/bin/env python3
"""Create credentials once. Never overwrite an existing environment."""
import base64
import os
from pathlib import Path
import secrets

root = Path(__file__).resolve().parents[1]
os.umask(0o077)
if (root / '.env').exists():
    raise SystemExit('.env already exists; existing credentials were not changed.')
runtime = root / 'runtime'
if runtime.exists():
    raise SystemExit('runtime already exists; inspect it before initializing a new environment.')
(runtime / 'secrets').mkdir(parents=True)
(runtime / 'config').mkdir()
(runtime / 'state').mkdir()
values = {
    'GHCR_OWNER': 'qqawer', 'DEPLOY_ROOT': str(root), 'RUNTIME_DIR': str(runtime),
    **{key: secrets.token_hex(24) for key in (
        'MYSQL_ROOT_PASSWORD', 'MYSQL_APP_PASSWORD', 'REDIS_PASSWORD',
        'NACOS_PASSWORD', 'NACOS_IDENTITY_VALUE', 'JWT_KEY')},
    'NACOS_AUTH_TOKEN': base64.b64encode(secrets.token_bytes(48)).decode(),
}
with (root / '.env').open('x') as f:
    for key, value in values.items():
        f.write(f'{key}={value}\n')
for key in ('jenkins_admin_password', 'jenkins_actions_password'):
    (runtime / 'secrets' / key).write_text(secrets.token_hex(24) + '\n')
# Mounted read-only into non-root application containers. No business credentials
# are placed in source control or in image layers.
namespaces = {
    'user': 'b8cbecc7-537f-4507-b2d2-3e18dbd787c1',
    'goods': '80f74565-a2d2-4cb1-9820-cebc8d97f58e',
    'inventory': 'e2f59a3b-5bcc-470b-ad61-8b2f0264525b',
    'order': 'ce4b3f78-1db4-4418-9cb9-a3e1bd406c88',
}
for service in ('user_srv', 'goods_srv', 'inventory_srv', 'order_srv', 'user-web', 'goods-web', 'order-web'):
    domain = service.replace('_srv', '').replace('-web', '')
    data_id = service.replace('_', '-') + '.json'
    if service == 'order_srv':
        data_id = 'orders-srv.json'
    folder = runtime / 'config' / service
    folder.mkdir()
    config = f'host: nacos\nport: 8848\nnamespace: {namespaces[domain]}\nuser: nacos\npassword: {values["NACOS_PASSWORD"]}\ndataid: {data_id}\ngroup: dev\n'
    for mode in ('debug', 'pro'):
        target = folder / f'config-{mode}.yaml'
        target.write_text(config)
        # Parent folders stay private; Docker mounts the individual file.
        target.chmod(0o644)
print('Created .env, private runtime credentials and Nacos bootstrap files. No service was started.')
