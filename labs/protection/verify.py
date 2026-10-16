#!/usr/bin/env python3
"""Exercise only the local synthetic goods endpoint; fault mode briefly stops lab ES."""
import argparse
from collections import Counter
import json
import http.client
from pathlib import Path
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('scenario', choices=['normal', 'empty', 'burst', 'fault'])
args = parser.parse_args()
output = Path(__file__).resolve().parents[2] / '.local/protection'
output.mkdir(parents=True, exist_ok=True)

def request(keyword='苹果'):
    start = time.monotonic()
    url = 'http://127.0.0.1:18022/g/v1/store/goods?' + urllib.parse.urlencode({'q':keyword,'pnum':12})
    try:
        response = urllib.request.urlopen(url, timeout=10)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        result = {'status':response.status, 'trace_id':response.headers.get('X-Trace-ID'),
                  'retry_after':response.headers.get('Retry-After'), 'body':json.load(response),
                  'elapsed_ms':round((time.monotonic()-start)*1000,3)}
    assert result['trace_id'] and len(result['trace_id']) == 32
    return result

def trace(result, decision, count):
    spans = []
    for _ in range(30):
        try:
            with urllib.request.urlopen('http://127.0.0.1:16686/api/v3/traces/' + result['trace_id'], timeout=5) as response:
                raw = response.read().decode()
            spans = []
            for line in raw.splitlines():
                for resource in json.loads(line).get('result', {}).get('resourceSpans', []):
                    service = next(a['value']['stringValue'] for a in resource['resource']['attributes'] if a['key'] == 'service.name')
                    for scope in resource.get('scopeSpans', []):
                        spans.extend(dict(s,service=service) for s in scope.get('spans', []))
            if len(spans) == count:
                break
        except urllib.error.HTTPError as error:
            if error.code != 404:
                raise
        time.sleep(.25)
    assert len(spans) == count, (decision, len(spans), count)
    root = next(s for s in spans if s['name'] == 'GET /g/v1/store/goods')
    attrs = {a['key']:a['value'] for a in root.get('attributes', [])}
    assert attrs['protection.result']['stringValue'] == decision, attrs
    assert (root.get('status',{}).get('code') == 2) == (result['status'] >= 500)
    if count == 1:
        assert {s['service'] for s in spans} == {'goods-web'}
    else:
        by_id = {s['spanId']:s for s in spans}
        for span in spans:
            if span['spanId'] != root['spanId']:
                assert span.get('parentSpanId') in by_id
        assert {s['service'] for s in spans} == {'goods-web','goods-srv'}
    for span in spans:
        assert span['traceId'] == result['trace_id']
        data = json.dumps(span.get('attributes',[]),ensure_ascii=False)
        assert '苹果' not in data and '火星探测器' not in data
    result['spans'] = spans
    result['jaeger_url'] = 'http://127.0.0.1:16686/trace/' + result['trace_id']
    return result

results = []
if args.scenario in ['normal','empty']:
    time.sleep(1.1)
    result = request('火星探测器' if args.scenario == 'empty' else '苹果')
    total = 0 if args.scenario == 'empty' else 5
    assert result['status'] == 200 and result['body']['total'] == total and len(result['body']['data']) == total
    assert not result['body'].get('degraded')
    results.append(trace(result,'allowed',5 if total == 0 else 6))
elif args.scenario == 'burst':
    time.sleep(1.1)
    start = time.monotonic()
    results = [request() for _ in range(20)]
    duration = time.monotonic()-start
    counts = Counter(r['status'] for r in results)
    assert duration < 1 and counts == {200:5,429:15}, (duration,counts)
    trace(next(r for r in results if r['status']==200),'allowed',6)
    trace(next(r for r in results if r['status']==429),'rate_limited',1)
    assert all(r['retry_after']=='1' and 'data' not in r['body'] for r in results if r['status']==429)
    print('burst',dict(counts),'seconds',round(duration,3),flush=True)
else:
    # First permit a healthy half-open probe if a previous run left the circuit open.
    time.sleep(3.2)
    healthy = request()
    assert healthy['status'] == 200 and healthy['body']['total'] == 5, healthy
    # Isolate error-ratio samples from earlier successful requests.
    time.sleep(10.2)
    subprocess.run(['docker','stop','grpctest-es-lab-es-1'],check=True,stdout=subprocess.DEVNULL)
    try:
        for _ in range(3):
            result = request()
            assert result['status']==503 and result['body']['code']=='GOODS_QUERY_DEPENDENCY_UNAVAILABLE',result
            results.append(result)
            time.sleep(.25)
        opened = request()
        assert opened['status']==503 and opened['body']['code']=='GOODS_QUERY_CIRCUIT_OPEN',opened
        results.append(opened)
        time.sleep(3.2)
        probe = request()
        assert probe['body']['code']=='GOODS_QUERY_DEPENDENCY_UNAVAILABLE',probe
        results.append(probe)
        reopened = request()
        assert reopened['body']['code']=='GOODS_QUERY_CIRCUIT_OPEN',reopened
        results.append(reopened)
        print('dependency failures -> open -> failed probe -> reopened verified',flush=True)
    finally:
        subprocess.run(['docker','start','grpctest-es-lab-es-1'],check=True,stdout=subprocess.DEVNULL)
        for _ in range(60):
            try:
                with urllib.request.urlopen('http://127.0.0.1:9201/_cluster/health?wait_for_status=yellow&timeout=1s',timeout=2) as response:
                    health = json.load(response)
                if health['status'] in ['yellow','green']:
                    break
            except (OSError,http.client.HTTPException):
                pass
            time.sleep(.5)
        else:
            raise AssertionError('lab ES did not recover')
    time.sleep(3.2)
    recovered = request()
    assert recovered['status']==200 and recovered['body']['total']==5,recovered
    results.append(recovered)
    again = request()
    assert again['status']==200 and again['body']['total']==5,again
    results.append(again)
    for result in results[:3]+[probe]:
        assert result['body']['degraded'] is True and 'data' not in result['body']
        trace(result,'dependency_error',5)
    trace(opened,'circuit_open',1)
    trace(reopened,'circuit_open',1)
    trace(recovered,'allowed',6)
    trace(again,'allowed',6)
(output/(args.scenario+'.json')).write_text(json.dumps({'scenario':args.scenario,'results':results},ensure_ascii=False,indent=2))
print(json.dumps({'scenario':args.scenario,'statuses':dict(Counter(r['status'] for r in results)),
                 'samples':[{'status':r['status'],'trace_id':r['trace_id'],'ms':r['elapsed_ms'],'code':r['body'].get('code'),'spans':len(r.get('spans',[]))} for r in results if 'spans' in r]},ensure_ascii=False,indent=2))
