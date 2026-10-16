#!/usr/bin/env python3
"""Local synthetic goods experiment; requires matching slow/isolation Web profile."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import http.client
import json
from pathlib import Path
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument('scenario',choices=['slow','isolation'])
args=parser.parse_args()
root=Path(__file__).resolve().parents[2]
delay_file=root/'.local/protection/query-delay-ms'
output=root/'.local/protection/advanced'
output.mkdir(parents=True,exist_ok=True)
url='http://127.0.0.1:18022/g/v1/store/goods?'+urllib.parse.urlencode({'q':'苹果','pnum':12})

def request():
    start=time.monotonic()
    try: response=urllib.request.urlopen(url,timeout=10)
    except urllib.error.HTTPError as error: response=error
    with response:
        return {'status':response.status,'trace_id':response.headers.get('X-Trace-ID'),
                'body':json.load(response),'elapsed_ms':round((time.monotonic()-start)*1000,3)}

def get_trace(trace_id,count):
    for _ in range(40):
        try:
            with urllib.request.urlopen('http://127.0.0.1:16686/api/v3/traces/'+trace_id,timeout=5) as response: raw=response.read().decode()
            spans=[]
            for line in raw.splitlines():
                for resource in json.loads(line).get('result',{}).get('resourceSpans',[]):
                    service=next(a['value']['stringValue'] for a in resource['resource']['attributes'] if a['key']=='service.name')
                    for scope in resource.get('scopeSpans',[]): spans.extend(dict(s,service=service) for s in scope.get('spans',[]))
            if len(spans)==count: return spans
        except urllib.error.HTTPError as error:
            if error.code!=404: raise
        time.sleep(.25)
    raise AssertionError(('trace incomplete',trace_id,count))

def verify(result,decision,count):
    spans=get_trace(result['trace_id'],count)
    http=next(s for s in spans if s['name']=='GET /g/v1/store/goods')
    attrs={a['key']:a['value'] for a in http.get('attributes',[])}
    assert attrs['protection.result']['stringValue']==decision,attrs
    if decision=='circuit_open': assert attrs['protection.breaker']['stringValue']=='SlowRequestRatio',attrs
    assert (http.get('status',{}).get('code')==2)==(result['status']>=500)
    by_id={s['spanId']:s for s in spans}
    for span in spans:
        assert span['traceId']==result['trace_id']
        if span is not http: assert span.get('parentSpanId') in by_id
        assert '苹果' not in json.dumps(span.get('attributes',[]),ensure_ascii=False)
    if count==1: assert {s['service'] for s in spans}=={'goods-web'}
    if result['status']==200: assert result['body']['total']==5 and len(result['body']['data'])==5 and not result['body'].get('degraded')
    result['spans']=spans
    result['jaeger_url']='http://127.0.0.1:16686/trace/'+result['trace_id']
    return result

results=[]
try:
    if args.scenario=='slow':
        delay_file.write_text('400\n')
        for _ in range(3):
            result=request();assert result['status']==200,result;results.append(result)
        opened=request();assert opened['body']['code']=='GOODS_QUERY_CIRCUIT_OPEN',opened;results.append(opened)
        time.sleep(3.2)
        probe=request();assert probe['status']==200,probe;results.append(probe)
        reopened=request();assert reopened['body']['code']=='GOODS_QUERY_CIRCUIT_OPEN',reopened;results.append(reopened)
        delay_file.write_text('0\n')
        time.sleep(3.2)
        recovered=request();assert recovered['status']==200,recovered;results.append(recovered)
        again=request();assert again['status']==200,again;results.append(again)
        for result in results[:3]+[probe]:
            verify(result,'allowed',7)
            wait=next(s for s in result['spans'] if s['name']=='lab.goods.delay')
            assert int(wait['endTimeUnixNano'])-int(wait['startTimeUnixNano'])>=390_000_000
            assert all(s.get('status',{}).get('code')!=2 for s in result['spans'])
        for result in [opened,reopened]: verify(result,'circuit_open',1)
        for result in [recovered,again]: verify(result,'allowed',6)
    else:
        delay_file.write_text('1200\n')
        barrier=threading.Barrier(8)
        def simultaneous(): barrier.wait();return request()
        with ThreadPoolExecutor(max_workers=8) as pool: results=list(pool.map(lambda _:simultaneous(),range(8)))
        allowed=[r for r in results if r['status']==200]
        blocked=[r for r in results if r['status']==503]
        assert len(allowed)==2 and len(blocked)==6,results
        for result in allowed: verify(result,'allowed',7)
        for result in blocked:
            assert result['body']['code']=='GOODS_QUERY_BUSY' and result['body']['degraded'] and 'data' not in result['body']
            verify(result,'concurrency_limited',1)
        # Only compare timestamps emitted by the same Web process, avoiding clock skew.
        events=[]
        for result in allowed:
            rpc=next(s for s in result['spans'] if s['name']=='Goods/GoodsList' and s['service']=='goods-web')
            events.extend([(int(rpc['startTimeUnixNano']),1),(int(rpc['endTimeUnixNano']),-1)])
        running=peak=0
        for _,change in sorted(events): running+=change;peak=max(peak,running)
        assert peak==2
        delay_file.write_text('0\n')
        released=request();assert released['status']==200,released;verify(released,'allowed',6);results.append(released)
        # Occupy both slots, then actually disconnect the two HTTP clients.
        delay_file.write_text('4000\n')
        cancelled=[];connections=[]
        for _ in range(2):
            trace_id=uuid.uuid4().hex
            connection=http.client.HTTPConnection('127.0.0.1',18022,timeout=5)
            connection.request('GET',url.split('18022',1)[1],headers={'traceparent':'00-'+trace_id+'-2222222222222222-01'})
            connections.append(connection);cancelled.append({'trace_id':trace_id})
        time.sleep(.25)
        busy=request();assert busy['body']['code']=='GOODS_QUERY_BUSY',busy;results.append(busy)
        start=time.monotonic()
        for connection in connections: connection.close()
        delay_file.write_text('0\n')
        for _ in range(20):
            released=request()
            if released['status']==200: break
            assert released['body']['code']=='GOODS_QUERY_BUSY',released
            time.sleep(.1)
        else: raise AssertionError('cancelled clients leaked isolation slots')
        assert time.monotonic()-start<2
        verify(released,'allowed',6);results.append(released)
        verify(busy,'concurrency_limited',1)
        for result in cancelled:
            spans=get_trace(result['trace_id'],4)
            http=next(s for s in spans if s['name']=='GET /g/v1/store/goods')
            wait=next(s for s in spans if s['name']=='lab.goods.delay')
            assert any(a['key']=='http.response.status_code' and int(a['value']['intValue'])==408 for a in http['attributes'])
            assert int(wait['endTimeUnixNano'])-int(wait['startTimeUnixNano'])<3_000_000_000
            assert not any(s['name'] in ['goods.search','elasticsearch.search'] for s in spans)
            result.update(status='client disconnected',spans=spans)
        results.extend(cancelled)
finally:
    # The control file is always reset; it is never exposed through an HTTP parameter.
    delay_file.write_text('0\n')
(output/(args.scenario+'.json')).write_text(json.dumps({'scenario':args.scenario,'results':results},ensure_ascii=False,indent=2))
print(json.dumps({'scenario':args.scenario,'results':[{'status':r['status'],'ms':r.get('elapsed_ms'),'code':r.get('body',{}).get('code'),'trace_id':r['trace_id'],'spans':len(r['spans'])} for r in results]},ensure_ascii=False,indent=2))
