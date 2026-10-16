#!/usr/bin/env python3
"""Read-only checks of actual order -> delayed MQ -> compensation traces."""
import argparse
import json
from pathlib import Path
import time
import urllib.error
import urllib.request

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('scenario', choices=['timeout', 'retry', 'duplicate', 'compensation', 'outbox'])
parser.add_argument('trace_id')
args = parser.parse_args()
assert len(args.trace_id) == 32 and all(c in '0123456789abcdef' for c in args.trace_id)
spans = []
for _ in range(90):
    try:
        with urllib.request.urlopen('http://127.0.0.1:16686/api/v3/traces/' + args.trace_id, timeout=5) as response:
            raw = response.read().decode()
        spans = []
        for line in raw.splitlines():
            for resource in json.loads(line).get('result', {}).get('resourceSpans', []):
                service = next(a['value']['stringValue'] for a in resource['resource']['attributes'] if a['key'] == 'service.name')
                for scope in resource.get('scopeSpans', []):
                    spans.extend(dict(s, service=service) for s in scope.get('spans', []))
        consumers = [s for s in spans if s['name'] == 'order-reback process']
        successful = [s for s in consumers if s.get('status', {}).get('code') != 2]
        ready = bool(successful)
        if args.scenario == 'retry':
            ready = ready and any(s.get('status', {}).get('code') == 2 for s in consumers)
        if args.scenario == 'duplicate':
            ready = len(successful) >= 3
        if args.scenario == 'outbox':
            ready = ready and len([s for s in spans if s['name'] == 'rocketmq.send order_timeout']) >= 2
        if ready:
            break
    except urllib.error.HTTPError as error:
        if error.code != 404:
            raise
    time.sleep(1)
else:
    raise AssertionError('async completion not observed in Jaeger')
by_id = {s['spanId']: s for s in spans}
assert len(by_id) == len(spans)
assert {s['traceId'] for s in spans} == {args.trace_id}
assert {s['service'] for s in spans} == {'order-web', 'order-srv', 'goods-srv', 'inventory-srv'}
root = next(s for s in spans if s['name'] == 'POST /trace-demo/orders')
for span in spans:
    parent = span.get('parentSpanId')
    if span is not root:
        assert parent in by_id, ('missing parent', span['name'])
    else:
        assert not parent
    assert int(span['endTimeUnixNano']) >= int(span['startTimeUnixNano'])
    data = json.dumps({'name': span['name'], 'attributes': span.get('attributes', []), 'events': span.get('events', [])})
    assert not any(private in data for private in ['trace-only', '13800000000', 'Bearer ', 'eyJ', 'trace-async-'])
    if span['name'].startswith('rocketmq.send '):
        assert span['kind'] == 4
    if span['name'] in ['order_timeout process', 'order-reback process']:
        assert span['kind'] == 5
        assert by_id[parent]['name'] in ['rocketmq.send order_timeout', 'rocketmq.send order-reback', 'rocketmq.order.prepare']
    if span['name'] == 'order.close':
        assert by_id[parent]['name'] == 'order_timeout process'
    if span['name'] == 'inventory.reback':
        assert by_id[parent]['name'] == 'order-reback process'
    if span['name'] == 'mysql.inventory.release':
        assert by_id[parent]['name'] == 'inventory.reback'
if args.scenario != 'compensation':
    timeouts = [s for s in spans if s['name'] == 'order_timeout process']
    assert timeouts
    assert min(int(s['startTimeUnixNano']) for s in timeouts) > int(root['endTimeUnixNano'])
    assert any(s['name'] == 'order.close' for s in spans)
if args.scenario == 'retry':
    assert any(a['key'] == 'rocketmq.reconsume_times' and int(a['value']['intValue']) >= 1 for s in consumers for a in s.get('attributes', []))
if args.scenario == 'outbox':
    producers = [s for s in spans if s['name'] == 'rocketmq.send order_timeout']
    assert len({s['parentSpanId'] for s in producers}) == 1, 'restarted outbox lost original parent'
summary = {'scenario': args.scenario, 'trace_id': args.trace_id, 'span_count': len(spans),
           'consumer_attempts': len(consumers), 'failed_consumer_attempts': len(consumers) - len(successful),
           'jaeger_url': 'http://127.0.0.1:16686/trace/' + args.trace_id}
output = Path(__file__).resolve().parents[2] / '.local/tracing/async'
output.mkdir(parents=True, exist_ok=True)
(output / (args.scenario + '-verified.json')).write_text(json.dumps(dict(summary, spans=spans), ensure_ascii=False, indent=2))
print(json.dumps(summary, ensure_ascii=False))
