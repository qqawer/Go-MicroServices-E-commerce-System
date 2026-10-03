# Elasticsearch 小数据实验（阶段 0、1）

本目录独立于商城运行配置与 Go 模块。目标是看见分词、匹配、排序和写入的实际结果，再决定如何接商品服务。

## 第 0 批：环境与版本

- ES：`8.19.22`，基础镜像固定多架构 digest，Mac ARM64 使用原生 arm64 镜像。
- 中文分词：官方 `analysis-smartcn 8.19.22`，随镜像构建安装；不是 IK，也不预设两者切词相同。
- 官方 Go 客户端：`github.com/elastic/go-elasticsearch/v8 v8.19.7`，锁定于本目录 `go.mod/go.sum`；本机 Go `1.25.6`。商城根模块原有 `olivere/elastic/v7` 不在本批迁移。
- ES 容器：2 CPU、2 GiB 内存上限、1 GiB JVM 堆；Docker 的全部内存不等于 ES 能任意使用的内存。
- ES 地址：`http://127.0.0.1:9201`；没有发布 9300，专用 bridge 网络 `grpctest-es-lab_lab`。
- 可选 Kibana：`8.19.22`，镜像固定 digest，`ui` profile 启用；本机入口 `http://127.0.0.1:5601`，限制 2 CPU / 2 GiB，连接同一网络内的 `http://es:9200`。
- ES 配置：Mac 的 `grpctest/.local/es-lab/elasticsearch/config` → 容器 `/usr/share/elasticsearch/config`。
- ES 数据：Mac 的 `grpctest/.local/es-lab/elasticsearch/data` → 容器 `/usr/share/elasticsearch/data`。两者均为目录绑定挂载，不依赖容器自身文件层。
- 本地合成数据实验关闭 ES 身份认证/TLS。此配置仅限本机实验，不用于 AWS 部署。

在 RPC 仓库根目录执行：

```bash
cd labs/elasticsearch
# 首次准备；prepare-dirs.sh 应在 ES 停止时运行。
docker compose build es
bash prepare-dirs.sh
docker compose up -d --no-build --wait --wait-timeout 180
go run .
curl -fsS 'http://127.0.0.1:9201/_cluster/health?pretty'
docker compose exec -T es bin/elasticsearch-plugin list
docker compose ps
```

生命周期：

```bash
docker compose --profile '*' stop          # 停止全部实验容器；ES 数据保留，MySQL tmpfs 清空
docker compose --profile '*' start --wait  # 启动已创建的实验容器；SQL 对照需重新 seed
docker compose --profile '*' down         # 删除实验容器与网络，保留 Mac 的 config/data
```

需要清空 ES 重练时，先 `down`，将 Mac 上的 `elasticsearch` 目录改名备份，再执行 `bash prepare-dirs.sh` 和启动命令；目录挂载不会被 `docker compose down -v` 删除。不要改项目名、复用商城 Compose，或删除机器上其他旧 ES 容器/卷。

本机实际目录：

```text
/Users/outsider/Downloads/Golang_Development/grpctest/.local/es-lab/elasticsearch/
├── config/   # elasticsearch.yml、jvm.options、keystore 等配置
└── data/     # ES 管理的索引数据文件，不手工修改里面的文件
```

`prepare-dirs.sh` 从固定版本实验镜像补齐初始配置，已有 `elasticsearch.yml` 时保留原配置；同时让 Mac 用户拥有文件，ES 容器通过 GID 0 读写。`.local/` 被 Git 忽略，不提交数据与 keystore。若修改 `elasticsearch.yml`，也需留意 Compose `environment` 中的同名启动配置。

2026-10-03 按学习需求从 Docker 命名卷迁移到上述目录：先停止 Kibana 和 ES，再复制完整 config/data；原卷 `grpctest-es-lab_es-data` 保留为迁移前备份，已不在新 Compose 的挂载和清理范围内。

