# 验证记录

本文件保留不同时间的验证结果；旧代码的检查结论不自动适用于本次提交。当前本地修复已使订单 RPC 恢复构建，交易依赖仍未启用，`CreateOrder` 返回 `FailedPrecondition`。2026-09-17 最新一轮已连接 AWS 并完成首次 RPC 发布及 Mac Web 联调，见下方记录；较早的离线验证另行保留。

## 2026-09-20 独立测试环境、迁移与 CI 回归

- 两仓 `bash ci/check.sh` 通过：4 个 RPC / 3 个 Web 生产入口 readonly 构建，既有订单与 Web 配置回归启用 race。
- `python3 tests/integration/run.py` 完整通过：独立 MySQL 8.0/Redis 7.4；11 表基线、seed 事务失败回滚、重复执行保留改动数据、迁移/seed 摘要与未知版本保护、并发迁移只执行一次、失败 DDL dirty 阻断、显式重建恢复。
- 真正经过 gRPC 编解码和 MySQL 的用户创建/密码验证、商品列表关联与图片 JSON、库存查询、购物车增改删回归均通过；Redis 临时端口认证/PING 通过。用户密码测试保留生产 bcrypt cost 14，race 下改为每次 RPC 独立期限。
- 保护性检查通过：同名无归属卷不被复用或删除，错误 marker/非本机数据库被拒绝，缺失初始化 SQL 可以重建。全部本轮临时测试容器、网络和数据卷已清理，没有访问或修改 AWS。
- 两仓 workflow 已加入 checks 前置门禁。以上为提交前的本地验收记录，GitHub 远端结果以对应提交的 Actions 运行记录为准；线上部署开关保持原样。
- 当前只管理带 marker 的独立测试库。AWS 存量库尚未纳入迁移记录；没有验证生产升级/回滚、完整 Nacos/Consul 启动或 HTTP 端到端流程，验证码/短信/JWT 权限与交易修复仍按后续批次执行。说明见 [tests/README.md](../tests/README.md)。

## 2026-09-17 追加：关闭开发隧道后的公网直连验收

- AWS 安全组新增 TCP 50051–50054、6379 两条规则，来源 137.132.26.103/32；界面成功回显并确认原 5 条入站规则保留。
- RPC_BIND_ADDRESS、REDIS_BIND_ADDRESS 在服务器设为 0.0.0.0；Compose 配置校验通过，Redis 重建后 healthy，Jenkins #3 发布同组 RPC 镜像并返回 SUCCESS。四个 Consul 检查 passing。
- configure-local-web.py --host 98.83.120.128 更新并读回三个 local JSON；RPC target、Redis host/port 正确，dev 内容逐项相等。旧 local JSON 和部署文件已备份。
- 开发 SSH 控制连接已关闭，Mac 15051–15054、16379 均无监听。三个 Web 在各自目录以 go run . 重新启动，三个 /health 200。
- 登录 200；商品列表含 420 件商品、详情/品牌/分类 200；无 token 购物车 401；购物车添加、查询、PATCH 200；订单列表 200、不存在详情 404。保留演示购物车商品 421、数量 1，无新订单。
- lsof 确认 user-web/goods-web/order-web 进程直接连接 98.83.120.128 的 RPC 端口及 Redis 6379，不经过本机转发。此处未复测交易或支付功能，其限制仍与上一轮一致。

## 2026-09-17 AWS RPC + Mac Web 实测

