# 商品创建与库存初始化（2026-09-22）

原 `CreateGoods` 未检查 Save 错误，旧表的必填 stocks 列也没有模型映射，可能返回 id=0 的“成功”。Web 提交的 stocks 未接库存服务，SetInv/InvDetail 也未正确区分数据库错误。

## 当前流程

1. Web 校验管理员 JWT、Idempotency-Key 和 JSON。将账号 ID 与请求编号做 SHA256，传给商品 RPC 的 requestKey。
2. 商品库事务认领 goods_creation.request_key；比较请求内容摘要。新请求保存商品和初始库存数量，商品 inventory_ready=false、on_sale=false。商品资料表的旧 stocks 列只写兼容值，库存服务是库存来源。
3. 提交商品库事务后，商品 RPC 调用库存 InitInv，单次最长 5 秒。商品库事务不跨越网络调用。
4. 库存库事务同时保存 inventory_initialization 和 inventory。goods 唯一约束防止重复库存。重试检查原初始数量，返回当前库存；从 100 调成 90 后重试初始化 100，仍返回 90。
5. 收到确认后，商品库只在首次就绪时设置 inventory_ready=true 并采用原请求的 sale 值。已就绪请求再次重试，不恢复旧资料/上架状态。

同 key + 同请求返回同一商品；不同内容返回 AlreadyExists。默认无自动后台重试：客户端使用原账号、原 key、原 body 重试。商品已保存但库存调用失败/响应丢失时，返回已知商品 ID + inventoryReady=false；HTTP 返回 503。初始化记录需保留，删除原商品后同 key 不重建。库存冲突/记录丢失要人工核对，不能换 key 试图“修复”原请求。

库存未就绪时阻止资料/状态修改，尤其不能通过 PATCH 提前上架；商品详情/列表仍能显示 pending 商品及 inventory_ready=false。这不是整个订单库存算法的完成证明。

## 接口及连接

- 新 RPC `GetGoodsInventory` 查询当前库存。pending 返回 ready=false；库存服务不可用传播错误，不伪装成 stocks=0。
- `SetInv` 是显式设置绝对库存，创建重试必须使用 `InitInv`。两者都拒绝负库存；允许初始库存 0。
- 商品服务通过现有 Consul 发现 `inventory-srv`，可在商品服务 Nacos JSON 的 `inventory_srv.name` 指定其他注册名。默认无需增加配置，启动时不等待库存连接；查询商品资料不依赖库存可用。
- 同步四份 goods proto 和两份 inventory proto；固定生成工具见 `scripts/generate-goods-proto.sh`。

## 迁移与部署

`deploy/mysql/migrations/0002_goods_inventory_initialization.sql` 添加两个进度表、商品就绪字段，以及库存唯一和非负约束。原商品 default true 的前提是已经有对应库存，迁移工具会拒绝不满足此条件的数据。

生产已有数据时，在部署目录执行：

```sh
sudo python3 scripts/migrate-business.py check
sudo python3 scripts/migrate-business.py up
```

首次接管只允许四个业务库的结构与冻结的 0001_schema.json 完全匹配；不会重跑种子或 DROP 业务表。up 先保存权限 0600 的完整业务库备份，登记原基线，再逐条应用增量迁移。dirty、未知版本、checksum 变化均阻止继续执行。迁移部分失败需人工检查 DDL，不自动重跑。与发布脚本共用 release.lock。

服务器必须同步脚本、基线清单和迁移文件。先迁移，再发布 RPC，最后重启本地 Web。release.py 在更换容器前校验所有迁移已完成且哈希匹配；升级不会由业务启动自动执行。空库先按既有流程 bootstrap，再应用增量；测试环境由 dbtool 从 0001 开始迁移。

本批是增量表结构，旧版二进制回滚保留新列/表，不反向删除业务数据。业务库备份包含密码哈希等业务数据，留在私有运行目录，不提交仓库。

## 验证与范围

真实 MySQL/gRPC 覆盖创建、查询、当前库存保持、并发同 key、内容冲突、响应丢失后的新 handler 恢复、数据库写失败回滚、pending 上架拒绝和合法零库存。HTTP 路由验证 key、账号作用域、权限、参数与 200/409/503/504 返回。隔离迁移测试验证基线漂移拒绝、备份、保留现有库存、重复执行与 dirty 拒绝。

此处不需要 MQ。下单后的扣减/返库事务和幂等仍按计划批次 04；RocketMQ 的订单事务消息、重复消费、回查和恢复按批次 05 一起实现。现有 CreateOrder 保护继续保留，不把本次初始化当成下单流程已经安全。
