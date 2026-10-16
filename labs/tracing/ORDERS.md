# 第二批：订单 → 商品 / 库存的真实追踪验证

本文保留第二批的历史验证结果。第三批已启用消费者和 dispatcher，当前 compose 会传 --async，原合成订单到期后会关闭；最新操作与结果见 [ASYNC.md](ASYNC.md)。下面14段指第二批同步核心，verify_order.py 现允许异步扩展并另报总段数。

2026-10-16。参考 mxshop 的 HTTP/gRPC/业务 Span 分层，延续第一批 OpenTelemetry→Jaeger。本次修改正常生产入口，运行验收使用独立本地入口、合成身份和隔离 SQL 库。没有部署云端，也没有触发真实支付。

## 为什么要补消息上下文

订单服务查商品后，实际通过 RocketMQ 事务生产者执行本地事务回调；回调内调用库存服务，再提交订单库。共享 `TransactionListener` 原来用一个新 `OrderListener{}` 执行，回调会使用独立 Background Context。仅给 gRPC 两端装 stats handler，库存链仍会丢失请求的父级。

现在发送前把 W3C `traceparent` / `tracestate` 注入消息属性，回调从消息提取 Trace Context。仍保留原有独立的 15 秒回调期限，不继承 HTTP 请求取消；追踪关联和业务取消策略是两件事。`message_test.go` 专门验证请求已取消时仍能恢复追踪身份。

mxshop 固定版本 `2bbe1b3a0eaa59bf26a3a2047769979a0e206c11` 有订单 Web 客户端、订单 RPC 服务端和手动业务 Span；被核对的 `mxshop_srvs/order_srv/initialize/srv_conn.go` 没有给商品/库存连接安装追踪客户端拦截器。本次补齐了这一跳，并沿用已有 OTel 依赖，不引入旧 Jaeger SDK。

## 实际调用树

```text
POST /trace-demo/orders                  order-web
└─ Order/CreateOrder client              order-web
   └─ Order/CreateOrder server           order-srv
      └─ order.create
         ├─ order.prepare
         │  └─ Goods/BatchGetGoods client
         │     └─ Goods/BatchGetGoods server    goods-srv
         │        └─ mysql.goods.batch_read
         └─ rocketmq.order.prepare              order-srv，Producer
            └─ order.transaction
               └─ Inventory/Sell client
                  └─ Inventory/Sell server     inventory-srv
                     └─ inventory.sell
                        └─ mysql.inventory.reserve
```

正常链为 14 个 Span、4 个 service。`rocketmq.order.prepare` 包含发送及同步本地事务回调的时间，不等于单独的 broker 网络耗时；broker 没有运行 OTel SDK。商品查询和库存扣减中的 SQL Span 是应用侧耗时，没有记录 SQL 正文。订单库保存、购物车软删除和 outbox 写入包含在事务 Span 内，未逐条拆分。

主要修改：正常订单 Web 初始化及 router/srv_conn，订单/库存 RPC main，订单下游 srv_conn，商品→库存连接，`creation.go`、`order.go`、商品 BatchGetGoods、库存 Sell，以及 `internal/tracing/message.go`。独立运行器为 RPC 两个 `cmd/*-trace-demo` 和 Web `cmd/order-trace-demo`。

## 启动实验环境

前提：第一批 [README](README.md) 的 Jaeger、商品 RPC、MySQL 和网络 `grpctest-es-lab_lab` 已启动。商品服务仍只读取 `es_store_demo` 的合成商品，商品 1 单价 44.90 元；库存实验不会修改商品库或 ES。

RPC 根目录执行（当前 Mac ARM64）：

