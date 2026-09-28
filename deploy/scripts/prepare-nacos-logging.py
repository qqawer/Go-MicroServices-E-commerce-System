#!/usr/bin/env python3
"""Derive smaller log retention from the pinned Nacos image without changing log levels."""
import hashlib
import io
import json
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parents[1]
SOURCE_SHA256 = 'f1f63d558fa80ca1c56ecb2e582e3fca9fe7beb611ccbba79377bc6dd5c9cc33'
LIMITS = {'maxFileSize': '10MB', 'maxHistory': '2', 'totalSizeCap': '30MB', 'cleanHistoryOnStart': 'true'}


def bounded_config(source):
    if hashlib.sha256(source).hexdigest() != SOURCE_SHA256:
        raise ValueError('Nacos logging source changed; review the pinned image before generating retention config')
    tree = ET.fromstring(source)
    policies = tree.findall('.//rollingPolicy')
    if len(policies) != 32 or any(p.get('class') != 'ch.qos.logback.core.rolling.SizeAndTimeBasedRollingPolicy'
                                  for p in policies):
        raise ValueError('Unexpected Nacos rolling policies')
    text = source.decode('utf-8')
    # Preserve upstream license, logging destinations/levels and Spring elements.
    for name, value in LIMITS.items():
        text, count = re.subn(f'<{name}>[^<]*</{name}>', f'<{name}>{value}</{name}>', text)
        if count != len(policies):
            raise ValueError(f'Unexpected Nacos logging setting: {name}')
    ET.fromstring(text)
    return text


def main():
    settings = dict(line.split('=', 1) for line in (ROOT / '.env').read_text().splitlines()
                    if line and not line.startswith('#'))
    compose = ['docker', 'compose', '--env-file', str(ROOT / '.env'), '-f', str(ROOT / 'compose.yaml')]
    config = json.loads(subprocess.check_output(compose + ['config', '--format', 'json'], text=True))
    image = config['services']['nacos']['image']
    if '@sha256:' not in image or not image.startswith('nacos/nacos-server:'):
        raise ValueError('Expected a pinned official Nacos image')
    inspected = subprocess.run(['docker', 'image', 'inspect', image], capture_output=True)
    if inspected.returncode:
        subprocess.run(['docker', 'pull', image], check=True, timeout=300)
    # Never start the extraction container; it cannot run or write business data.
    cid = subprocess.check_output(['docker', 'create', '--network', 'none', '--entrypoint', '/bin/true', image], text=True).strip()
    try:
        archive = subprocess.check_output(['docker', 'cp', cid + ':/home/nacos/conf/nacos-logback.xml', '-'], timeout=30)
        with tarfile.open(fileobj=io.BytesIO(archive)) as tar:
            source = tar.extractfile('nacos-logback.xml').read()
        generated = bounded_config(source)
    finally:
        subprocess.run(['docker', 'rm', cid], check=True, stdout=subprocess.DEVNULL, timeout=30)
    destination = Path(settings['RUNTIME_DIR']) / 'logging' / 'nacos-logback.xml'
    destination.parent.mkdir(parents=True, exist_ok=True)
    if destination.exists() and destination.read_text() == generated:
        print('Nacos logging retention already prepared')
        return
    with tempfile.NamedTemporaryFile(mode='w', dir=destination.parent, delete=False) as handle:
        handle.write(generated)
        temporary = Path(handle.name)
    temporary.chmod(0o644)
    temporary.replace(destination)
    print('Prepared 32 Nacos log policies: 10 MiB active / 30 MiB archives each, 2 past days; '
          'recreate nacos to activate the mounted config')


if __name__ == '__main__':
    main()
