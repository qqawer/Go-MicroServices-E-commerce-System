# 阶段 5，第 1 批：一件商品的正常同步流程

2026-10-07 在正在运行的本地学习环境逐步执行并核对。ES/IK 8.19.22，商品库 `es_store_demo`，索引 `grpctest-goods-store-demo-ik`，真实商品 RPC、HTTP 查询接口及商城学习页沿用 [ES_SEARCH.md](ES_SEARCH.md)。本批完成正常流程；故障恢复、进程重启等剩余验收继续分批进行。

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

下一批：实际验证 ES 故障和恢复、手动重试及进程重启。完成剩余场景后再评估阶段 5 是否可以结束，不把本批标记为全部验收完成。
