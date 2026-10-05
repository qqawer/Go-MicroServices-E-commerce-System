# 商品快照导入：MySQL → Go → Elasticsearch

2026-10-05，按用户选择，使用仓库 `goods_srv/grpctest_goods_srv.sql` 的 **420 条历史商品**。这是本地历史快照实验，不是当前云端业务库的实时导出。商城搜索仍使用原来的 MySQL LIKE；本批完成独立导入与验证。

## 数据实际经过哪里

```text
仓库 SQL 备份
    → 本地实验 MySQL：es_goods_snapshot_20261005.goods
    → Go 命令 cmd/import-goods：只读事务，每批读取 100 条
    → POST /grpctest-goods-snapshot-20261005/_bulk
    → ES：420 份文档，_id 与商品 ID 一致（421–840）
```

MySQL 和 Go 命令容器使用现有实验 Docker 网络。MySQL 没有开放宿主机端口；Go 命令运行结束后容器自动删除。ES 文档继续保存在已挂载的 Mac `data` 目录。

- [恢复脚本](prepare-goods-snapshot.py)：只在新建的独立数据库恢复商品备份；同名库存在时拒绝覆盖。原 40 条实验商品不受影响。
- [导入命令](cmd/import-goods/main.go)：用 `id > 上批最后一个 ID ORDER BY id LIMIT 100` 读取未软删除的商品，以 Bulk `index` 动作写入。每个动作行后跟一行商品 JSON。
- [Mapping](cmd/import-goods/mapping.json)：名称、简介使用 SmartCN；ID、分类、品牌和整数价格用于精确条件；保留上架、包邮、新品、热门和库存准备字段。
- [核对脚本](verify-goods-import.py)：逐一比较全部文档的 `_id` 与全部导入字段，另用 Python Decimal 独立核对金额转换。

数据库通过只读、可重复读事务提供本次导入的一致视图。这里仅用于静态快照：后续修改、下架和删除仍需阶段 3 的同步机制；重复全量写入不会自动清除 ES 中已经不在源库里的旧文档。

## 字段转换

| MySQL | ES | 规则 |
| --- | --- | --- |
| `id` | `_id` 和 `id` | 同一商品固定 ID，重跑时替换同一份文档 |
| `name` | `name` | 原名称，SmartCN 分词；`name.raw` 用于完整值匹配 |
| `goods_brief` | `brief` | 原简介，SmartCN 分词 |
| `category_id` / `brands_id` | `category_id` / `brand_id` | 保存数值 ID |
| `shop_price` | `price_cents` | 元 → 分：MySQL 先转 `DECIMAL(18,2)`，再乘 100 转整数，避免 Go 浮点乘法后截断 |
| `on_sale`、`ship_free`、`is_new`、`is_hot` | 同名字段 | 保留布尔含义 |
| 备份缺少 `inventory_ready` | `inventory_ready: null` | 恢复库补一个可空字段表示未知，不推断库存已准备 |

例如 421 号“烟台红富士苹果12个”的 `shop_price = 44.9`，得到 `price_cents = 4490`。历史价格原本是 FLOAT，这一转换不能恢复它已经丢失的十进制精度；本快照 420 条价格均与独立 Decimal 换算结果一致。

全部 420 条的 `inventory_ready` 都是未知，因此加上 `inventory_ready: true` 过滤会得到空结果；这不是商品未导入。图片、订单和实际库存数量未进入该搜索文档。

## 本次验证结果

| 检查 | 结果 |
| --- | --- |
| 先导 10 条 | 10 条全部字段一致 |
| 全量导入 | 100 + 100 + 100 + 100 + 20，5 次 Bulk，420 条 |
| 金额与字段核对 | 420 条全部一致；金额另用 Decimal 计算 |
| 再执行完整导入 | 仍为 420 条，无重复文档 |
| 搜索名称“苹果” | 14 条命中 |
| 临时索引故意拒绝 425 号 | 程序非零退出，报告商品 ID 与 ES 原因；首批其他 99 条成功 |
| 移除临时拒绝规则后重跑 | 420 条完整恢复，全部字段一致；临时故障索引和 pipeline 已清理 |
| 原 40 条实验 | MySQL、ES 数量仍各为 40 |

