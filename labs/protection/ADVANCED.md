# 第2批：商品查询慢调用熔断与并发隔离

2026-10-16。延续第1批买家商品列表保护，仍只接 `GET /g/v1/store/goods`。mxshop固定2bbe1b3的商品示例主要展示QPS规则和Entry/Exit，本批在该接入方式上扩展Sentinel SlowRequestRatio，并用Go有界channel预订并发名额；没有照搬上游不存在的完整实验。

## 三种数量不要混淆

- QPS：一个时间窗口通过多少次请求。
- 慢调用比例：已完成调用中，耗时超过阈值的比例。200也可能慢。
- 并发占用：此刻有多少次商品RPC及结果校验尚未结束。

新配置 `slow_ms` 大于0才添加慢调用规则，`max_inflight` 大于0才启用有界名额，默认0保留第1批行为。慢调用规则与原ErrorRatio规则并存，统计窗口10秒、最低样本/打开期沿用已有字段，比例固定50%。本版本SDK用RT > slow_ms判定慢，达到最低样本且慢比例 >=50%可打开。半开即使返回200，只要仍超慢阈值也重新打开。[固定版本源码](https://github.com/alibaba/sentinel-golang/blob/v1.0.4/core/circuitbreaker/circuit_breaker.go)

并发名额用 `select { case slots <- struct{}{}: ...; default: ... }` 原子占位，不排队。调用完成/失败/取消释放；熔断拒绝也会释放刚占的名额。满时503、`GOODS_QUERY_BUSY`、degraded:true、Retry-After:1，Trace标记concurrency_limited；拒绝不执行RPC，也不记为RPC依赖错误。

保护顺序：入口QPS → 并发占位 → 两种熔断检查 → RPC/结果校验 → 完成统计、归还名额 → 写HTTP响应。完成函数用sync.Once配合defer兜底，确保只归还一次。本次从第1批的纯defer改为写响应前显式完成，减少客户端已收正文但统计/释放尚未完成的时序差，也让慢调用计时不含后续正文编码/写出。

这是Web单进程的依赖并发限制，不是整个集群的库存容量或线程池。HTTP调用取消后，实际下游是否停止仍取决于它是否遵守Context，本地注入器明确遵守；本轮没有给所有第三方调用提供强制终止保证。

## 可控延迟只在演示入口

RPC `cmd/goods-es-demo/delay.go` 为本地runner增加可选 `-query-delay-file`。只匹配买家GoodsList，读取挂载文件0..5000ms，使用可取消计时器；非买家查询、BatchGetGoods等直接通过。新增 `lab.goods.delay` Span记录延迟数值，真实搜索、ES和MySQL仍执行。生产 `goods_srv` handler没有加入延迟，也没有HTTP查询参数/请求头控制这个开关。

通过额外的 `slow.compose.yaml` 覆盖本地RPC命令和挂载，不修改原tracing compose启动契约。不启覆盖文件时，不需要延迟控制文件。当前实验已启动覆盖层，控制值已恢复0。

在RPC根目录：

```sh
mkdir -p .local/protection
printf '0\n' > .local/protection/query-delay-ms
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -mod=readonly -o .local/tracing/goods-rpc ./cmd/goods-es-demo
docker compose -f labs/tracing/compose.yaml -f labs/protection/slow.compose.yaml \
  --profile demo up -d --no-build --force-recreate goods-rpc
```

文件在宿主机更新，容器只读挂载。脚本在请求批次之间原地写文件，不用重命名替换其inode。重启商品runner会沿用原逻辑用相同ID/字段重新索引合成商品；本轮数据逐项校验一致，不把它描述成完全没有ES写入。

Web根目录构建，先核对并停止占用18022的旧demo，再运行对应profile：

```sh
CGO_ENABLED=0 go build -mod=readonly -o .local/tracing/goods-web ./cmd/goods-es-demo
OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://127.0.0.1:4318/v1/traces \
OTEL_TRACES_SAMPLER_ARG=1 .local/tracing/goods-web -protect -protect-profile slow
```

| profile | QPS | SlowMs | max_inflight | 用途 |
| --- | ---: | ---: | ---: | --- |
| basic | 5 | 0 | 0 | 第1批原教学参数 |
| slow | 50 | 200 | 2 | 同时启用慢调用/并发，观察慢调用及恢复 |
| isolation | 50 | 0 | 2 | 单独验证名额，避免慢调用熔断掩盖结果 |

profile均为本地教学参数，最低样本3、打开3秒。正式入口配置例子可增加 `slow_ms:200,max_inflight:2`，但是本轮没有变更云端Nacos或启用线上规则。参数不是容量建议；配置启动读取，状态和名额不跨进程共享。

## 实验1：200的慢调用也能熔断

使用全新slow profile进程，避免历史样本影响，然后在RPC根目录执行：

```sh
python3 labs/protection/verify_slow.py slow
```

脚本设400ms延迟：前三次均200/5件商品，437.282、407.160、406.748ms，各7个Span、无Error；下一次503/0.591ms，只有Web根Span，protection.breaker=SlowRequestRatio。等3秒，慢探测仍200/405.780ms，但下一次再次503/0.360ms。设0后再等3秒，快探测200/4.482ms及后续200/4.624ms均为正常6段。

慢成功样本d23b2744c0fa50b7a276307622b3f300；打开084caff63e518b4411681cb49260d6f7；慢探测9a8478b1f52dc53ea5590fcd2dcb2954；重新打开03659ce5f7a048a6c704fbab0be87c86；恢复dd1350cc44677e3adb5c53d513f7a18d。Span名lab.goods.delay说明400ms是实验注入，不把它归因成ES本身变慢。

## 实验2：8个同时查询，只有2个进入RPC

停止Web、改用 `-protect -protect-profile isolation` 启动全新进程；保留OTLP配置，然后运行：

```sh
python3 labs/protection/verify_slow.py isolation
```

脚本设1200ms延迟，8个客户端用Barrier同时发送。2个200/约1243ms/7段，6个503/18.421～19.068ms/1段，机器码GOODS_QUERY_BUSY。按同一个Web进程发出的RPC Span开始/结束时间计算，观察到峰值2，避免跨机器时钟差干扰判断。完成后设0，新查询200/4.090ms，证明名额归还。

然后设4000ms，两个HTTP客户端先占满名额，第三个请求确认BUSY；实际关闭两个连接。两段lab.goods.delay分别约257.020/257.165ms就结束，尚未到4秒，也未进入ES搜索。服务端HTTP Span记408；客户端已经断开，没有收到一个408正文。新查询在2秒断言范围内通过、响应约4.009ms。

取消场景为在连接关闭后定位Trace，注入了模拟W3C父级；该虚拟父Span没有上报，Jaeger可能提示缺失外部父级。这不属于正常请求断链。其两个Trace为918d03e289f5403098a587b5451b008a、4f488a4ae8454b138491fb70426a0201。

并发通过样本b9c0cd206525003ff7bf79c1b644fb57；拒绝8f84269f0bafed03ce3475ff4b7b144a。脚本所有退出路径都把文件恢复0；当前最后已切回slow profile，两种新保护均开启，注入延迟为0，正常查询6段/5件商品。

## 验证边界和交付

脚本核对HTTP、数量、同Trace父子、慢调用触发规则、延迟Span、拒绝无RPC、并发重叠峰值、取消前后名额恢复。路由race测试另外用20个并发拒绝请求和可取消RPC替身检查严格名额、取消及正常释放；慢调用测试验证慢200、慢探测重开、快探测关闭。旧路由测试等待入口统计窗口清空，避免不同测试共享SDK单进程计数导致误拒绝。RPC注入器测试检查取消、非目标方法及最大值限制，已加入CI允许列表。

两仓完整ci/check.sh（含race）通过，demo构建及真实HTTP/RPC/ES/MySQL实验通过。原40/420/16/16索引的ID/业务字段逐项不变，合成库存100/订单5笔不变。证据在 `.local/protection/advanced/{slow,isolation,data-check,final-normal}.json`，CI日志在两仓.local。耗时仅为本机小样本，不是生产容量结论。

范围只包含买家列表依赖，未扩展详情/管理/交易接口，没有云端部署、分布式配额、线程池或自动扩缩容。下一批按用户选择继续，不因这一处完成就宣称整个系统治理完成。
