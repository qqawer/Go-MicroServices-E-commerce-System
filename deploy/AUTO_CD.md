# RPC 自动部署

当前运行方式：Web 在 Mac 上启动，四个 RPC 在现有 AWS 主机上运行。RPC 仓库的 main 推送触发以下流程：

```text
push main
  → GitHub 托管 runner：构建入口、Go/Python 回归、独立 MySQL/Redis 集成
  → GitHub 托管 runner：构建四个 Linux 镜像并发布 GHCR digest 清单
  → AWS 部署 runner：下载本次清单，调用本机 Jenkins，等待最终结果
  → Jenkins：临时登录 GHCR、拉取整组镜像、更新四个 RPC、检查健康
```

任一检查/镜像失败，部署不会执行。Jenkins 失败会让 Actions 的 deploy job 失败。成功后，`runtime/state/images.env` 与 `runtime/state/rpc-<完整提交>.json` 记录实际发布版本。

## 为什么使用部署 runner

过去的部署通过 Mac 主动触发 Jenkins；GitHub 托管 runner 没有到服务器 SSH 的固定网络路径，服务器也缺少长期 GHCR 私有包读取凭据。

现有 AWS 上新增 `aws-grpctest-deploy` runner，注册到私有仓库 `qqawer/grpctest`，标签为 `self-hosted, Linux, X64, grpctest-deploy`。只把 deploy job 分配给它；测试和构建继续在 GitHub 上运行。runner 主动连接 GitHub，部署时访问 `http://127.0.0.1:8080` 的 Jenkins，不增加 AWS 入站规则。

runner 以 `actionsrunner` 用户运行，不加入 sudo 或 Docker 组。目录为 `/opt/grpctest/actions-runner`，systemd 服务为 `actions.runner.qqawer-grpctest.aws-grpctest-deploy.service`，随主机启动。Jenkins 沿用已有 Docker 权限；没有给 runner 直接控制容器的权限。

`deploy/scripts/install-deploy-runner.py` 安装固定初始版本 2.337.0，并校验 GitHub API 返回的 SHA256。安装时从标准输入读取一小时有效的注册 token；注册后由 runner 正常自动更新，避免长期固定旧版本无法接受任务。

## 凭据与开关

- Repository variable：`ENABLE_DEPLOY=true`。关闭它可以暂停后续自动发布。
- GitHub Environment：`aws`，允许 main 分支部署。
- Environment secret：`JENKINS_API_TOKEN`，沿用服务器现有 actions 用户的 API token。
- deploy job 权限：`contents: read`、`packages: read`。
- GHCR 使用本次 deploy job 的 `GITHUB_TOKEN`。通过 Jenkins 的 `GHCR_TOKEN` 密码参数传递，由 `release-with-registry.py` 使用标准输入登录 Docker；凭据写入临时 `DOCKER_CONFIG`，结束后删除。没有在 AWS 保存长期 GitHub PAT，也没有把 Mac SSH 私钥上传 GitHub。

`ci/trigger_jenkins.py` 的 `JENKINS_URL` 只允许回环 HTTP 地址；旧 SSH 隧道模式仍可用。HTTP 不跟随重定向，日志不输出认证值。Jenkins 原有 PRELOADED 手动发布方式保留。

已有 Jenkins 不会重新执行初始化脚本。升级时需要同步 `Jenkinsfile`、`scripts/release-with-registry.py`，并给已有任务补充 `GHCR_USER` 字符串参数和 `GHCR_TOKEN` 密码参数。更新前备份任务配置与 Pipeline，保留已有用户、权限、构建记录。

## 核对发布

1. 在仓库 Actions 中打开对应提交，确认 checks、四个 images、deploy 均成功。
2. 在 Jenkins `grpctest-deploy` 中确认同次构建为 SUCCESS。
3. 查看 AWS 上四个 RPC 容器的镜像 `org.opencontainers.image.revision` 标签，必须等于本次完整提交 SHA；确认 Docker healthy 与 Consul passing。
4. 重启 Mac 的 goods-web，通过 GET 商品验证读取。商品状态专用 RPC 随本次商品修复一起发布；部署探针不代替业务回归。

需要重新发布时可以对 main 执行 workflow_dispatch；它仍会先运行检查和构建，不跳过门禁。需要回退时，使用已有发布记录中的 revision/images 触发 Jenkins；数据库与 Nacos 不随镜像回退。

## 范围

本次只开启 RPC 仓库的自动部署。Web 仓库继续执行 CI、生成镜像，Web 服务仍由 Mac 本地运行。保留已有 AWS IP、端口、安全组规则、数据库卷和 Nacos 配置。

自动部署不解除订单的 `FailedPrecondition` 保护，库存事务、MQ 与支付仍按原计划后续实施。生产结构迁移也没有放进每次发布，不能用重新导入 SQL 代替迁移。

参考：[GitHub self-hosted runner](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/add-runners)、[runner systemd 服务](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/configure-the-application)、[GHCR 与 GITHUB_TOKEN](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry)。
