# 商品查询压测基线

这套工具用于测量本机的商品查询链路：`k6 → HTTP goods-web → TCP gRPC goods_srv → Docker MySQL`。它复用现有业务查询代码，创建一次性测试环境和合成数据，再保存原始结果供复核。它不会给 AWS 发送压力请求，也不会加载部署环境的 Nacos、Consul 或数据库密码。

这里说明的是运行方法和统计口径。实际性能数字应引用一次完整运行的结果报告；脚本存在、构建通过或 smoke 通过，都不代表系统已经达到某个容量。

已完成的实验：[2026-09-24 本机商品查询基线](reports/2026-09-24-goods-baseline.md)，包含两档数据、三轮确认、SQL 证据及尚未完成的优化工作。

## 快速运行

在 `grpctest` 仓库根目录执行。需要 Python 3、Go、本地 Docker/Compose，以及同级的 `grpctest_web` 仓库。默认 Web 入口是 `../grpctest_web/tests/performance/goods-web/main.go`；另一个目录下的 Web 仓库可用 `--web-root` 指定。

```sh
# macOS arm64：安装或重新核验固定版本 k6，不修改全局 PATH。
python3 tests/performance/install_k6.py

# 检查数据生成和统计逻辑，不启动压力请求。
python3 -m unittest discover -s tests/performance -p 'test_*.py'

# 先做一万商品的短时冒烟检查。
python3 tests/performance/run.py --profile smoke --sizes 10000

# 两档数据的探索与重复确认。
python3 tests/performance/run.py --profile baseline --sizes 10000 100000
```

安装器目前仅支持 macOS arm64，固定使用官方 k6 v2.3.0。它核对完整官方 checksum 文件中的对应条目、代码内固定的归档 SHA256，再检查 ZIP 内的固定二进制条目。已有缓存也重新核验，验证记录保存在 `.local/tools/k6-v2.3.0/verified.json`；验证失败时不会执行下载物。其他平台会明确报错。

runner 会自行构建两个仓库的独立入口，不需要先运行平时的 `go run .`。它的参数如下：

| 参数 | 默认值与含义 |
| --- | --- |
| `--profile` | `smoke`；也可选 `baseline` |
| `--sizes` | `10000 100000`；只能选这两档，不能重复 |
| `--web-root` | 同级 `grpctest_web` 仓库路径 |
| `--k6` | `.local/tools/k6-v2.3.0/k6`；覆盖时由调用者提供可信的本地二进制 |
| `--vus` | `100`，允许 1–200；同时用作预分配与最大 VU 数量 |

例如 Web 仓库不在同级目录时：

```sh
python3 tests/performance/run.py --profile smoke --sizes 10000 \
  --web-root /absolute/path/to/grpctest_web
```

不要同时运行多份 baseline。压力发生器、两个 Go 进程和 Docker 虚拟机共享本机资源，其他任务会影响结果。运行期间保持电脑唤醒，避免合盖或手动休眠。

## 数据和查询场景

`fixtures.py` 使用固定种子 `20260924` 生成全新的 `fixture.sql` 和 `manifest.json`。一万商品是十万商品数据的前缀；两档都包含 20 个品牌、62 个三级分类树节点、1,000 个分类品牌关系，以及与商品同数的库存行。商品均已上架且库存初始化完成；库存行用于完整测试数据，这批查询不读取库存服务。

品牌、分类和价格使用不同的伪随机序列。品牌在 20 个品牌间分布，价格为 1–2,000 的整数，每十件商品中一件名称包含 `Focus`。这是可复现的合成分布，不代表真实商城的热门商品分布。图片使用 `example.invalid` 地址，压测只读取 JSON，不下载图片。

manifest 记录每种筛选的预期总数、筛选条件、页码、每页数量和 1,024 个详情样本。生成器测试会从 SQL 独立重算数量、外键关系和样本内容，避免只用接口自己的返回值证明接口正确。

| 场景 | 请求与覆盖内容 |
| --- | --- |
| `detail` | 在 1,024 个确定的分散样本中轮换查询商品详情，并核对商品、品牌、分类、价格 |
| `list` | 无筛选列表第一页，每页 20 条 |
| `deep` | 约 80% 位置的深分页，每页 20 条；一万商品第 400 页，十万商品第 4,000 页 |
| `filtered` | 品牌 `b=1`，价格 100–1,000，含上下界 |
| `price` | 仅价格 100–1,000 |
| `category` | 一级分类 `c=1`，检查返回商品属于它的末级分类 |
| `keyword` | 名称包含 `Focus` |
| `mixed` | 每十次安排五次详情、三次列表、两次品牌与价格组合筛选 |

`goods.js` 还支持 `hot_detail`，固定访问首个详情样本，但当前 runner 的 smoke/baseline 不安排这个场景。`detail` 也不是遍历全部十万件商品。

当前 HTTP 分类字段拼写为 `ctegory`，断言遵循真实接口。列表没有显式排序，脚本不把某个 ID 顺序视为承诺。每次请求仍需通过 HTTP 200、合法 JSON、精确总数、分页条数、页内不重复 ID、品牌/分类关联和对应筛选条件检查；详情还核对 manifest 的准确字段。错误 JSON、错误业务数据都计失败，并终止后续压力轮次。错误路径即使返回 HTTP 200，也不能算业务成功。

