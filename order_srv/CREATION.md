# 下单、RocketMQ 与自动恢复（批次 05）

本批沿用 mxshop 的事务消息路线，对照的是 [订单启动](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/order_srv/main.go)、[MQ 初始化](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/order_srv/initialize/rocketMQ.go) 和订单 handler。参考 [RocketMQ 事务消息语义](https://rocketmq.apache.org/docs/featureBehavior/04transactionmessage/) 与 [官方 Docker 部署](https://rocketmq.apache.org/docs/quickStart/03quickstartWithDockercompose/)。

## 原来缺什么

库存扣减/归还的幂等、订单事务决定和待发送消息表已经存在，但生产者、消费者和自动恢复没有挂到启动流程。CreateOrder 因而仍返回交易未就绪。旧创建流程每次请求新建一个事务生产者，并用内存字段传回订单结果；重试会产生新订单号，回调还会重新读取可能已改变的购物车。

## 当前流程

1. Web 从 JWT 取得用户，要求 `Idempotency-Key`。RPC 按用户 + 请求编号认领下单；请求正文不同返回 AlreadyExists。
2. 第一次请求将收货信息、勾选的购物车行、商品名称/价格/数量及库存清单保存到原 `order_transaction.snapshot`。重新提交同一正文和编号时使用原快照，购物车变化不会替换已接受的请求。
3. 共用事务生产者发送 `order-reback` 半消息。无请求字段的 `TransactionListener` 为每次执行建立独立上下文，HTTP/RPC 结果依据数据库读取。
4. 使用订单号调用库存 Sell。成功后，在订单库同一个事务内保存订单和明细、移除仍与快照一致的购物车行、记下超时事件，并将事务决定设为 COMMITTED。商品金额从旧 float32 协议的十进制表示规范为“分”，拒绝无效/过多小数及金额溢出；新订单用整数计算并保存，旧 float 字段继续兼容展示。
5. 订单成功意味着撤销“返库”事务消息；取消意味着提交“返库”事务消息。Sell 错误不能证明未扣库存，所以必须保存取消决定，再安排返库。订单提交回执丢失时，读取事务决定，避免误退已创建订单。
6. Broker 回查同一持久决定。取消与订单提交竞争同一锁，已取消不能被迟到回调重新提交；库存已有取消流水时也拒绝迟到扣减。
7. 启动时和每 5 秒运行恢复任务。超过 60 秒仍 PREPARING 的请求会被取消；到期 demo 订单关闭。原 outbox dispatcher 重发未确认事件。取消/关闭的请求还会用幂等 Reback RPC 确认库存最终已释放，覆盖 MQ 回查耗尽、死信或消息投递无法确认的情况。库存服务不可用则留下记录继续重试。

重试分两种：通信失败/CREATING 时保留原 key 查询或重试；CREATE_CANCELLED 是已确定取消，原 key 不能复活，确认需要重新购买时用新 key 发起新请求。新 key 表示新的一次购买，不负责消除不同 key 之间的重复购买。

## 消息及结果

| 项目 | 内容 |
| --- | --- |
| 事务生产者组 | grpctest-order-transaction |
| 普通事件生产者组 | grpctest-order-events |
| 返库主题 / 消费组 | order-reback / grpctest-inventory-reback |
| 超时主题 / 消费组 | order_timeout / grpctest-order-timeout |
| 请求查询 | GetOrderCreation(userId, requestKey)，始终按本人查询 |
| 未确认 | CREATING；同一 key 查询或重试 |
| 创建成功 | WAIT_BUYER_PAY，带 ID、orderSn、totalCents |
| 创建取消 | CREATE_CANCELLED，可能还在重试返库 |
| 到期关闭 | TRADE_CLOSED；库存释放确认由恢复任务完成 |

绝对到期时间在第一次接受请求时保存，不会因重试或重发延时消息延长。延时等级向上取整，消费者仍检查数据库中的到期时间；后台扫描补偿丢失/延迟的唤醒。

`stock_released=true` 只在库存 RPC 确认完成后写入。数据库提交回执未知时继续重复调用同一个幂等操作。进程健康仅表示基础 RPC 已启动，不保证依赖此刻可用；MQ 故障时不会把 pending 当成创建成功。

## 配置与部署

新增 `0004_order_creation_runtime.sql`，扩展已有两张业务表和事务表，不新建一套订单状态表，也不修改 0001–0003。旧订单新增 cents 列为 NULL，不重新计算旧金额。

AWS 的 dev 配置由 `deploy/scripts/configure-mq.py --apply` 备份后补充，保留其余配置。订单示例：

```json
"rocket": {"enabled": true, "host": "mqnamesrv", "port": 9876, "mode": "demo", "order_ttl_seconds": 1800}
```

库存只需 enabled、host、port。未启用 MQ 的本地 RPC 仍可提供购物车/库存基础接口，创建订单保持未就绪拦截。Mac 只运行 Web 时不需要安装或直连 MQ；不要为 MQ 新开公网入站规则。

在服务器 deploy 目录执行（先同步本批部署文件）：

```sh
sudo docker compose --env-file .env -f compose.yaml up -d --wait mqnamesrv mqbroker
sudo docker exec -i grpctest-infra-mqbroker-1 sh < rocketmq/topics.sh
sudo python3 scripts/migrate-business.py check
sudo python3 scripts/migrate-business.py up
sudo python3 scripts/configure-mq.py --apply
```

再发布 RPC 镜像，最后重启本地 order-web。发布 gate 检查 MQ 容器健康及迁移版本。MQ 使用固定镜像摘要、持久 store/log 卷、SYNC_FLUSH 和内网地址。`topics.sh` 检查创建成功输出及主题路由：实测 mqadmin 在 Broker 启动初期可输出 Java 异常而退出 0，不能只检查端口/退出码。

## SDK 竞态与源码补丁

真实 Broker + Go race detector 复现了 SDK 的订阅版本读写、消息缓存读写竞态。依赖固定为 Apache 上游 [99c433634e09](https://github.com/apache/rocketmq-client-go/commit/99c433634e09f72fa2778ca04411de29d4fc9cff)，并在 third_party 保存带 LICENSE/NOTICE 的运行源码。上游提交已有订阅锁修复，本仓只在 `consumer/process_queue.go` 的两处缓存读取补上既有互斥锁。UPSTREAM.json 保存原文件摘要，LOCAL_CHANGES.md 说明修改；没有跟随浮动分支。

消费者继续并发处理消息。业务回调在停止时先取消并等待完成，然后才关闭数据库/RPC 依赖。后台恢复负责 MQ 重试耗尽或死信后的业务补偿。本批没有宣称 SDK 所有路径都无竞态；回归覆盖本应用实际使用的路径。

## 验证命令与边界

```sh
bash ci/check.sh
python3 tests/integration/run.py
python3 tests/mq/run.py
```

真实 MySQL 回归覆盖同 key 并发、用户隔离、改变正文拒绝、快照不随购物车变化、缺货/SQL/提交回执错误、提前超时消息、取消与迟到执行、恢复任务。MQ 测试使用独立标记数据库与独立 Broker、真实商品/订单/库存 RPC 进程，验证正常创建、重复请求、重启、重复返库消息和 Broker 停机；清理只针对测试自己创建的资源。

本批仅开放 **demo 订单**：禁止付款状态更新、不生成支付宝链接。支付渠道验签、金额核验、查单/关单及付款竞争仍归批次 06。单 Broker 的持久卷及同步刷盘不等于多副本容灾；不会宣称机器/磁盘丢失后消息仍可靠。数据库仍是恢复依据。
