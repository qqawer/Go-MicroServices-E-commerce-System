# AWS 部署记录 · 2026-09-17 RPC 首次发布与 Mac 联调

## 2026-09-29 实例降配与公网地址更新

原实例 `i-06a10bd902160fade` 已从 `t2.large` 切换为 `t3.medium`（2 vCPU、4 GiB），保留 CPU 积分 `standard` 模式。仍使用原 20 GiB gp3 卷和全部 Docker 数据卷；本次未扩容磁盘。停止后的磁盘快照为 `snap-0f19c01097e2e3844`，本机另有私密数据库与配置备份。

当前公网地址为 **`54.162.202.24`**，私网仍是 `172.31.40.8`。实例没有弹性 IP，停机后再启动仍可能换地址；`98.83.120.128`、`54.237.231.158` 均已不用于本项目当前连接。源码 debug/pro YAML、三个 Web 的 Nacos local RPC/Redis 地址、order-web 的 local/dev 支付回调已同步更新，密钥和业务数据保留。原安全组未修改，Mac 的 `42.61.2.190/32` 和 `137.132.26.227/32` 来源规则仍在。

Nacos 当前入口：<http://54.162.202.24:8848/nacos/>。Consul 和 Jenkins 仍只绑定服务器回环，管理时使用下方 SSH 隧道。以下旧日期章节中的资源、地址和状态为历史快照。

## 2026-09-24 公网通知连通与沙箱启用

用户在 AWS 增加 HTTP/TCP 80 来源 `0.0.0.0/0` 后，已从 Mac 直连验证入口可达。Nacos `orders-srv.json / dev`、`order-web.json / local,dev` 的 `alipay.enabled` 已改为 true，订单 RPC `rocket.mode=alipay`；配置私有备份 `runtime/state/before-enable-alipay-20260924T062825Z`。仅重启云端 order-srv/order-web，实际代码镜像未变；七个应用健康、Consul RPC passing。公网无签名通知返回400 failure，普通路径404、回调GET405；支付表、订单表、库存操作表均仍0条，没有创建或支付测试订单。本机order-web需要使用者重启；下一步创建新订单并完成真实沙箱付款/成功通知验收。

以下为同日启用前的部署过程记录。

## 2026-09-24 支付代码与云端 Web 发布

