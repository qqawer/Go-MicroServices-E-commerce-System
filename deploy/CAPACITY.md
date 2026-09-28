# 磁盘容量、日志与镜像保留

2026-09-28 实际故障：19 GiB 根盘达到 91%，RocketMQ 报 `CODE 14 / disk full` 并拒绝订单事务消息，但 TCP 健康检查仍通过。一次性清理旧镜像和归档日志后下单恢复，真实沙箱支付通过。以下规则避免同样的部署误判，不替代业务回归或完整监控。

## Nacos 文件日志

`stack.sh up` 会先从 Compose 固定的 Nacos 2.5.1 镜像提取原始 `nacos-logback.xml`，核对 SHA-256 和 32 个轮转策略，再生成 `runtime/logging/nacos-logback.xml`。Compose 只读挂载到 Nacos 原配置位置。保持全部日志级别、文件路径和 logger 绑定；镜像配置发生变化时拒绝自动生成，需先审查版本。

每个 logger 的活动文件约 10 MiB 轮转，归档合计最多 30 MiB，最多保留两个历史日，并在启动时清理。大小上限优先，因此日志量大时可能不足三天；32 个策略的活动文件与归档预算约 1.25 GiB，不包含其他组件、Tomcat 访问日志或 JVM 输出，也不是文件系统硬配额。轮转和归档清理由 Nacos/Logback 执行，不用外部任务截断活动日志。

已有服务器先同步 `compose.yaml`、`scripts/prepare-nacos-logging.py`、`scripts/stack.sh`，然后：

```bash
cd /opt/grpctest/deploy
sudo python3 scripts/prepare-nacos-logging.py
sudo docker compose --env-file .env -f compose.yaml config --quiet
sudo docker compose --env-file .env -f compose.yaml up -d --no-deps --pull never --force-recreate --wait --wait-timeout 180 nacos
```

重建会短暂中断 Nacos 连接，数据卷和已发布配置保留。以后修改生成规则也必须重新生成并重建 Nacos，因为文件 bind mount 不会随宿主机原子替换自动换 inode。回退时恢复上一份 Compose / 生成文件后重建，不删除数据卷。

## 应用镜像

每次 `release.py` 成功发布后自动清理一次，可用下列命令先预览、再显式清理：

```bash
sudo python3 scripts/image_retention.py
sudo python3 scripts/image_retention.py --apply
```

仅处理当前 `GHCR_OWNER` 下七个项目镜像，要求有正确源码标签、完整提交号和可重新拉取的 registry digest。每个服务保留最近三个不同源码版本，额外保护所有运行/停止容器使用的镜像、`images.env` 当前版本、本次部署前后版本，以及创建不足 24 小时的镜像。未知本地别名、基础设施镜像和无 registry digest 的预导入镜像保留。24 小时内频繁构建可能暂时超过三个版本。

清理与部署共用 `release.lock`，删除前再次检查容器引用，使用无 `--force` 的精确 image ID 删除。不会调用全局 prune，也不删除容器、卷、数据库、消息、构建缓存或 GHCR 远端镜像。最后一次候选清单和实际删除结果保存在 `runtime/state/image-retention-last.json`。清理失败会输出警告，已完成的健康发布仍保留；旧版本远端仍可拉取，回滚仍需相应 registry 权限。

## 发布前容量与 MQ 写入检查

同步 `capacity.py`、`image_retention.py` 和 `release.py` 到服务器 `scripts/`。这些部署脚本由 Jenkins 从宿主机目录执行，不会随业务镜像自动更新。

先在 Broker 中创建独立 `grpctest-healthcheck` Topic（新环境也已加入 `rocketmq/topics.sh`）：

```bash
sudo docker exec grpctest-infra-mqbroker-1 sh mqadmin updateTopic \
  -n mqnamesrv:9876 -b mqbroker:10911 -t grpctest-healthcheck -r 1 -w 1 -p 6
```

发布器在拉镜像前检查实际 Broker 数据所在文件系统：使用率必须低于 85%，可用空间至少 3 GiB。拉取后再次检查，要求仍低于 85% 且至少 2 GiB，然后才能替换应用容器。这些是当前单机环境的容量预算，不保证任意大小的镜像都能拉取；拉取失败或后置容量检查失败时保留旧容器和发布状态。

两处同时向独立 Topic 发送小探测消息，必须收到指定 Broker 的 `SEND_OK`；仅退出码 0、端口通、`FLUSH_DISK_TIMEOUT` 或异常文本都不能通过。探测不向订单 Topic 发送，且不启动订单消费者。探测消息由 Broker 既有保留规则清理。不放宽 Broker 磁盘保护阈值。

若部署被磁盘门槛挡住，先运行镜像清理预览和磁盘检查，确认日志轮转正常，再评估容量；不能通过删业务数据或提高拒写阈值处理。当前只有发布时检查和失败流水线提示，尚未增加持续磁盘告警，也不能保证运行期完全不会耗尽容量。

依据：[Nacos 2.5.1 原始日志配置](https://github.com/alibaba/nacos/blob/2.5.1/distribution/conf/nacos-logback.xml)、[RocketMQ 5.5.0 sendMessage 实现](https://github.com/apache/rocketmq/blob/rocketmq-all-5.5.0/tools/src/main/java/org/apache/rocketmq/tools/command/message/SendMessageCommand.java)。

## 本批验证

`bash ci/check.sh` 的生产入口构建和 Go race 回归通过；Python CI 共 15 项通过，其中本批新增 10 项覆盖容量拒绝、拉取后拒绝、MQ 非成功结果、保留版本和删除前引用复查。错误门槛在修复前会放行，修复后拒绝且保留旧发布状态。

AWS 实测：TCP 健康时向不存在的探测 Topic 发送仍会被门槛拒绝；专用 Topic 返回 `SEND_OK` 后通过。仅在探测 Topic 上验证，不向业务 Topic 注入故障。Nacos 重建后七份已发布配置哈希一致、32 组日志保留参数在容器内核对一致，全部基础设施及七个业务服务健康；归档自动清理后 Nacos 文件日志约 460 MiB，根盘使用率从 70% 降至 66%。这是该次验证的快照，不是长期稳定性或压测结果。本批未执行 Terraform apply。
