#!/usr/bin/env python3
"""Disposable local HTTP -> TCP gRPC -> MySQL benchmark. Never accepts a remote URL."""
import argparse
from collections import Counter, defaultdict
from datetime import datetime, timezone
import gzip
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import secrets
import shutil
import signal
import subprocess
import sys
import threading
import time
from urllib.request import build_opener, ProxyHandler

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
sys.path.insert(0, str(ROOT / 'tests'))
from env import Environment, execute
from fixtures import generate


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2, ensure_ascii=False) + '\n')
    path.chmod(0o600)


def command(args, cwd=ROOT, timeout=180):
    return subprocess.check_output(args, cwd=cwd, text=True, timeout=timeout).strip()


def stop_process(process):
    if process and process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=12)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)


def ready(process, path):
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError(f'benchmark service exited; inspect {path.parent}')
        if path.exists():
            try:
                return json.loads(path.read_text())['address']
            except (json.JSONDecodeError, KeyError):
                pass
        time.sleep(0.1)
    raise RuntimeError('benchmark service readiness timed out')


def percentile(values, fraction):
    if not values:
        return None
    values = sorted(values)
    index = (len(values) - 1) * fraction
    lo = int(index)
    hi = min(lo + 1, len(values) - 1)
    return values[lo] + (values[hi] - values[lo]) * (index - lo)


def summarize_points(path, seconds):
    """Count business outcomes directly; a 200 with wrong JSON is not successful QPS."""
    outcomes = Counter()
    latencies = defaultdict(list)
    timestamps = []
    attempts = successes = dropped = http_requests = 0
    with gzip.open(path, 'rt') as stream:
        for line in stream:
            item = json.loads(line)
            if item.get('type') != 'Point':
                continue
            metric, data = item['metric'], item['data']
            value = data['value']
            if metric == 'business_outcomes':
                outcomes[data['tags']['outcome']] += int(value)
                if 'time' in data:
                    timestamps.append(datetime.fromisoformat(data['time'].replace('Z', '+00:00')).timestamp())
            elif metric == 'business_attempts':
                attempts += int(value)
            elif metric == 'business_successes':
                successes += int(value)
            elif metric == 'dropped_iterations':
                dropped += int(value)
            elif metric == 'http_reqs':
                http_requests += int(value)
            elif metric == 'business_duration':
                latencies['all'].append(value)
                latencies[data['tags']['case']].append(value)
    if not attempts or attempts != sum(outcomes.values()) or attempts != http_requests or successes != outcomes['ok']:
        raise RuntimeError('incomplete/inconsistent benchmark output; do not report successful throughput')
    if attempts != len(latencies['all']):
        raise RuntimeError('missing request latency samples')
    timing = {key: dict(samples=len(values), p50_ms=percentile(values, .5),
                        p95_ms=percentile(values, .95), p99_ms=percentile(values, .99),
                        max_ms=max(values)) for key, values in latencies.items()}
    # Counts / scheduled injection window. Requests may finish in the separately
    # recorded grace period, so this is not a completion-window throughput rate.
    span = max(timestamps) - min(timestamps) if timestamps else None
    return dict(attempts=attempts, successes=successes, http_requests=http_requests,
                response_timestamp_span_seconds=span,
                sent_rps=attempts / seconds, successful_qps=successes / seconds,
                error_rate=(attempts - successes) / attempts, outcomes=dict(outcomes),
                dropped_iterations=dropped, latency=timing)


def sql_snapshot(env):
    query = """SELECT COALESCE(JSON_ARRAYAGG(JSON_OBJECT(
      'digest',DIGEST, 'text',DIGEST_TEXT, 'count',COUNT_STAR,
      'seconds',SUM_TIMER_WAIT/1000000000000, 'rows_examined',SUM_ROWS_EXAMINED,
      'rows_sent',SUM_ROWS_SENT)), JSON_ARRAY())
      FROM performance_schema.events_statements_summary_by_digest
      WHERE SCHEMA_NAME='grpctest_goods_srv' AND DIGEST_TEXT LIKE 'SELECT%';"""
    return json.loads(env.sql(query))


