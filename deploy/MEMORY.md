# 单机学习环境的内存预算

这组配置用于当前小数据量、低并发的单机环境，目的是为后续评估 4 GiB 实例留出余量。2026-09-28 的调整和验证发生在 8 GiB 的 `t2.large` 上；9 月 29 日的实际 4 GiB 切换记录见文末。两次有限验证均不能证明任意业务负载都能在 4 GiB 上运行。

## 配置改动

| 服务 | 原配置 | 当前配置 |
| --- | --- | --- |
| Nacos 2.5.1 | 初始堆 512 MiB，最大堆 768 MiB，年轻代 256 MiB | 初始堆 128 MiB，最大堆 384 MiB，年轻代 64 MiB |
| Jenkins | 初始堆 256 MiB，最大堆 768 MiB | 初始堆 128 MiB，最大堆 384 MiB |
| MySQL 8.0 | InnoDB 缓冲池 256 MiB | InnoDB 缓冲池 128 MiB |

Jenkins 仅编排部署；编译、隔离数据库和 MQ 回归在 GitHub Actions 执行。调整前，业务表与索引合计约 2.9 MiB，MySQL 实际缓存页约 25 MiB，因此 128 MiB 缓冲池仍覆盖当前工作集。连接数上限仍为 150，`innodb_flush_log_at_trx_commit=1` 保持不变。

RocketMQ 的 Broker/NameServer 堆、直接内存和刷盘策略保持原值；没有关闭服务、停用回归测试或新增 Swap。七个 Go 服务合计占用不到 100 MiB，本批没有改动 Go 代码。

`-Xmx` 只限制 Java 堆，进程还会使用元空间、代码缓存、线程栈等内存，不能把它当作容器总内存上限。Jenkins 的需求会随插件和任务增长，Nacos 的需求会随配置及连接增长；增加数据量、服务数量或压测并发时，需要重新测量。

## 测量方法

主机占用使用 `/proc/meminfo` 的 `MemTotal - MemAvailable`，避免把可回收文件缓存全部算成不可释放内存；服务快照使用 `docker stats --no-stream`。两者口径不同，不能直接相加。

调整前的五次主机采样为 3566–3583 MiB；Nacos 约 651 MiB、Jenkins 约 618 MiB、MySQL 约 427 MiB。稳定占用目标为约 3 GiB，部署过程的观察目标为低于 3.5 GiB，以预留系统和短时波动空间。这些是本环境的操作预算，不是框架的最低硬件要求。

配置调整后的 60 秒轻负载采样为 3020.4–3047.3 MiB，均值 3034.4 MiB，比调整前约少 0.5 GiB；120 次查询全部成功，P95 为 53.23 ms，最大值 171.34 ms。前后采样时长与请求节奏不同，不能用这些数字声称接口吞吐或延迟提升。完整部署的峰值另取持续观察记录，不能用这段轻负载结果替代。

验证包含：

- 商品列表、详情的 HTTP 200 与业务 JSON 校验，调整前 20 次请求作为对照，调整后持续 60 秒、每秒两次查询；这是低负载验收，不是最大吞吐压测。
- Nacos 七份已发布配置的哈希一致，Jenkins 部署账号及历史记录保留，MySQL 重启后实际缓冲池参数正确。
- 既有支付订单状态、金额和库存一致，独立 RocketMQ 探测 Topic 写入返回 `SEND_OK`。
- 提交后的完整 GitHub CI/CD，以及发布过程中主机内存与内核 OOM 计数采样。

本次原始测量保存在服务器 `runtime/state/memory-baseline-20260928.json`、`memory-http-baseline-20260928.json`、`memory-after-tuning-20260928.json` 和 `memory-observe-20260928.jsonl`。这些运行期文件不提交仓库。观察覆盖的时间和负载有限；切换到 4 GiB 后仍须重新验证，不能把本次结果写成长时稳定性或生产容量结论。

## 已有服务器应用与回退

先确认 Jenkins 没有执行中或排队的部署任务，备份原 `compose.yaml`，再同步已审查的新文件。Compose 的修改不会随业务镜像自动同步到服务器。逐个重建下面的基础设施服务，每个健康后再继续；会产生短暂重连。

```bash
cd /opt/grpctest/deploy
sudo docker compose --env-file .env -f compose.yaml --profile ci config --quiet
for service in nacos jenkins mysql; do
  sudo docker compose --env-file .env -f compose.yaml --profile ci \
    up -d --no-deps --no-build --pull never --force-recreate \
    --wait --wait-timeout 240 "$service" || break
done
```

本次先在线试调 MySQL 缓冲池并核对查询，再重建 MySQL 让容器启动参数与 Compose 一致；没有留下 `SET PERSIST` 的额外覆盖。原数据卷保留。回退时恢复服务器备份 `runtime/state/before-memory-tuning-20260928-compose.yaml`，对上述服务逐个重建；禁止通过删除卷来回退。

依据：[Nacos JVM 环境变量](https://www.nacos.io/zh-cn/docs/quick-start-docker.html)、[Jenkins 内存需求](https://www.jenkins.io/doc/book/scaling/hardware-recommendations/)、[MySQL 缓冲池在线调整](https://dev.mysql.com/doc/refman/8.0/en/innodb-buffer-pool-resize.html)。


## 2026-09-29：实际切换到 4 GiB

同一 EC2 实例已切换为 `t3.medium`（2 vCPU、4 GiB），系统可用物理内存约 3831 MiB。沿用上述 JVM/MySQL 配置、CPU 积分 standard 模式和原 20 GiB gp3 磁盘，没有添加 swap。切换前停止应用及中间件并创建磁盘快照；启动后按依赖顺序恢复服务。

15 个容器均已健康，四个 RPC 的 Consul 检查 passing；RocketMQ 专用健康主题实际写入返回 SEND_OK。已付款订单 2 仍为 TRADE_SUCCESS、金额 4490 分，商品 421 库存仍为 99/version 3。Mac 用更新后的源码 YAML 启动 goods-web，商品列表和详情均成功读取。

本次为学习环境迁移验证。4 GiB 下的内存观测保存在服务器 `runtime/state/memory-t3-20260929.jsonl`，查询样本保存在 `runtime/state/http-t3-20260929.json`；它们不是吞吐量上限或长期稳定性证明。当前公网地址与恢复入口见 [AWS 部署记录](DEPLOYED_AWS.md#2026-09-29-实例降配与公网地址更新)。