```sh
mkdir -p .local/tracing/orders
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -mod=readonly -o .local/tracing/goods-rpc ./cmd/goods-es-demo
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -mod=readonly -o .local/tracing/orders/order-rpc ./cmd/order-trace-demo
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -mod=readonly -o .local/tracing/orders/inventory-rpc ./cmd/inventory-trace-demo

docker compose -f labs/tracing/compose.yaml --profile demo up -d --no-build --force-recreate goods-rpc
docker exec grpctest-es-lab-mysql-1 mysql -uroot -e 'CREATE DATABASE IF NOT EXISTS trace_orders_demo CHARACTER SET utf8mb4; CREATE DATABASE IF NOT EXISTS trace_inventory_demo CHARACTER SET utf8mb4;'
docker compose -f labs/tracing/orders.compose.yaml up -d namesrv broker
```

等待 NameServer、broker 日志显示启动成功，再创建两个真实主题：

```sh
for topic in order-reback order_timeout; do
  docker exec -e 'JAVA_OPT_EXT=-Xms64m -Xmx128m' grpctest-tracing-orders-broker-1 \
    sh mqadmin updateTopic -n trace-namesrv:9876 -b trace-broker:10911 -t "$topic" -r 4 -w 4
done
docker compose -f labs/tracing/orders.compose.yaml up -d inventory order
docker compose -f labs/tracing/orders.compose.yaml logs --tail 20 inventory order
```

出现两条 `isolated ... tracing RPC ready` 后，在 Web 根目录启动：

```sh
mkdir -p .local/tracing
CGO_ENABLED=0 go build -mod=readonly -o .local/tracing/order-web ./cmd/order-trace-demo
OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://127.0.0.1:4318/v1/traces \
OTEL_TRACES_SAMPLER_ARG=1 .local/tracing/order-web
```

先核对并停止占用 18023 的旧 demo 进程，避免并行启动覆盖 token 文件。当前后台 PID/日志在 Web `.local/tracing/order-web.{pid,log}`。运行器启动时签发有效期 1 小时的合成用户 JWT，文件 `.local/tracing/order-tokens.json` 权限 0600；走真实 JWT 中间件及真实下单 handler，没有模拟订单/商品/库存 RPC，也不需要用户服务。token 过期后重启 Web，旧 token 不再使用。该运行器仅供本机学习，未把 3002 商城结算页改接到它。

订单/库存数据库固定为 `trace_orders_demo`、`trace_inventory_demo`，RPC 映射到回环 15054/15053，HTTP 为回环 18023。独立实验 RocketMQ 无宿主机端口映射。订单和库存库会自动建表；首次种入三个购物车用户 60101/60102/60103，商品 1 数量分别为 2/101/1；首次库存 100。重启不会重置已存在的库存或重建成功订单的购物车。支付类型为 demo。

## 发起请求与只读验收

在 Web 根目录运行下面 Python。第一次使用 `success`；其他分支按说明修改 `case` 和 `key`。token 只在内存读取，不打印：

```python
import json, urllib.request, urllib.error
from pathlib import Path
case, key = 'success', 'trace-order-success-20261016'
tokens = json.loads(Path('.local/tracing/order-tokens.json').read_text())
req = urllib.request.Request('http://127.0.0.1:18023/trace-demo/orders',
    data=json.dumps({'name': 'trace-only-name', 'address': 'trace-only-address',
                     'mobile': '13800000000', 'post': 'trace-only'}).encode(),
    headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + tokens[case],
             'Idempotency-Key': key})
try:
    response = urllib.request.urlopen(req, timeout=30)
except urllib.error.HTTPError as error:
    response = error
print(response.status, response.headers.get('X-Trace-ID'), json.load(response))
```

在 RPC 根目录用返回的 ID 核对：

```sh
python3 labs/tracing/verify_order.py success TRACE_ID
```

只读脚本查询 Jaeger，检查每个 Span 的父级、预期 service 集合、错误状态、时间及请求数据排除。CLI 场景可选 `success|replay|stock-shortage|goods-unavailable|recovered`。重复成功请求用 `replay`，因为复用已有结果不再查商品/扣库存，预期只有 5 个 Span。只有新的成功创建才用 `success` 或 `recovered` 的 14 段断言。

本次真实结果如下：