首次全量的 Go 计时（读取、5 次 Bulk、一次 refresh 和 count）为 **121.54 ms**；连同一次性 Docker 命令容器启动为 **0.683 s**。不包括数据库恢复、编译、单独字段核对或开发时间。它是 420 条本机小数据的单次记录，不是吞吐量或线上性能结论。

原始记录保存在 `grpctest/.local/es-lab/goods-import/`，被 Git 忽略：`source.json`、`smoke.json`、`full.json`、`repeat.json`、`partial-failure.json` 和各索引的 verification JSON。源 SQL SHA-256 为 `392a36f5aad6fb8a74f7a1886ff0a99d7825a471f27c855e3ff7595fea9e6f7b`。

## 自己在 Kibana 查看

打开 [Dev Tools](http://localhost:5601/app/dev_tools#/console)，逐条执行：

```http
GET /grpctest-goods-snapshot-20261005/_count

GET /grpctest-goods-snapshot-20261005/_doc/421

GET /grpctest-goods-snapshot-20261005/_search
{
  "track_total_hits": true,
  "query": { "match": { "name": "苹果" } },
  "sort": [{ "_score": "desc" }, { "id": "asc" }]
}
```

依次可以看到 420 条、421 号商品内容，以及 14 条“苹果”命中。

## 重跑导入

前提：ES、SmartCN 和 `compare` profile 的 MySQL 已按 [实验 README](README.md) 启动。本机 Docker 是 ARM64。

从 `grpctest` 根目录开始：

```bash
cd labs/elasticsearch
# 仅首次恢复执行；数据库存在时跳过这一行，脚本会拒绝覆盖。
python3 prepare-goods-snapshot.py
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o ../../.local/es-lab/import-goods ./cmd/import-goods
cd ../..

docker run --rm --network grpctest-es-lab_lab \
  --mount "type=bind,src=$PWD/.local/es-lab/import-goods,dst=/import-goods,readonly" \
  -e 'MYSQL_DSN=root@tcp(mysql:3306)/es_goods_snapshot_20261005?timeout=5s&readTimeout=30s&writeTimeout=30s' \
  -e 'ES_URL=http://es:9200' \
  --entrypoint /import-goods \
  'mysql:8.0@sha256:7dcddc01f13bab2f15cde676d44d01f61fc9f99fe7785e86196dfc07d358ae2b' \
  -index grpctest-goods-snapshot-20261005

python3 labs/elasticsearch/verify-goods-import.py
```

这里复用已有 MySQL 镜像作为一次性 Go 程序的运行环境，`--entrypoint /import-goods` 执行的是编译好的 Go 程序，并不会再启动一个 MySQL 服务。上述空密码仅属于现有隔离实验库。

如需先试 10 条，在命令末尾使用一个新的专用索引名，例如 `-index grpctest-goods-snapshot-my-smoke -limit 10`；核对时给 Python 脚本加 `--index grpctest-goods-snapshot-my-smoke --limit 10`。不要向完整索引运行 10 条试导，否则最终总数校验会不符。

导入检查 HTTP 状态和 Bulk 每个 item 的状态，失败时报告 ID 并退出，不会把部分失败当成功。修正原因后重跑会按相同 `_id` 补入或替换。重用索引要求 `_meta.lab` 标记匹配；不同来源或重建使用新的索引名。本脚本不删除任何既有 ES 索引。

MySQL 实验容器的存储是 tmpfs：停止后可能丢失恢复的快照库，可以从保留的 SQL 重新准备；ES 使用宿主机目录，生命周期不同。

依据：[Bulk API](https://www.elastic.co/guide/en/elasticsearch/reference/8.19/docs-bulk.html)、[Refresh API](https://www.elastic.co/guide/en/elasticsearch/reference/8.19/indices-refresh.html)。