def sql_delta(before, after):
    old = {row['digest']: row for row in before}
    result = []
    for row in after:
        base = old.get(row['digest'], {})
        delta = {key: row[key] - base.get(key, 0) for key in ('count', 'seconds', 'rows_examined', 'rows_sent')}
        if delta['count']:
            result.append(dict(digest=row['digest'], text=row['text'], **delta))
    return sorted(result, key=lambda row: row['seconds'], reverse=True)


def sql_trace(path, offset):
    # Read production GORM output after the load stops; add no query callbacks.
    with path.open('rb') as stream:
        stream.seek(offset)
        text = stream.read().decode('utf-8')
    text = re.sub(r'\x1b\[[0-9;]*m', '', text)
    groups = defaultdict(list)
    for milliseconds, statement in re.findall(r'\[([0-9.]+)ms\]\s+\[rows:[^\]]+\]\s+(SELECT[^\n]+)', text):
        normalized = re.sub(r"'[^']*'", '?', statement)
        normalized = re.sub(r'\b\d+(?:\.\d+)?\b', '?', normalized)
        normalized = re.sub(r'\(\?(?:,\?)+\)', '(?...)', normalized)
        groups[normalized].append(float(milliseconds))
    return [dict(text=statement, count=len(values), p50_trace_ms=percentile(values, .5),
                 p95_trace_ms=percentile(values, .95)) for statement, values in sorted(groups.items())]


def sample_resources(env, mysql, processes, output, done):
    # Monitoring is identical for all measured runs, limited to owned processes/DB.
    with output.open('w') as handle:
        while not done.is_set():
            sample = {'utc': datetime.now(timezone.utc).isoformat()}
            try:
                sample['processes'] = command(['ps', '-o', 'pid=,pcpu=,time=,rss=,comm=', '-p',
                                                ','.join(str(p.pid) for p in processes)], timeout=5)
                sample['mysql'] = json.loads(execute(env.docker + ['stats', '--no-stream', '--format', '{{json .}}', mysql], timeout=8).stdout)
                sample['loadavg'] = os.getloadavg()
            except (OSError, RuntimeError, subprocess.SubprocessError, json.JSONDecodeError) as exc:
                sample['sampling_error'] = type(exc).__name__
            handle.write(json.dumps(sample) + '\n')
            handle.flush()
            done.wait(5)


def preflight(base, manifest):
    opener = build_opener(ProxyHandler({}))
    def get(path):
        with opener.open(base + path, timeout=10) as response:
            return json.load(response)
    assert get('/health')['status'] == 'healthy'
    for expected in manifest['details'][:16]:
        data = get('/g/v1/goods/' + str(expected['id']))
        for key in ('id', 'name', 'shop_price', 'brand', 'ctegory'):
            if data[key] != expected[key]:
                raise RuntimeError(f'preflight detail mismatch: {key}')
    for name, c in manifest['cases'].items():
        data = get(c['path'])
        if data['total'] != c['total'] or len(data['data']) != c['page_size']:
            raise RuntimeError(f'preflight case mismatch: {name}')


