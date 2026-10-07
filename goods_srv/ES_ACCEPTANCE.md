# 阶段 5：本地商品同步与搜索验收

2026-10-07 分两批在正在运行的本地学习环境逐步执行并核对。ES/IK 8.19.22，商品库 `es_store_demo`，索引 `grpctest-goods-store-demo-ik`，真实商品 RPC、HTTP 查询接口及商城学习页沿用 [ES_SEARCH.md](ES_SEARCH.md)。第 1 批完成正常流程，第 2 批完成实际 ES 停机、恢复、手动重试和商品服务重启。本地学习验收完成；云端上线、真实库存链路另行验收。

## 为什么这样做

观察同一商品 ID 在每一步的变化，可以区分数据库保存、索引同步和买家可见性。MySQL 保存事实数据，GORM 钩子向 ES 写搜索副本；买家搜索先由 ES 匹配，再回 MySQL 检查当前状态并取详情。

本地演示没有库存服务。真实 CreateGoods RPC 先创建待确认商品，返回 `inventory_ready=false`，并不会将其发布给买家。本批随后只对这件测试商品用 GORM 设置测试用的准备状态及上架状态，触发原有 AfterUpdate 钩子。这是明确的库存确认模拟，不是实际库存 RPC 或完整创建业务链路验收。

## 实现和实际结果

新增 [逐步操作命令](../cmd/goods-es-demo-step/main.go)，每次只执行一个步骤。创建、改资料、下架、删除调用已运行的真实商品 RPC；`ready` 为前述测试条件设置。命令限制演示库、IK 索引，其他步骤还核对商品 ID 与测试 SKU，避免改到原有样例。

本轮请求 key/SKU 为 `ESFLOW20261007`，商品 ID `17`，旧名称标记 `ESFLOW20261007A`，新名称标记 `syncrenameprobe`。每步独立读取 MySQL、ES 文档和真实 HTTP 买家搜索结果，并断言预期值：

| 步骤 | MySQL | ES | 买家搜索 |
| --- | --- | --- | --- |
| create | `ESFLOW20261007A 松露礼盒`，44.90 元，未准备、未上架 | ID 17，4490 分，两状态 false | 旧标记 0 件 |
| ready，模拟确认 | 已准备、已上架 | 相同 ID，两状态 true | 旧标记 1 件，返回 44.90 元 |
| update | `syncrenameprobe 松露礼盒`，39.90 元 | 同一 ID，3990 分，完整字段保留 | 旧标记 0 件，新标记 1 件，返回 39.90 元 |
| off | `on_sale=false` | 文档保留，`on_sale=false` | 新标记 0 件 |
| delete | `deleted_at` 已设置，软删除 | GET 文档 404，`found=false` | 两个标记均 0 件 |

查询是默认 OR，各个分词只要有一个匹配即可。因此改名后的零命中检查使用不同的专用标记，避免共同的“松露”“礼盒”造成正常召回。测试曾尝试只改数字标记末尾 A/B，发现它们仍共享词项，随后改用独立英文词 `syncrenameprobe`；这是测试关键词的调整。

ES 写入确认与可搜索之间有刷新间隔。本轮断言前显式调用 `_refresh` 使验证确定；业务写入代码仍沿用原来的近实时行为，没有每次写入强制刷新。

最终核对：原 40 条实验、420 条历史快照、旧 16 条 SmartCN 对照和当前 16 条 IK 演示的全部文档 ID/字段与本批开始前一致；HTTP 返回的原 16 件商品也逐项一致。测试商品留有 MySQL 软删除记录与创建请求记录，ES 已移除。本轮核对的是实际 HTTP 返回内容，未重新执行浏览器页面操作验收。

## 自己按步骤重跑

在 RPC 仓库根目录构建操作命令：

