# 从搜索框到 ES：阶段 4 本地接通

2026-10-05 已接通真实商城前端、商品 Web handler、gRPC、ES 和 MySQL。学习入口是 <http://127.0.0.1:3002/#/goods>，只在本机访问，使用独立的 16 条合成商品。原有 `3001/8022` 服务和云端配置未切换；420 条历史商品仍在原快照库与索引。

## 原来与现在

原来买家搜索按商品名称执行 SQL `LIKE`。现在商品服务启用 ES 配置后，`StoreVisibleOnly=true` 的买家查询走下面的链路，管理查询仍走原 SQL：

```text
商城搜索框
  → GET /g/v1/store/goods?q=苹果&p=1&pnum=12
  → goods-web：校验参数，最多等待 5 秒
  → GoodsList RPC：名称和简介交给 ES，最多等待 3 秒
  → ES 返回按相关性排序的商品 ID，以及索引命中总数
  → MySQL 回读这些 ID，核对当前状态并读取商品详情
  → 按 ES 原顺序返回给页面
```

ES `multi_match` 查询 `name^2`、`brief`，沿用默认 OR：任一分词可匹配，名称加权，不要求输入字符串连续出现。常见词也可能扩大结果，因此“空结果”实验使用“火星探测器”，不使用带有“的”等常见词的长句。规则见 [Multi-match 官方说明](https://www.elastic.co/guide/en/elasticsearch/reference/8.19/query-dsl-multi-match-query.html)。

品牌、所选分类及其后代、价格、新品、热门、上架和库存准备状态放在 ES filter。价格参数仍是整数元，向 ES 查询时乘 100 转整数分。结果按 `_score desc, id asc` 排序，商品 ID 用于相同分数下的确定顺序；不同请求之间发生商品变更时，页间结果仍可能移动，不是跨页快照。

MySQL 回读再次检查软删除、上架、库存准备、品牌、分类、价格和标记，返回当前价格。关键词与相关性仍由 ES 决定，因此名称/简介修改在索引刷新前可能按旧文本命中。下单继续依赖原有独立校验，搜索结果不保证实际库存数量。

## 分页和数量的明确约定

`GoodsListResponse` 增加可兼容旧调用方的 `search_index` 字段，四份商品 proto 已同步生成。Web 在 ES 查询响应中传回 `search_index: true`。

- `total` 是 ES 当前查询的命中总数，不是 MySQL 的实时有效商品总数。
- `data` 只保留这一页候选中通过 MySQL 校验的商品，可能不足一页，也可能为空；不拿后页商品补位，不偷偷改变页码含义。
- 页面显示“匹配约 X 件”，说明商品更新可能影响数量；非零 total 的空页显示“这一页暂无在售商品”。
- `from=(page-1)*size`，默认每页 10、最多 100；最多浏览前 10000 条，最后一页按窗口剩余额度截断。页面也限制到这一范围，超出范围的直接 API 请求返回 400。使用的是 [ES 的 from/size 分页](https://www.elastic.co/guide/en/elasticsearch/reference/8.19/paginate-search-results.html)，本批没有引入深分页游标。

ES 超时、分片失败或不可用不会回退到 LIKE 并伪装成功：依赖错误为 gRPC Unavailable / HTTP 503，请求超时为 504，取消为 408。ES 无匹配时直接返回空数组，不执行无 ID 条件的 MySQL 全表查询。

## 本地实际运行在哪里

| 组件 | 地址 / 数据 |
| --- | --- |
| 商城学习页 | `127.0.0.1:3002`，真实 store-web 前端；页面标明合成样例 |
| 商品 HTTP | `127.0.0.1:18022`，复用真实查询 handler，仅注册商品列表、详情、分类 GET 路由 |
| 商品 RPC | `127.0.0.1:15052` → Docker `grpctest-es-store-goods-rpc:50052`，复用真实 GoodsServer |
| MySQL | 既有本地实验容器，新建独立库 `es_store_demo` |
| ES | 既有 `127.0.0.1:9201`，独立索引 `grpctest-goods-store-demo-ik` |
| 实际服务配置 | `grpctest/.local/es-lab/store-search/config.json`，`elasticsearch.enabled=true` |

启动入口是两仓的 `cmd/goods-es-demo/main.go`。RPC 使用本地 JSON 配置，跳过云端 Nacos/Consul 注册；正常商品服务的 Nacos 入口仍然可用。演示商品的库存准备状态是**明确设置的测试条件**，没有启动真实库存、用户、购物车、订单或支付链路。前端演示代理也只指向本地只读 HTTP，不能通过此入口登录、下单。

首次写入演示商品使用 GORM Create，自动触发上一批钩子；重启演示 RPC 时重新读取现存演示商品并调用同一索引写入逻辑，不重置商品 SQL 值。这不是后台同步进程，不提供云端补偿保证。

打开页面后可依次试：

1. 搜索“苹果”：5 件，包括名称不含苹果的“山野果园礼盒”，它由简介命中。
2. 再选“数码用品”：2 件；价格/品牌/新品等组合条件可通过 HTTP 参数验证。
3. 搜索“火星探测器”：0 件。
4. 清除筛选：16 件，每页 12 件，第二页剩 4 件。

Kibana 可以执行 `GET /grpctest-goods-store-demo-ik/_search` 查看对应索引。原来的快照索引 `grpctest-goods-snapshot-20261005` 仍是 420 条，不能把其未知库存状态直接设为真实可售。

## 2026-10-06：切换为 IK

此前名称和简介已经使用中文 SmartCN，并非默认英文分词。现在对照本地 mxshop `2bbe1b3a0eaa59bf26a3a2047769979a0e206c11` 的 `mxshop_srvs/goods_srv/model/es_goods.go`，两个字段改为 `ik_max_word`；与 mxshop 一样未单独覆盖 `search_analyzer`，写入与全文查询都使用它。查询仍是默认 OR，搜索“青苹果手机”也可能匹配含“苹果”的水果。

安装来源是 [IK 维护方](https://github.com/infinilabs/analysis-ik) 的 `https://get.infini.cloud/elasticsearch/analysis-ik/8.19.22`。已检查安装包内 `elasticsearch.version=8.19.22`，Dockerfile 固定包 SHA-256 `2cf38b4e21af1514dc30b7b641d630f9cf3bfc02479985185e36050e61bfda23`。镜像同时保留 SmartCN，容器重建后两个插件均为 8.19.22。安装器提示包仍含旧式 Security Policy；本轮运行验证通过，此提示不代表未来 ES 版本兼容。

IK 默认配置和词典位于宿主机 `.local/es-lab/elasticsearch/config/analysis-ik/`，其中 `IKAnalyzer.cfg.xml` 可配置扩展词典。本轮未添加自定义词典或远程词典；目录权限沿用 UID/GID 的共享方式，不使用 `chmod 777`。

迁移时先备份配置、RPC 二进制和原索引文档，再停止本地演示 RPC、重建 ES。因为已有字段不能原地更换索引分词器，演示 RPC 使用新索引 `grpctest-goods-store-demo-ik`，启动时从同一个 `es_store_demo` MySQL 库读取 16 条商品，经原有索引写入逻辑重新写入。没有重新造商品或修改 SQL 字段；搜索框、Web、gRPC 接口及 GORM 钩子流程保持一致。

原 `grpctest-goods-store-demo` 保留为切换时的 SmartCN 对照，不再接收演示商品更新；40 条小实验与 420 条快照仍使用原索引。保留的旧索引不是自动回滚开关：商品变动后回切仍需补齐数据。回退本轮可使用 `.local/es-lab/ik-migration/` 中的旧 RPC 二进制和配置，恢复旧索引前核对数据。

实际验证：

- `_analyze`：“青苹果手机”用 SmartCN 得到“青 / 苹果 / 手机”，IK max 得到“青苹果 / 苹果 / 手机”；“中国科学技术大学”用 IK smart 得到完整词，IK max 得到“中国科学技术大学 / 中国 / 科学技术 / 科学 / 技术 / 大学”。
- 新索引的 16 个 ID 和全部业务字段与切换前一致；原 40、420、16 条文档逐项核对一致。
- 真实 HTTP：“苹果”5 件，加数码分类 2 件；“火星探测器”0 件；全部商品第 2 页 4 件，总数 16，响应 `search_index=true`。
- Chrome 搜索框实际输入“青苹果手机”后显示约 5 件，“青苹果手机 128G”排第一。
- 既有真实 MySQL/ES/gRPC 集成测试通过，覆盖钩子同步、中文检索、筛选、分页、陈旧索引校验及故障回滚。RPC `bash ci/check.sh` 通过。ES healthy，Kibana available。

证据保存在 `.local/es-lab/ik-migration/`，包括 `analyze.json`、`http-checks.json`、`integration.log` 和 `ci.log`。本次是本地演示切换，不代表云端部署；阶段 5 整体验收仍另行推进。

Kibana 可直接执行：

```http
GET _analyze
{
  "analyzer": "ik_max_word",
  "text": "中国科学技术大学"
}

GET /grpctest-goods-store-demo-ik/_mapping
```

已有环境升级时，在 RPC 仓库根目录先备份 `.local/es-lab/store-search/` 和 ES config，再执行：

```sh
docker compose -f labs/elasticsearch/compose.yaml build es
docker stop grpctest-es-store-goods-rpc grpctest-es-lab-es-1
bash labs/elasticsearch/prepare-dirs.sh
docker compose -f labs/elasticsearch/compose.yaml up -d --no-build --no-deps --wait es
```

随后将本地演示 JSON 的 `elasticsearch.index` 改成 `grpctest-goods-store-demo-ik`，按下文命令重编译覆盖原 RPC 二进制，然后 `docker start grpctest-es-store-goods-rpc`。首次启动仍按下文完整命令；不要重新创建同名 RPC 容器。

## 复现与停止

前提：按 [实验 README](../labs/elasticsearch/README.md) 启动本地 MySQL、ES、IK。下列为首次启动；本轮服务已经运行，不要重复占用端口。

在 `grpctest` 根目录：

```sh
mkdir -p .local/es-lab/store-search
cp cmd/goods-es-demo/config.example.json .local/es-lab/store-search/config.json
docker exec grpctest-es-lab-mysql-1 mysql -uroot -e 'CREATE DATABASE IF NOT EXISTS es_store_demo CHARACTER SET utf8mb4'
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -mod=readonly -o .local/es-lab/store-search/goods-rpc ./cmd/goods-es-demo
docker run -d --name grpctest-es-store-goods-rpc --network grpctest-es-lab_lab \
  --memory 256m --cpus 1 -p 127.0.0.1:15052:50052 \
  --mount "type=bind,src=$PWD/.local/es-lab/store-search/goods-rpc,dst=/goods-rpc,readonly" \
  --mount "type=bind,src=$PWD/.local/es-lab/store-search/config.json,dst=/demo-config.json,readonly" \
  --entrypoint /goods-rpc \
  'mysql:8.0@sha256:7dcddc01f13bab2f15cde676d44d01f61fc9f99fe7785e86196dfc07d358ae2b' \
  -config /demo-config.json
```

在另一个终端进入 `grpctest_web` 根目录，执行 `go run ./cmd/goods-es-demo`。再开终端进入其 `store-web` 目录：

```sh
STORE_GOODS_API=http://127.0.0.1:18022 \
STORE_USER_API=http://127.0.0.1:18022 \
STORE_ORDER_API=http://127.0.0.1:18022 \
VITE_CATALOG_DEMO=true npm run dev -- --port 3002
```

前台运行的两个进程用 Ctrl-C 停止；RPC 用 `docker stop grpctest-es-store-goods-rpc` 停止，`docker start grpctest-es-store-goods-rpc` 恢复。本轮后台 Web、前端进程的 PID 和日志保存在 `grpctest_web/.local/es-store-search/`；只停止对应演示进程，不需要停止原 `8022/3001` 服务。MySQL 实验容器使用 tmpfs，停止 MySQL 后演示库可能需要重新准备。

## 验证结果与下一批

真实 MySQL、ES、gRPC 隔离测试覆盖名称/简介查询、分类后代、品牌/价格/标记过滤、同分稳定排序、正确页偏移、末页和空结果。故意跳过钩子制造陈旧索引后，MySQL 能剔除下架、库存未准备、删除和结构化条件已不匹配的商品，total 保留索引语义，并返回当前价格。缺少 ES 索引时返回 Unavailable，没有 SQL 回退。

浏览器实测搜索、分类、分页、空结果；只暂停演示 RPC 时展示服务错误，恢复后刷新正常。ES 本身未停止。测试入口为 [goods_search_test.go](handler/goods_search_test.go)、[goods_search_integration_test.go](handler/goods_search_integration_test.go)，后者随 [上一批集成命令](ES_SYNC.md#已验证什么) 运行。实际记录位于 `.local/es-lab/store-search/`，Web/前端记录在 Web 仓库 `.local/es-store-search/`。

两仓 `ci/check.sh` 的本地构建和回归均通过；商品搜索相关 race 测试、`go vet` 通过；商城前端 247 项测试及生产构建通过。验证结束时，原 MySQL/ES 小实验各 40 条、历史快照各 420 条、新演示各 16 条，临时集成库已清理。这些结果不代表远端 CI 或云端部署。

云端商品库、Nacos 和部署未切换。以后启用云端前，应先更新 RPC/Web/前端以识别新增字段，准备可访问的 ES、核对实际商品与库存准备状态、暂停商品写入导入同源数据，再启用同步和搜索配置。本地 ES 的 `127.0.0.1:9201` 不能直接作为云端容器连接地址。

关闭配置并重启商品服务可显式恢复原 SQL 查询，同时会停止钩子同步；再次启用前需要补齐停用期间的数据。这是人为切换，不是故障时自动回退。
