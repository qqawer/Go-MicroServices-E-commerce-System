# 搜索链路追踪：OpenTelemetry → Jaeger

2026-10-16 第一批：参照 mxshop 的 HTTP 中间件、gRPC 两端埋点和业务 Span 分层，接通本地商品搜索。mxshop 的旧 OpenTracing / `jaeger-client-go` 已弃用；这里使用 OpenTelemetry SDK 1.34.0、otelgrpc 0.59.0、OTLP/HTTP 和 Jaeger 2.22.0。SDK 版本与 RPC 仓库原有 OTel API 1.34.0 对齐，属于本轮固定兼容组合，不宣称最新 SDK。

本批仅接商品 Web/RPC 和搜索内部依赖。原 order handler 存在一个未配置有效 Tracer 的 OpenTracing Span，订单、库存、MQ 和浏览器 JS 还未接入本轮 OTel Trace。

## 实际链路

```text
GET /g/v1/store/goods                  goods-web，HTTP server
└─ Goods/GoodsList                    goods-web，gRPC client
   └─ Goods/GoodsList                 goods-srv，gRPC server
      └─ goods.search                goods-srv，业务逻辑
         ├─ elasticsearch.search     goods-srv，ES 请求/读取响应体
         └─ mysql.goods.read         goods-srv，商品回读及分类/品牌 Preload
```

ES/MySQL 这两个 Span 是商品服务记录的客户端操作耗时，不是 ES/MySQL 进程自己发送的服务端 Span。分类筛选的预查询没有单独埋点；没有为每条 SQL 或整个数据库安装插件。父 Span 已包含子 Span 时间，不能把整棵树的耗时相加。

`context.Context` 携带当前 Span。HTTP 提取 W3C `traceparent`，gRPC stats handler 通过 metadata 注入/提取，srv 将同一上下文交给两个依赖 Span。HTTP 响应提供 `X-Trace-ID`，可直接拿去 Jaeger 查询。Span 只记录静态操作名、路由模板、依赖类型、状态等；不记录查询词、Authorization、请求正文、SQL 文本或 DSN。

## 文件

- RPC `internal/tracing/tracing.go`：进程级初始化、采样、批处理、限时导出和正常退出 Flush；Web 独立 Go 模块中同名包保持同样配置契约。
- Web `internal/tracing/http.go`：Gin 请求 Span、传播及响应 Trace ID。
- Web 商品 srv_conn、RPC goods_srv/main.go：gRPC client/server stats handler；正常生产入口和本地演示入口都已接好，是否导出取决于显式环境变量。
- RPC `goods_srv/handler/goods_search.go`：业务、ES I/O 和 MySQL 回读 Span。
- [compose.yaml](compose.yaml)：独立 Jaeger 容器及可选 demo 商品 RPC；专用 ES 实验网络，端口只绑定 127.0.0.1。
- [verify.py](verify.py)：只读验证 HTTP 返回、Jaeger v3 Trace 数据、跨服务父子关系、错误状态及输入排除。

## 启动

前提：既有 ES/MySQL 与演示库已按 [ES_SEARCH.md](../../goods_srv/ES_SEARCH.md) 准备；本轮执行结束时这些服务和追踪演示都已在运行。以下二进制目标为当前 Mac ARM64 使用的 Linux arm64 容器。

在 RPC 仓库根目录：

```sh
mkdir -p .local/tracing
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -mod=readonly \
  -o .local/tracing/goods-rpc ./cmd/goods-es-demo
docker compose -f labs/tracing/compose.yaml up -d --no-build jaeger
```

首次从旧 ES 演示切换时，同名 RPC 容器会冲突，先停并改名备份：

```sh
docker stop grpctest-es-store-goods-rpc
docker rename grpctest-es-store-goods-rpc grpctest-es-store-goods-rpc-before-tracing
```

本轮已执行过上面两条，重复使用当前追踪环境时跳过它们。然后：

```sh
docker compose -f labs/tracing/compose.yaml --profile demo up -d --no-build goods-rpc
```

容器从 `http://jaeger:4318/v1/traces` 上报。Jaeger UI 是 <http://127.0.0.1:16686>，宿主机 OTLP/HTTP 入口是 `127.0.0.1:4318`。Jaeger 采用内存存储，重启/重建会丢失历史 Trace；本批 JSON 证据另存在 `.local/tracing/`。Jaeger 采集与查询不复用商品 ES 索引。

在 Web 仓库 `grpctest_web` 根目录：