```sh
mkdir -p .local/es-lab/stage5-normal
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -mod=readonly \
  -o .local/es-lab/stage5-normal/goods-step ./cmd/goods-es-demo-step

demo_step() {
  docker run --rm --network grpctest-es-lab_lab \
    --mount "type=bind,src=$PWD/.local/es-lab/stage5-normal/goods-step,dst=/goods-step,readonly" \
    --mount "type=bind,src=$PWD/.local/es-lab/store-search/config.json,dst=/demo-config.json,readonly" \
    --entrypoint /goods-step \
    'mysql:8.0@sha256:7dcddc01f13bab2f15cde676d44d01f61fc9f99fe7785e86196dfc07d358ae2b' \
    -config /demo-config.json "$@"
}

# 每次新实验选择新 key；已删除商品的原创建 key 不能复用。
demo_step -step create -key ESFLOW20261007R2
```

记下输出中的新 ID；下面每条命令分别执行，替换 `新ID`，每一步检查结果后再继续：

```sh
demo_step -step ready  -key ESFLOW20261007R2 -id 新ID
demo_step -step update -key ESFLOW20261007R2 -id 新ID
demo_step -step off    -key ESFLOW20261007R2 -id 新ID
demo_step -step delete -key ESFLOW20261007R2 -id 新ID
```

创建/模拟上架后去商城搜索 `ESFLOW20261007R2A`，修改后搜索 `syncrenameprobe`。每次更新后等下一次索引刷新再搜索；Kibana 中也可手动执行以下请求。本轮的 ID 是 17，重跑时替换为新 ID：

```http
GET /grpctest-goods-store-demo-ik/_doc/17

POST /grpctest-goods-store-demo-ik/_refresh

GET /grpctest-goods-store-demo-ik/_search
{
  "query": {"term": {"id": 17}}
}
```

MySQL 可通过本地实验容器核对：

```sh
docker exec grpctest-es-lab-mysql-1 mysql --default-character-set=utf8mb4 -uroot \
  -e 'SELECT id,name,CAST(shop_price AS DECIMAL(18,2)) AS price,on_sale,inventory_ready,deleted_at FROM goods WHERE id=17' \
  es_store_demo
```

每次结束都对本次创建的商品执行 delete；同时进行多个实验时，固定新名称可能匹配多件商品，应按 ID 核对。本机证据在 `.local/es-lab/stage5-normal/`，包含各步骤命令日志、`*-observed.json`、初始文档和最终商品列表。命令构建和 `go vet` 通过，业务实现本批没有修改。

## 第 2 批：实际 ES 故障、恢复和商品进程重启

这批复用逐步操作命令，没有修改业务逻辑。新建测试商品 ID `18`，请求 key/SKU `ESFAULT20261007`，通过真实 RPC 创建，再按第 1 批方式模拟库存确认；初始价格 44.90 元，上架且可搜索。

随后实际执行 `docker stop grpctest-es-lab-es-1`，让已运行的商品服务无法访问 ES。只停止 ES；MySQL 与商品 RPC 保持运行。故障操作通过真实 RPC 发起，验证脚本在 finally 中启动 ES，确保故障检查失败时也恢复服务。

| 故障期间的操作 | 实际结果 | MySQL 核对 |
| --- | --- | --- |
| 修改 ID 18 的名称/价格 | gRPC Internal，更新商品失败 | 名称、44.90 元和原状态均未改变 |
| 下架 ID 18 | gRPC Internal | `on_sale` 仍为 true |
| 删除 ID 18 | gRPC Internal，提示查询后重试 | `deleted_at` 仍为空 |
| 用 `ESFAULT20261007NEW` 新建另一件商品 | gRPC Internal，提示用原请求编号重试 | 商品行和创建请求记录均为 0 条 |
| 买家 HTTP 搜索 | HTTP 503，`商品服务暂不可用` | 未返回 SQL 搜索结果或空列表伪装成功 |

每次失败后都读取测试商品的 MySQL 字段，并与停机前逐项比较，确认当前事务回滚。新建使用不同请求 key，确保真的尝试创建；重复已完成的旧创建请求不需要重新写 ES，不能用它证明新增故障。

ES 启动并达到 healthy 后，先不重试：MySQL 与 ES 都仍是原名称、44.90 元，搜索重新返回这件商品；失败的新建商品仍不存在。恢复 ES 没有自动执行失败操作。

