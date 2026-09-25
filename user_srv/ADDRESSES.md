# 收货地址与选址下单

原项目没有地址簿，创建订单时必须直接填写收货姓名、手机号和地址。本批新增地址管理，并保留原来的直接填写下单方式。

## 服务归属与协议

地址属于用户，在现有 user-srv 进程中注册独立 `mxshop.address.v1.AddressBook` gRPC 服务，数据存入用户库的 `user_address`。无需新增服务进程、Nacos 配置或公网端口。普通用户和管理员的 HTTP 地址接口都只操作 JWT 对应的本人地址。

协议源文件为 `model/proto/address.proto`。`scripts/generate-address-proto.sh` 使用固定版本的 protoc / Go 插件生成用户服务及订单服务的客户端；Web 仓同步该协议。地址字段为 `name`、`mobile`、`province`、`city`、`district`、`address`，不增加默认地址功能。

- 所有 RPC 强制正数用户 ID；查单条、修改、删除都使用地址 ID + 用户 ID。不存在、他人所有、已删除的地址均不可访问。
- 列表按 ID 倒序分页，默认第 1 页、10 条，最大每页 100 条，拒绝溢出偏移。
- PATCH 使用 optional 字段区分未提交与空值。未提交保留原值，提交空值拒绝；事务内锁行后只更新指定列，并验证合并后的完整地址，避免并发修改互相覆盖。
- 删除使用软删除。数据库读写失败返回失败，不透传数据库内部错误。
- 姓名及省市区各最多 20 个字符，手机号为 11 位大陆手机号。省、市、区和详细地址以空格连接后最多 100 个字符，与现有订单地址列一致。

## 选址下单与重试

Web 接口仍为 `POST /g/v1/order`，新增 `address_id` 模式，对应订单 RPC `CreateOrderFromAddress`；原来直接提交 `name/address/mobile` 的方式仍调用 `CreateOrder`。两种输入不可混用，新方法在旧 RPC 上不会静默退回旧逻辑。

订单服务第一次接受请求时，用 JWT 对应的用户 ID 与地址 ID 调用 AddressBook，复制收货信息到原有 `order_transaction.snapshot`，随后生成订单时写入订单的收货字段。这是订单快照，不是订单查询时再联表读取地址。

例如第一次以地址 7、请求编号 K 下单时收货地址为 A：之后把地址 7 改为 B 或删除，再用同一用户、K、同一正文重试，仍使用 A。原编号下换地址 ID 或留言会返回冲突；需要另一笔订单应使用新编号。

为做到这一点，地址模式的请求哈希包含固定版本标识、用户 ID、地址 ID、留言；不使用可变的地址内容。先读取已保存的请求，再决定是否需要访问地址服务。旧直接填写请求的哈希编码不变。地址服务暂时不可用时，已有请求仍能重放，新的选址请求明确失败；原直接填写方式不依赖地址服务。

复用现有下单幂等、快照、RocketMQ 和恢复流程，没有额外的地址消息队列或后台任务。订单服务的地址客户端随进程初始化、自动重连并统一关闭；默认发现已有的 `user-srv`。

## 与 mxshop 的对照

对照固定提交 `2bbe1b3a0eaa59bf26a3a2047769979a0e206c11`：

- [地址模型](https://github.com/iceymoss/mxshop/blob/2bbe1b3a0eaa59bf26a3a2047769979a0e206c11/mxshop_srvs/userop_srv/model/userop.go)与 [RPC](https://github.com/iceymoss/mxshop/blob/2bbe1b3a0eaa59bf26a3a2047769979a0e206c11/mxshop_srvs/userop_srv/handler/address.go)：地址放在独立 userop 服务，修改/删除限定用户；本项目先复用现有用户服务承载独立协议。
- 原列表没有分页；结构体条件中的零用户值可能被 GORM 忽略。这里拒绝零用户，使用显式查询条件，管理员也只查看本人地址。
- [Web 下单](https://github.com/iceymoss/mxshop/blob/2bbe1b3a0eaa59bf26a3a2047769979a0e206c11/mxshop-api/order-web/api/order/order.go)仍直接传收货信息。这里增加地址 ID 选择，并复用本项目已有的持久快照，保证重试不随地址簿变化。

## 升级与验证

新增编号迁移 `0006_user_addresses.sql`，仅创建 `grpctest_user_srv.user_address`；不改旧迁移、不回填或删除现有用户/订单数据。先使用既有 `deploy/scripts/migrate-business.py check/up` 完成预检、私有备份和迁移，再发布 RPC，最后发布 Web。

运行 `bash ci/check.sh`、`python3 tests/integration/run.py`、`python3 tests/mq/run.py`。地址集成测试使用隔离 MySQL + gRPC，覆盖 CRUD、用户隔离、软删除、分页、并发部分更新；订单集成覆盖首次快照、修改/删除后重试、服务不可用时重放及新旧下单兼容。测试不在 AWS 业务库创建用户或订单。

HTTP 请求示例见 Web 仓的 `ADDRESSES.md`。本地运行 Web 时，更新后重启 user-web 和 order-web 才会加载新接口。
