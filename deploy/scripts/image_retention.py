#!/usr/bin/env python3
"""Keep recent application revisions; never prune volumes, containers or build caches."""
import argparse
import collections
import datetime
import fcntl
import json
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[1]


def docker(*args):
    return subprocess.check_output(['docker', *args], text=True, timeout=90)


def candidates(images, protected, owner, now):
    allowed = {f'ghcr.io/{owner}/grpctest-{s}' for s in ('user_srv', 'goods_srv', 'inventory_srv', 'order_srv')}
    allowed |= {f'ghcr.io/{owner}/grpctest_web-{s}' for s in ('user-web', 'goods-web', 'order-web')}
    groups = collections.defaultdict(list)
    for image in images:
        digests = image.get('RepoDigests') or []
        repos = {ref.split('@')[0] for ref in digests}
        labels = image.get('Config', {}).get('Labels') or {}
        revision = labels.get('org.opencontainers.image.revision', '')
        if len(repos) != 1 or not repos <= allowed or not re.fullmatch('[0-9a-f]{40}', revision):
            continue
        repo = next(iter(repos))
        source = 'grpctest_web' if '/grpctest_web-' in repo else 'grpctest'
        if labels.get('org.opencontainers.image.source') != f'https://github.com/{owner}/{source}':
            continue
        # Unknown/local aliases are not disposable registry copies.
        if any(not (tag.startswith(repo + '@sha256:') or tag.startswith(repo + ':sha-'))
               for tag in image.get('RepoTags') or []):
            continue
        groups[repo].append(image)
    result = []
    for images in groups.values():
        recent = []
        for image in sorted(images, key=lambda x: x['Created'], reverse=True):
            revision = image['Config']['Labels']['org.opencontainers.image.revision']
            if revision not in recent:
                recent.append(revision)
        for image in images:
            revision = image['Config']['Labels']['org.opencontainers.image.revision']
            created = datetime.datetime.fromisoformat(image['Created'].replace('Z', '+00:00'))
            references = {image['Id'], *(image.get('RepoDigests') or []), *(image.get('RepoTags') or [])}
            if revision in recent[:3] or references & protected or now - created < datetime.timedelta(days=1):
                continue
            result.append(image)
    return result


def prune(owner, state, protected_refs=(), apply=False):
    if not re.fullmatch('[a-z0-9][a-z0-9-]*', owner):
        raise ValueError('Invalid registry owner')
    ids = sorted(set(docker('image', 'ls', '-q', '--no-trunc').split()))
    if not ids:
        return
    images = json.loads(docker('image', 'inspect', *ids))
    containers = docker('ps', '-aq').split()
    protected = {x['Image'] for x in json.loads(docker('inspect', *containers))} if containers else set()
    protected.update(protected_refs)
    current = state / 'images.env'
    if current.exists():
        protected.update(line.split('=', 1)[1] for line in current.read_text().splitlines() if '=' in line)
    # Resolve manifests to image IDs as well, especially multi-platform images.
    refs = sorted(protected)
    if refs:
        protected.update(x['Id'] for x in json.loads(docker('image', 'inspect', *refs)))
    selected = candidates(images, protected, owner, datetime.datetime.now(datetime.timezone.utc))
    report = {'apply': apply, 'candidates': [{k: x[k] for k in ('Id', 'RepoDigests', 'Created')} for x in selected],
              'deleted': [], 'skipped_in_use': []}
    report_path = state / 'image-retention-last.json'
    if apply:
        report_path.write_text(json.dumps(report, indent=2) + '\n')
        report_path.chmod(0o600)
    for image in selected:
        if not apply:
            continue
        image_id = image['Id']
        if docker('ps', '-aq', '--filter', 'ancestor=' + image_id).strip():
            report['skipped_in_use'].append(image_id)
            continue
        # No --force: Docker protects newly referenced images too.
        docker('image', 'rm', image_id)
        report['deleted'].append(image_id)
        report_path.write_text(json.dumps(report, indent=2) + '\n')
    if apply:
        report_path.write_text(json.dumps(report, indent=2) + '\n')
    print(f'Image retention: {len(selected)} candidates, {len(report["deleted"])} removed; '
          'running/stopped containers and recent 3 revisions protected')
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    settings = dict(line.split('=', 1) for line in (ROOT / '.env').read_text().splitlines()
                    if line and not line.startswith('#'))
    state = Path(settings['RUNTIME_DIR']) / 'state'
    with (state / 'release.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        prune(settings['GHCR_OWNER'], state, apply=args.apply)


if __name__ == '__main__':
    main()
