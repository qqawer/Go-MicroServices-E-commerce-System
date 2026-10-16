# 商品查询保护，第1批：限流、错误比例熔断和失败提示降级

本文保留第1批参数和实测结果。第2批慢调用熔断与并发隔离已完成，见[ADVANCED.md](ADVANCED.md)。当前本地Web使用slow profile（50QPS、慢阈值200ms、并发2），注入延迟已恢复0；复现下面第1批burst实验前，应重启为basic profile。

2026-10-16。本批只保护买家商品列表 `GET /g/v1/store/goods`，参考mxshop的Sentinel入口和规则，在现有代码加入两个独立资源。RPC业务和索引结构不改，继续使用真实本地Web→商品RPC→ES/MySQL和OTel/Jaeger。源码在同级Web仓库 `goods-web/protection/query.go`，路由集成在 `goods-web/api/goods/goods.go`。

## 教学规则及响应

| 层次 | 资源 | 教学参数 | 拒绝/故障 |
| --- | --- | --- | --- |
| 入口流量 | store.goods.query，Inbound | Direct+Reject，1秒窗口阈值5 | 429、GOODS_QUERY_RATE_LIMITED |
| 商品RPC | store.goods.rpc.list，Outbound | 10秒统计，最少3次，错误比例>=50%，打开3秒，探测1次 | 熔断503、GOODS_QUERY_CIRCUIT_OPEN |
| 依赖失败响应 | 已实际调用的商品RPC | 基础设施错误/期限及损坏的商品结果 | 503、GOODS_QUERY_DEPENDENCY_UNAVAILABLE |

后两种都返回 `degraded:true` 和固定msg，不返回商品data或伪造空结果。Retry-After是建议，429为1秒，503为配置冷却期向上取整秒数。限流先于熔断执行：高流量下可能先命中429。限流拒绝不进入RPC资源，也不会计入其故障比例。

Sentinel沿用已有v1.0.4，不宣称最新或升级了依赖；其实际源码达到50%即可打开。mxshop示例是3次/2000ms，不是3QPS；这里特意使用5次/1000ms便于观察。阈值均为教学值，未标定生产容量。

## 启动

前提：本地商品RPC15052、ES9201、MySQL及Jaeger16686已按 `labs/tracing/README.md` 启动；当前18022商品Web及3002页面继续沿用。现有环境已在运行。

Web根目录构建，然后核对 `.local/tracing/goods-web.pid` 对应命令，停止占用18022的旧demo进程，再启动新实例：

```sh
mkdir -p .local/tracing
CGO_ENABLED=0 go build -mod=readonly -o .local/tracing/goods-web ./cmd/goods-es-demo
OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://127.0.0.1:4318/v1/traces \
OTEL_TRACES_SAMPLER_ARG=1 .local/tracing/goods-web -protect
```

正常goods-web入口已新增query_protection配置，默认关闭；本轮仅在本地demo显式开启。正常入口支持qps/min_requests/open_ms，默认值50/20/5000只是实现缺省值，不能当成容量结论。初始化在进程启动时执行，配置变更后重启，不提供规则热更新。未修改云端Nacos/部署，也没有数据库迁移。

去掉-protect并重启该demo可关闭本地保护。单个实例内存保存流量统计和熔断状态，重启清空；不提供多实例合计或按用户/IP的限额。

## 实际验证

RPC根目录依次执行：

```sh
python3 labs/protection/verify.py normal
python3 labs/protection/verify.py empty
python3 labs/protection/verify.py burst
python3 labs/protection/verify.py fault
```

前三种只读；fault会真实停止 `grpctest-es-lab-es-1`，在finally中恢复并等待ES可查询，需保留实验环境使用权。脚本仅写本地JSON证据，不改商品数据，不需要真实身份或付款。

fault先让可能残留的半开状态恢复，并等旧统计窗口过去；然后停ES，连续发3次真实失败查询，检查熔断拒绝。3秒后仍保持ES停止，让半开探测失败、重新打开；最后恢复ES，等待健康后探测并确认后续请求均成功。ES启动期间可能关闭尚未就绪的连接，ready轮询处理该过渡状态，不能把docker start返回当作ES就绪。

| 场景 | 本次实际结果 | Trace证据 |
| --- | --- | --- |
| 正常搜索苹果 | 200、5件，42.278ms | 6Span，7cb1418307813d588ec14829ac72039a |
| 无匹配 | 200、0件，26.492ms | 5Span、无SQL，fc5a06a9fbf723e30aaf105e1d351bd5 |
| 20次顺序突发 | 约43ms内5个200、15个429 | 拒绝样本556dc507e60e986238d9bdc606b949a5，仅1Span |
| 实际ES故障 | 首3次503、degraded，均实际访问依赖 | 5Span；首例4083d7f24bcedb2dfe98171c3f51a0a7 |
| 熔断打开 | 503，0.910ms，无RPC | 1Span，0f0e2d9417e2dd0e828de62563846e13 |
| 半开失败 | 503，5.720ms，重新访问ES并失败 | 5Span，ed103698dcba54c2831c95b62e4bc607 |
| 再次打开 | 503，0.336ms，无RPC | 1Span，aee340c8fb25e15338132019114d2f53 |
| ES恢复后探测 | 200、5件，36.875ms；再一次200/7.685ms | 各6Span；cf37a0132e773dff24a093123f3fa9dc / 242fd1e79a916770ff98b89a1e57481a |

本机小样本时延只用于说明分支，不是吞吐量/性能基准。43ms内20次是顺序HTTP请求，不是并发压测；不能从它推算生产QPS或并发下严格无超发的保证。

## Trace读法与测试

HTTP根Span记录 `protection.result` 为allowed/rate_limited/circuit_open/dependency_error。429是预期流控，不标服务器Error；503会标Error。限流/熔断拒绝只有Web根Span；正常进入下游是原6段，故障为5段。接口已经拒绝时，下游无Span是本次控制流的结果。

脚本核对状态、数量、父子关系、服务集合、标记及关键词排除，证据存 `.local/protection/{normal,empty,burst,fault}.json`。Jaeger使用内存历史，重启后应重跑生成新ID。独立路由测试以RPC计数验证拒绝前未调用依赖，覆盖业务错误/取消、空结果、半开失败/成功、损坏结果和关闭开关。Web完整ci/check.sh含race通过，demo构建通过。

最终ES为green；原40/420/16/16四个索引的ID及业务字段与基线逐项一致；原合成库存100、订单5笔不变，见 `.local/protection/data-preservation.json`。故障已经恢复，18022继续开启教学保护，3002页面可继续使用，普通手动搜索无需改协议。前端已有429/503固定提示，本轮未进行浏览器点击验收。

第1批只验证错误比例熔断。第2批已在同一商品接口补慢调用比例与并发隔离，启动和实测另读ADVANCED.md。共享分布式限流、缓存商品降级、控制台及其他接口尚未扩展，不自动进入API网关或生产切换。