```sh
mkdir -p .local/tracing
CGO_ENABLED=0 go build -mod=readonly -o .local/tracing/goods-web ./cmd/goods-es-demo
OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://127.0.0.1:4318/v1/traces \
OTEL_TRACES_SAMPLER_ARG=1 .local/tracing/goods-web
```

先停止旧的本地演示 Web 进程以释放 18022，再执行此命令。本轮后台进程 PID 位于 Web 的 `.local/tracing/goods-web.pid`，日志为 `.local/tracing/goods-web.log`。停止前核对 PID 的命令，避免使用历史 PID 误停其他进程。前端继续使用原 3002，代理指向 18022；无需改动页面请求协议。

重新编译运行中的 RPC 后使用 `docker compose -f labs/tracing/compose.yaml --profile demo up -d --no-build --force-recreate goods-rpc`，确保绑定到新二进制；Web 同样需重启进程。

## 采样与关闭

- 没有 `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`：不创建导出 SDK，正常业务继续运行。
- `OTEL_SDK_DISABLED=true`：不创建导出 SDK，适用于新启动的进程；变更配置后重启。
- 显式端点启用后，根 Trace 默认采样率 0.1；`OTEL_TRACES_SAMPLER_ARG=1` 为本地教学全采样。ParentBased 会沿用传入父级的采样决定，不能用此配置覆盖一个未采样的父级。
- 批处理队列是有界内存队列，满时可丢追踪；导出超时 2 秒、本轮不做导出重试。正常关闭给 Flush 最多 5 秒。它们是追踪可靠性和开销的取舍，不是业务事务机制。

生产入口只在商品进程启动时初始化一次。没有修改云端 Nacos 或部署配置。此处环境变量初始化只处理明确列出的设置，不等同于支持全部 OTel 自动配置变量。

## 验证与读图

在 RPC 根目录运行：

```sh
python3 labs/tracing/verify.py success
python3 labs/tracing/verify.py empty
# 模拟上游传入 traceparent；该虚拟父 Span 自身不导出。
python3 labs/tracing/verify.py success --propagate
```

脚本不停止服务、不修改商品。使用 Jaeger 2.22 的 `/api/v3/traces/{id}` 读取 OTLP JSON，而不是旧教程的 `/api/traces`。默认生成完整的 HTTP 根 Trace；`--propagate` 只用于验证跨入口延续上游身份，可能显示未上报的外部父 Span。

实际验证结果：

| 场景 | HTTP/商品 | Trace |
| --- | --- | --- |
| 搜索苹果 | 200，5件 | 6个Span、2个service，同Trace ID、父子关系正确 |
| 搜索火星探测器 | 200，0件 | 5个Span，无MySQL回读，全部正常 |
| 实际停止ES | 503 | 5个Span，ES→业务→两端RPC→HTTP均标Error，无MySQL回读 |
| 暂停Jaeger超过导出超时 | 200，5件 | 业务返回；后台导出超时可能丢Span，恢复采集后新请求能导出 |

本轮故障用 `docker stop grpctest-es-lab-es-1`，在 finally 中恢复 ES；ES 健康后搜索恢复。若要重跑，先停 ES，执行 `python3 labs/tracing/verify.py failure`，随后务必执行 `docker start grpctest-es-lab-es-1` 并等待健康。不要在 ES 故障时运行 success 断言。

在 Jaeger 选择 `goods-web`，点击 Find Traces，或直接打开脚本输出的 `jaeger_url`。展开树，比较 `elasticsearch.search` 和 `mysql.goods.read`；红色错误应从最深的失败依赖开始定位。一个空结果不是错误，也不需要回 MySQL。

证据在 RPC `.local/tracing/{success,empty,failure,collector-unavailable}.json`；两仓 `ci/check.sh` 通过，含新增 race 测试。测试验证真实 HTTP→gRPC 父子传播、采样配置校验、OTLP 导出、正常退出 Flush 和输入排除。原40/420/旧16/当前16条ES文档的ID与全部业务字段在演示结束已逐项核对一致，结果见 `data-preservation.json`。

## 参考与下一批

- [Jaeger 2.22 getting started](https://www.jaegertracing.io/docs/2.22/getting-started/)
- [旧 Jaeger Go SDK 的弃用说明](https://github.com/jaegertracing/jaeger-client-go)
- [OpenTelemetry Go instrumentation](https://opentelemetry.io/docs/languages/go/instrumentation/)
- [传播](https://opentelemetry.io/docs/concepts/context-propagation/)
- [otelgrpc 0.59.0](https://pkg.go.dev/go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc@v0.59.0)

下一批再接订单 Web→订单 RPC→商品/库存 RPC，并核对 MQ 异步边界；当前不能宣称全部微服务已完成链路追踪。
