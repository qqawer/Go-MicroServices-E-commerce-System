#!/usr/bin/env python3
"""Read-only verification of local HTTP -> gRPC -> ES/MySQL trace parentage."""
import argparse
import json
from pathlib import Path
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('scenario', choices=['success', 'empty', 'failure'])
parser.add_argument('--propagate', action='store_true', help='simulate an upstream W3C traceparent')
args = parser.parse_args()
root = Path(__file__).resolve().parents[2] / '.local/tracing'
root.mkdir(parents=True, exist_ok=True)
trace_id = uuid.uuid4().hex
parent_id = '2222222222222222'
keyword = '火星探测器' if args.scenario == 'empty' else '苹果'
params = urllib.parse.urlencode({'q': keyword, 'pnum': 12, 'probe': 'private-trace-probe'})
request_headers = {'Authorization': 'Bearer private-test-token'}
if args.propagate:
    request_headers['traceparent'] = '00-' + trace_id + '-' + parent_id + '-01'
req = urllib.request.Request('http://127.0.0.1:18022/g/v1/store/goods?' + params,
    headers=request_headers)
try:
    with urllib.request.urlopen(req, timeout=10) as res:
        status, headers, response = res.status, res.headers, json.load(res)
except urllib.error.HTTPError as res:
    status, headers, response = res.code, res.headers, json.load(res)
received_trace_id = headers.get('X-Trace-ID')
assert received_trace_id and len(received_trace_id) == 32, 'HTTP trace ID missing'
if args.propagate:
    assert received_trace_id == trace_id, 'HTTP context propagation failed'
trace_id = received_trace_id
if args.scenario == 'failure':
    assert status == 503 and response == {'msg': '商品服务暂不可用'}, (status, response)
else:
    total = 0 if args.scenario == 'empty' else 5
    assert status == 200 and response['search_index'] and response['total'] == total
    assert len(response['data']) == total

want_count = 6 if args.scenario == 'success' else 5
spans = []
for attempt in range(30):
    try:
        with urllib.request.urlopen('http://127.0.0.1:16686/api/v3/traces/' + trace_id, timeout=5) as res:
            raw = res.read().decode()
        spans = []
        for line in raw.splitlines():
            result = json.loads(line).get('result', {})
            for resource in result.get('resourceSpans', []):
                attrs = resource['resource'].get('attributes', [])
                service = next(a['value']['stringValue'] for a in attrs if a['key'] == 'service.name')
                for scope in resource.get('scopeSpans', []):
                    for span in scope.get('spans', []):
                        spans.append(dict(span, service=service))
        if len(spans) == want_count:
            break
    except urllib.error.HTTPError as err:
        if err.code != 404:
            raise
    time.sleep(.5)
assert len(spans) == want_count, ('missing spans', spans)
assert {s['traceId'] for s in spans} == {trace_id}
assert {s['service'] for s in spans} == {'goods-web', 'goods-srv'}
http = next(s for s in spans if s['name'] == 'GET /g/v1/store/goods')
client = next(s for s in spans if s['name'] == 'Goods/GoodsList' and s['kind'] == 3)
server = next(s for s in spans if s['name'] == 'Goods/GoodsList' and s['kind'] == 2)
search = next(s for s in spans if s['name'] == 'goods.search')
es = next(s for s in spans if s['name'] == 'elasticsearch.search')
assert http.get('parentSpanId', '') == (parent_id if args.propagate else '')
for child, parent in [(client, http), (server, client), (search, server), (es, search)]:
    assert child['parentSpanId'] == parent['spanId'], (child['name'], 'broken parent')
if args.scenario == 'success':
    db = next(s for s in spans if s['name'] == 'mysql.goods.read')
    assert db['parentSpanId'] == search['spanId']
else:
    assert not any(s['name'] == 'mysql.goods.read' for s in spans)
if args.scenario == 'failure':
    assert all(s.get('status', {}).get('code') == 2 for s in spans), 'error status missing'
else:
    assert all(s.get('status', {}).get('code', 0) != 2 for s in spans)
for s in spans:
    text = json.dumps({'name': s['name'], 'attrs': s.get('attributes', []), 'events': s.get('events', [])}, ensure_ascii=False)
    assert all(secret not in text for secret in [keyword, 'private-trace-probe', 'private-test-token'])
    assert int(s['startTimeUnixNano']) <= int(s['endTimeUnixNano'])
summary = [{'service': s['service'], 'name': s['name'], 'kind': s['kind'],
            'duration_ms': round((int(s['endTimeUnixNano']) - int(s['startTimeUnixNano'])) / 1e6, 3),
            'error': s.get('status', {}).get('code') == 2} for s in spans]
evidence = {'scenario': args.scenario, 'trace_id': trace_id, 'http_status': status,
            'response': response, 'spans': spans, 'summary': summary,
            'jaeger_url': 'http://127.0.0.1:16686/trace/' + trace_id}
(root / (args.scenario + '.json')).write_text(json.dumps(evidence, ensure_ascii=False, indent=2))
print(json.dumps({k: evidence[k] for k in ['scenario', 'trace_id', 'http_status', 'summary', 'jaeger_url']}, ensure_ascii=False, indent=2))
