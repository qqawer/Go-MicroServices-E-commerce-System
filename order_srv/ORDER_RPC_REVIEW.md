# 订单 RPC 对照审查：先修哪里，如何验证

原审查日期：2026-09-17。审查对象是修复前的 `grpctest/order_srv` 工作区，包括当时尚未提交的改动；本地 HEAD 为 `b08f0a3b29f2bc05fe0a5cfe3db3d986f5ac06c4`。参考仓库固定为 [iceymoss/mxshop 的 58142016702831ed31a62b9ece431c935a24ba04](https://github.com/iceymoss/mxshop/tree/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/order_srv)，参考项目的源码链接均锁定该版本。

## 本轮落实状态与旧计划映射（2026-09-17）

原审查结束后，本轮已获授权修复订单的构建和基础启动流程。下表记录实际完成范围，并与 [2026-09-16 实施计划](/Users/outsider/Downloads/Golang_Development/reports/grpctest-improvement-plan-2026-09-16/IMPLEMENTATION_PLAN.md) 对齐；**旧计划批次 01 仍是部分完成，不能把订单基础修复等同于整批环境、CI 或批次 02 认证验收完成。**

| 范围 | 本轮状态与当前入口 | 旧计划归属及仍待完成内容 |
| --- | --- | --- |
| C1—C5：订单构建 | 已删除重复入口，修正 proto/注册、生成接口嵌入、config/global 类型与依赖；订单包测试通过 | 批次 01 的订单构建子项完成；生成工具固定、测试环境、迁移/seed、CI 和跨仓验收仍待完成 |
| S1、S2、S4：基础依赖 | [InitConfig](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/initialize/config.go)、[InitDB](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/initialize/db.go)、[InitSrvConn](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/initialize/srv_conn.go) 已修正订单 DB/global 归属、保留扁平 YAML、校验 Nacos 与业务配置、传播失败，并提供连接清理；下游连接在期限内等待传输就绪后复用 | 批次 01 的订单启动基础完成；批次 02 的共享配置/认证仍须独立验收。后续复用这些基础函数，新增交易依赖时扩展统一启动流程 |
| S3：监听、注册与退出 | [main 的 run/serve/stopServer](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/main.go) 使用默认端口 `50054`，Consul 注册和健康检查使用实际监听端口；正常退出注销并清理依赖，排空超时则返回失败，由主进程退出释放资源 | 本地替身验证已覆盖动态端口及清理；超时路径有独立回归。AWS Consul 反向可达性、真实部署验收仍未完成 |
| 无 tracing span 与交易未就绪 | [handler](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/handler/order.go) 已允许没有 parent span；对合法、未取消的创建订单请求，交易依赖未初始化时在写订单/扣库存前返回 `FailedPrecondition` | 仅完成基础保护；完整跨服务 tracing 仍归批次 11，订单交易能力仍未启用 |
| 库存、MQ、支付竞争 | 本轮未启用 Redis/Redsync、MQ producer 或 consumer，未改交易状态机 | 批次 04 完成数据库事务/唯一约束/幂等；批次 05 统一实现持久化创建意图、MQ 生命周期、回查与恢复；批次 06 处理支付和关闭竞争 |

本轮还将订单 Nacos SDK 日志级别设为 `warn`，避免 SDK 的 Info 路径记录完整配置；按既有要求忽略 `.DS_Store`，将四个已跟踪文件取消跟踪并保留本地副本。这些改动不代表任何 CI 项目已完成。

最终版本已通过：`go build -mod=readonly ./user_srv ./goods_srv ./inventory_srv ./order_srv`；同四目标在 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64` 下 readonly 构建；`go test -mod=readonly ./order_srv/... -count=1`；`go test -race -mod=readonly ./order_srv/... -count=1 -timeout=30s`。停服回归使用真实 gRPC 和故意忽略 context 的阻塞 handler：原实现超时后继续等待，修复后报告未排空并断开客户端，测试最后释放 handler 完成清理。这些检查使用本地替身和 SQL mock，尚未验证真实 MySQL/MQ 交易、AWS 反向连通或自动部署。

退出语义分开处理：正常停止先注销服务并清理连接；gRPC 排空超时后返回 `errShutdownTimeout`，跳过共享依赖的提前清理，由入口的失败退出结束进程并释放资源，避免仍活动的 handler 使用已关闭或被清空的依赖。默认 `5 秒` 是 gRPC 排空等待期限，不是整个进程退出的绝对时长；Consul 注销还受自己的 HTTP 超时约束。

`grpc.health.v1` 的 `SERVING` 在本轮只表示基础进程存活并提供 RPC，**不表示订单交易 ready**。`InitSrvConn` 的连接就绪也只证明传输可用，不证明库存、消息补偿或支付链路正确。后续按批次 04 → 05 → 06 建立交易能力，复用本轮初始化、连接和退出流程；不要先照历史代码临时接上 Redis/MQ，再在旧计划中重复替换。

第 1—4 节保留原审查证据与当时的建议，属于**修复前历史基线**。其中“当前”“缺少”“尚未修改”描述原审查现场，不能作为本轮仍未修复的清单；存续文件的旧行号也可能变化。当前状态以上表为准，后续执行顺序以第 5 节及旧计划为准。

## 原审查说明（历史记录）

**只恢复编译，需要的是有限的包、类型、依赖和注册修补，不必先重写下单算法。** 原审查阶段已在隔离副本里验证这一点，当时没有修改真实工作区的业务代码。构建、基础启动和下单正确性需要分别验收；本轮实施结果见顶部状态表。

对照发现，本地订单 handler 在归一化模块路径和文件末尾空白后，与参考版本相同；主要缺口是接入过程和启动接线。两边也保留了相同的业务风险，因此“照参考补齐”和“业务已经正确”需要分别验收。只改 Nacos IP 或看到 gRPC `SERVING` 都不能代替这些检查。

原审查阶段只提出修改建议，没有修改业务代码、配置、依赖或 CI/CD，也没有启动、部署服务或更改云端状态。这是审查阶段的操作边界，不代表随后的授权修复未发生。以下历史本地链接保留审查时的定位，已删除文件改用固定基线链接。

## 1. 结论与证据范围

| 层次 | 已看到的问题 | 能得出的结论 |
| --- | --- | --- |
| 构建阻断 | 同目录混合 package、错误的 proto 导入、错误服务注册、缺失类型与导入、生成接口不匹配、模块依赖不完整 | 需要先让订单 RPC 在现有 Go 模块下编译通过 |
| 静态确认的启动与调用问题 | DB 初始化接到了库存 global；订单 MQ、库存锁与消费者缺少启动接线；订单 handler 假定存在 tracing span | 即使修完语法，仍不能直接认为服务可用 |
| 待集成验证 | Consul 反向可达性、真实数据库表、消息重投与库存补偿、支付与超时竞争 | 源码对照不能代替隔离环境下的业务验证 |

### 原审查阶段编译记录（修复前）

原审查阶段在真实工作区和只读 HEAD 基线中构建用户、商品、库存三个 RPC 均通过；加入订单目标后，首先出现 `updates to go.mod needed`。为查看后续被遮住的错误，当时复制源码到临时目录，逐层修补并重跑构建；该审查步骤没有改动真实仓库的 `go.mod`、`go.sum` 或 Go 文件。

| 验证位置与步骤 | 实测结果 |
| --- | --- |
| 真实工作区：`go build -mod=readonly ./user_srv ./goods_srv ./inventory_srv` | 通过 |
| 真实工作区：四个 RPC 联合 readonly 构建 | 订单目标被模块文件更新要求阻断 |
| 真实工作区：`go build -mod=readonly ./order_srv/config` | `config.go:33:19: undefined: GoodsConfig` |
| 未修补副本：`go build -mod=readonly ./order_srv/global` | `undefined: proto`、`undefined: producer`、`undefined: rocketmq` |
| 临时副本：先解析缺失 resolver 依赖，再逐层修复下文 C1—C5 | 依次暴露混合 package、错误导入、未定义类型/包、缺少 `MqInfo`、错误注册对象、缺少接口嵌入；没有通过业务改写绕过错误 |
| 临时副本：完成这些机械修补后 | 单独订单 readonly 构建通过；再修正 DB 的 global 导入后，四个 RPC 联合 readonly 构建也通过 |

因此，原始构建只显示一个模块错误，不代表只缺一个依赖；反过来，消息补偿等业务风险也不是必须先解决的编译条件。

本次构建使用 `go1.25.6 darwin/arm64`，没有验证 Linux 镜像或启动业务服务。后续正式修复时，单独构建订单入口应使用 `-o` 指定输出到临时位置，避免默认输出名 `order_srv` 与源码目录冲突；四个入口联合构建则不需要生成可执行文件。

下面的“静态确认”是由源码中的实际声明、调用与导入得出，不代表已经运行过出错路径。没有重新读取 AWS 的 Nacos 内容，也没有把历史部署记录当成本次云端验证。

## 2. 第一阶段：让订单 RPC 编译通过

### C1. initialize 目录混入了第二个 main

[本仓历史基线 initialize/main.go:1](https://github.com/qqawer/grpctest/blob/b08f0a3b29f2bc05fe0a5cfe3db3d986f5ac06c4/order_srv/initialize/main.go#L1) 声明 `package main`，而同目录的 [initialize/config.go:1](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/initialize/config.go:1)、`db.go`、`srv_conn.go` 都是 `package initialize`。这个文件还重复了服务入口，并导入了自身所在的 initialize 包；该重复文件已在本轮修复中删除。

参考项目只在订单目录根部保留 [main.go](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/order_srv/main.go#L35)，[initialize 目录](https://github.com/iceymoss/mxshop/tree/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/order_srv/initialize) 只放初始化函数。

**建议修改：** 确认它是重复入口后，删除这个副本；如果确实是独立示例，则移到独立命令目录。不要只把 `package main` 改成 `package initialize`，否则仍保留重复启动逻辑及自导入问题。

**验收：** `order_srv/initialize` 下所有实际参与构建的 `.go` 文件属于同一包，且该包不导入自己。

### C2. 主入口的 proto 路径和注册对象都不对

[本地 main.go:10](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/main.go:10) 导入 `grpctest/order_srv/model/proto`，实际生成代码在 `grpctest/order_srv/proto`。此外，[main.go:51](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/main.go:51) 调用 `RegisterInventoryServer`，并传入不存在的 `handler.UnimplementedOrderServer`；本地业务实现实际叫 [handler.OrderServer](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/handler/order.go:57)。

参考项目导入自己的 [order_srv/proto](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/order_srv/main.go#L22)，并在 [main.go:96](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/order_srv/main.go#L96) 注册订单服务。

**建议修改：** 导入本地真实的 proto 包，并使用 `proto.RegisterOrderServer(server, &handler.OrderServer{})`。同时处理下一项生成接口要求。重复的 blank import 可以顺手清理，但不要把它与主要构建阻断混为一谈。

**验收：** 本地生成的 `Order` 服务注册到 gRPC server，实际订单方法由 `handler.OrderServer` 承接；不能仅注册一个默认返回 `Unimplemented` 的占位实现。

### C3. 本地新生成接口要求嵌入 UnimplementedOrderServer

[本地 order_grpc.pb.go:140](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/proto/order_grpc.pb.go:140) 的 `OrderServer` 接口还要求 `mustEmbedUnimplementedOrderServer()`，而 [handler/order.go:57](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/handler/order.go:57) 目前是空结构体。修正注册函数后，这个接口差异仍会导致编译失败。

参考项目使用旧版生成代码，其 [OrderServer 接口](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/order_srv/proto/order.pb.go#L1307) 没有这项要求；参考 handler 的空结构体因此不能直接照搬。本地 [生成文件头与版本约束](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/proto/order_grpc.pb.go:1) 与参考版本不同。

**建议修改：** 在业务 `OrderServer` 中按值嵌入 `proto.UnimplementedOrderServer`，保留已有业务方法；可增加编译期接口断言 `var _ proto.OrderServer = (*OrderServer)(nil)`。不要手改生成文件、复制旧 `.pb.go`，或为绕过接口要求整体降级 gRPC。[gRPC-Go v1.71.0 的生成器说明](https://github.com/grpc/grpc-go/blob/v1.71.0/cmd/protoc-gen-go-grpc/README.md) 也明确了按值嵌入的要求。本地 Web 与 RPC 的 `order.proto` 已逐字核对一致，当前没有更换通信协议的理由。

**验收：** 接口断言与服务注册一起通过编译；已有业务方法仍会覆盖嵌入类型的默认实现。当前 [go.mod:12](/Users/outsider/Downloads/Golang_Development/grpctest/go.mod:12) 的 gRPC v1.71.0 已满足本地生成文件标注的 v1.64.0 最低要求，这里不能简单归因为“gRPC 版本太旧”。

### C4. global 指向库存配置，且缺少所用包的导入

[本地 global/global.go:5](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/global/global.go:5) 导入的是 `inventory_srv/config`。这样声明的 `global.ServerConfig` 没有订单所需的 `GoodsSerInfo`、`InventorySerInfo` 等字段；[InitSrvConn:18](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/initialize/srv_conn.go:18) 已经在读取这些字段。该 global 文件还声明了 `proto`、`producer`、`rocketmq` 类型，却没有导入对应包。

参考的 [global/global.go:3](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/order_srv/global/global.go#L3) 导入订单自己的 config/proto，以及实际使用的 RocketMQ 包。

**建议修改：** 切换到 `grpctest/order_srv/config`，补齐本文件实际使用的订单 proto、RocketMQ client 和 producer 导入。只补模块依赖不会自动修复 Go 文件的 import。

**验收：** 订单 global 的配置和客户端都来自订单包；订单初始化代码能够解析自己的商品、库存服务配置；编译器不再报告未声明的包名或缺失字段。

这里需要避免一个误判：`ServerConfig` 没有预先 `&config.ServerConfig{}`，**本身不能证明一定发生空指针**。[InitConfig:78](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/initialize/config.go:78) 使用 `json.Unmarshal(..., &global.ServerConfig)`，有效 JSON 对象可以为这个指针分配结构体。真正需要修的是错误类型，以及反序列化后的必填项校验；它与后文没有任何初始化赋值的 MQ、Rs 不同。

### C5. 订单配置存在未定义类型，依赖还需按实际导入整理

[本地 config/config.go:33](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/config/config.go:33) 使用 `GoodsConfig`，但该文件定义的是 `GoodsSrvConfig`。参考配置让 [商品与库存服务名使用同一种结构](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/order_srv/config/config.go#L53)。

另外，[handler/order.go:324](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/handler/order.go:324) 已使用 `ServerConfig.MqInfo`，而本地订单配置没有这个字段。临时副本修正前面的类型与 import 后，编译器确实继续报出 `MqInfo` 未定义。

**建议修改：** 库存字段改用本地已经定义的 `GoodsSrvConfig`；补齐现有 MQ 调用实际需要的配置类型与 `MqInfo` 字段，再处理运行阶段的配置值和初始化。依赖方面，[srv_conn.go:8](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/initialize/srv_conn.go:8) 使用 `grpc-consul-resolver`，当前模块文件未声明它；临时副本解析到了 v1.5.3，正式修复时需审阅它引入的模块差异。

注意：本地 [go.mod:102](/Users/outsider/Downloads/Golang_Development/grpctest/go.mod:102) 已声明 RocketMQ v2.1.2，[go.mod:106](/Users/outsider/Downloads/Golang_Development/grpctest/go.mod:106) 已声明 opentracing；不能把所有导入错误都处理成再次安装这些包。参考项目的 [go.mod](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/go.mod#L5) 只能用来核对依赖用途，不能整体替换本地版本。

**验收：** 先修正确包和导入，再整理 `go.mod` / `go.sum`；审阅依赖差异后执行 `go build -mod=readonly`，确认不会继续要求修改模块文件。构建整个仓库会涉及其他示例程序，应与订单服务目标构建分开解释。

## 3. 第二阶段：接通启动流程与实际配置

### S1. 订单数据库初始化仍使用库存 global

[本地 initialize/db.go:5](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/initialize/db.go:5) 导入 `grpctest/inventory_srv/global`，所以 [第 20 行](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/initialize/db.go:20) 读的是库存配置，[第 38 行](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/initialize/db.go:38) 写的也是库存 DB。即便上一阶段已经修好订单 global.go，这个独立文件仍然接错对象。

参考 [initialize/db.go:6](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/order_srv/initialize/db.go#L6) 使用订单 global，并将连接保存到订单 DB。

**建议修改：** 改用订单 global，确认 DSN 取自订单的 MySQL 配置。不要启动时自动运行旧的 `model/main` 建表工具，也不要把参考源码中的数据库地址、账号或密码复制进来。

**验收：** 在隔离配置下完成 `InitConfig → InitDB`，明确连接的是订单数据库，订单 `global.DB` 非空且可查询；库存 global 不受这次初始化影响。按当前静态调用顺序，库存配置未初始化时可能先空指针；即使它被别的路径初始化，也仍会写错 DB。这两种后果都不是只改 Nacos 地址能解决的。

### S2. 保留本地配置格式，加载后校验业务必需字段

当前 [订单 YAML](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/config-debug.yaml:1) 是扁平结构，使用 `namespace`、`dataid`；当前 [InitConfig](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/initialize/config.go:21) 也按这种结构读取，并已传入 Nacos 登录凭据。参考项目则使用 [嵌套 NacosServer/NacosInfo](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/order_srv/config/config.go#L61) 与旧 SDK；两者不是可直接互换的配置格式。

**建议修改：** 维持本地 `config-debug.yaml` / `config-pro.yaml` 选择方式，以及当前 RPC 已有的显式挂载路径支持。配置加载结束时验证实际使用的服务名、业务 host、MySQL、Consul、商品和库存服务名；若启用 MQ/Redis，则把相应地址与必要鉴权字段纳入同一次校验。缺值时给出字段名并停止启动，避免等到请求阶段才发现 nil 或空地址。

本地 MySQL 配置使用字符串端口和 JSON 字段 `name`，参考使用整数端口和 `db`。它们可以各自正确工作，前提是 JSON、结构体和 DSN 格式一致；不能只复制参考 struct，留下现有 JSON 不变。参考 [非 debug 路径还有 `config-por.yaml` 拼写](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/order_srv/initialize/config.go#L26)，也不应复制。

**验收：** 用脱敏配置核对字段反序列化；缺少必要字段时在服务注册前明确失败；合法配置保留现有命名与 Data ID。这里的工作是完善订单初始化，不涉及新增 JWT 初始化入口。

### S3. Nacos 地址、监听地址与 Consul 注册地址是三件事

| 字段/检查 | 本地源码中的用途 | 需要满足的条件 |
| --- | --- | --- |
| bootstrap YAML 的 `host` | Mac 访问 AWS Nacos，见 [config-debug.yaml:2](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/config-debug.yaml:2) | 这是配置中心地址，不是订单服务地址 |
| `-ip` / `-port` | gRPC 监听，默认 `0.0.0.0:50051`，见 [main.go:26](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/main.go:26) | 监听 IP 可以是 `0.0.0.0`；本机多 RPC 同时启动必须使用不同端口 |
| 业务 JSON 的 `host` | Consul 健康检查和客户端发现的地址，见 [main.go:71](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/main.go:71)、[main.go:84](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/main.go:84) | 必须能被 Consul 和调用方访问，不能填 `0.0.0.0` |
| Compose 的订单 `-port 50054` | [compose.apps.yaml:57](/Users/outsider/Downloads/Golang_Development/grpctest/deploy/compose.apps.yaml:57) 显式覆盖代码默认值 | 这里有覆盖，不能说容器必然监听错端口；本机直接运行时需自己指定 |
| 容器内部探针 `127.0.0.1` | 从容器内部检测自身进程 | 它成功不代表云端 Consul 能访问 Mac 服务 |

同一 Docker 网络内，业务 host 可以是订单容器的服务 DNS 名；Mac 运行服务、AWS 运行 Consul 时，需要真实可达的 Mac 地址及网络路径。只把 YAML 改成 AWS Nacos IP，不会自动打通这条反向连接，也不能用 AWS 的公网 IP 来代表 Mac 上的订单进程。参考项目也分别使用 [监听地址](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/order_srv/main.go#L54) 和 [注册地址](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/order_srv/main.go#L115)。

按本地 [配置生成脚本:32](/Users/outsider/Downloads/Golang_Development/grpctest/deploy/scripts/configure-nacos.py:32)，Consul 服务名为 `orders-srv`，容器 DNS 名为 `order-srv`；这两个字段用途不同。结合 Compose 的 `-port 50054`，该 Docker 场景预期的服务注册地址与 Consul 检查目标都是 `order-srv:50054`。这是对部署源码的核对，不是线上注册结果。现有 Compose 还调用已缺失的自建 healthcheck 程序，部署前需要另行适配。

**验收：** 从 Consul 所在环境验证注册地址可达，并核对注册端口就是进程实际监听端口；从订单客户端环境发起一个真实 RPC。当前没有进行这项跨机器实测。

### S4. 下游连接需要显式报告失败，配置日志需要脱敏

[本地 InitSrvConn:17](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/initialize/srv_conn.go:17) 和 [第 33 行](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/initialize/srv_conn.go:33) 构建商品、库存客户端；错误分支只记录后返回，主入口无法据此判断初始化是否失败。这段与参考 [InitSrvConn](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/order_srv/initialize/srv_conn.go#L14) 基本一致，因此参考项目不能作为可靠性证明。

**建议修改：** 让初始化错误传回启动入口，并对实际 RPC 加期限和错误处理；创建 client 对象成功不能代替下游服务可达性检查。[第 23 行](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/initialize/srv_conn.go:23) 与 [第 39 行](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/initialize/srv_conn.go:39) 仍打印完整业务配置，应改为只记录非敏感的服务名和地址。参考项目也打印配置和 DSN，不能直接沿用这种日志习惯。

**验收：** 商品/库存服务缺失、解析失败、请求超时都返回可定位的错误；启动日志不含密码、连接串或完整配置对象。

## 4. MQ 与下单链路：启动缺口和业务风险

**实施边界更新：** 本节保留原始接线缺口及参考方式，不能按表格直接逐项启用旧实现。本轮只补无 span 保护及交易未就绪拦截；库存正确性先由批次 04 的数据库事务与幂等保证，MQ 接线与持久化决定、回查、恢复在批次 05 一起实现，支付/关闭竞争归批次 06。Redis 锁不作为先行恢复订单交易的必要步骤，完整 tracing 归批次 11。

### 明确缺少的接线

| 问题 | 本地证据和触发点 | 参考如何接入；建议修改 |
| --- | --- | --- |
| 订单普通 MQ producer 没有初始化 | [global.go:27](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/global/global.go:27) 只有 `MQOrder`、`MQInventory` 声明，没有赋值；[下单发送超时消息:279](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/handler/order.go:279)、[超时发送归还消息:475](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/handler/order.go:475) 却会调用它们 | 参考 [main:44](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/order_srv/main.go#L44) 调用 MQ 初始化，[rocketMQ.go:12](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/order_srv/initialize/rocketMQ.go#L12) 建立并启动两个 producer。本地应补齐连接与生命周期，失败返回启动错误；参考只记录错误后继续使用 producer 的做法不应照抄 |
| 超时关单和库存归还没有消费者启动 | 本地有 [TimeOutReback:450](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/handler/order.go:450)，但订单 main 没有订阅它；库存 main 也没有启动归还消息消费者 | 参考 [订单 main:121](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/order_srv/main.go#L121)、[库存 main:86](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/inventory_srv/main.go#L86) 分别创建、订阅并启动 consumer。本地需核对 topic/group 一致，启动失败可见，退出时关闭 |
| 库存分布式锁没有实例 | [库存 global.go:20](/Users/outsider/Downloads/Golang_Development/grpctest/inventory_srv/global/global.go:20) 的 `Rs` 没有初始化赋值；[Sell:76](/Users/outsider/Downloads/Golang_Development/grpctest/inventory_srv/handler/inventory.go:76) 调用它 | 参考 [库存 main:38](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/inventory_srv/main.go#L38) 调用 `InitRs`。本地需初始化库存实际使用的 Redis/Redsync，并按当前 Redis 配置接入鉴权；不能只给订单 global 声明一个 `Rs` |
| handler 假定所有 RPC 都有 tracing span | [order handler:141](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/handler/order.go:141) 取 span，[第 153 行](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/handler/order.go:153) 直接 `.Context()`；[main:50](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/main.go:50) 的 server 没有对应 interceptor | 参考 [main:85](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/order_srv/main.go#L85) 初始化 tracer，[main:94](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/order_srv/main.go#L94) 安装 interceptor。本地可补追踪接线，或让业务允许无 span；无需为了编译先引入整套 tracing 基础设施 |

上述属于静态确认的调用缺口。另以本地 SDK 做了最小程序验证：`SpanFromContext(context.Background())` 返回 nil，直接调用其 `.Context()` 会 panic；没有通过真实下单请求复现。

配置与基础设施也要配套：本地 [configure-nacos.py:31](/Users/outsider/Downloads/Golang_Development/grpctest/deploy/scripts/configure-nacos.py:31) 的 RPC 配置模板没有 `rocket`，也没有库存所需 Redis 配置；[compose.yaml](/Users/outsider/Downloads/Golang_Development/grpctest/deploy/compose.yaml:1) 没有 RocketMQ 服务。这是本地文件的检查结果，不能据此断言当前云端配置完全相同。正式启动前，应核对所选择的实际配置与可达依赖，必要依赖初始化失败时不能继续对外声明健康。

### 参考项目也存在的业务风险

以下是源码与依赖语义推导，**没有运行 MySQL/MQ 故障注入，不能写成已经发生过实际数据损失**。本地与参考保留了相同业务逻辑，应在恢复构建后另行验证，不纳入最小编译修补。

| 优先验证项 | 源码推导的故障路径 | 建议和验收 |
| --- | --- | --- |
| 多 SKU 库存归还可能一直回滚 | [本地库存:179](/Users/outsider/Downloads/Golang_Development/grpctest/inventory_srv/handler/inventory.go:179) 在商品循环内反复把同一扣减记录 `status` 改成 2；第二次值不变时 `RowsAffected==0` 导致整个事务回滚。参考 [库存:177](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/inventory_srv/handler/inventory.go#L177) 相同。当前 DSN 没有 `clientFoundRows`，该模型也没有 `UpdatedAt` 自动更新来改变这个结果 | 事务内锁定或条件认领待归还记录，完成所有商品增量后只更新一次归还状态；各分支明确结束事务。验证两个 SKU 各扣 1 后消息只归还各 1，重复消息不再增加 |
| 库存已提交却因解锁失败返回失败 | [本地库存:110](/Users/outsider/Downloads/Golang_Development/grpctest/inventory_srv/handler/inventory.go:110) 先提交扣减事务，再因解锁错误返回失败；[订单:209](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/handler/order.go:209) 对 `Sell` 的所有错误都丢弃补偿消息。参考 [库存:108](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/inventory_srv/handler/inventory.go#L108)、[订单:209](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/order_srv/handler/order.go#L209) 相同 | 区分已提交结果与解锁清理错误；超时/丢包属于结果未知，按订单号核对扣减流水并幂等处理。注入提交后解锁失败及回包丢失，验证最终库存平衡 |
| 订单提交失败或事务回查异常被当成确定结果 | [本地订单:289](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/handler/order.go:289) 未检查数据库提交错误就返回成功；[第 303 行](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/handler/order.go:303) 只看查询行数，不区分数据库异常与没有订单。参考 [订单:289](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/order_srv/handler/order.go#L289) 相同 | 检查事务创建、写入、提交及查询错误；数据库不可用时保留未知状态等待回查，不能提前承诺成功或确定归还。分别注入提交失败、查询失败验证 |
| 超时关闭可能覆盖并发支付成功 | [本地 TimeOutReback:457](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/handler/order.go:457) 先读状态，之后无条件保存关闭状态；[UpdateOrderStatus:444](/Users/outsider/Downloads/Golang_Development/grpctest/order_srv/handler/order.go:444) 可以在中间写入支付成功。参考 [订单:450](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/order_srv/handler/order.go#L450) 相同 | 在数据库中用条件更新或行锁协调“待支付→已支付/已关闭”，只有关闭成功者产生归还消息，并保证消息最终送达。重放“超时读取→支付成功→超时保存”，验证已支付订单不会被关闭或归还 |

多 SKU 推导依据还包括 [go-sql-driver/mysql v1.7.0 的 `clientFoundRows` 说明](https://github.com/go-sql-driver/mysql/blob/v1.7.0/README.md#clientfoundrows)：默认返回实际改变的行数。最终仍需在隔离 MySQL 中确认事务行为。

不要按字面把 RocketMQ 状态反过来：这里事务消息的 topic 是库存归还。订单成功时 `RollbackMessageState` 表示丢弃归还消息；需要补偿时 `CommitMessageState` 表示投递归还消息。这个方向本身不是 bug，需要修的是何时能确定结果、何时应保持未知。

每次下单创建/关闭事务 producer 的并发与延迟回查生命周期也值得后续审查，但不属于本次最小恢复编译范围。尤其不能简单把当前 `OrderListener` 全局共享：它持有请求相关的可变结果，重构时必须按订单号关联并基于持久状态回查。

## 5. 建议实施顺序与验收

| 阶段 | 当前状态与下一步范围 | 完成标准 |
| --- | --- | --- |
| A. 构建与基础启动（批次 01，部分完成） | 本轮 C1—C5、订单配置/DB/下游连接、端口注册、退出清理及保护已完成；保留并复用这些基础函数 | 四 RPC 本机/Linux 构建与订单包测试/race 已通过；超时退出修复的定向及 race 回归也通过。生成工具、隔离测试环境、迁移/seed、CI 仍待验收 |
| B. 库存正确性（批次 04，待完成） | 在旧计划 02/03 的前置验收后，实现数据库事务、订单号唯一约束、扣减与返库幂等、未知结果查询 | 多 SKU 全成全败；重复与乱序请求不改变已确定结果。不要以临时 Redis 锁替代数据库正确性 |
| C. 订单与 MQ（批次 05，待完成） | 持久化创建意图与决定；统一实现 producer/consumer 生命周期、事务回查和恢复任务 | 在故障、重复、超时、进程重启下收敛。复用现有启动/清理流程，完成后才开放创建订单交易能力 |
| D. 支付与关闭（批次 06，待完成） | 支付流水、条件状态转换、渠道未知结果及超时补偿 | 支付与关闭竞争不会产生“已付款却返库”等非法结果，核对数据库、库存与消息结果 |
| E. 追踪与部署（批次 11/12，待完成） | 完整 tracing、与代码匹配的配置挂载/镜像/探针和交付 | 基础 `SERVING` 与交易就绪分开验收；CI/CD 文件存在不表示部署链路已验证 |

近期按旧计划补齐批次 01 剩余项，再验收批次 02 的认证与共享配置；不要因本轮订单修复而重开一套相同的基础初始化实现。批次 03 的 API/购物车前置条件与后续 04—06 的业务验证也没有被本轮基础测试替代。

首轮业务验收至少覆盖：一件商品正常下单；多件商品的库存不足；扣减成功后订单写入失败；订单未支付超时；两种商品一起归还；同一归还消息顺序重投和并发消费；支付与超时同时发生。每项都核对订单状态、扣减/归还流水和最终库存，分别注明失败注入点与预期结果。支付与关闭的并发控制还须与外部支付结果及对账规则一致。

本报告中的验证标准是后续执行清单，尚未执行的项目不应在提交说明或部署记录中写成“已通过”。