## smoke 与 baseline 的区别

`smoke` 对详情、六种列表查询及混合场景分别以 2 RPS 运行 3 秒，主要检查初始化、数据和真实 HTTP/gRPC/MySQL 链路。**它不是容量报告，也不能据此写稳定 QPS。**

`baseline` 对每个指定的数据量按以下顺序执行：

1. 混合请求以 20 RPS 预热 30 秒。
2. 详情和六种列表场景分别以 20 RPS 探索 30 秒。
3. 混合请求依次以 20、50、100、200 RPS 探索，每档 30 秒。某档业务错误率超过 1%，或出现 dropped iterations，就停止继续升压。
4. 只有**本次指定的最大数据量**在 100 RPS 探索中通过全部门槛，才进入最多三轮 100 RPS、每轮 120 秒的混合确认。每轮确认前另有 100 RPS、30 秒预热。确认阶段若业务错误率超过 1% 或出现 dropped iterations，会提前停止。

“全部门槛”是业务错误率 ≤0.1%、P95 ≤300 ms、P99 ≤1,000 ms、dropped iterations 为 0，且 k6 阈值检查通过。这些是本实验选定的验收条件，不能解释为所有商城都应采用的标准。确认轮次及其是否通过，以保存的结果为准；跳过确认不能写成确认完成。

这里没有十分钟持续稳定性测试，也没有搜索最大容量。200 RPS 的短时探索不是“最大稳定 QPS”证明；三轮两分钟确认也不能当作十分钟 soak。

## 指标的含义