- Jenkins `grpctest-deploy #2` SUCCESS；四个 RPC 容器 healthy，Consul 四项检查 passing。发布的业务提交是 e74d2e4，采用验证仓库/提交/服务标签的预导入 image ID 模式。未开启 GitHub 自动部署。
- 本机隔离 MySQL 验证了空库导入、带完成标记重复执行不改数据、非空库无标记时拒绝执行。随后在 AWS 执行相同导入：11 张业务表、420 件商品、113 品牌、369 分类、1801 分类品牌关系、420 条库存各 100。
- Mac 通过 SSH 隧道调用四个真实 RPC：创建演示用户并校验密码、商品列表/详情、库存查询、购物车查询通过；CreateOrder 返回预期 FailedPrecondition，未创建订单。
- 三个 Web 均在各自目录以不带环境变量的 `go run .` 启动。`USER` 覆盖 Nacos 用户的故障已用 `TestDebugEnvironmentDoesNotOverrideNacosUser` 先复现、修复后通过；显式 MXSHOP_DEBUG=false 仍生效。
- HTTP 验证：三个 /health 为 200；420 件商品列表、421 详情、品牌、分类、空 banner 为 200；正确登录 200 且有效期约 24 小时；错误验证码与错误密码均 400 且无 token；普通用户请求管理员接口 403。
- 跨 Web Bearer token 验证：无 token 购物车 401；添加商品 421、查询、PATCH 数量/选中状态均 200；订单列表 200/空数组，不存在订单详情 404。修正订单 handler 对 goods-web Claims 的错误类型引用后，原先 HTTP 500 不再复现。
- HTTP 新建订单返回预期的 500 / gRPC FailedPrecondition（msg:9）；这是当前依赖缺失保护，不能列为下单成功。未调用外部短信或支付。
- 本机三服务 readonly 构建、配置回归、Bash/Python 语法检查通过；运行日志、Nacos 缓存和私密测试账号未提交。预导入镜像的错误标签拒绝检查通过。容器健康通过后的真实失败回滚仍未验证。

## 2026-09-17 当前提交离线验证

- 四个 RPC 的 readonly 构建与 Linux amd64 镜像构建通过；订单本地回归测试含 `-race` 通过。这些是本地检查，当时的镜像发布 workflow 不运行 `go test`；2026-09-20 的更新见上方。
- Actions workflow 经 actionlint 检查；Jenkins 触发脚本的五项离线预检通过。GitHub 只读检查确认 `ENABLE_DEPLOY` 未配置、`aws` Environment 尚未创建，未启用自动部署。
- 在私有临时目录生成配置，验证 `.env` 权限为 `0600`、重复初始化拒绝覆盖；Nacos 配置生成器准备七份 JSON，仅运行本地生成路径，没有调用 Nacos API。临时目录随后移除。
- 两份 Compose 经 `docker compose config --format json` 校验。逐项断言四个 RPC 的 `grpc_health_probe` 端口和参数、三个 Web 的 `wget /health` 与 `/app/<服务>/config-debug.yaml` 只读挂载；Web 不设置 `MXSHOP_CONFIG_FILE`。基础设施默认发布端口仍绑定 `127.0.0.1`。
- RPC 镜像内标准探针可执行，目标不可达时返回非零退出码；未恢复已删除的自建 `cmd/healthcheck`。
- Python AST、Bash 语法检查及提交候选文件检查通过；`.env`、其备份、runtime、私钥、Terraform 状态/缓存/计划被忽略，安全样例与 `.terraform.lock.hcl` 可提交。

本轮没有重新运行 Terraform 校验或云端 plan/apply，没有启动业务容器，也没有验证订单交易、线上回退或支付链路。

## 2026-09-16 本机历史验证

2026-09-16，本机 Mac / Docker Desktop。验证使用临时目录、随机容器名与临时端口；临时 Jenkins 和 Nacos 容器已移除，未改动本机原有 MySQL/Nacos 数据。

