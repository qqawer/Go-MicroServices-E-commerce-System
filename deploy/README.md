# Ubuntu 24.04 / 26.04：Docker、GitHub Actions 与 Jenkins

本目录用于一台 Ubuntu 24.04 或 26.04 云服务器上的学习环境。基础设施与应用分别由两个 Compose 项目管理，更新应用不会重建数据库。

**还没有服务器时**：先按 [Terraform 自动创建服务器](terraform/README.md) 操作。该方式会创建 EC2，并在首次开机时自动执行本文第 1、2 节的安装与初始化步骤，无需再手动上传目录或重复生成密码。已有服务器则从本文第 1 节开始。

```text
两个仓库分别 push main
  → 构建生产入口并执行回归；RPC 额外验证独立 MySQL/Redis、迁移与 seed
  → GitHub Actions 编译各服务并构建 linux/amd64 镜像
  → 推送 GHCR，收集每个镜像的 sha256 digest
  → SSH 隧道访问服务器本机的 Jenkins
  → Jenkins 校验发布清单、基础设施健康和业务表是否存在
  → 拉取整组镜像 → 更新容器 → 等待启动健康检查
  → 成功：记录版本；失败：尝试恢复上一组容器
```

PR 执行检查和镜像构建，不推送、不部署。两仓先运行 `ci/check.sh` 的生产入口构建与 Go race 回归；RPC 再执行独立 MySQL/Redis 的迁移和集成验证，全部通过后才构建镜像。操作和边界见 [独立测试说明](../tests/README.md)。`/health` 和 gRPC health 是启动检查，不证明登录、库存扣减、下单和支付业务正确。

## 当前状态与限制

- 已提供 MySQL、Redis、Nacos、Consul、Jenkins，以及四个 RPC / 三个 Web 服务的部署配置。
- `grpctest_web` 的三个 `jwt_test.go` 已按要求删除且未恢复；流水线现在执行既有配置回归，完整 JWT 权限修复仍在下一批。
- 订单 RPC 已完成基础构建与启动接线修复；当前交易依赖没有启用，`CreateOrder` 返回 `FailedPrecondition`。库存幂等、事务消息、恢复任务与支付竞争仍按原计划 04–06 实施，不能把编译或探针成功当作交易链路完成。
- MySQL 首次启动只创建四个空库和业务账号。新增显式空库初始化脚本校验商品 SQL 并移除 DROP，再创建当前模型的业务表和示例库存；有数据时拒绝覆盖，见第 2 节。发布脚本仍只检查各库是否存在表，不代替版本化迁移。
- Nacos 脚本提供七份新的云端 dev 配置，不覆盖已有在线配置。短信与支付宝凭据留空；MQ 配置和消费者在交易阶段接入。库存正确性由后续数据库事务与约束保证，Redis 锁仅为可选的竞争控制，不作为本轮部署前提补入。
- 2026-09-17 已初始化 AWS 业务库，由 Jenkins #2 首次发布四个 RPC、#3 切换公网端口映射；Mac Web 的登录、商品、购物车已实测，见 [AWS 部署记录](DEPLOYED_AWS.md) 和 [验证记录](VALIDATION.md)。`ENABLE_DEPLOY` 仍关闭，服务器 GHCR 权限与 Actions 跨机自动部署尚未接通。

## 1. 准备服务器

建议先用 2 vCPU / 8 GiB 内存 / 60 GiB gp3 的单机学习环境；这是容量起点，不是性能承诺。加入 Jenkins 后需观察内存，订单消息队列等组件加入后再评估。构建业务镜像由 GitHub 托管 runner 完成，服务器只拉取镜像。

将本目录的部署源码复制到服务器 `/opt/grpctest/deploy`，排除 `.env`、`runtime/`、`.terraform/` 和任何私钥。这是最终路径：初始化后不要随意移动，因为 `.env` 中记录了 Docker bind mount 的绝对路径。Jenkins 使用相同路径挂载，保证容器内调用宿主机 Docker 时路径一致。