版本依据：[Docker 安装](https://www.elastic.co/guide/en/elasticsearch/reference/8.19/docker.html)、[SmartCN](https://www.elastic.co/docs/reference/elasticsearch/plugins/analysis-smartcn)、[官方 Go 8.19 客户端](https://www.elastic.co/guide/en/elasticsearch/client/go-api/8.19/installation.html)、[v8.19.7 源码](https://github.com/elastic/go-elasticsearch/tree/v8.19.7)。客户端与服务端同为 8.19 系列；补丁号并不要求相等，实际连接由 `go run .` 验证。

2026-10-02 实测：两业务仓库初始工作区干净；Mac ARM64 / 16 GiB，Docker Engine 29.2.1 / Compose 5.0.2，Docker VM 约 7.65 GiB，磁盘剩余约 93 GiB。ES `green`，SmartCN `8.19.22`，Go 连接通过；容器内存上限 2,147,483,648 字节，CPU 上限 2。旧 ES 7.x 容器保持停止，其他正在运行的容器未修改。

本机首次下载卡在 `docker-credential-desktop get`。只为公开镜像建立了不含凭据的 `.local/es-lab/docker/config.json`，没有修改原 `~/.docker/config.json`。若同样卡住，在 RPC 根目录执行以下一次性配置，然后回到本目录重试构建：

```bash
mkdir -p .local/es-lab/docker
python3 - <<'PY'
import json
from pathlib import Path
Path('.local/es-lab/docker/config.json').write_text(json.dumps({
    'cliPluginsExtraDirs': [str(Path.home() / '.docker/cli-plugins')]
}))
PY
# 先取得当前已确认是本机的 Docker endpoint，再切换客户端配置。
export DOCKER_HOST="$(docker context inspect --format '{{.Endpoints.docker.Host}}')"
export DOCKER_CONFIG="$PWD/.local/es-lab/docker"
```

本机 Docker Desktop 的 `internal: true` 网络未发布宿主机端口，因此最终用专用 bridge 网络加 `127.0.0.1` 端口绑定，并由宿主机 Go 客户端验证可达。

### 跟随语雀在 Kibana 中练习

```bash
docker compose --profile ui up -d --no-build --wait --wait-timeout 300 kibana
docker compose stop kibana  # 只停 Kibana，ES 继续运行
```

首次需下载镜像；若本机凭据助手阻塞，使用上面的临时 Docker 配置。浏览器打开 [Dev Tools](http://127.0.0.1:5601/app/dev_tools#/console)，本地实验无需登录。左侧写 ES 请求，点击该请求旁的运行按钮，右侧查看 JSON 结果。先分两次执行：

```http
GET /

GET _cat/indices?v
```

第一条确认连接的 ES 版本与集群名称，第二条查看索引；其中 `grpctest-es-lab-products` 是前面准备的实验商品。个人练习可另建 `yuque-practice` 索引，避免与重复运行的商品实验互相影响。Kibana 自己也会创建系统索引，用于保存界面配置。

入口与连接关系是“浏览器 → localhost:5601 的 Kibana → Docker 网络中 es:9200”。宿主机 `localhost:9201` 是直接访问 ES 的入口，在 Dev Tools 中只写 `GET /…` 等请求路径即可。启动配置依据 [Kibana 8.19 Docker 文档](https://www.elastic.co/guide/en/kibana/8.19/docker.html)。

语雀课程使用较旧版本和 IK；这里为 ES 8.19.22 + SmartCN。分词示例使用 `"analyzer": "smartcn"`，结果不承诺与 `ik_max_word/ik_smart` 相同；旧的自定义 mapping type 写法也不能直接照搬。遇到对应章节时再逐条核对，不提前更换当前分词器。

## 第 1 批：文档、分词、倒排索引

商城原查询位于 `goods_srv/handler/goods.go` 的 `GoodsList`，用 `name LIKE '%关键词%'` 查名称中的连续文本。ES 全文查询把商品文本和查询文本分别分析成词项，再按词项找商品；这是搜索语义的改变。

| 名词 | 本实验中的具体对象 |
| --- | --- |
| 文档 document | `products.ndjson` 每行一件合成商品的 JSON，`_id` 使用商品 `id` |
| 索引 index | `grpctest-es-lab-products`，保存 40 份文档及检索结构；不是 MySQL 的某一条索引定义 |
| Mapping | `mapping.json`：`name/brief` 是全文 `text`，`category/brand` 是精确 `keyword`，价格是整数分 |
| 分词 analysis | `smartcn` 将“苹果手机”变成“苹果 / 手机”；`standard` 在本样例中是“苹 / 果 / 手 / 机” |
| 倒排索引 | 从词查文档，而不是先拿每一件商品的全文做字符串判断 |

Mapping 同时设定 1 个主分片、0 个副本：这个小实验只有一个节点，不安排无法分配到其他节点的副本。`green` 代表当前配置下分片均已分配，不代表这套单节点实验具备容灾能力。

将 40 件商品导入后，名称字段的词项命中如下；这是用 `term` 查询验证出来的文档集合，是倒排结构的教学投影，不是 Lucene 内部文件格式：

```text
苹果   → 1, 2, 3, 4, 6, 9, 10, 12, 31
手机   → 1, 2, 5, 6, 9, 10, 13, 14, 22, 23, 24
红富士 → 3

“苹果手机”经分词 → 苹果 + 手机
OR：取并集 → 15 件
AND：取交集 → 1, 2, 6, 9, 10
```

只搜“苹果”不能自动区分水果和手机，因为二者确实共享这个词。分类过滤可以限制业务范围。需要连续词序时应另研究 `match_phrase`；本实验的 AND 只要求词项均存在。

分词原理见 [Text analysis](https://www.elastic.co/docs/manage-data/data-store/text-analysis)；本次全部切词以 `_analyze` 实际响应为准。

## 第 2 批：同数据的搜索与精确过滤

要求 Python 3.10+ 标准库，无需 pip。进入本目录，逐条执行并阅读输出：

```bash
# ES 已按阶段 0 启动。现在添加专用、临时 MySQL 对照库。
docker compose --profile compare up -d --no-build --wait --wait-timeout 180
python3 lab.py analyze
python3 lab.py seed
python3 lab.py search
```

MySQL 使用项目现有测试镜像的固定 digest，实测 `8.0.46`，独立数据库 `es_lab`、排序规则 `utf8mb4_0900_ai_ci`。内存上限 768 MiB，数据在 512 MiB 的 tmpfs，停止即丢失；无宿主机端口，脚本通过本容器内的 mysql 客户端访问。它与业务库没有数据或配置联系。ES 和 MySQL 均从同一份 40 行 fixture 导入，没有从商城导出数据。

`seed` 会重建固定名称、带本实验 `_meta.lab` 标记的 ES 索引和专用 SQL 表。它不会修改其他索引；不带 marker 的同名索引会被拒绝。每次 seed 连续 Bulk 两遍，逐项验证成功且总数始终为 40。

2026-10-02 实测命中数量如下。此表为了单独比较关键词语义，尚未过滤上架状态；因此含 9 号下架商品和 10 号库存未准备商品。SQL 按 ID 排序，ES 按 `_score` 降序、同分按 ID 升序。

| 查询 | MySQL `name LIKE` | ES `match(name)` OR | ES `match(name)` AND | ES `multi_match(name^3, brief)` OR |
| --- | ---: | ---: | ---: | ---: |
| 苹果 | 9 | 9 | 9 | 12 |
| 苹果手机 | 4 | 15 | 5 | 19 |
| 手机苹果 | 0 | 15 | 5 | 19 |
| 红富士苹果 | 1 | 9 | 1 | 12 |
| 不存在的火星商品 | 0 | 1 | 0 | 2 |
| zzznomatch20261002 | 0 | 0 | 0 | 0 |

对应几件关键样例：

- 1 号“苹果手机 128G”：LIKE 与 AND 都匹配“苹果手机”。
- 2 号“苹果 智能 手机 256G”：不含连续的“苹果手机”，LIKE 不匹配；两个词项都在，AND 匹配。
- 3 号“红富士苹果 5斤”：默认 OR 查询“苹果手机”时，因为“苹果”而命中；AND 不命中。
- 7 号“水果礼盒”：名称不含苹果，简介含“红富士苹果”，只有包含简介的全文查询能命中“苹果”。
- 6 号“苹果手机保护壳”：两个词都在，也能命中 AND；要区分配件与手机，需要分类条件。
- “不存在的火星商品”分成“不 / 存在 / 的 / 火星 / 商品”，默认 OR 命中对照文档里的“商品”。这不是空结果词；独有字符串才得到四组全空。

`match` 会分析查询文本，默认 OR；`term` 直接匹配已索引的词项，不替你分词。实测 `term(name, 苹果手机)` 得到空，而 `term(name.raw, 苹果手机 128G)` 精确命中 1 号。`name.raw` 是同一名称的 keyword 子字段，并非第二份商品。[match 说明](https://www.elastic.co/docs/reference/query-languages/query-dsl/query-dsl-match-query)

### 名称和简介怎样参与排序

ES 全文字段默认用 BM25 评分，会参考词在该字段中的稀有程度、词频和字段长度。`_score` 是当前查询下的相关性分数，不是概率，也不能拿两次不同查询的分数直接比较。[Similarity 说明](https://www.elastic.co/docs/reference/elasticsearch/index-settings/similarity)

`multi_match` 的 `best_fields` 对各字段分别算分，再取最佳字段分数；本实验没有设置 `tie_breaker`，因此不把名称与简介得分简单相加。`name^3` 给名称字段分数乘 3，也不保证所有名称命中永远排在简介命中之前。[multi_match 说明](https://www.elastic.co/docs/reference/query-languages/query-dsl/query-dsl-multi-match-query)

固定样本中，31 号名称是“苹果”、简介是“字段权重对照商品”，32 号把二者对调。只看这两件商品时：

| 查询字段 | 31 号得分（名称命中） | 32 号得分（简介命中） | 排序 |
| --- | ---: | ---: | --- |
| `name, brief` | 2.0304 | 3.7159 | 32 → 31 |
| `name^3, brief` | 6.0913 | 3.7159 | 31 → 32 |

过滤到这两件商品并不会重新计算全索引的字段统计，因此未加权时，两个字段分数不必相同。`search` 保存 31 号的完整 `_explain`，可以查看 boost、idf、tf 的计算。上述具体分数来自刚 seed 后；写入或删除实验会改变索引统计，重建 seed 后再对照此表。

### 组合筛选的具体请求

```json
{
  "query": {
    "bool": {
      "must": [{"multi_match": {
        "query": "苹果手机", "fields": ["name^3", "brief"], "operator": "and"
      }}],
      "filter": [
        {"term": {"category": "phone"}},
        {"term": {"brand": "apple"}},
        {"range": {"price_cents": {"gte": 500000, "lte": 650000}}},
        {"term": {"on_sale": true}},
        {"term": {"inventory_ready": true}}
      ]
    }
  }
}
```

结果只有 1 号“苹果手机 128G”，价格 599900 分，即 5999 元，SQL 相同连续词与结构化条件也得到 1 号。只保留手机分类、上架及库存准备过滤时，ES 得到 1、2 号，6 号配件和 9、10 号状态不合格商品被排除。`filter` 决定是否通过，且不为这些条件增加相关性分数。[Query/filter context](https://www.elastic.co/docs/reference/query-languages/query-dsl/query-filter-context)

这只是合成数据的静态筛选；尚未解决真实 MySQL 与 ES 延迟、下架后的陈旧索引、分类后代范围和商城分页合同。这些属于阶段 2–4。

## 第 3 批：写入、更新、删除与近实时可见性

```bash
python3 lab.py writes
```

已验证以下结果：

| 动作 | 实际结果 | 为什么这样验证 |
| --- | --- | --- |
| `PUT /索引/_doc/90` | 新增成功，实时 GET 读到；refresh 前搜索 0 条，显式 refresh 后 1 条 | 写入已确认与搜索可见是两个时刻 |
| `POST /索引/_update/90?refresh=wait_for` | 改价为 12345 分，搜索读回相同价格 | 等待刷新后再断言，避免偶发时序结果 |
| `DELETE /索引/_doc/90?refresh=wait_for` | 搜索 0 条 | 验证删除的搜索可见性 |
| Bulk：91 号正常，92 号价格为字符串 `invalid` | HTTP 200、`errors=true`；91 状态 201，92 状态 400 | Bulk 不是整批事务，HTTP 成功不等于每项成功 |
| 删除临时 91 号 | ES 总数恢复 40 | 实验不留下额外文档 |

为稳定观察刷新前后的差别，脚本短暂将这个实验索引的 `refresh_interval` 设为 `-1`，在 `finally` 中恢复 `1s`，并显式触发一次 refresh。这不是自然刷新延迟的计时实验；refresh 使新 segment 对搜索可见，也不是持久化备份或数据库事务提交。[近实时搜索](https://www.elastic.co/docs/manage-data/data-store/near-real-time-search)、[refresh 参数](https://www.elastic.co/docs/reference/elasticsearch/rest-apis/refresh-parameter)

Bulk 使用“动作行 + 文档行”的 NDJSON，并以换行结束；脚本检查每一项的状态和 `errors`，失败项保留具体 `_id` 与错误原因。[Bulk API](https://www.elastic.co/docs/api/doc/elasticsearch/operation/operation-bulk)

## 结果文件与重跑边界

每步完整 HTTP 请求、响应及 SQL 都写入 RPC 根目录 `.local/es-lab/<步骤>.json`（被 Git 忽略）。同一步重跑会覆盖该记录。`analyze` 不要求已经 seed，`search/writes` 需要 seed；`writes` 遇到异常后可重新 seed 恢复基线。

完成分步学习后，可执行 `python3 lab.py all` 从头复验。本轮分步执行与整体重跑都通过，重复导入均为 40 条；没有测 QPS、P95 或宣称速度提升。两种查询结果集合不同，微小数据的耗时也不具有性能代表性。

本目录新增的都是实验材料：`Dockerfile/compose.yaml` 管环境，独立 `go.mod/go.sum/main.go` 验证官方客户端，`mapping.json/products.ndjson/lab.py` 定义和执行实验。本批不修改商品服务、Web、业务数据库或部署配置，尚未进行线上接入。