- RPC `fc64b6a`、Web `fca45be` 已经 Actions → Jenkins 发布；[RPC CI/CD](https://github.com/qqawer/grpctest/actions/runs/35944645337)、[Web CI/CD](https://github.com/qqawer/grpctest_web/actions/runs/35944731730) 成功，最终 Web Jenkins #13 SUCCESS。实际四 RPC/三 Web 的镜像 revision 已核对，七个应用容器 healthy；四 RPC 在 Consul passing，Web 仅使用服务发现、不向 Consul 注册。
- 新增独立 Web 部署 runner、仓库加密 Jenkins Secret 和 main-only `aws` 环境，Web 的 `ENABLE_DEPLOY=true`。构建仍在托管 runner，服务器 runner 只负责触发 Jenkins；临时 GHCR 配置已清理。
- 0005 支付迁移已应用，版本 1–5 dirty=0；备份 `runtime/state/before-migration-20260924T014921Z.sql`。原有库存、订单、库存操作指纹不变，支付表仍空，没有创建渠道交易。
- 沙箱凭据已配置；AWS 使用当前应用发起只读查单，得到预期“订单不存在”。此检查不代替真实成功付款、通知验签或商户金额核验。
- Nginx 绑定公网 80，仅代理 POST `/g/v1/pay/alipay/notify` 到 order-web；其他路径404、GET405、超限413。三个 Web 的宿主机8021–8023均仅绑定127.0.0.1。服务器内通知POST经代理返回预期503（支付关闭）；Mac公网80仍超时，AWS控制台未登录，尚未改安全组 `sg-01a6adef2222761c1`。
- Nacos local/dev 的 notice_url 已填 `http://98.83.120.128/g/v1/pay/alipay/notify`，return_url 留空；两端 enabled:false、rocket.mode=demo。公网连通后再启用并测试新订单。具体操作见 [通知入口说明](PAYMENT_CALLBACK.md)。

## 2026-09-21 自动部署配置更新

现有 AWS 新增仅用于 RPC 发布的 GitHub runner，Jenkins 沿用原任务和权限，增加临时 GHCR 认证。配置与核对方式见 [AUTO_CD.md](AUTO_CD.md)。四个 RPC 发布版本以 Actions 对应运行、Jenkins 构建结果和 `runtime/state/rpc-<提交>.json` 为准。Web 继续在 Mac 运行；以下为 9 月 17–19 日的历史部署快照。

2026-09-17 已通过 SSH 连接服务器，初始化业务数据，并由 Jenkins `grpctest-deploy #2` 成功发布四个 RPC。当前五个基础组件、四个 RPC 容器均正常；Consul 的四项 gRPC 检查均 passing。随后按用户要求改为 Mac 公网直连：Jenkins #3 应用 RPC 端口映射并成功，Redis 映射重建后健康；关闭开发隧道后真实接口复验通过。完整下单和自动 Actions 部署仍未完成。下面资源容量和旧安全组规则保留各自记录时的快照。

## 2026-09-19 MySQL 端口映射更新

按用户要求，Compose 增加 `MYSQL_BIND_ADDRESS`（仓库默认 `127.0.0.1`），服务器 `.env` 设置为 `0.0.0.0`，仅重建 MySQL 容器。实际映射已为 `0.0.0.0:3306`，容器 healthy；镜像、原 `grpctest-infra_mysql-data` 卷和全部 11 张业务表行数保持不变。通过服务器私网地址使用 `grpctest` 账号执行 `SELECT 1` 成功，其他容器未重建，四个 RPC 的 Consul 检查均 passing。旧配置及前后验证结果保存在 `runtime/backups/mysql-direct-20260919T063725Z/`。

端口映射切换时未修改 AWS 安全组或 Nacos 配置，Mac 公网出口为 `42.61.2.190`，首次直连公网 3306 超时。随后从同一 Mac 使用 `grpctest` 账号直连 `98.83.120.128:3306` 成功，并确认四个业务库可访问；管理员凭据更新后，也已验证 `root` 公网登录，容器配置同步且业务账号未变。实际密码仅保存在服务器和本地私密文件中，不进入 Git。Consul 仍仅绑定服务器 `127.0.0.1:8500`，其 `/ui/` 在服务器内返回 HTTP 200。

## 本次已上线的内容

- RPC 镜像对应提交 `e74d2e484e9328beed7886a1662acfc0f529551c`，Linux amd64；Go 业务源码未额外修改。部署脚本和 Compose 同步到 `/opt/grpctest/deploy`。
- GitHub Actions 已发布 GHCR 镜像，但现有本机凭据缺少私有包读取权限，服务器也未登录 GHCR。本次使用本机从同提交构建的镜像，经 SSH 导入；Jenkins 的 `PRELOADED=true` 校验不可变 image ID、仓库、提交和服务标签后启动容器。正常 CI digest 发布路径保持不变。
- Jenkins #1 因 PRELOADED 参数未正确注册而在镜像校验前失败，没有启动容器；补齐现有任务的 Boolean 参数后，#2 返回 SUCCESS。旧任务定义和部署文件已备份到 `runtime/backups/`。
- `runtime/state/images.env` 和 `runtime/state/rpc-e74d2e484e9328beed7886a1662acfc0f529551c.json` 保存此次发布状态。没有部署云端 Web，没有开启 `ENABLE_DEPLOY`。
- AWS MySQL 从空业务库初始化 11 张表。商品 SQL 校验固定 SHA256，剔除 5 条 DROP；导入 420 件商品、113 个品牌、369 个分类、1801 条分类品牌关系，并为每件商品建立初始库存 100。banner 原始数据为空。
- 创建了一个普通演示账号，密码用现有 RPC 哈希逻辑写入。留下商品 421、数量 1 的购物车；没有创建订单。账号、token 仅保存于 Mac Web 仓库被忽略的 `.local/`，不进入此记录。
- Nacos 新增三个 Web 的 **local** 分组，dev 逐项验证未变。Mac 的 RPC target 和 Redis host 现已改为 98.83.120.128 的真实服务端口；云端仍用 Docker DNS。启动方法见配套 Web 仓库 `LOCAL_DEVELOPMENT.md`。

| RPC | 容器与 Consul 检查地址 | Consul 服务名 | Mac 公网直连 |
| --- | --- | --- | --- |
| 用户 | `user-srv:50051` | `user-srv` | `98.83.120.128:50051` |
| 商品 | `goods-srv:50052` | `goods-srv` | `98.83.120.128:50052` |
| 库存 | `inventory-srv:50053` | `inventory-srv` | `98.83.120.128:50053` |
| 订单 | `order-srv:50054` | `orders-srv` | `98.83.120.128:50054` |

服务器 .env 新增 RPC_BIND_ADDRESS=0.0.0.0、REDIS_BIND_ADDRESS=0.0.0.0；四个 RPC 及 Redis 对外映射端口。新增安全组规则：`sgr-0717fb2ae797a9548` 放行 TCP 50051–50054，`sgr-07ba3579096e3aefd` 放行 TCP 6379，来源均为 `137.132.26.103/32`。保存后界面显示 7 条入站规则，原 5 条全部保留。Consul 检查从容器网络访问上表地址，标准容器探针则检查自身回环地址；两者均已实测成功。

本次端口切换前已备份 .env、Compose、配置生成脚本和 local JSON：`runtime/backups/direct-access-20260917T065150Z`。端口切换没有重新导入 SQL；数据卷保留。

## 服务器与资源

| 项目 | 实测 |
| --- | --- |
| 公网 IPv4 / SSH 用户 | `98.83.120.128` / `ubuntu` |
| 私网 IPv4 | `172.31.40.8` |
| 系统 | Ubuntu 26.04 LTS，x86_64 |
| 实例 | t2.large，2 vCPU，约 7.7 GiB 内存 |
| 磁盘 | 20 GiB 块设备，根分区约 19 GiB |
| 部署后磁盘占用 | 6.9 GiB 已用，12 GiB 可用，38% |
| 部署后空闲内存 | 约 2.2 GiB 已用，5.5 GiB available |
| Docker / Compose | 29.8.1 / v5.5.1 |
| 服务器部署目录 | `/opt/grpctest/deploy` |

20 GiB 暂时容纳当前基础组件。业务镜像、数据库和 Jenkins 构建记录增长后需要重新评估；本次未扩容。初次资源检查未读取 AWS 安全组、EBS 类型或账单；后续 Nacos 来源规则复核见下文。

这是用户手动创建的实例，未纳入本目录 Terraform state。不要执行 Terraform apply 来“接管”它；当前模板会尝试创建另一套资源。

## 管理页面

Nacos 的 HTTP 端口已按用户要求改为公网直连：[http://98.83.120.128:8848/nacos/](http://98.83.120.128:8848/nacos/)。AWS 安全组 `sg-01a6adef2222761c1`（launch-wizard）的 TCP 8848 当前允许两个来源：原有 `42.61.2.190/32`（规则 `sgr-0c1eef2046c19176d`）保持不变；2026-09-17 新增当前 Mac 出口 `137.132.26.103/32`（规则 `sgr-0b8c99a0962db2e85`）。新增后从 Mac 直连控制台返回 HTTP 200。如果 Mac 换网络导致公网 IP 改变，需为新来源添加规则；保留旧规则需遵循用户要求。如果 EC2 公网 IP 改变，需更新浏览器访问地址。

服务器 `.env` 设置 `NACOS_HTTP_BIND_ADDRESS=0.0.0.0` 和 `NACOS_GRPC_BIND_ADDRESS=0.0.0.0`，仅重建 Nacos 容器，命名卷保留。TCP 9848 新增来源 `137.132.26.103/32`（规则 `sgr-0a677fd979f814c51`），供 Mac 的 Nacos Go SDK v2 使用；原有两条 8848 规则均保留。上述是 Nacos 变更记录；后续 RPC/Redis 对外映射见本文件开头。Consul、Jenkins 仍绑定回环地址；MySQL 已于 2026-09-19 更新绑定，见上方记录。开发隧道已关闭，以下管理页面可按需通过单独的管理隧道访问：

| 组件 | Mac 浏览器入口 | 账号 |
| --- | --- | --- |
| Jenkins | <http://127.0.0.1:18080/login> | `admin` |
| Nacos | <http://127.0.0.1:18848/nacos/> | `nacos` |
| Consul | <http://127.0.0.1:18500/ui/> | 当前无 ACL，依赖内部网络与 SSH 隔离 |

管理员密码仅保存在本机私密文件 `/Users/outsider/Downloads/grpctest-aws-98.93.115.75-access.txt`，权限为 `0600`；不在本记录展示。不要提交该文件到 Git。

服务器 `.env` 与 `runtime/secrets/` 保存运行凭据。Actions API token 位于 `/opt/grpctest/deploy/runtime/secrets/jenkins_actions_token`，尚未配置到 GitHub。

Mac 上的 SSH 私钥为 `/Users/outsider/Downloads/Golang_Development/grpctest.pem`，权限已设为 `0600`。这是私钥，不是可执行命令；使用方式：

```bash
ssh -i /Users/outsider/Downloads/Golang_Development/grpctest.pem \
  -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes ubuntu@54.162.202.24
```

首次连接的主机密钥采用 SSH 首次信任记录在本机 known_hosts 中；此后连接强制匹配。2026-09-17 已通过旧主机密钥严格校验新公网 IP，确认密钥一致，实测私网 IP 为 `172.31.40.8`。尚未通过 AWS 控制台独立核验指纹。

Mac 睡眠、网络变化或 EC2 更换公网 IP 可能中断隧道。以下命令仅用于检查或关闭之前以旧 IP 建立的隧道（socket 文件名保留旧 IP）：

```bash
ssh -S /Users/outsider/.ssh/grpctest-aws-98.93.115.75.ctl -O check ubuntu@98.93.115.75
ssh -S /Users/outsider/.ssh/grpctest-aws-98.93.115.75.ctl -O exit ubuntu@98.93.115.75
```

需要 Jenkins/Nacos 管理隧道时，在 Mac 终端另行连接；此命令会占用当前终端，按 Ctrl-C 关闭隧道。该管理隧道与 Web 启动无关：

```bash
ssh -i /Users/outsider/Downloads/Golang_Development/grpctest.pem \
  -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes \
  -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 -o ServerAliveCountMax=3 \
  -N -L 127.0.0.1:18080:127.0.0.1:8080 \
  -L 127.0.0.1:18848:127.0.0.1:8848 \
  -L 127.0.0.1:18500:127.0.0.1:8500 ubuntu@54.162.202.24
```

## 已完成

- MySQL、Redis、Nacos、Consul、Jenkins 全部 healthy；设置命名数据卷、容器自动重启与 Docker 日志轮转。
- Redis 验证密码访问与匿名拒绝；MySQL 业务账号可访问四个业务库。
- Nacos 已启用鉴权；四个命名空间和七份 dev 配置已导入并逐项读回核对。内部地址使用 `mysql`、`redis`、`consul` 等 Docker 服务名。
- Jenkins 已创建管理员、受限的 actions 用户与 `grpctest-deploy` Pipeline；API token 和 Docker 调用验证成功，匿名 API 被拒绝。
- 本机三个管理页面经隧道返回 HTTP 200；原有本机 MySQL / Nacos 数据未修改。

## Nacos 配置与代码入口

云端配置已再次通过接口读取并比对。控制台登录后选择以下命名空间和 `dev` 分组；默认的 `public` 命名空间为空是正常的。

| 命名空间 | Namespace ID（YAML 中填写的值） | Data ID |
| --- | --- | --- |
| grpctest-user | `b8cbecc7-537f-4507-b2d2-3e18dbd787c1` | `user-srv.json`、`user-web.json` |
| grpctest-goods | `80f74565-a2d2-4cb1-9820-cebc8d97f58e` | `goods-srv.json`、`goods-web.json` |
| grpctest-inventory | `e2f59a3b-5bcc-470b-ad61-8b2f0264525b` | `inventory-srv.json` |
| grpctest-order | `ce4b3f78-1db4-4418-9cb9-a3e1bd406c88` | `orders-srv.json`、`order-web.json` |

2026-09-17 已从在线 API 逐项读回：上述四个命名空间、七份 `dev` 配置和服务器 bootstrap 均匹配；七份 `pro` 配置查询均返回 404。源码的 `config-pro.yaml` 面向另行准备的 pro 分组，不可直接拿来读取当前云端 dev 配置。服务器 runtime 下虽同时生成 debug/pro 两种文件名，当前两者均指向 dev；Compose 显式挂载 debug 文件。

三个 Web JSON 的 `auth.key` 均非空且相同，`jwt_expire` 均为 24。JWT 密钥来自 Nacos 业务 JSON，bootstrap YAML 中的 password 是 Nacos 登录密码，两者用途不同。

云端应用连接 Nacos 使用 `nacos:8848`，连接 MySQL / Redis / Consul 分别使用 `mysql:3306` / `redis:6379` / `consul:8500`。RPC 注册地址分别为 `user-srv`、`goods-srv`、`inventory-srv`、`order-srv`，订单的服务发现名称为 `orders-srv`。这些地址通过 Docker 网络解析，不需要填 EC2 公网 IP。

历史配置核对时，七个入口都支持 `MXSHOP_CONFIG_FILE`，并曾使用 `/run/config/nacos.yaml` 挂载。用户当时修改的 Nacos 账户密码已同步到源码 YAML、服务器 `.env` 和 14 份 runtime bootstrap；记录时未启动业务镜像。

当前源码已有变化：四个 RPC 保留该覆盖路径；三个 Web 已删除覆盖逻辑，本地 Compose 改为只读挂载 `/app/<服务>/config-debug.yaml`。RPC 探针改用 `grpc_health_probe`，Web 改用 `wget /health`。这些文件现已同步线上；四个 RPC 已通过实际发布验收。Web Compose 的挂载/探针已核对，但云端 Web 未启动。

同时修正库存 `config-pro.yaml` 误用商品 Data ID，以及订单初始化器引用库存 global 和库存配置文件路径的问题。当时订单其他编译问题尚待处理；当前本地已完成基础构建与启动修补，交易流程仍未启用。源码 debug/pro YAML 已改为公网直连 Nacos，本机 Web 默认 debug 读取 local，RPC 源码的 debug 仍读取 dev；此前额外的 `config-aws.local.yaml` 和双端口本机隧道已移除；`model/main` 和 `consul_test` 下的旧独立示例未用于本次部署，不能当成云端初始化脚本直接执行。

服务器查看状态：

```bash
cd /opt/grpctest/deploy
sudo bash scripts/stack.sh status
sudo df -h /
sudo docker stats --no-stream
```

安装日志：`/var/log/grpctest-install.log`；首次启动日志：`/var/log/grpctest-stack.log`。停止容器时不要使用带 `-v` 的 down 命令，否则会删除数据卷。

## 仍需完成

1. 按原计划 04–06 完成库存事务/幂等、订单 MQ/恢复与支付竞争处理。当前 CreateOrder 在写库、扣库存前返回 FailedPrecondition，不能作为可交易服务上线。
2. 本次空库 bootstrap 是当前模型的显式基线；未来结构演进要新增迁移，不能改已导入的 SQL 后重灌现有数据。隔离环境、迁移版本管理和完整 seed 仍按批次 01 验收。
3. 配置 GitHub Secrets、私有 GHCR 读取权限和 runner 网络，才可开启自动部署；本次 Jenkins 手动发布不等于 Actions→Jenkins 已接通。
4. 认证、短信、支付、完整业务回归与真实容器更新/回退仍待对应批次验收。Mac 已通过的登录/商品/购物车不代替交易与恢复测试。

Jenkins 通过 Docker socket 拥有主机控制能力，限本人可信任务使用。当前为单机学习环境；没有配置异机备份或高可用。