| 项目 | 结果与范围 |
| --- | --- |
| 两份 GitHub Actions workflow | `actionlint v1.7.7` 通过 |
| 基础设施与应用 Compose | 生成临时凭据后 `docker compose config --quiet` 通过 |
| Python / Bash | Python AST、Bash 语法检查通过；重复生成环境被拒绝；`.env` 权限为 0600 |
| Jenkins 镜像 | 本机 arm64 镜像真实构建成功 |
| Jenkins 初始化 | 临时容器启动成功；创建任务与 actions token；匿名 API 访问返回 403 |
| Jenkins Pipeline | Jenkins 自带 Declarative Pipeline 校验通过 |
| Jenkins 失败结果 | 用空镜像清单触发真实任务，任务正确返回 FAILURE；未挂载 Docker socket，未执行真实部署 |
| Nacos 2.5.1 | 临时鉴权实例启动成功；管理员初始化、登录、四个命名空间、七份配置导入与读回通过；重复执行通过 |
| Web 业务构建 | 三个服务的 Linux amd64 / CGO_ENABLED=0 构建通过 |
| RPC 业务构建 | 用户、商品、库存三个服务的 Linux amd64 / CGO_ENABLED=0 构建通过 |
| 健康探针 | 当时的自建探针 Linux amd64 构建通过；这些程序后来已删除，该结果不适用于当前探针 |
| Web Dockerfile | `SERVICE=user-web` 的 linux/amd64 镜像真实构建成功 |
| 发布状态与失败恢复 | 用临时环境和模拟 Docker 调用验证：成功保存 digest；失败保留旧状态并发出恢复命令；首次失败移除新增应用容器 |
| 差异检查 | 两个仓库 `git diff --check` 通过 |

本机验证阶段尚未检查云端环境。随后真实 AWS SSH、五个基础组件共同运行和资源占用已验证，见下方云端记录。GitHub Secrets 与 GHCR 权限、Actions→SSH→Jenkins 的跨机器调用、实际业务容器更新/回退、数据库迁移与业务链路仍未验证。失败恢复的模拟校验不等于真实集群演练。

当时订单 RPC 的 `go build -mod=readonly ./order_srv` 失败，首先报告需要更新 go.mod；静态检查还能看到既有错误导入、混合 package、错误 gRPC 注册等。工作流没有忽略该服务，发布会按设计被阻断。

当时未增加或运行业务 `go test` 阶段；原有 JWT 测试文件后来按用户要求删除。本轮订单本地回归检查与旧记录、CI 流水线分开报告，当时的镜像发布 workflow 不运行 `go test`。

## Terraform 自动创建服务器

随后新增 `terraform/`，使用临时下载并核对官方 SHA256 的 Terraform 1.16.2 与 AWS provider 6.64.0 进行验证：

- `terraform fmt -check`、`terraform validate` 通过。
- 使用临时 mock AWS provider 做离线 plan：默认 t3.large / 60 GiB、磁盘加密与终止后保留、仅开放 SSH 等断言通过。
- 将 SSH 来源设置为 `0.0.0.0/0` 时变量校验正确拒绝；两个离线场景通过，临时验证文件及 SSH 密钥已清理。
- 解码并解压 cloud-init 载荷，逐一核对 12 个文件与本地源码一致，启动命令通过 Bash 语法检查。gzip 载荷为 9,581 字节，小于 EC2 的 16,384 字节限制。
- 没有调用真实 AWS 账号的 plan/apply，没有创建 EC2 或产生云端资源费用。镜像查找、权限、配额、可用区容量和云端首次启动仍需登录 AWS 后验证。

AWS provider 已缓存到被 Git 忽略的 `.terraform/`；`.terraform.lock.hcl` 记录选定版本。该离线检查不代表云端部署已完成。

## 现有 AWS 服务器的真实部署

2026-09-16，按用户授权通过 SSH 连接其自行创建的 `98.93.115.75`，未执行 Terraform apply，未新增或调整 AWS 实例、磁盘或安全组。