def run_case(env, tools, services, base, manifest_path, directory, case, rate, seconds, vus, kind):
    directory.mkdir(mode=0o700)
    settings = dict({k: v for k, v in os.environ.items() if not k.startswith('K6_')}, K6_NO_USAGE_REPORT='true', K6_NO_COLOR='true',
                    BASE_URL=base, MANIFEST=str(manifest_path), CASE=case, RATE=str(rate),
                    SECONDS=str(seconds), VUS=str(vus), SUMMARY=str(directory / 'k6-summary.json'))
    # Ignore externally configured k6 options/outputs; telemetry remains local.
    before = sql_snapshot(env)
    rpc_log = directory.parent / 'rpc.log'
    rpc_log_offset = rpc_log.stat().st_size
    mysql = env.container('mysql')
    started = time.monotonic()
    utc_started = datetime.now(timezone.utc)
    process = None
    done = threading.Event()
    monitor = None
    try:
        with (directory / 'k6.log').open('w') as log:
            process = subprocess.Popen([str(tools['k6']), 'run', '--quiet', '--no-usage-report',
                '--config', str(tools['empty_config']), '--out', 'json=' + str(directory / 'points.json.gz'),
                str(HERE / 'goods.js')], env=settings, stdout=log, stderr=subprocess.STDOUT)
            monitor = threading.Thread(target=sample_resources, args=(env, mysql, [*services, process], directory / 'resources.jsonl', done), daemon=True)
            monitor.start()
            code = process.wait(timeout=seconds + 60)
    finally:
        stop_process(process)
        done.set()
        if monitor:
            monitor.join(timeout=15)
    wall_seconds = time.monotonic() - started
    utc_finished = datetime.now(timezone.utc)
    elapsed_utc = (utc_finished - utc_started).total_seconds()
    if code not in (0, 99):
        raise RuntimeError(f'k6 exited {code}; inspect {directory / "k6.log"}')
    result = summarize_points(directory / 'points.json.gz', seconds)
    result.update(case=case, target_rps=rate, seconds=seconds, vus=vus, kind=kind,
                  wall_seconds=wall_seconds, utc_started=utc_started.isoformat(),
                  utc_finished=utc_finished.isoformat(), elapsed_utc_seconds=elapsed_utc, k6_exit_code=code)
    span = result['response_timestamp_span_seconds']
    result['timing_valid'] = abs(elapsed_utc - wall_seconds) < 2 and span is not None and seconds - 2 <= span <= seconds + 10
    result['sql'] = sql_delta(before, sql_snapshot(env))
    result['gorm_trace'] = sql_trace(rpc_log, rpc_log_offset)
    result['sql_notes'] = ('PFS digest coverage is partial for prepared statements. GORM trace counts include them; '
                           'parent Find trace timing includes Preload, so do not sum trace times as independent SQL latency.')
    lat = result['latency']['all']
    result['passes_gates'] = (result['timing_valid'] and result['error_rate'] <= .001 and not result['dropped_iterations'] and
                              lat['p95_ms'] <= 300 and lat['p99_ms'] <= 1000 and code == 0)
    write_json(directory / 'result.json', result)
    if not result['timing_valid']:
        raise RuntimeError('clock/suspend or incomplete timing window detected; discard this round and rerun while awake')
    print(f"{kind} {case} rate={rate}: success={result['successes']}/{result['attempts']} "
          f"P95={lat['p95_ms']:.1f}ms P99={lat['p99_ms']:.1f}ms dropped={result['dropped_iterations']}", flush=True)
    # Fail immediately on wrong data, so subsequent loads cannot hide a bad fixture.
    if result['outcomes'].get('invalid_data') or result['outcomes'].get('invalid_json'):
        raise RuntimeError('business data validation failed; pressure stopped')
    return result


def capture_explain(env, out):
    queries = {
        'list_count': 'SELECT count(*) FROM goods WHERE deleted_at IS NULL',
        'list_page': 'SELECT * FROM goods WHERE deleted_at IS NULL LIMIT 20',
        'filtered_count': 'SELECT count(*) FROM goods WHERE brands_id=1 AND shop_price>=100 AND shop_price<=1000 AND deleted_at IS NULL',
        'filtered_page': 'SELECT * FROM goods WHERE brands_id=1 AND shop_price>=100 AND shop_price<=1000 AND deleted_at IS NULL LIMIT 20',
        'price_count': 'SELECT count(*) FROM goods WHERE shop_price>=100 AND shop_price<=1000 AND deleted_at IS NULL',
    }
    out.write_text('\n\n'.join(name + '\n' + query + '\n' + env.sql('USE grpctest_goods_srv; EXPLAIN ANALYZE ' + query + ';') for name, query in queries.items()) + '\n')


