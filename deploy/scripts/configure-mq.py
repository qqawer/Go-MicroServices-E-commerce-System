#!/usr/bin/env python3
"""Enable demo order/MQ runtime in existing Nacos configs, preserving other fields."""
import argparse
import json
import os
from pathlib import Path
import time
import urllib.parse
import urllib.request

ROOT=Path(__file__).resolve().parents[1]
ENTRIES=[('ce4b3f78-1db4-4418-9cb9-a3e1bd406c88','orders-srv.json'),('e2f59a3b-5bcc-470b-ad61-8b2f0264525b','inventory-srv.json')]
def main():
 parser=argparse.ArgumentParser(description=__doc__)
 parser.add_argument('--apply',action='store_true')
 args=parser.parse_args()
 settings=dict(line.split('=',1) for line in (ROOT/'.env').read_text().splitlines() if line and not line.startswith('#'))
 def request(path,params,method='GET'):
  encoded=urllib.parse.urlencode(params)
  req=urllib.request.Request('http://127.0.0.1:8848/nacos'+path+('?' + encoded if method=='GET' else ''),data=encoded.encode() if method!='GET' else None,method=method)
  with urllib.request.urlopen(req,timeout=10) as response:return json.load(response)
 token=request('/v1/auth/login',{'username':'nacos','password':settings['NACOS_PASSWORD']},'POST')['accessToken']
 changes=[]
 for namespace,dataid in ENTRIES:
  params=dict(namespaceId=namespace,group='dev',dataId=dataid,accessToken=token)
  response=request('/v2/cs/config',params)
  if response.get('code')!=0:raise RuntimeError('Cannot read '+dataid)
  old=response['data'];cfg=json.loads(old)
  mq={'enabled':True,'host':'mqnamesrv','port':9876}
  if dataid=='orders-srv.json':mq.update(mode='demo',order_ttl_seconds=1800)
  if cfg.get('rocket')==mq:continue
  if cfg.get('rocket'):raise RuntimeError('Existing rocket configuration differs; reconcile it explicitly: '+dataid)
  cfg['rocket']=mq
  changes.append((params,old,json.dumps(cfg,ensure_ascii=False,indent=2)))
 if not args.apply:
  print('Would enable demo MQ runtime in:',[p['dataId'] for p,_,_ in changes]);return
 if not changes:print('MQ configuration already matches');return
 os.umask(0o077)
 backup=ROOT/'runtime/state'/('before-mq-config-'+time.strftime('%Y%m%dT%H%M%SZ',time.gmtime())+'.json')
 with backup.open('x') as handle:json.dump({p['dataId']:old for p,old,_ in changes},handle)
 applied=[]
 try:
  for params,old,new in changes:
   # Refuse an intervening edit; never publish a stale full document.
   if request('/v2/cs/config',params)['data']!=old:raise RuntimeError('Configuration changed during update')
   applied.append((params,old))
   response=request('/v2/cs/config',{**params,'content':new,'type':'json'},'POST')
   if response.get('code')!=0:raise RuntimeError('Nacos rejected update')
   for _ in range(20):
    if json.loads(request('/v2/cs/config',params)['data'])==json.loads(new):break
    time.sleep(.5)
   else:raise RuntimeError('Cannot verify Nacos update')
 except Exception:
  for params,old in applied:request('/v2/cs/config',{**params,'content':old,'type':'json'},'POST')
  raise
 print('Enabled demo MQ for order/inventory; private backup:',backup)
if __name__=='__main__':main()