k6 使用 `constant-arrival-rate`，按目标速率安排请求，不因上一条请求变慢而主动降低目标速率。`--vus 100` 是执行请求的 VU 预算，**不表示测过 100 个同时在途请求**。VU 不够而未发出的请求会计入 `dropped_iterations`，不能混入成功请求。参见 [k6 constant-arrival-rate 官方说明](https://grafana.com/docs/k6/latest/using-k6/scenarios/executors/constant-arrival-rate/)。

- `successful_qps = 业务断言成功的请求数 / 计划发压秒数`。
- `sent_rps = 实际发送请求数 / 计划发压秒数`。
- `error_rate = 已发送但未通过业务断言的请求数 / 实际发送请求数`。
- `dropped_iterations` 单独报告，它们没有发出 HTTP 请求，不计入上述错误率分母。

计划窗口结束后，k6 最多允许 10 秒完成在途请求；HTTP 单次超时为 5 秒。成功计数可能包含宽限期完成的请求，因此这里的 QPS 是按计划发压窗口归一化的业务吞吐，**不是按实际完成窗口计算的速率**。`wall_seconds` 还包含启动、收尾和采样退出时间，应与 `seconds` 分开看。

P50/P95/P99 来自每次请求的 `response.timings.duration`，包括发送、等待响应和接收响应；不包含 DNS、连接建立、TLS、连接等待及客户端 JSON 解析/断言时间。结果同时包含全部请求和实际发生的各场景分项，失败请求也有延迟样本。这个口径见 [k6 内置指标说明](https://grafana.com/docs/k6/latest/using-k6/metrics/reference/)。

汇总程序读取原始 Point 数据重新计算成功数、错误分类和延迟，并检查请求数、结果数、HTTP 次数和延迟样本数是否一致；缺失或不一致的输出会拒绝作为有效结果。不要把仅设置了阈值但没有实际样本的 summary 项当成实测数据。

runner 还同时记录 UTC 起止时间、UTC elapsed、monotonic elapsed 和原始 `business_outcomes` 时间戳跨度。UTC 与 monotonic 的差值必须小于 2 秒，响应结果时间戳跨度必须落在 `seconds - 2` 至 `seconds + 10` 秒内；否则该轮保存为 `timing_valid: false`，随后终止并清理，不能引用它的 QPS。此前确实有一轮出现 UTC 跳过约 12 分钟、monotonic 没有同步变化的异常，已中止并排除，不能与有效轮次合并。

macOS 运行时会启动绑定 runner PID 的 `caffeinate -i -w`，只在本任务期间防止空闲休眠，退出即解除，不修改永久电源设置。它不能保证阻止合盖或手动休眠，因此保留上述时间一致性检查。

## 结果文件与诊断依据

每次运行创建 `.local/benchmarks/<UTC时间>-<随机后缀>/`，开始时会打印目录。目录保留本机证据，不自动写入 Git。

| 文件 | 内容与来源 |
| --- | --- |
| `metadata.json` | 两个仓库的 HEAD 和工作区状态、Go/k6 版本、硬件、Docker 信息、后台容器、拓扑与门槛 |
| `source/`、`source-sha256.json` | 两个仓库的 performance 工具源码快照及哈希 |
| `<size>/fixture.sql`、`manifest.json` | 本档数据、预期结果和 SQL 内容哈希 |
| `<size>/environment.json` | 测试项目名、MySQL 版本/镜像/关键配置、行数和迁移状态 |
| `<size>/<轮次>/points.json.gz` | k6 原始时序 Point，含 case/outcome 标签；是重新计算统计的依据 |
| `<size>/<轮次>/k6-summary.json`、`k6.log` | k6 自身汇总、阈值结果与执行日志 |
| `<size>/<轮次>/result.json` | runner 从原始 Point 汇总的业务 QPS、错误、延迟、门槛、SQL 诊断数据 |
| `<size>/results.json`、顶层 `results.json` | 对应数据量和整次运行的轮次列表，预热/探索/确认通过 `kind` 区分 |
| `<size>/<轮次>/resources.jsonl` | 本次创建的 Go/k6 进程、MySQL 容器资源和主机负载采样 |
| `<size>/rpc.log`、`goods-web.log` | 生产 GORM SQL 日志与 Web 请求日志；`web.log` 为入口进程输出 |
| `<size>/explain.txt` | 结束后对列表/组合筛选/价格 count 等代表性 SQL 执行 `EXPLAIN ANALYZE`，不是所有查询的执行计划 |
| `<size>/final-sql.json` | 最后的 MySQL Performance Schema digest 快照 |

原始 gzip 输出使用 k6 的 [JSON 输出能力](https://grafana.com/docs/k6/latest/results-output/real-time/json/)。summary 本身不会自动展开全部标签，错误分类和场景明细应看 runner 从 `points.json.gz` 生成的结果。

SQL 诊断有两种不同来源。`sql` 是每轮前后 Performance Schema digest 的差值，包含执行次数、累计时间和扫描行数，但部分预处理语句的 digest 覆盖不完整，不能视为全部 SQL。`gorm_trace` 则从该轮 `rpc.log` 新增片段抽取 SQL 模式、次数和 trace 延迟，不给业务查询加额外回调。**GORM 的父级 Find trace 时间包含 Preload，父查询与分类/品牌 Preload 的 trace 时间不能相加当作独立 SQL 总耗时。**

资源采样约每五秒一次，采样命令本身也有耗时。`ps pcpu` 有历史统计口径，不是精确的该五秒瞬时 CPU；`time` 列是进程累计 CPU 时间，`rss` 是采样时驻留内存。Docker 的 NetworkIO/BlockIO 是累计量，不能直接当作每秒带宽或 IOPS。采样失败会保留 `sampling_error`，不要把缺失数据当成零。采样、完整响应断言和日志写入本身也是本机实验成本，比较前后结果必须保持相同设置。

## 隔离、清理和与部署环境的差异

每个数据量创建随机命名的 `grpctest-test-bench-*` 环境。Docker 必须使用本地 Unix socket；端口仅绑定 `127.0.0.1`。数据库使用随机密码和匹配的 test marker，数据迁移、种子导入与清理都经过现有测试环境的所有权检查。RPC 入口再校验 loopback、测试凭据和数据库 marker。

runner 的 `finally` 会先停止它启动的 Web/RPC 进程，再清理该测试项目的容器、网络和卷，并删除临时 `connection.json`。它不执行全局 prune，也不会停止其他项目。若进程被强杀或机器断电导致清理未执行，可从本次 `environment.json` 找到准确项目名，再执行：

```sh
python3 tests/env.py down --project grpctest-test-bench-本次随机后缀
```

私有测试状态仍位于 `.local/testing/<项目名>/state.json`，保存它能支持所有权校验。`.local/` 已被 Git 忽略；密码、临时连接文件、原始 Docker metadata、日志和原始结果都不要强制加入 Git。要公开结果时，另写经过筛选的报告。

这条链路复用生产 Web 路由、handler、中间件、验证器、RPC 客户端策略，以及商品 RPC 查询实现和数据库初始化。GORM Info 日志、Web Info 请求文件日志和生产默认数据库连接池均保留。隔离入口的差异是：

- 内存配置，跳过 Nacos/Consul；没有云端服务发现和公网延迟。
- Web 使用 Gin release 模式、临时 JWT key 和随机 loopback 端口；外层仅允许健康检查及商品列表/详情 GET。
- RPC 用只读 wrapper 转发生产列表/详情，其他 RPC 不可用；库存服务和 MQ 不参与查询。
- 本地测试环境仍会启动一个空闲 Redis 容器，记录在环境信息中，但查询链路不使用 Redis。
- 压力发生器、服务与 MySQL 容器共享本机资源，不能把结果直接换算为 AWS 的吞吐能力。

现有 HTTP 读 handler 没有把请求取消传到 RPC，入口也没有为了压测额外调优 deadline。客户端报超时，不代表服务端数据库查询立即停止；停止压力后的在途工作可能影响随后的观察。

## 后续工作

基线完成后，根据查询计划、SQL 证据和资源采样选择一个具体问题，例如某种筛选的索引或深分页成本；一次修改一个主要因素，再使用相同数据、硬件、日志、采样、预热和请求组成复测。索引优化、连接池调优、缓存、最大容量搜索及长期稳定性测试并不是这套基线脚本已经完成的工作。