def run_dataset(args, tools, root_dir, size):
    env = Environment('grpctest-test-bench-' + secrets.token_hex(4))
    directory = root_dir / str(size)
    directory.mkdir(mode=0o700)
    manifest = generate(size, directory)
    processes, logs, results = [], [], []
    try:
        env.up()
        env.build_tool()
        env.dbtool('up')
        env.dbtool('seed', seed=directory / 'fixture.sql')
        env.sql('ANALYZE TABLE grpctest_goods_srv.goods, grpctest_goods_srv.brands, grpctest_goods_srv.category;')
        credentials = env.credentials()
        credentials.update(user='grpctest', password=env.state['app_password'])
        write_json(directory / 'connection.json', credentials)
        write_json(directory / 'environment.json', dict(project=env.project,
            mysql_version=env.sql('SELECT VERSION();'),
            mysql_settings=env.sql("SHOW VARIABLES WHERE Variable_name IN ('innodb_buffer_pool_size','max_connections','performance_schema','innodb_flush_log_at_trx_commit');"),
            rows=env.sql('SELECT COUNT(*) FROM grpctest_goods_srv.goods;'),
            migration_status=env.dbtool('status').stdout,
            mysql_image=command(env.docker + ['inspect', '--format', '{{.Image}}', env.container('mysql')]),
            redis_idle=True))
        log = (directory / 'rpc.log').open('w'); logs.append(log)
        rpc = subprocess.Popen([str(tools['rpc']), '--config', str(directory / 'connection.json'),
                                '--ready-file', str(directory / 'rpc-ready.json')], stdout=log, stderr=subprocess.STDOUT)
        processes.append(rpc)
        rpc_address = ready(rpc, directory / 'rpc-ready.json')
        log = (directory / 'web.log').open('w'); logs.append(log)
        web = subprocess.Popen([str(tools['web']), '--rpc', rpc_address,
            '--ready-file', str(directory / 'web-ready.json'), '--log-dir', str(directory)], stdout=log, stderr=subprocess.STDOUT)
        processes.append(web)
        base = 'http://' + ready(web, directory / 'web-ready.json')
        preflight(base, manifest)
        print(f'{size} goods: fixture and HTTP/RPC preflight passed.', flush=True)

        def measured(label, case, rate, seconds, kind):
            result = run_case(env, tools, processes, base, directory / 'manifest.json', directory / label, case, rate, seconds, args.vus, kind)
            result.update(size=size, run=label)
            results.append(result)
            write_json(directory / 'results.json', results)
            return result

        if args.profile == 'smoke':
            for case in ['detail', *manifest['cases'], 'mixed']:
                r = measured(case, case, 2, 3, 'smoke')
                if r['error_rate'] or r['dropped_iterations']:
                    raise RuntimeError('smoke failed')
        else:
            measured('warmup', 'mixed', 20, 30, 'warmup')
            for case in ['detail', *manifest['cases']]:
                measured('explore-' + case, case, 20, 30, 'exploratory')
            confirm_allowed = False
            for rate in (20, 50, 100, 200):
                r = measured(f'mixed-{rate}', 'mixed', rate, 30, 'exploratory')
                if rate == 100:
                    confirm_allowed = r['passes_gates']
                if r['error_rate'] > .01 or r['dropped_iterations']:
                    print('Stopping ramp after errors/dropped iterations.', flush=True)
                    break
            # Fixed representative rate, not a claimed maximum-capacity search.
            if size == max(args.sizes) and confirm_allowed:
                for repeat in range(1, 4):
                    measured(f'confirm-{repeat}-warmup', 'mixed', 100, 30, 'warmup')
                    r = measured(f'confirm-{repeat}', 'mixed', 100, 120, 'confirmation')
                    if r['error_rate'] > .01 or r['dropped_iterations']:
                        break
            elif size == max(args.sizes):
                print('Skipping confirmation: exploratory 100 RPS did not pass gates.', flush=True)
        capture_explain(env, directory / 'explain.txt')
        write_json(directory / 'final-sql.json', sql_snapshot(env))
    finally:
        for process in reversed(processes):
            stop_process(process)
        for log in logs:
            log.close()
        (directory / 'connection.json').unlink(missing_ok=True)
        if env.state:
            env.down()
    return results


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--profile', choices=('smoke', 'baseline'), default='smoke')
    parser.add_argument('--sizes', type=int, nargs='+', choices=(10000, 100000), default=[10000, 100000])
    parser.add_argument('--web-root', type=Path, default=ROOT.parent / 'grpctest_web')
    parser.add_argument('--k6', type=Path, default=ROOT / '.local/tools/k6-v2.3.0/k6')
    parser.add_argument('--vus', type=int, default=100)
    args = parser.parse_args()
    if not 1 <= args.vus <= 200 or len(args.sizes) != len(set(args.sizes)):
        parser.error('require 1..200 VUs and distinct dataset sizes')
    os.chdir(ROOT)
    args.web_root = args.web_root.resolve()
    args.k6 = args.k6.resolve()
    root_dir = ROOT / '.local/benchmarks' / (datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ') + '-' + secrets.token_hex(3))
    root_dir.mkdir(parents=True, mode=0o700)
    os.umask(0o077)
    tools = dict(k6=args.k6, rpc=root_dir / 'goods-rpc', web=root_dir / 'goods-web', empty_config=root_dir / 'k6-config.json')
    write_json(tools['empty_config'], {})
    print('Artifacts: ' + str(root_dir), flush=True)
    metadata = dict(profile=args.profile, sizes=args.sizes, utc=datetime.now(timezone.utc).isoformat(),
        rpc_revision=command(['git', 'rev-parse', 'HEAD']), web_revision=command(['git', 'rev-parse', 'HEAD'], args.web_root),
        rpc_worktree=command(['git', 'status', '--short']), web_worktree=command(['git', 'status', '--short'], args.web_root),
        go=command(['go', 'version']), k6=command([str(args.k6), 'version']),
        os=platform.platform(), machine=platform.machine(), cpus=os.cpu_count(),
        hardware=command(['sysctl', '-n', 'machdep.cpu.brand_string', 'hw.memsize']) if sys.platform == 'darwin' else '',
        docker=command(['docker', 'info', '--format', '{{json .}}']),
        background_containers=command(['docker', 'ps', '--format', '{{.Names}} {{.Image}}']),
        topology='k6 + Go HTTP + Go gRPC on host, MySQL in local Docker; loopback, shared resources',
        sql_logging='production GORM Info', pool='production defaults (unbounded MaxOpenConns)',
        gates=dict(p95_ms=300, p99_ms=1000, business_error_rate=.001, dropped_iterations=0))
    # Docker info may include user/home/proxy paths; private artifacts only, never copy it wholesale into Git.
    write_json(root_dir / 'metadata.json', metadata)
    source_dir = root_dir / 'source'
    source_dir.mkdir()
    for source, destination in [(HERE, source_dir / 'rpc'), (args.web_root / 'tests/performance', source_dir / 'web')]:
        shutil.copytree(source, destination, ignore=shutil.ignore_patterns('__pycache__'))
    hashes = {str(p.relative_to(source_dir)): hashlib.sha256(p.read_bytes()).hexdigest() for p in source_dir.rglob('*') if p.is_file()}
    write_json(root_dir / 'source-sha256.json', hashes)
    subprocess.run(['go', 'build', '-mod=readonly', '-o', str(tools['rpc']), './tests/performance/goods-rpc'], check=True, timeout=180)
    subprocess.run(['go', 'build', '-mod=readonly', '-o', str(tools['web']), './tests/performance/goods-web'], cwd=args.web_root, check=True, timeout=180)
    results = []
    for size in args.sizes:
        results.extend(run_dataset(args, tools, root_dir, size))
        write_json(root_dir / 'results.json', results)
    print('Completed; owned test resources removed. Artifacts: ' + str(root_dir), flush=True)


if __name__ == '__main__':
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(143))
    awake = None
    try:
        # Task-scoped idle-sleep prevention, released on exit (also if the parent dies).
        if sys.platform == 'darwin':
            awake = subprocess.Popen(['/usr/bin/caffeinate', '-i', '-w', str(os.getpid())])
        main()
    finally:
        stop_process(awake)