| 场景 | 请求及结果 | 业务核对 | Trace ID / Span 数 |
| --- | --- | --- | --- |
| 正常 | 60101 数量 2；200、WAIT_BUYER_PAY、8980 分 | 1 个订单、1 次库存流水，100→98 | `cf161a0b9c5dec87d65e06c60d1684a0` / 14 |
| 重复提交 | 相同用户、key、正文；200、同一订单 | 仍 1 个订单，库存仍 98 | `5ffed1764416b0923b3f389415d6dd45` / 5 |
| 库存不足 | case `stock-shortage`，key `trace-order-stock-20261016`；409、CREATE_CANCELLED | 没有该用户订单/扣减流水；库存仍 98 | `ca9f8f3b1b9f92bd644a50be354dff11` / 14 |
| 商品服务停止 | case `goods-unavailable`，key `trace-order-goods-20261016`；504 | 未创建该用户订单意图，未进入库存扣减；库存仍 98 | `3816da1fd973a63aa8e358dd3c02a9f0` / 6 |
| 商品恢复后重试 | 同一 60103 用户/key；200、4490 分 | 第 2 个订单、第 2 次库存流水，98→97 | `9ed44a2fc97b50dc761639c28ba44bbe` / 14 |

故障只停止 `grpctest-es-store-goods-rpc`，发请求后在 finally 中 `docker start` 恢复，并等待原搜索接口恢复 200/5 件商品。gRPC 已建立连接断开后等待至原请求期限，因此本次是 504，而不是假定立即返回 503。第一次故障前查数据库，60103 没有订单意图；随后同一请求成功创建。

库存不足时库存 RPC 返回 ResourceExhausted。当前 otelgrpc 0.59.0 的 serverStatus 映射不会把该码标为 server Error；客户端及手动业务 Span 标 Error，合计 4 段。订单服务则成功返回“已取消”的业务结果，因此 HTTP 409、订单 RPC OK，不应要求整条链都红。RPC 状态属性、Span 状态和业务订单状态需要结合看。

只读 SQL 核对（过滤软删除购物车）：

```sh
docker exec grpctest-es-lab-mysql-1 mysql -uroot -e '
SELECT id,user,status,total_cents FROM trace_orders_demo.orderinfo;
SELECT user_id,request_key,state FROM trace_orders_demo.order_transaction;
SELECT goods,stocks,version FROM trace_inventory_demo.inventory;
SELECT order_sn,status,detail FROM trace_inventory_demo.stockselldetail;
SELECT order_sn,topic,published FROM trace_orders_demo.order_event;
SELECT user,goods,nums FROM trace_orders_demo.shoppingcart WHERE deleted_at IS NULL;'
```

最终为两个成功订单、两条扣库存流水、库存 97（version 2），库存不足的购物车仍在。证据保存于 `.local/tracing/orders/`，含五组 HTTP 响应、Jaeger Span、只读 SQL、CI 日志；请求凭据不会写入这些证据。

## 本轮边界

本地 broker、事务半消息、同步事务回调、商品/库存 RPC 以及两个 SQL 数据库都是真实调用。演示没有启动超时、补偿消费者或恢复 worker，也没有为其安装追踪。两个成功订单的 `order_timeout` outbox 已发送；库存不足产生 `order-reback` 的未发送 outbox，仍保留以便核对，不能宣称异步补偿已完成。后续重建实验 broker 可能丢失消息；本轮不提供持久消息的恢复保证。

正常生产入口的埋点已经改好；云端环境变量和部署未切换。本轮也没有给浏览器 JavaScript、支付、退款或全套后台任务加追踪。下一批可以单独处理消息生产/消费、重复投递、补偿和请求结束后的链路关联。

两仓 `bash ci/check.sh` 已通过（构建、既有 Python 检查、Go race 回归），另构建三个新增运行器；消息单测验证保留追踪身份但不继承已取消请求。五条真实链的父子关系和状态断言全部通过。商品搜索正常，原四个 ES 索引的 ID/业务字段与第一批基线逐项一致。
