#!/usr/bin/env python3
"""Read-only parentage/privacy checks for a local order Trace already in Jaeger."""
import argparse
import json
from pathlib import Path
import time
import urllib.error
import urllib.request

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('scenario', choices=['success', 'replay', 'stock-shortage', 'goods-unavailable', 'recovered'])
parser.add_argument('trace_id', help='X-Trace-ID from POST /trace-demo/orders')
args = parser.parse_args()
assert len(args.trace_id) == 32 and all(c in '0123456789abcdef' for c in args.trace_id)
count = {'replay': 5, 'goods-unavailable': 6}.get(args.scenario, 14)
spans = []
for _ in range(30):
    try:
        with urllib.request.urlopen('http://127.0.0.1:16686/api/v3/traces/' + args.trace_id, timeout=5) as res:
            raw = res.read().decode()
        spans = []
        for line in raw.splitlines():
            for resource in json.loads(line).get('result', {}).get('resourceSpans', []):
                service = next(a['value']['stringValue'] for a in resource['resource']['attributes'] if a['key'] == 'service.name')
                for scope in resource.get('scopeSpans', []):
                    spans.extend(dict(s, service=service) for s in scope.get('spans', []))
        if len(spans) == count:
            break
    except urllib.error.HTTPError as err:
        if err.code != 404:
            raise
    time.sleep(.5)
assert len(spans) == count, ('missing/unexpected spans', len(spans), count)
by_key = {(s['service'], s['name']): s for s in spans}
assert len(by_key) == count, 'duplicate operations'
expected = [
    ('order-web', 'POST /trace-demo/orders', None),
    ('order-web', 'Order/CreateOrder', ('order-web', 'POST /trace-demo/orders')),
    ('order-srv', 'Order/CreateOrder', ('order-web', 'Order/CreateOrder')),
    ('order-srv', 'order.create', ('order-srv', 'Order/CreateOrder')),
    ('order-srv', 'order.prepare', ('order-srv', 'order.create')),
]
if args.scenario != 'replay':
    expected.append(('order-srv', 'Goods/BatchGetGoods', ('order-srv', 'order.prepare')))
if count == 14:
    expected.extend([
        ('goods-srv', 'Goods/BatchGetGoods', ('order-srv', 'Goods/BatchGetGoods')),
        ('goods-srv', 'mysql.goods.batch_read', ('goods-srv', 'Goods/BatchGetGoods')),
        ('order-srv', 'rocketmq.order.prepare', ('order-srv', 'order.create')),
        ('order-srv', 'order.transaction', ('order-srv', 'rocketmq.order.prepare')),
        ('order-srv', 'Inventory/Sell', ('order-srv', 'order.transaction')),
        ('inventory-srv', 'Inventory/Sell', ('order-srv', 'Inventory/Sell')),
        ('inventory-srv', 'inventory.sell', ('inventory-srv', 'Inventory/Sell')),
        ('inventory-srv', 'mysql.inventory.reserve', ('inventory-srv', 'inventory.sell')),
    ])
assert set(by_key) == {(svc, name) for svc, name, _ in expected}
for service, name, parent in expected:
    span = by_key[service, name]
    assert span['traceId'] == args.trace_id
    assert span.get('parentSpanId', '') == (by_key[parent]['spanId'] if parent else ''), ('broken parent', service, name)
    assert int(span['endTimeUnixNano']) >= int(span['startTimeUnixNano'])
    data = json.dumps({'name': name, 'attributes': span.get('attributes', []), 'events': span.get('events', [])})
    for private in ['trace-only-name', 'trace-only-address', '13800000000', 'trace-only', 'Bearer ', 'eyJ', 'trace-order-']:
        assert private not in data, ('request data in trace', name)
errors = {(s['service'], s['name']) for s in spans if s.get('status', {}).get('code') == 2}
if args.scenario == 'stock-shortage':
    assert errors == {('order-srv', 'order.transaction'), ('order-srv', 'Inventory/Sell'),
                      ('inventory-srv', 'inventory.sell'), ('inventory-srv', 'mysql.inventory.reserve')}
elif args.scenario == 'goods-unavailable':
    assert len(errors) == 6
else:
    assert not errors
out = Path(__file__).resolve().parents[2] / '.local/tracing/orders'
out.mkdir(parents=True, exist_ok=True)
(out / (args.scenario + '-verified.json')).write_text(json.dumps({'scenario': args.scenario, 'trace_id': args.trace_id, 'spans': spans}, ensure_ascii=False, indent=2))
print(json.dumps({'scenario': args.scenario, 'spans': count, 'services': sorted({s['service'] for s in spans}), 'error_spans': len(errors), 'jaeger_url': 'http://127.0.0.1:16686/trace/' + args.trace_id}, ensure_ascii=False))