```bash
cd /opt/grpctest/deploy
sudo bash scripts/install-docker.sh
sudo python3 scripts/init-env.py
sudo bash scripts/stack.sh check
sudo bash scripts/stack.sh up
sudo bash scripts/stack.sh status
```

`init-env.py` 仅首次执行，生成 `.env`、随机密码、JWT 密钥与 Nacos bootstrap 文件；遇到已有配置拒绝覆盖。密钥保存在被 Git 忽略的 `runtime/` 和 `.env` 中。不要通过重新生成 `.env` 给已有数据库改密码：数据库卷中的账号不会随环境变量自动修改。

Docker 安装脚本使用官方 apt 源，遇到冲突软件包会退出，不自动卸载已有 Docker/数据库。数据库、Redis、Nacos、Consul、Jenkins 数据保存在独立命名卷；`stack.sh stop` 不删卷。不要使用 `docker compose down -v` 清理已有环境。

## 2. 配置 Nacos 和业务数据库

```bash
cd /opt/grpctest/deploy
sudo python3 scripts/configure-nacos.py
# 可先查看并修改 runtime/nacos 中的 JSON；其中包含凭据，不要提交到 Git。
sudo python3 scripts/configure-nacos.py --apply
```

脚本初始化 Nacos 管理员密码，创建四个命名空间，将七份配置写入 dev 分组并读回检查。重跑时只补缺失项；发现现有配置不同会退出，不覆盖在线配置。所有应用采用 `MXSHOP_DEBUG=true`，这里表示选择 dev 分组，并不等同于 Gin debug 模式；云端 JSON 的 Gin mode 是 release。

四个 RPC 保留 `MXSHOP_CONFIG_FILE`：Compose 将 `runtime/config/<服务>/config-debug.yaml` 只读挂载到 `/run/config/nacos.yaml`，并指定该路径。三个 Web 使用原有 debug/pro 选择方式，Compose 将配置直接挂载到 `/app/<服务>/config-debug.yaml`，与镜像工作目录及 Go 代码的相对路径一致；Web 不设置 `MXSHOP_CONFIG_FILE`。这些挂载文件使用 `nacos:8848` 和服务器生成的账号密码，镜像中不写入凭据。

RPC 容器使用镜像提供的 `grpc_health_probe`，检查 `127.0.0.1:50051`–`50054`；Web 容器使用 Alpine 的 `wget` 检查 `127.0.0.1:8021`–`8023` 的 `/health`。这些是容器内自检。Consul 从自己的容器发起检查，使用下表对应的 RPC 容器 DNS 名及端口；两者应指向同一服务，但不能混用各自的 `127.0.0.1`。

源码 debug/pro YAML 已按用户要求改为 Mac 直连 `98.83.120.128:8848`。Mac Web 默认 debug 选择已有的 local 分组，RPC 源码 debug 仍读取 dev；pro 分组仍需另行准备。云端容器继续读取挂载的 runtime 配置，通过 Docker 服务名访问中间件。Mac 的 local 配置已使用 AWS 公网 RPC/Redis 地址，详见 Web 仓库 LOCAL_DEVELOPMENT.md。

如果需要浏览器直接访问 Nacos，在服务器 `.env` 设置 `NACOS_HTTP_BIND_ADDRESS=0.0.0.0`，并在 AWS 安全组中仅向指定客户端公网 IPv4 `/32` 放行 TCP 8848，然后执行 `sudo docker compose --env-file .env -f compose.yaml --profile ci up -d --no-deps --wait nacos`。Mac 直连 Nacos Go SDK v2 还需在 `.env` 设置 `NACOS_GRPC_BIND_ADDRESS=0.0.0.0`，并仅向客户端 `/32` 放行 TCP 9848；SDK 根据 YAML 中的 8848 自动加 1000 连接该端口。历史实例的设置记录见 `DEPLOYED_AWS.md`；仓库默认仍将 8848/9848 绑定回环地址，本轮未更改线上设置。RPC/Redis 的可选公网映射见第 3 节；云端 Go 容器继续通过 Docker 服务名连接。

