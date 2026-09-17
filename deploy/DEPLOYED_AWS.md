# AWS 部署记录 · 2026-09-16（2026-09-17 地址与 Nacos 复核）

以下云端数据是 2026-09-16 部署、2026-09-17 地址与 Nacos 复核时的历史快照，不是本轮提交重新探测的结果。记录时五个基础组件通过健康检查，业务服务尚未上线，完整 CI/CD 尚未接通。本轮只修复本地 Compose 与代码的匹配关系，没有同步服务器或启用自动部署。

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

服务器 `.env` 设置 `NACOS_HTTP_BIND_ADDRESS=0.0.0.0` 和 `NACOS_GRPC_BIND_ADDRESS=0.0.0.0`，仅重建 Nacos 容器，命名卷保留。TCP 9848 新增来源 `137.132.26.103/32`（规则 `sgr-0a677fd979f814c51`），供 Mac 的 Nacos Go SDK v2 使用；原有两条 8848 规则均保留。其他组件仍绑定 `127.0.0.1`。以下管理页面也可通过原有 SSH 隧道访问：

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
  -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes ubuntu@98.83.120.128
```

首次连接的主机密钥采用 SSH 首次信任记录在本机 known_hosts 中；此后连接强制匹配。2026-09-17 已通过旧主机密钥严格校验新公网 IP，确认密钥一致，实测私网 IP 为 `172.31.40.8`。尚未通过 AWS 控制台独立核验指纹。

Mac 睡眠、网络变化或 EC2 更换公网 IP 可能中断隧道。以下命令仅用于检查或关闭之前以旧 IP 建立的隧道（socket 文件名保留旧 IP）：

```bash
ssh -S /Users/outsider/.ssh/grpctest-aws-98.93.115.75.ctl -O check ubuntu@98.93.115.75
ssh -S /Users/outsider/.ssh/grpctest-aws-98.93.115.75.ctl -O exit ubuntu@98.93.115.75
```

确认旧隧道已关闭后，在 Mac 终端重新连接；此命令会占用当前终端，按 Ctrl-C 关闭隧道：

```bash
ssh -i /Users/outsider/Downloads/Golang_Development/grpctest.pem \
  -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes \
  -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 -o ServerAliveCountMax=3 \
  -N -L 127.0.0.1:18080:127.0.0.1:8080 \
  -L 127.0.0.1:18848:127.0.0.1:8848 \
  -L 127.0.0.1:18500:127.0.0.1:8500 ubuntu@98.83.120.128
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

当前源码已有变化：四个 RPC 保留该覆盖路径；三个 Web 已删除覆盖逻辑，本地 Compose 改为只读挂载 `/app/<服务>/config-debug.yaml`。RPC 探针改用 `grpc_health_probe`，Web 改用 `wget /health`。这些文件尚未同步线上，不能把历史配置核对当作新版本部署验收。

同时修正库存 `config-pro.yaml` 误用商品 Data ID，以及订单初始化器引用库存 global 和库存配置文件路径的问题。当时订单其他编译问题尚待处理；当前本地已完成基础构建与启动修补，交易流程仍未启用。源码 debug/pro YAML 已改为公网直连 Nacos，本机用 `MXSHOP_DEBUG=true` 读取 dev；此前额外的 `config-aws.local.yaml` 和双端口本机隧道已移除；`model/main` 和 `consul_test` 下的旧独立示例未用于本次部署，不能当成云端初始化脚本直接执行。

服务器查看状态：

```bash
cd /opt/grpctest/deploy
sudo bash scripts/stack.sh status
sudo df -h /
sudo docker stats --no-stream
```

安装日志：`/var/log/grpctest-install.log`；首次启动日志：`/var/log/grpctest-stack.log`。停止容器时不要使用带 `-v` 的 down 命令，否则会删除数据卷。

## 业务上线前仍需完成

1. 将本地基础修复与配置挂载变更在隔离环境完成发布验收；订单当前 `CreateOrder` 返回 `FailedPrecondition`，不能作为可交易服务上线。
2. 初始化四个业务库的表结构及必要数据；历史检查时表数为 0，本轮未重新读取。旧商品 SQL 含 DROP TABLE，未自动执行。
3. 按原计划 04–06 完成库存数据库事务/幂等、订单 MQ/恢复与支付竞争处理，并补齐实际使用的配置；Redis 锁仅为可选的竞争控制。
4. 后续配置 GitHub Secrets、GHCR 拉取权限和可达服务器的 runner；完成交易与部署验收后再考虑开启 `ENABLE_DEPLOY`。本轮提交不配置这些远端项目。
5. 首次发布业务容器，验证实际业务流程与失败恢复；健康探针通过不代表交易流程正确。

Jenkins 通过 Docker socket 拥有主机控制能力，限本人可信任务使用。当前为单机学习环境；没有配置异机备份或高可用。
