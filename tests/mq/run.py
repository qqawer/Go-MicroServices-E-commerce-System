#!/usr/bin/env python3
"""Real broker + separate RPC processes, using only owned disposable test data."""
from pathlib import Path
import concurrent.futures
import json
import os
import secrets
import signal
import socket
import subprocess
import sys
import time
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
from env import Environment, ROOT, execute

IMAGE='apache/rocketmq:5.5.0@sha256:7e8f6c9dbd9df742ed26ba69c00d4ad69e2f86a56f3ca7782ff8144dd0798132'
def freeport():
 with socket.socket() as s:s.bind(('127.0.0.1',0));return s.getsockname()[1]
def wait_for(check,timeout=90):
 deadline=time.monotonic()+timeout
 while time.monotonic()<deadline:
  try:
   if check():return
  except (OSError,ValueError):pass
  time.sleep(1)
 raise RuntimeError('timed out waiting for isolated MQ test condition')
def listening(port):
 with socket.create_connection(('127.0.0.1',port),timeout=1):return True

def main():
 os.chdir(ROOT)
 env=Environment('grpctest-test-mq-'+secrets.token_hex(4));processes={};logs={};containers=[];volumes=[]
 try:
  env.up();env.build_tool();env.dbtool('up');env.dbtool('seed',seed=ROOT/'tests/seed.sql')
  config=env.credentials();config.update(user='grpctest',password=env.state['app_password'])
  connection=env.directory/'connection.json'
  fd=os.open(connection,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
  with os.fdopen(fd,'w') as f:json.dump(config,f)
  nsport,brokerport=freeport(),freeport()
  ports={name:freeport() for name in ['goods','inventory','order']}
  broker_config=(ROOT/'deploy/rocketmq/broker.conf').read_text().replace('brokerIP1=mqbroker','brokerIP1=127.0.0.1').replace('listenPort=10911','listenPort='+str(brokerport))
  brokerpath=env.directory/'broker.conf';brokerpath.write_text(broker_config);brokerpath.chmod(0o644)
  label=['--label','io.grpctest.test.marker='+env.state['marker'],'--label','com.docker.compose.project='+env.project]
  def launch(service,args):
   name=env.project+'-'+service+'-1'
   execute(env.docker+['run','-d','--name',name,*label,'--label','com.docker.compose.service='+service,'--network',env.project+'_test','--network-alias',service,*args],timeout=180)
   containers.append(name);return name
  ns=launch('mqnamesrv',['-p',f'127.0.0.1:{nsport}:9876','-e','JAVA_OPT_EXT=-Xms128m -Xmx128m -Xmn64m',IMAGE,'sh','mqnamesrv'])
  wait_for(lambda:listening(nsport))
  mounts=[]
  for kind in ['store','logs']:
   volume=env.project+'-mq-'+kind
   execute(env.docker+['volume','create',*label,volume]);volumes.append(volume)
   mounts+=['-v',volume+':/home/rocketmq/'+kind]
  execute(env.docker+['run','--rm','--user','0:0','--network','none',*mounts,IMAGE,'sh','-ec','chown -R rocketmq:rocketmq /home/rocketmq/store /home/rocketmq/logs'])
  broker=launch('mqbroker',[*mounts,'-p',f'127.0.0.1:{brokerport}:{brokerport}','-v',str(brokerpath)+':/home/rocketmq/broker.conf:ro','-e','JAVA_OPT_EXT=-Xms256m -Xmx512m -Xmn128m -XX:MaxDirectMemorySize=256m',IMAGE,'sh','mqbroker','-c','/home/rocketmq/broker.conf'])
  wait_for(lambda:listening(brokerport))
  for topic in ['order-reback','order_timeout']:
   def create_topic():
    update=execute(env.docker+['exec',broker,'sh','mqadmin','updateTopic','-n','mqnamesrv:9876','-b','127.0.0.1:'+str(brokerport),'-t',topic,'-r','4','-w','4'],check=False,timeout=60)
    (env.directory/(topic+'-create.log')).write_text(update.stdout+update.stderr)
    return 'create topic to ' in update.stdout and ' success.' in update.stdout
   wait_for(create_topic)
  def routed():
   r=execute(env.docker+['exec',broker,'sh','mqadmin','topicRoute','-n','mqnamesrv:9876','-t','order-reback'],check=False,timeout=30)
   (env.directory/'route.log').write_text(r.stdout+r.stderr)
   return 'brokerAddrs' in r.stdout and 'grpctest-broker' in r.stdout
  wait_for(routed)
  binaries=env.directory/'bin';binaries.mkdir(exist_ok=True)
  for service in ports:
   execute(['go','test','-mod=readonly','-race','-tags=integration','-c','-o',str(binaries/service),'./tests/mq/'+service],env=dict(os.environ,GOWORK='off'),timeout=180)
  execute(['go','build','-mod=readonly','-o',str(binaries/'client'),'./tests/mq/client'],env=dict(os.environ,GOWORK='off'))
  variables=dict(os.environ,GOWORK='off',GRPCTEST_TEST_CONFIG=str(connection),TEST_MQ_PORT=str(nsport),**{'TEST_'+s.upper()+'_ADDRESS':'127.0.0.1:'+str(p) for s,p in ports.items()})
  def start(service):
   handle=open(env.directory/(service+'-'+secrets.token_hex(3)+'.log'),'w');logs.setdefault(service,[]).append(handle)
   p=subprocess.Popen([str(binaries/service),'-test.run=^TestMQService$','-test.timeout=12m'],env=dict(variables,TEST_RPC_ADDRESS='127.0.0.1:'+str(ports[service])),stdout=handle,stderr=subprocess.STDOUT)
   processes[service]=p
   def ready():
    if p.poll() is not None:raise RuntimeError(service+' test service exited; inspect '+handle.name)
    return listening(ports[service])
   wait_for(ready)
  def stop(service,kill=False):
   p=processes.pop(service)
   p.kill() if kill else p.terminate()
   result=p.wait(timeout=45)
   if not kill:assert result==0, service+' failed or race detector reported an error'
  def call(action,key):
   r=execute([str(binaries/'client'),action,key],env=variables,timeout=40)
   # SDK logging may precede the JSON response.
   lines=[s for s in r.stdout.splitlines() if s.startswith('{') and 'orderSn' in s]
   return json.loads(lines[-1])
  def stock():return int(env.sql('SELECT stocks FROM grpctest_inventory_srv.inventory WHERE goods=421;'))
  def cart():env.sql('INSERT INTO grpctest_orders_srv.shoppingcart (`user`,goods,nums,checked) VALUES (1,421,2,1);')
  for s in ports:start(s)
  initial=call('create','mq-normal')
  assert initial['status']=='WAIT_BUYER_PAY' and initial['totalCents']==2500,initial
  assert stock()==98
  with concurrent.futures.ThreadPoolExecutor(max_workers=10) as pool:
   results=list(pool.map(lambda _:call('create','mq-normal'),range(20)))
  assert all(x['id']==initial['id'] for x in results) and stock()==98
  print('PASS: real goods/order/inventory RPC + broker creates one order and deducts once across 20 retries',flush=True)
  stop('order',kill=True);start('order')
  assert call('get','mq-normal')['id']==initial['id']
  wait_for(lambda:call('get','mq-normal')['status']=='TRADE_CLOSED' and stock()==100,100)
  print('PASS: order process crash/restart preserves result and automatically closes/refunds at the original deadline',flush=True)
  # A direct consumer fixture has no order decision: only inventory MQ consumption can refund it.
  env.sql('DELETE FROM grpctest_inventory_srv.inventory WHERE goods=421; INSERT INTO grpctest_inventory_srv.stockselldetail(order_sn,status,detail) VALUES (\'mq-duplicate-refund\',1,\'[{"Goods":421,"Num":2}]\');')
  execute([str(binaries/'client'),'refund','mq-duplicate-refund'],env=variables,timeout=120)
  time.sleep(3)
  assert env.sql("SELECT status FROM grpctest_inventory_srv.stockselldetail WHERE order_sn='mq-duplicate-refund';")=='1'
  env.sql('INSERT INTO grpctest_inventory_srv.inventory (id,goods,stocks,version) VALUES (1,421,98,0);')
  wait_for(lambda:stock()==100)
  assert env.sql("SELECT status FROM grpctest_inventory_srv.stockselldetail WHERE order_sn='mq-duplicate-refund';")=='2'
  print('PASS: failed consumer work retries after database repair; 20 real messages restore stock only once',flush=True)
  # Broker unavailable: half-message outcome stays pending; restart loses no intent.
  execute(env.docker+['stop',broker],timeout=45)
  cart();pending=call('create','mq-offline')
  assert pending['status']=='CREATING' and stock()==100,pending
  stop('order',kill=True)
  execute(env.docker+['start',broker]);wait_for(lambda:listening(brokerport))
  env.sql("UPDATE grpctest_orders_srv.order_transaction SET created_at=DATE_SUB(NOW(3),INTERVAL 2 MINUTE) WHERE request_key='mq-offline';")
  start('order')
  wait_for(lambda:call('get','mq-offline')['status']=='CREATE_CANCELLED',90)
  assert call('create','mq-offline')['status']=='CREATE_CANCELLED' and stock()==100
  print('PASS: broker outage + process restart cancels abandoned request without duplicate order or deduction',flush=True)
  # Simulate a crash after inventory commit, before order commit. Restart recovery must release it.
  execute(env.docker+['stop',broker],timeout=45)
  pending=call('create','mq-stock-commit')
  assert pending['status']=='CREATING'
  number=pending['orderSn'];stop('order',kill=True)
  env.sql("UPDATE grpctest_inventory_srv.inventory SET stocks=98 WHERE goods=421; INSERT INTO grpctest_inventory_srv.stockselldetail VALUES ('"+number+"',1,'[{\"Goods\":421,\"Num\":2}]'); UPDATE grpctest_orders_srv.order_transaction SET created_at=DATE_SUB(NOW(3),INTERVAL 2 MINUTE) WHERE order_sn='"+number+"';")
  execute(env.docker+['start',broker]);wait_for(lambda:listening(brokerport));start('order')
  wait_for(lambda:call('get','mq-stock-commit')['status']=='CREATE_CANCELLED' and stock()==100)
  print('PASS: persisted deduction without an order converges to cancellation/refund after restart',flush=True)
  for s in list(processes):stop(s)
 finally:
  for p in processes.values():
   if p.poll() is None:
    p.terminate()
    try:p.wait(timeout=30)
    except subprocess.TimeoutExpired:p.kill();p.wait()
  for handles in logs.values():
   for f in handles:f.close()
  if env.state:
   env.ensure_owned()
   for name in containers:
    result=execute(env.docker+['logs','--tail','100',name],check=False)
    (env.directory/(name+'.log')).write_text(result.stdout+result.stderr)
    result=execute(env.docker+['exec',name,'sh','-c','tail -n 80 /home/rocketmq/logs/rocketmqlogs/broker.log /home/rocketmq/logs/rocketmqlogs/namesrv.log'],check=False)
    (env.directory/(name+'-broker.log')).write_text(result.stdout+result.stderr)
    execute(env.docker+['rm','-f','-v',name],check=False)
   for volume in volumes:execute(env.docker+['volume','rm',volume])
   env.down()
   path=env.directory/'connection.json'
   if path.exists():path.unlink()

if __name__=='__main__':main()