| 配置 | 云端地址 |
| --- | --- |
| 应用 bootstrap 中的 Nacos | `nacos:8848` |
| RPC 的 MySQL | `mysql:3306`，账号 `grpctest` |
| Redis | `redis:6379`，使用生成的密码 |
| Consul | `consul:8500` |
| RPC 注册地址 | `user-srv:50051`、`goods-srv:50052`、`inventory-srv:50053`、`order-srv:50054` |
| Consul 服务名 | `user-srv`、`goods-srv`、`inventory-srv`、`orders-srv` |

本地数据库插件需要公网直连 MySQL 时，在服务器 `.env` 设置 `MYSQL_BIND_ADDRESS=0.0.0.0`，并在 AWS 安全组仅向客户端公网 IPv4 `/32` 放行 TCP 3306。执行 `sudo docker compose --env-file .env -f compose.yaml up -d --no-deps --pull never --wait mysql` 应用端口映射，保留原 `mysql-data` 卷。插件连接 EC2 公网 IP、3306，使用 `grpctest` 及现有 MySQL 应用密码；云端 RPC 仍连接 `mysql:3306`，Nacos 配置无需修改。

注册地址是容器 DNS 名，服务名是发现时查询的标识，两者可以不同。新的云端配置统一使用 `inventory-srv`，不要混用旧导出中的 `inevntory-srv`。应用的 Nacos SDK 已接入 bootstrap 中的用户名和密码。

四个业务库分别为 `grpctest_user_srv`、`grpctest_goods_srv`、`grpctest_inventory_srv`、`grpctest_orders_srv`。部署前需准备表结构与业务数据，并完成订单交易与恢复验证；目前不启用应用自动部署。不要直接执行旧 `model/main` 工具，它们仍有写死的数据库地址和凭据。

### 空库导入示例数据

商品 SQL 源于本仓库 `goods_srv/grpctest_goods_srv.sql`，先上传到服务器临时路径，再执行：

```bash
cd /opt/grpctest/deploy
sudo python3 scripts/bootstrap-demo-db.py --goods-sql /tmp/grpctest_goods_srv.sql
sudo python3 scripts/bootstrap-demo-db.py --goods-sql /tmp/grpctest_goods_srv.sql --apply
sudo python3 scripts/configure-local-web.py --host 98.83.120.128
```

第一条只做预检。导入仅接受校验过的原始 SQL，剔除五条 DROP；配合 mysql/02-business-schema.sql 创建当前 RPC 模型所需表并设置 420 件商品各 100 库存。完成标记在 runtime/state/demo-db-bootstrap.json。再次执行保持原数据；没有标记的非空库拒绝导入。中途失败需检查已创建的表，不自动清空重试。不要编辑已应用的基线来充当未来迁移。

