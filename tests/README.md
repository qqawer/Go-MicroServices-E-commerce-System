# 独立测试环境、迁移与回归

本轮只完成修订清单第 1 项。验证码、短信、JWT/Claims 的后续修复没有混入本次修改。

## 原来的问题与本次处理

| 原来的情况 | 本次处理 |
| --- | --- |
| AWS 的四个业务库已经有演示数据，旧建表示例还包含写死的数据库地址 | 独立本地 Compose 项目、MySQL/Redis 容器、网络和数据卷，随机密码与 loopback 临时端口，不读取 AWS 配置 |
| bootstrap 只能初始化空库，无法记录后续按顺序修改表结构的过程 | 固定 `0001_baseline.sql`，在数据库保存迁移编号、文件名、SHA256 和 dirty 状态；后续追加编号 SQL |
| 重复准备数据可能覆盖正在验证的库存状态 | seed 与结构迁移分开，首次事务写入，重复执行跳过，改动 seed 则要求显式重建测试环境 |
| CI 只编译镜像，已有回归没有执行 | 两仓加入 checks 门禁，检查成功才构建镜像；RPC 增加真实 MySQL/Redis 集成验证 |

## 使用方式

要求：本机 Docker（Unix socket）、Docker Compose v2+、Go 1.25.6、Python 3.10+。CI 固定 Go 1.25.6，与现有 Dockerfile 一致。首次执行需要下载固定 digest 的 MySQL 8.0 和 Redis 7.4 镜像。

在 RPC 仓库根目录运行：

```bash
# 构建 4 个生产入口并执行已有的订单配置、启动、连接和交易保护回归
bash ci/check.sh

# 一次完整集成验证：自动创建临时环境，成功或失败后均清理自己的容器和卷
python3 tests/integration/run.py

# 保留一套环境用于后续开发：启动、迁移、seed、显示端口
python3 tests/env.py up
python3 tests/env.py status

# 新增迁移文件后执行；已经执行的版本不会重复应用
python3 tests/env.py migrate
python3 tests/env.py seed

# 明确删除这套测试数据后重新准备；不会访问 AWS
python3 tests/env.py reset

# 删除这套测试环境的容器、网络和数据卷
python3 tests/env.py down
```

默认项目名 `grpctest-test`；需要多套时给每条命令传同一个 `--project grpctest-test-自定义英文后缀`。脚本拒绝其他项目前缀、非本机 Docker context，以及与本次 marker 不符的既有同名卷/网络/容器。

密码和初始化 marker 自动生成在 `.local/testing/<项目>/state.json`，权限 0600，Git 和 Docker 构建上下文均排除 `.local/`。不要复制服务器 `.env` 到测试目录。输出只显示端口和私密文件路径。`down` 保留这份状态，方便下次以相同设置重建；它不会删除其他 Docker 项目。

Web 仓库执行 `bash ci/check.sh`：构建三个生产入口，执行操作系统 `USER` 覆盖 Nacos 用户名的既有配置回归；不需要启动 Web，也不连接 AWS。

## 结构迁移与数据 seed

共用迁移文件在 `deploy/mysql/migrations/`。`0001_baseline.sql` 冻结已有五张商品表和六张用户/库存/订单表，来源分别为经过 SHA256 审查的 `goods_srv/grpctest_goods_srv.sql` 和原 `deploy/mysql/02-business-schema.sql`。没有导入原商品 dump 的 DROP 或数据语句。

已应用文件不可修改。后续结构变更新增 `0002_描述.sql`、`0003_描述.sql`，连续编号，在此测试环境先验证。SQL 使用全限定库表名，避免切换默认数据库影响迁移记录。迁移执行器在同一个 MySQL 连接持有锁，并记录每个版本的摘要。

MySQL DDL 不能假装成可整体回滚的事务：执行前先写 dirty，只有全部语句成功才清除。中途失败保留 dirty 并拒绝自动重试；测试环境可用 `reset` 恢复。这里没有绕过记录的 `force`，也没有可能清空业务库的 `down migration` 命令。

`tests/seed.sql` 是小型固定 fixture：普通用户 1、品牌/分类 1、商品 421、库存 100、购物车数量 2。用户密码字段是不可登录的测试占位值，图片使用 `example.invalid`，不会发送短信或访问外部图片。seed 只允许四个业务库中的 INSERT，数据与 seed 完成记录在同一事务提交；失败全部回滚。重复 seed 不恢复已修改的数据。

迁移版本保存在独立的 `grpctest_migrations.schema_migrations`，seed 摘要在 `seed_history`。工具只有在数值 loopback 地址和数据库 marker 同时匹配时才执行；没有迁移历史但已有业务表时，拒绝自动接管。

AWS 现有库与旧 bootstrap 保持原样，没有执行线上 DDL，也没有登记线上基线。日后上线新增业务迁移时，需先核验线上实际结构、备份与历史基线，再采用同一批 SQL；本工具当前只管理带 marker 的隔离测试环境，不能直接用于生产迁移。

## 验证范围和边界

完整集成脚本验证：

- 同名但不归本测试项目所有的卷被拒绝，原卷保留；非 loopback 和错误 marker 被拒绝。
- 非空未登记库拒绝基线；空库依次应用 0001（11 表）和 0002（增加商品创建及库存初始化记录，合计 13 表）。
- seed 中途失败回滚；重复迁移和 seed 不增加重复数据，也不重置已修改的库存。
- 已执行 SQL/seed 摘要变化、未知迁移版本被拒绝。
- 并发增量迁移只应用一次，原数据保留。
- 失败 DDL 的部分结果如实保留并标记 dirty，后续执行被阻止；显式重建恢复。
- Redis 通过本机临时端口完成认证和 PING。
- 真实 MySQL + 实际 gRPC 编解码：用户创建/读取及正确、错误密码；商品列表/详情的分类与品牌关联、品牌筛选和 JSON 图片；库存查询；购物车读取、合并、修改、删除。

