# 第三批：MQ 异步消费、超时关闭和库存补偿

2026-10-16。在第二批真实下单链上继续传播 OTel 上下文，保留现有订单决策、SQL 事务、outbox、重试和库存幂等语义。本轮不部署云端、不触发支付。

## 为什么 HTTP 结束以后还能看到子 Span

`order_timeout` 的生产者把 W3C 追踪信息写入消息属性；订单消费者逐条提取它，创建 Consumer Span。订单关闭时又把当前追踪信息随返库 outbox 一起保存，发送 `order-reback` 时恢复并注入，库存消费者继续提取。消费者不继承已经结束的 HTTP Context，仍服从 MQ runtime 的停机取消和原有处理期限。

每条消息有独立的处理 Span。一个 RocketMQ 回调即便收到多条消息，也不能把第一条消息的父级套给整个批次。单消息的消费 Span 使用消息上下文为父级，是本项目选择的关联方式；多消息聚合的 Span Link 设计不在本轮实现范围。[OTel messaging conventions](https://opentelemetry.io/docs/specs/semconv/messaging/messaging-spans/)

原 HTTP Span 不需要保持打开。Trace 的后续 Span 可以在几十秒后甚至重投时继续出现；这不是用户 HTTP 请求阻塞了几十秒。正常新订单在异步消费前有 15 段，原同步核心仍为 14 段，增加的 1 段是 outbox 发送。

```text
order.transaction
└─ rocketmq.send order_timeout              Producer；发送后结束
   └─ order_timeout process                 Consumer；约30秒后
      └─ order.close
         └─ rocketmq.send order-reback      Producer
            └─ order-reback process         Consumer，inventory-srv
               └─ inventory.reback
                  └─ mysql.inventory.release
```

本图只展开新增边界。正常首次完成共 21 Span、4 service，HTTP 与所有同步调用仍在同一个 Trace。broker 本身没有发 Span；返库是库存进程内部方法调用，消费者不会凭空多出一对 gRPC Span。

## 代码和迁移

- `internal/tracing/message.go`：可保存的 Properties、恢复上下文、逐消息 Consumer Span、重消费次数属性。
- `order_srv/handler/transaction.go`：outbox 创建时存上下文，实际发送时恢复并创建 Producer Span；`order.close` 记录关闭操作。
- `order_srv/handler/order.go`：TimeOutReback 提取逐消息上下文；独立取消决定/事务回查保留消息追踪身份。
- `inventory_srv/handler/inventory.go`：AutoReback 提取消息上下文；Reback 与库存归还 SQL Span。
- `order_srv/model/transaction.go`：新增 nullable JSON `OrderEvent.TraceContext`。
- `deploy/mysql/migrations/0010_order_event_trace_context.sql`：**部署新代码前**按既有迁移流程新增 `grpctest_orders_srv.order_event.trace_context`。

生产入口不会自动迁移数据库。即使关闭追踪导出，新的 outbox 模型也需要该列；不能跳过迁移直接部署。现有事件为 NULL，照常发送/处理，但没有历史追踪信息时无法自动接回原 HTTP。新事件只保存当前传播器启用的 `traceparent` / `tracestate`，不存 JWT、收货人或地址。重复事件冲突不会覆盖第一次创建的上下文。

outbox 的发送确认不明时可能重复发送；本轮不把 SQL 和 MQ 宣称为共同事务。所有重复消费仍依靠原库存流水、订单决策和条件更新处理，Span 不负责回滚或去重。

mxshop 固定提交 `2bbe1b3a0eaa59bf26a3a2047769979a0e206c11` 的 main 中分别订阅 TimeOutReback / AutoReback，超时消息触发关单，再发送库存归还消息。这两处 handler 没有提取消息追踪信息，发返库消息还使用 Background。本轮参考业务路径，沿用本项目已有更完整的 outbox/幂等逻辑，补 OTel 传播，没有照搬旧事务代码。

## 本地启动

先按 [ORDERS.md](ORDERS.md) 准备 Jaeger、商品 RPC、MySQL、broker 和两个主题。端口、数据库、网络继续沿用。当前 compose 对订单和库存运行器传 `--async`：订单启用真实超时消费者及每5秒 outbox dispatcher；库存启用真实返库消费者。**演示刻意没有运行 RecoverOrders 的直接 RPC 兜底**，以便证明关单/返库由 MQ 投递完成。生产调度配置不改。

RPC 根目录：

```sh
mkdir -p .local/tracing/async
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -mod=readonly -o .local/tracing/orders/order-rpc ./cmd/order-trace-demo
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -mod=readonly -o .local/tracing/orders/inventory-rpc ./cmd/inventory-trace-demo
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -mod=readonly -o .local/tracing/async/mq-replay ./cmd/mq-trace-replay
docker compose -f labs/tracing/orders.compose.yaml up -d --no-build --force-recreate inventory order
docker cp .local/tracing/async/mq-replay grpctest-tracing-orders-broker-1:/tmp/mq-trace-replay
```

运行器仅对隔离库新增追踪列；建表只处理缺失的表，避免重复启动把已有索引字符串列改回 TEXT。演示付款关闭，只提供 dispatcher 查询所需的空 `order_payment` guard 表，不用于测试真实支付。

Web 根目录重建并重启 `cmd/order-trace-demo`（先核对/停止旧 PID，不能并行覆盖 token 文件）：

```sh
CGO_ENABLED=0 go build -mod=readonly -o .local/tracing/order-web ./cmd/order-trace-demo
OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://127.0.0.1:4318/v1/traces \
OTEL_TRACES_SAMPLER_ARG=1 .local/tracing/order-web
```

18023 的真实 JWT/handler 不变，token 仍在 `.local/tracing/order-tokens.json`，一小时到期需要重启 Web 重新签发。新增合成身份如下；使用 ORDERS.md 的 Python 请求例子，修改 `case` 和 `key`：

| case | 用户 | 商品1数量 | key |
| --- | ---: | ---: | --- |
| timeout | 60104 | 2 | trace-async-timeout-20261016 |
| retry | 60105 | 1 | trace-async-retry-20261016 |
| compensation | 60106 | 101 | trace-async-compensation-20261016 |
| outbox | 60107 | 1 | trace-async-outbox-20261016 |

`--async` 模式的新订单期限为30秒，对应已有 `timeoutDelay` 的30秒等级。实际时机还受 broker 调度影响，SQL expires_at 仍为关单依据。[RocketMQ 延迟等级](https://rocketmq.apache.org/zh/docs/4.x/producer/04message3/)。不带 --async 的运行器保留第二批行为和一小时期限。重启不会重置库存、重建已有订单用户的购物车；这些固定样例当前已经执行完，再提交相同 key 是幂等读取。

启用消费者时，第二批的两个历史合成订单已到期，真实消费者先关闭它们并归还3件商品，库存97→100；它们的旧 outbox 没有追踪字段，不能回填成原请求 Trace。本轮新实验以恢复后的100为基线。

## 验证和复现故障

只读追踪校验：

```sh
python3 labs/tracing/verify_async.py timeout b7a48e1803406df6b4785df70eebfd73
python3 labs/tracing/verify_async.py retry fdc3015428c1db075a181c3418218147
python3 labs/tracing/verify_async.py compensation 0f1af26ec66dca3fcc72e11cc1f468ea
python3 labs/tracing/verify_async.py outbox 1273c0f405fc6529b75e58f9f1cec632
python3 labs/tracing/verify_async.py duplicate b7a48e1803406df6b4785df70eebfd73
```

它逐级检查父子关系、同一 Trace、4个service、Consumer/Producer kind、重试次数、请求数据排除，以及超时消费者开始晚于 HTTP 结束；outbox 另外检查重启前后发送 Span 的父级相同。Jaeger 内存重启后旧ID会消失，需用新的实验请求生成 Trace。`verify_order.py` 现在只校验原同步核心，允许新增异步 Span，同时打印总段数，避免拿旧的14段当整条异步 Trace 的固定数量。

### 返库首次失败，真实 Broker 重投

创建 retry 订单后，在另一个终端对其库存流水持锁40秒；只操作该合成 UUID：

```sh
docker exec grpctest-es-lab-mysql-1 mysql -uroot -e "START TRANSACTION; SELECT order_sn FROM trace_inventory_demo.stockselldetail WHERE order_sn='RETRY_ORDER_SN' FOR UPDATE; SELECT SLEEP(40); COMMIT;"
```

订单30秒到期，关单及发消息正常；库存消费者第一次请求在5秒期限内等不到锁，SQL回滚，返回 ConsumeRetryLater。释放锁后 Broker 真实重投，第二次成功。样本24段、两次返库消费，一次 Error，`rocketmq.reconsume_times` 从0变为1，库存100→99→100；只产生一笔有效归还。这里不是手动重复调用 handler。

### 额外复制消息，验证业务幂等

从原 Trace 的 `rocketmq.send order-reback` Span 取其 TraceID/SpanID，组装 `00-TRACE_ID-SPAN_ID-01`：

```sh
docker exec grpctest-tracing-orders-broker-1 /tmp/mq-trace-replay \
  --topic order-reback --order SYNTHETIC_ORDER_SN --traceparent ORIGINAL_TRACEPARENT
```

本轮额外发送两条返库消息，再用超时 Producer 的 traceparent 发送一条 order_timeout。它们有新的 broker 消息ID，但订单号与原消息相同；属于主动复制业务消息，与上一实验 Broker 自己的重投分别记录。原正常链由21增至29段，三次返库消费都可见，库存和version没有再次增加，超时复制也不再产生第二笔返库 outbox。

### outbox 进程重启后重发

创建 outbox 订单后停止订单进程；仅把这笔合成事件 `published` 改为0，保留 `trace_context`，模拟“消息已存储但确认丢失”留下的待重发状态，再启动进程：

```sh
docker stop --time 30 grpctest-tracing-orders-order-1
docker exec grpctest-es-lab-mysql-1 mysql -uroot -e "UPDATE trace_orders_demo.order_event SET published=0 WHERE order_sn='OUTBOX_ORDER_SN' AND topic='order_timeout';"
docker start grpctest-tracing-orders-order-1
```

这一步是明确的 SQL 故障夹具，**没有声称真实丢失 broker 确认**。重启后 dispatcher 重新读取事件，发送 Span 仍挂原 order.transaction，两个发送 Span 父级相同。两个延迟消息最终都消费，最终24段；只关闭一次订单、只归还一次库存。

## 结果和边界

| 场景 | 实际结果 | Trace |
| --- | --- | --- |
| 正常超时 | 200下单，30秒后TRADE_CLOSED，100→98→100 | 初次21段；b7a48e1803406df6b4785df70eebfd73 |
| 首次返库失败 | SQL期限触发失败，Broker重投成功，100→99→100 | 24段；fdc3015428c1db075a181c3418218147 |
| 库存不足取消 | 409、无成功订单，库存保持100，建立取消标记 | 早期快照18段，补发消费收齐后21段；0f1af26ec66dca3fcc72e11cc1f468ea |
| outbox重启 | 两次发送保留同一父级；超时消息重复也只归还一次 | 最终24段；1273c0f405fc6529b75e58f9f1cec632 |
| 主动复制消息 | 两条返库副本、一条超时副本，没有再次加库存 | 正常链最终29段，沿用第一条ID |

库存不足时事务半消息提交成为补偿消息；dispatcher 后续也可能发送 durable outbox。因此成功消费一次不等于将来不再出现相同订单消息。消费者处理成功留下的是取消围栏（status3），没有把未扣减的101件商品加回库存。

最终5个合成成功订单均已关闭、库存100/version10、5笔已释放流水和2笔取消围栏、outbox待发送0；包含第二批两个历史订单。stock_released 是恢复任务的确认标记，本实验没有运行该worker，不把它当 MQ 是否已完成的唯一依据。

证据在 RPC `.local/tracing/async/`，含首次完成快照、最终 Span、SQL、data-check.json 和CI日志。两仓完整 ci/check.sh、Go race、四个演示命令构建、追踪及SQL断言通过。原40/420/16/16四个ES索引逐项不变，商品搜索正常。

本批只验收 demo 未支付订单、消息超时/补偿和重投。生产的定时 RecoverOrders 直接RPC兜底、支付宝支付/关单竞争、死信处理、遥测持久存储和云端部署未纳入本轮真实验收。正常生产消费者已使用同一追踪代码；运行验收仍是隔离本地环境。