configure-local-web.py 从 dev 生成三个 Web 的 local 分组，RPC target 和 Redis host 使用 --host 指定的 AWS 公网 IPv4。脚本允许更新自己上次保存的版本（含此前的隧道配置），在线配置若被独立修改则拒绝覆盖。更新前备份为 runtime/nacos-local/*.previous，逐项读回验证，dev 保持原值。

## 3. 初始化 Jenkins 与 GHCR 拉取权限

Jenkins 首次启动会自动创建 `grpctest-deploy` Pipeline：

- 管理员：`admin`；密码在 `runtime/secrets/jenkins_admin_password`。
- Actions 服务用户：`actions`，只有读取/构建/取消任务权限；自动生成的 API token 在 `runtime/secrets/jenkins_actions_token`。
- Web 入口只绑定 `127.0.0.1:8080`，通过 SSH 隧道管理。初始化脚本不会重置已有 Jenkins 账号或权限。

在 Mac 上按实际主机替换占位符，任选本机空闲端口，避免与已有 Nacos 冲突：

```bash
ssh -N -L 18080:127.0.0.1:8080 -L 18848:127.0.0.1:8848 -L 18500:127.0.0.1:8500 ubuntu@YOUR_EC2_HOST
```

浏览器访问 `http://localhost:18080` 管理 Jenkins，`http://localhost:18848/nacos` 管理 Nacos，`http://localhost:18500` 管理 Consul。

如果 GHCR 镜像是私有包，先在 Jenkins 容器中登录一次，输入具有对应包 `read:packages` 权限的 GitHub PAT：

```bash
cd /opt/grpctest/deploy
sudo docker compose --profile ci exec jenkins docker login ghcr.io -u qqawer
```

登录配置会保存在 Jenkins 数据卷中。不要使用 Actions 临时的 `GITHUB_TOKEN` 作为服务器长期凭据，也不要把 PAT 写进 Compose 或仓库。

本方案为了单机学习方便，Jenkins 用 root 访问 Docker socket，因此部署任务拥有这台主机的控制能力。仅用于本人可信仓库；不要开放给不可信 PR 或其他用户运行任意任务。正式环境应拆分控制器与受限部署节点。Consul/Jenkins 只发布到宿主机回环地址。MySQL/RPC/Redis 默认绑定回环地址；本机需要直连时，在服务器 .env 将对应 MYSQL_BIND_ADDRESS、RPC_BIND_ADDRESS、REDIS_BIND_ADDRESS 设为 0.0.0.0，并在安全组仅对 Mac 公网 IPv4 /32 放行对应 TCP 3306、50051–50054、6379。更新映射需要重建对应容器，数据卷保留。Nacos HTTP 可按上文单独启用受来源 IP 限制的公网访问；Consul 暂不配置 ACL，依赖隔离的内部网络。

### 无私有 GHCR 拉取凭据时的手动发布

Jenkins 参数 PRELOADED 默认为 false。已有 Jenkins 任务不会自动重新 bootstrap，升级时需同步 Pipeline 定义并添加 Boolean 参数 PRELOADED（默认 false），保留现有账号权限。

人工发布可从所填提交的干净源码构建 Linux amd64 镜像，附加以下标签后 docker save，经 SSH 传输并 docker load 到 AWS：

- org.opencontainers.image.source=https://github.com/qqawer/grpctest
- org.opencontainers.image.revision=完整 40 位源码提交
- io.grpctest.service=user_srv / goods_srv / inventory_srv / order_srv

在 Jenkins 选择 GROUP=rpc、填 REVISION、设置 PRELOADED=true，IMAGES_JSON 映射四个服务到 docker image inspect 得到的 `sha256:<64位 image ID>`。发布器检查镜像身份及三项标签，再检查依赖、更新容器并等待健康；这一路径不请求 GHCR。它依赖可信构建与 SSH 导入，标签不替代签名认证。正常 CI 仍提交 GHCR repo@digest，PRELOADED=false。

## 4. 配置两个 GitHub 仓库

两个仓库都已经有根目录 `Dockerfile` 和 `.github/workflows/release.yml`。旧的 `grpctest_web/user-web/Dockerfile` 与该目录下 Compose 不参与新流程。使用仓库根目录作为 Docker build context，镜像不复制配置文件、SQL、私钥或 `.env`。

分别为 `qqawer/grpctest` 和 `qqawer/grpctest_web` 创建 GitHub Environment `aws`，添加以下 Secrets：

| Secret | 值 |
| --- | --- |
| `DEPLOY_HOST` | EC2 的可达域名或 IPv4 |
| `DEPLOY_USER` | SSH 用户，例如 `ubuntu` |
| `DEPLOY_SSH_KEY` | 对应 SSH 私钥的完整多行内容 |
| `DEPLOY_KNOWN_HOSTS` | 已核验指纹的 SSH known_hosts 行，主机名须匹配 DEPLOY_HOST |
| `JENKINS_API_TOKEN` | 服务器 `runtime/secrets/jenkins_actions_token` 的内容 |

通过可信 EC2 控制台或已验证的连接核验 SSH 主机指纹；流水线不会禁用主机校验，也不会在部署时盲目信任 `ssh-keyscan` 的结果。

SSH 的安全组必须允许 Actions runner 到达服务器；只允许 Mac 的 IP 时，托管 runner 会连接失败。可使用具备固定出口/私网连接的 runner，并相应修改 workflow 的 `runs-on`。不要为 Jenkins 开公网 8080，也不要公开数据库端口。

仓库 Actions Variable `ENABLE_DEPLOY` 默认为未设置，此时仅构建/发布镜像。本轮保持关闭；以后服务器、业务配置、数据库与交易验证全部就绪，才在两个仓库的 **Repository variables** 中设为 `true`，才会执行部署 Job（不能只放在 Environment variable 中，因为 Job 的 if 在进入 environment 前判断）。

首次发布 GHCR 包时确认仓库 Actions 的 `GITHUB_TOKEN` 有 `packages:write` 权限；已存在的包还需给对应仓库 Actions 授权。workflow 已声明写包所需权限。环境 Secrets/分支保护按账号实际功能配置。

提交并推送这些文件后：PR 做构建检查；main 构建推送成功且 `ENABLE_DEPLOY=true` 后才能触发 Jenkins。任何一个服务构建失败，整组不部署。Actions 会一直等到 Jenkins 返回最终结果，Jenkins 失败不会被当成成功触发。

## 5. 查看版本、回退与维护

`runtime/state/images.env` 记录当前健康发布的镜像 digest，`runtime/state/<rpc|web>-<commit>.json` 记录成功发布。两个仓库更新各自的服务，Jenkins 串行执行，脚本还有跨进程文件锁。

需要主动回退时，将旧 JSON 中的 `images` 对象、对应 `revision` 和 `group` 填到 Jenkins 任务参数后构建。常规版本需保留 GHCR 历史镜像；预导入版本需保留宿主机 image ID，并设置 PRELOADED=true。回退不会回退数据库结构、数据或 Nacos 配置。

启动健康检查失败时，脚本尝试恢复以前存在的服务；首次部署没有旧容器时，只移除这次新建的应用容器。恢复失败会保持任务失败，需要检查日志。主机宕机、强制杀进程或 Jenkins 超时中止可能打断恢复，需要人工核对；单机 Compose 更新也会有短暂中断。

查看基础设施日志：`sudo bash scripts/stack.sh logs nacos`；停止基础设施：`sudo bash scripts/stack.sh stop`。如需查看应用日志，使用 `.env` 和 `runtime/state/images.env` 两份 env 文件配合 `compose.apps.yaml`。日志和配置可能含业务敏感信息，不要公开粘贴。

首次 Jenkins bootstrap 只执行一次，重建镜像不会覆盖已建的任务；更新 Jenkinsfile 时需同步更新任务的 Pipeline 定义。业务发布脚本从部署目录读取，变更后需同步服务器文件。固定的镜像 digest、Actions SHA 与插件版本需定期有计划地更新。

参考：[Docker Ubuntu 安装](https://docs.docker.com/engine/install/ubuntu/)、[GitHub 镜像发布](https://docs.github.com/en/actions/tutorials/publish-packages/publish-docker-images)、[Jenkins Docker](https://www.jenkins.io/doc/book/installing/docker/)、[Jenkins Remote API](https://www.jenkins.io/doc/book/using/remote-access-api/)、[Nacos 2.5 鉴权](https://www.nacos.io/docs/v2.5/manual/admin/auth/)。
