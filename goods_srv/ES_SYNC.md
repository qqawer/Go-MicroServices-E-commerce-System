# 商品与 ES 同步：沿用 mxshop 的 GORM 钩子

本批完成阶段 3 的代码和本地隔离验证。商品服务启用配置后，每次商品变更直接调用 ES；没有消息队列、同步任务表或后台补偿进程。后续阶段 4 已在独立本地商品服务启用配置并接通真实商城页面，见 [ES_SEARCH.md](ES_SEARCH.md)；原有云端业务服务尚未切换。

## 为什么这样接

全量导入负责把已有商品搬进 ES，钩子负责跟随后续修改。二者都用商品 ID 作为 ES 的 `_id`，更新同一商品不会另生成一份文档。原来的 420 条历史快照仍用于独立学习，不能当成当前业务库。

对照的是本地 mxshop 提交 `2bbe1b3a0eaa59bf26a3a2047769979a0e206c11` 的 `mxshop_srvs/goods_srv/model/goods.go`：`AfterCreate`、`AfterUpdate`、`AfterDelete` 直接调用 ES。这里保留这个做法，适配当前 ES 8.19 和商品创建流程。

```text
商品 RPC 请求
  → MySQL 事务内新增 / 修改 / 软删除商品
  → GORM 自动调用 AfterCreate / AfterUpdate / AfterDelete
  → 写入 / 删除 ES 中相同商品 ID 的文档
  → 钩子成功：继续提交 MySQL；钩子失败：返回错误并回滚当前 MySQL 事务
```