再手动调用相同的 update RPC，ID 18 改成 `syncrenameprobe 松露礼盒`、39.90 元，ES 对应 3990 分，搜索结果一致。执行 `docker restart grpctest-es-store-goods-rpc` 后，核对 MySQL、ES 的业务字段和 HTTP 响应均与重启前一致，相同 ID 未产生重复文档。演示入口在启动时会从演示库重新写入当前商品；这是该演示入口的全量重写逻辑，不能推断为正式商品服务有自动补偿队列。

恢复后的 off/delete 手动重试也成功。失败的新增请求用原 key `ESFAULT20261007NEW` 重试成功，得到 ID `20`；再次调用同一个创建请求仍返回 ID 20，MySQL 商品行、创建请求记录和 ES 文档各 1 份。ID 19 在回滚中消耗，自动递增 ID 出现间隔正常。

最后通过真实删除 RPC 清理 ID 18、20。MySQL 保留软删除和创建请求记录，ES 文档已移除。原 40、420、旧 16、当前 16 条文档的 ID 与全部业务字段再次核对一致，HTTP 原 16 件商品逐项一致。ES/MySQL/Kibana 均 healthy，演示 RPC 正常运行；本批验证的是实际 HTTP/RPC 返回和存储内容，没有重新操作浏览器页面。

## 怎样重跑故障这一批

先按上文定义 `demo_step`，用新 key 创建并模拟确认，记下新 ID。下列占位 `新ID` 必须替换；每条故障请求预期非零退出，应单独执行，避免脚本的 `set -e` 跳过恢复命令：

```sh
demo_step -step create -key ESFAULT20261007R2
demo_step -step ready -key ESFAULT20261007R2 -id 新ID
docker stop grpctest-es-lab-es-1
```

ES 停止期间分别尝试：

```sh
demo_step -step update -key ESFAULT20261007R2 -id 新ID
demo_step -step off -key ESFAULT20261007R2 -id 新ID
demo_step -step delete -key ESFAULT20261007R2 -id 新ID
demo_step -step create -key ESFAULT20261007R2NEW
curl -i 'http://127.0.0.1:18022/g/v1/store/goods?q=ESFAULT20261007R2A'
```

每步用上文的 MySQL 查询核对回滚，随后恢复 ES：

```sh
docker start grpctest-es-lab-es-1
docker compose -f labs/elasticsearch/compose.yaml start --wait --wait-timeout 120 es
```

先核对原状态，再重试 update，重启商品 RPC 后核对当前状态：

```sh
demo_step -step update -key ESFAULT20261007R2 -id 新ID
docker restart grpctest-es-store-goods-rpc
```

随后重试 off/delete，以及原失败的新建 key；重复新建要使用完全相同的 key 和请求内容。结束时对所有本次创建的商品执行 delete。ES 写入后先等待刷新或手动 `_refresh`，再对搜索结果作断言。

本机证据在 `.local/es-lab/stage5-recovery/`：各 `failed-*.log`、`failure-evidence.json`、`recovered-before-retry.json`、`retried-update.json`、`after-rpc-restart.json`、`retried-create.json`、`cleanup.json` 和前后商品快照。

## 验收结论与边界

结合前两批的 [隔离同步测试](ES_SYNC.md)、[搜索测试](ES_SEARCH.md) 和 [Bulk 导入验证](../labs/elasticsearch/GOODS_IMPORT.md)，本地阶段 5 已覆盖正常创建/重复同步、连续资料与状态变更、删除、中文检索、组合筛选、稳定分页/空结果、陈旧索引校验、Bulk 部分失败、实际 ES 不可达、恢复后手动重试及进程重启。ES 这一学习主题可以结束，下一主题为链路追踪。

验收没有把 MySQL 和 ES 变成共同事务。ES 请求超时但实际写入成功，或 ES 成功后 MySQL 提交失败，仍可能出现差异；这些情况需查询核对并修复，不能看到错误就断言两边一定都没改。本次实际停机验证的是明确不可达时的失败路径。没有持久同步任务或后台自动重试，直接 SQL/跳过钩子的更新也不会自动同步。

云端配置与服务仍未切换，实际库存确认、上线流程和业务库全量数据需在部署时另行核对。这些部署工作与本地学习验收分开记录。
