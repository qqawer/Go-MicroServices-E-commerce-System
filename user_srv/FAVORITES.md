# 商品收藏

原项目只有商品上的 `FavNum` 字段，没有用户与商品的收藏关系，也没有“我的收藏”接口。本批在现有用户服务中注册独立 `mxshop.favorites.v1.Favorites` RPC，复用原服务端口和用户数据库。

## 数据与请求语义

新增 `user_favorite` 表保存 `user_id`、`goods_id`、创建时间，唯一约束 `(user_id, goods_id)` 保证同一个人不会重复收藏同一商品。取消收藏硬删除这条关系，之后可以重新收藏。收藏与取消都可重复调用，不需要另设 `Idempotency-Key`。

- 新增：有效用户首次收藏前向商品服务确认商品存在；已存在的关系直接返回成功，所以成功回执丢失后重试不会依赖商品服务继续在线。
- 取消：只删除指定用户与商品的关系，不依赖商品是否仍存在。重复取消同样成功。
- 查询状态：按用户与商品返回 `favorited: true/false`，不是查询商品是否可购买。
- 列表：默认第 1 页、每页 10 条，最大 100 条，按收藏记录 ID 倒序。总数来自当前用户的收藏关系，包含后来被删除的商品。

列表先在用户库读取一页关系，再调用一次商品批量查询并按收藏顺序组装商品摘要。商品已删除时保留 `goods_id`，返回 `goods_missing: true` 和 `goods: null`；商品下架仍返回摘要及 `on_sale: false`。商品服务超时或数据库查询失败会返回错误，不能伪装成“全部商品已删除”。空页不请求商品服务。

HTTP 从 JWT 取得本人 ID，普通用户和管理员都只操作本人收藏。RPC 拒绝零值、负数的用户/商品 ID；分页拒绝非法页码和偏移溢出。没有按用户零值查询全部收藏的路径。

## 服务依赖和一致性边界

Favorites 在 user-srv 中使用商品 RPC 客户端，默认通过既有 Consul 发现 `goods-srv`，可选 `goods_srv.name` 覆盖服务名。连接不会等待商品服务恢复才启动，因此商品服务故障不会阻止用户登录、资料和地址功能启动。状态查询、取消收藏及已有收藏的重复新增不需要查询商品。

商品存在检查与收藏写入属于两个服务。商品可能在检查后删除，这时收藏关系按“商品已不存在”展示并允许取消；本批不引入跨库事务或消息队列。商品的旧 `FavNum` 字段没有随收藏自动加减，也不作为本人的收藏总数；准确的本人总数直接查询收藏表。

收藏列表复用 `Goods.BatchGetGoods`，因此同时修复它原有的三个问题：空 ID 列表可能查询全表、忽略数据库错误、未传递调用方 context。现在空列表直接返回空结果，最多接受 1000 个正整数 ID，并将取消、超时及查询失败正确返回。

## 与 mxshop 对照

对照固定提交 `2bbe1b3a0eaa59bf26a3a2047769979a0e206c11`：

- [模型](https://github.com/iceymoss/mxshop/blob/2bbe1b3a0eaa59bf26a3a2047769979a0e206c11/mxshop_srvs/userop_srv/model/userop.go#L31) 已有用户与商品唯一索引；本批保留这一基本约束，用明确的幂等插入处理并发重复。
- [RPC](https://github.com/iceymoss/mxshop/blob/2bbe1b3a0eaa59bf26a3a2047769979a0e206c11/mxshop_srvs/userop_srv/handler/userfav.go) 的 Save 忽略数据库错误，列表的结构体条件会忽略零值用户，且缺少分页；本批检查全部数据库错误并明确限定用户。
- [Web](https://github.com/iceymoss/mxshop/blob/2bbe1b3a0eaa59bf26a3a2047769979a0e206c11/mxshop-api/userop-web/api/userfav/userfav.go) 已在新增前查询商品，列表批量获取商品摘要；本批把规则放在收藏 RPC 中，供 HTTP 与其他调用方一致使用，并明确表示失效商品。

## 生成、验证与升级

协议源文件为 `user_srv/model/proto/favorites.proto`，用固定版本工具运行 `bash scripts/generate-favorites-proto.sh`。Web 仓同步这份协议及生成代码，并在现有用户 RPC 连接上调用收藏服务。

执行 `bash ci/check.sh` 和 `python3 tests/integration/run.py`，验证真实 MySQL 唯一约束、并发重复、用户隔离、取消后重新收藏、分页与失效商品，以及 HTTP 参数、鉴权和错误映射。商品批量查询的既有订单调用也由原集成回归覆盖。

先使用既有生产迁移工具预检、备份并应用 `0007_user_favorites.sql`，再发布 RPC，确认用户与商品服务都已更新，最后发布 Web。迁移只新增收藏表，不修改既有用户、商品、订单或地址数据。无需新增公网端口或 Nacos 必填项。

Apifox 端点和请求示例见 Web 仓的 `FAVORITES.md`。本地运行 Web 时，重启 user-web 才会加载新接口。