四个 RPC 的测试进程分开，因为原有复制的 proto 文件存在同名注册；没有设置忽略冲突的开关。gRPC 使用进程内传输，数据库为真实 MySQL。回归不等于完整服务启动或 HTTP→Nacos/Consul→RPC 端到端验收，也不覆盖库存扣减、MQ、支付或后续 JWT 权限修复。

原表结构与模型的剩余问题保留到后续业务批次：如商品 `stocks` 必填字段、用户响应漏 Mobile。商品详情关联预加载和品牌筛选已在 2026-09-21 修复并加入回归；分类创建的 `url` 列、真实返回 ID 和关联记录 ID/写入错误处理已在 2026-09-22 修复，回归见 `tests/integration/goods/category_test.go`。测试不会把剩余缺陷断言成正确行为。2026-09-22 商品创建与库存初始化已处理 stocks 映射和库存唯一约束；购物车行 ID/用户归属、部分更新和失败传播也已补齐，见 `order_srv/CART.md`。金额类型与交易扣减/返库仍按后续批次处理。

## CI

两仓的 `.github/workflows/release.yml` 执行顺序为 `checks → images → deploy`。`checks` 使用只读模块依赖，Go 回归开启 race；RPC 还运行上述临时 MySQL/Redis 集成脚本。测试错误会阻止镜像发布。部署仍受现有 `ENABLE_DEPLOY` 控制，本次没有打开自动部署。

普通检查只覆盖正式服务入口和明确列出的回归包，不执行带硬编码连接地址的旧示例程序。完整集成测试必须通过 `run.py` 提供私有连接文件，手动运行带 `integration` 标签的 Go 测试而缺少配置时会明确失败，不会悄悄连接默认数据库。


2026-09-22 库存与消息修复新增 `tests/integration/inventory/transactions_test.go` 和 `tests/integration/order/transaction_test.go`，验证扣减/返库幂等、真实数据库提交前后故障、MQ 消费确认与订单持久决定/待发送事件。生产迁移现为 0001–0003、15 张业务表；完整 runner 同时验证存量流水保留与订单决定回填。真实 RocketMQ Broker 和运行时恢复调度尚未接通，消息层使用替身。说明见 [库存事务](../inventory_srv/TRANSACTIONS.md)。


2026-09-29 历史订单：`tests/integration/order/history_test.go` 通过真实隔离 MySQL 和实际 gRPC 验证两名用户订单分别计数/分页、软删除排除、相同创建时间下按 ID 倒序，以及 10/1/0 条分页。RPC 的 COUNT/列表故障、取消与截止时间另由 `order_srv/handler/order_list_test.go` 验证，不再将查询失败当成空列表。此批未修改协议或数据库结构。

2026-09-29 分类、品牌、轮播图更新：`goods_srv/handler/catalog_update_test.go` 验证查询、写入和事务提交失败不会被报告为成功或记录不存在，分类/轮播图列表区分读失败与正常空列表，并检查请求取消。`tests/integration/goods/catalog_update_test.go` 使用真实 MySQL + gRPC 验证分类开关关闭/重复关闭/开启、一级分类 NULL 父级及子分类层级保持、空品牌 Logo 补填、轮播图字段保存与重复更新。本批没有新增协议、表或迁移；登录错误与旧 Web 接口取消传播仍在后续修复批次。

2026-09-30 查询错误与取消：`user_srv/handler/mobile_lookup_test.go` 区分正常登录查询、真实查无用户、数据库故障和请求取消/超时，包括执行中的 SQL 被期限中断。`goods_srv/handler/goods_legacy_read_test.go` 验证旧商品列表的分类查询、计数、分页错误及取消传播；旧管理查询仍保留全部上架/库存准备状态，不改成商城过滤。既有正式构建、race 回归及真实 MySQL + gRPC 集成继续覆盖正常流程；本批无协议或数据结构变化。

2026-09-30 mxshop 对照复查补漏：`goods_srv/handler/catalog_read_delete_test.go` 覆盖品牌计数/分页、子分类查询/关联预加载、品牌/分类/轮播图删除的 SQL 与提交错误，以及取消、期限、正常空结果；品牌/轮播图创建也验证取消及查重/写入/提交失败。品牌分页不再预读全表，失败不返回遗留数据；删除先检查 Error 再判断 RowsAffected。此批无表、协议或迁移变化，不包含压测或启用限流。

2026-09-30 管理员用户列表：`user_srv/handler/user_list_test.go` 验证 COUNT 与分页错误均返回失败、取消/超时传递、总数与偏移溢出以及稳定排序；`tests/integration/user/list_test.go` 在隔离 MySQL + gRPC 中验证逐页读取、总数和软删除过滤。原先全表读取后分页失败仍返回旧用户数据的路径已移除；0 使用默认 1/10、每页最多 100 的兼容行为保留。

2026-09-30 商品删除：`goods_srv/handler/catalog_read_delete_test.go` 的删除故障矩阵加入商品，覆盖事务开始、SQL、提交错误、缺失商品、成功、取消和超时，另验证无效 ID 不执行 SQL。`tests/integration/goods/rpc_test.go` 在隔离数据库的回滚事务内验证只软删除指定商品、取消前不删除、重复删除返回 NotFound、相邻商品仍可读取；不在 AWS 上删除现有商品。提交/网络错误不能证明事务未提交，必须查询实际状态确认。