`AfterUpdate` 的“After”指 SQL 更新之后，仍在事务提交之前；钩子报错会中断并回滚当前事务，见 [GORM 官方钩子说明](https://gorm.io/docs/hooks.html)。

## 文件与适配

| 文件 | 本批职责 |
| --- | --- |
| [model/goods_es_hooks.go](model/goods_es_hooks.go) | 三个钩子、完整文档读取、ES 写入与删除、错误返回 |
| [model/es_goods.go](model/es_goods.go) | 搜索字段和 IK Mapping；数据字段与阶段 2 一致，分词器已不同 |
| [initialize/es.go](initialize/es.go) | 建立官方 Go ES v8.19.7 客户端；启动时创建或核对索引 |
| [config/config.go](config/config.go)、[main.go](main.go) | 读取启用开关、地址、索引名，并在服务启动时初始化 |
| [handler/goods_update.go](handler/goods_update.go)、[handler/goods.go](handler/goods.go) | 更新和删除时给 GORM 模型带上商品 ID，供钩子定位文档 |
| [handler/goods_es_integration_test.go](handler/goods_es_integration_test.go) | 真实 MySQL/ES 验证及 ES 故障注入 |

当前修改接口使用 `Updates(map)`，模型不一定含完整商品。钩子通过同一个事务重新读取商品，再用 `PUT /索引/_doc/商品ID` 写完整文档，保留未修改字段及 `false` 值。指定相同 ID 的 Index API 会替换已有文档；搜索可见性仍受 refresh 影响，见 [ES 8.19 Index API](https://www.elastic.co/guide/en/elasticsearch/reference/8.19/docs-index_.html)。

创建商品分两步：先落库，此时未确认库存、未上架；库存确认成功后，再更新准备状态和上架状态。两步分别触发创建、更新钩子。初次落库失败回滚整个创建事务；库存确认阶段失败只回滚该阶段事务，保留之前已提交的待准备商品，用原请求编号重试。

价格沿用导入转换：MySQL 的元先转 `DECIMAL(18,2)`，乘 100 后成为整数分 `price_cents`，避免直接将 Go 的 `float32` 乘 100 后截断。历史 `inventory_ready=NULL` 仍可表达未知，正常业务商品使用实际布尔状态。

## 如何启用

这是商品服务的应用配置，加入它实际读取的 Nacos 服务 JSON；不是 ES 容器的 `elasticsearch.yml`：

```json
"elasticsearch": {
  "enabled": true,
  "url": "http://127.0.0.1:9201",
  "index": "grpctest-goods-search"
}
```

上面是插入现有 JSON 对象的一个字段，保留原有 MySQL、库存等设置。地址适用于商品服务直接在 Mac 上运行；若服务加入本地实验 Docker 网络，则使用 `http://es:9200`。启用后重启商品服务；ES 需要安装与服务端版本一致的 IK。已有 SmartCN 索引需换成新索引并重新导入；不能原地修改 `analyzer`，当前初始化检查会拒绝旧分词配置。420 条快照导入命令仍用于 SmartCN 教学对照，不能直接当作 IK 索引初始化器。初始化时索引不存在便创建，字段映射不兼容或连接失败则启动报错。

现有配置缺少这一段时，同步保持关闭。此批没有修改云端 Nacos 或部署业务服务。启用前将同一个业务数据库的现有数据导入同一个目标索引，并暂停写入完成切换；不能把历史快照索引混用于另一个数据库。

## 已验证什么

2026-10-05 在本地 Docker MySQL 8.0.46、ES 8.19.22 上运行，直接调用真实商品 RPC handler 方法，经过真实 GORM 事务和 ES HTTP 请求。库存服务使用测试替身；没有启动完整注册中心或进行商城页面验收。

| 场景 | 结果 |
| --- | --- |
| 初始化及再次连接 | 可创建索引，也能接受 ES 返回的已有 Mapping |
| 创建商品 | ES 存在完整文档；44.9 元成为 4490 分；库存准备和上架均为 false |
| 同请求确认库存 | 同一文档变为库存已准备、上架 |
| 改名、39.9 元改价、取消包邮 | 名称和 3990 分正确；false 能写入；其余字段保留 |
| 下架、取消新品 | 两个 false 正确写入，名称和价格仍保留 |
| ES 返回 503 | 新建、修改、删除报错；商品及创建请求记录的对应 MySQL 事务回滚 |
| 恢复后手动重试 | 状态成功同步；随后软删除商品时移除 ES 文档 |
| ES 文档已不存在 | 删除商品仍成功 |

测试使用独立临时数据库和索引，结束后删除；40 条小实验和 420 条历史快照不参与商品写入。故障通过替换测试客户端为返回 503 的 HTTP 服务模拟，没有停止用户正在学习的 ES。

项目 `bash ci/check.sh` 的构建、Python 检查和 Go race 回归通过；另外运行商品 handler/initialize 的 race 测试及 model/initialize/handler 的 `go vet`，均通过。验证后 MySQL 与 ES 中原两组数据仍分别为 40 条、420 条，临时钩子库和索引已清理。这些是本地检查结果，不代表远端 CI 或部署结果。

复现命令从 `grpctest` 目录运行，要求既有实验 Docker 网络和 MySQL/ES 已启动：

```sh
mkdir -p .local/es-lab
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -mod=readonly -c \
  -o .local/es-lab/goods-hooks.test ./goods_srv/handler
docker run --rm --network grpctest-es-lab_lab \
  --mount "type=bind,src=$(pwd)/.local/es-lab/goods-hooks.test,dst=/goods-hooks.test,readonly" \
  -e 'GOODS_ES_TEST_MYSQL_DSN=root@tcp(mysql:3306)/?charset=utf8mb4&parseTime=true&timeout=5s&readTimeout=30s&writeTimeout=30s' \
  -e 'GOODS_ES_TEST_URL=http://es:9200' \
  --entrypoint /goods-hooks.test \
  'mysql:8.0@sha256:7dcddc01f13bab2f15cde676d44d01f61fc9f99fe7785e86196dfc07d358ae2b' \
  -test.run '^TestGoodsESHooksIntegration$' -test.v -test.timeout 120s
```

未提供两个测试环境变量时，普通 `go test` 会跳过这一集成实验。实际运行记录在被 Git 忽略的 `.local/es-lab/hooks-sync/integration.json`。

## 这个做法的边界

ES 请求使用当前请求的上下文，最长 5 秒；关闭客户端自动重试。ES 故障会影响商品写操作。恢复后由调用方重新发起失败操作，没有后台自动补齐。

MySQL 与 ES 没有共同事务：ES 已成功而后续 MySQL 操作或提交失败、请求超时但 ES 实际已执行，都可能造成差异。MySQL 回滚不会撤销已经写入 ES 的内容。遇到这类情况需核对并修复索引，不能宣称两边始终强一致。恢复旧商品可重新导入；多余的已删除商品文档还需另行清理。

钩子只覆盖经过这些 GORM 操作的商品变更。直接执行 SQL、`UpdateColumn(s)` 或 `SkipHooks` 不触发这一同步；同样不应直接对无商品 ID 的模型进行批量写操作。阶段 4 已接入搜索，并在 MySQL 回读时校验当前商品状态；云端启用需另行完成数据和配置切换。