- 检查确认 Ubuntu 26.04 LTS、x86_64、t2.large、2 vCPU、约 7.7 GiB 可用物理内存、20 GiB 块设备；根分区约 19 GiB。安装脚本已支持 Ubuntu 24.04 / 26.04。
- Docker Engine 29.8.1 / Compose v5.5.1 安装成功；Jenkins 镜像在服务器真实构建成功。
- MySQL、Redis、Nacos、Consul、Jenkins 五个容器全部 healthy，无 OOMKilled；发布端口全部绑定 127.0.0.1，数据使用命名卷。
- MySQL 业务账号能访问四个目标数据库，业务表数量为 0；没有导入旧 SQL。
- Redis 带密码 PING 返回 PONG，无密码访问返回 NOAUTH。
- Nacos 四个命名空间、dev 分组七份配置全部与生成文件逐一读回比对一致；匿名配置读取返回 403。
- Jenkins actions API token 可读取可构建的 `grpctest-deploy` 任务；匿名 API 返回 403；Jenkins 容器可调用宿主机 Docker 和 Compose。未触发业务发布任务。
- Consul 已选出 leader。本机 SSH 隧道访问 Jenkins 登录页、Nacos 控制台、Consul UI 均返回 HTTP 200。
- 空闲运行时内存约 2.2 GiB 已用 / 5.5 GiB available；根分区 6.9 GiB 已用 / 12 GiB available（38%）。这是部署后快照，不是压测结果。
- 未读取 AWS 安全组配置、EBS 类型或账单，未验证公网业务访问、数据库恢复、重启后业务恢复或生产可用性。

访问方式、凭据位置和未完成事项见 [DEPLOYED_AWS.md](DEPLOYED_AWS.md)。

## 云端配置入口核对

2026-09-16，再次读取全部七份云端 Nacos 配置，与服务器生成文件逐项比对；确认 bootstrap 使用 `nacos:8848`，业务依赖使用 Docker 服务名，配置位于四个命名空间的 dev 分组。

- 当时七个 Go 初始化入口支持 `MXSHOP_CONFIG_FILE`，Compose 将七份 bootstrap 只读挂载到各容器 `/run/config/nacos.yaml` 并显式选择该路径；Compose 已同步服务器，服务器配置校验通过。
- 当时库存 pro YAML 的 Data ID 修正为 `inventory-srv.json`；订单初始化器改为订单 global 和订单配置文件路径，订单 RPC 其余编译问题尚未解决。
- 当时用户、商品、库存 RPC 与三个 Web 服务的 `go build -mod=readonly` 均通过；订单 RPC 因模块依赖问题失败。
- 在真实服务器运行两个临时非 root 容器，分别调用更新后的用户 RPC、user-web 配置初始化函数。通过只读挂载读取真实 bootstrap，Nacos 鉴权及业务配置读取成功；故意设置 `MXSHOP_DEBUG=false`，确认显式配置路径优先于默认 pro 文件。
- 临时程序只调用配置初始化，不连接业务数据库、不注册业务服务、不调用业务 API。容器与服务器临时程序已清理，未修改现有七份 Nacos 内容。
- 两个仓库 `git diff --check` 通过；未新增业务测试阶段，也未构建或发布正式业务镜像。

## Nacos 公网直连

2026-09-16，按用户要求启用浏览器直接访问 `http://98.93.115.75:8848/nacos/`：

- `compose.yaml` 新增可选 `NACOS_HTTP_BIND_ADDRESS`，默认仍为 127.0.0.1；现有实例 `.env` 设置为 0.0.0.0，只重建 Nacos。容器 healthy，原有命名卷保留，9848 与其他组件仍绑定回环地址。
- 在 AWS 控制台确认并保存安全组 `sg-01a6adef2222761c1` 的新规则 `sgr-0c1eef2046c19176d`：TCP 8848，仅来源 42.61.2.190/32。原有 SSH 规则未变更；控制台显示保存成功及两条入站规则。
- Mac 直接请求公网 URL 返回 HTTP 200；通过公网地址使用原账号密码登录成功，四个命名空间、七份 dev 配置全部可读。原 SSH 隧道也返回 HTTP 200。
- 浏览器已切换到公网地址并显示 Nacos 登录页面。客户端公网 IP 改变后需更新安全组来源；EC2 公网 IP 改变后需更新访问 URL。
