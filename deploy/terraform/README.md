# 用命令行自动创建 AWS 服务器

本目录补上“创建云资源”这一层。在 Mac 执行 Terraform，即可创建网络、EC2、磁盘和 SSH 公钥；服务器首次开机通过 cloud-init 自动执行上一层部署脚本，不需要再手动 scp 整个部署目录。

默认资源：Ubuntu 24.04 amd64、`t3.large`、60 GiB 加密 gp3 磁盘、一个独立 VPC/公网子网/互联网网关/路由表、安全组，以及导入现有 SSH 公钥。自动启动 MySQL、Redis、Nacos、Consul、Jenkins，并初始化七份云端 Nacos 配置。不创建 NAT Gateway、负载均衡器或付费数据库托管服务。

**`terraform apply` 会真正创建收费资源，包括实例、EBS 和公网 IPv4；`plan` 只查看计划。** 2 vCPU/8 GiB/60 GiB 是前面建议的学习环境配置，不代表免费套餐。按所选区域核对 AWS 价格后再执行 apply。实例使用 T3 Standard CPU 积分模式，积分用完会影响性能。

## 首次准备：在 Mac 执行

在具备 Homebrew 的 Mac 上安装所需工具；已经安装时无需重复安装：

```bash
brew tap hashicorp/tap
brew install awscli hashicorp/tap/terraform

# AWS CLI 2.32+：通过浏览器登录具有所需 EC2/VPC 权限的 IAM 身份。
aws login --profile grpctest
export AWS_PROFILE=grpctest
aws sts get-caller-identity
```

`aws login` 的 IAM 身份需要 `SignInLocalDevelopmentAccess` 权限，创建基础设施还需要相应 EC2/VPC 管理权限。如果账号使用 IAM Identity Center，改用 `aws configure sso --profile grpctest` 后 `aws sso login --profile grpctest`。登录在你自己的浏览器里完成，不需要把 AWS 密钥发到聊天里。

如果 Terraform 所用 SDK 不能直接识别 `aws login` 的 profile，可用 AWS 官方提供的 `credential_process` 兼容方式：

```bash
aws configure set credential_process 'aws configure export-credentials --profile grpctest --format process' --profile grpctest-terraform
export AWS_PROFILE=grpctest-terraform
aws sts get-caller-identity
```

准备 SSH 公钥。如果已有 ed25519/RSA 公钥，可直接使用其 `.pub` 路径；否则生成新的一对，勿覆盖已有文件：

```bash
ssh-keygen -t ed25519 -f ~/.ssh/grpctest-aws -C grpctest-aws
```

私钥保留在 `~/.ssh/grpctest-aws`，Terraform 只读取 `.pub` 文件并上传公钥。

## 填四个参数，然后创建

```bash
cd /Users/outsider/Downloads/Golang_Development/grpctest/deploy/terraform
cp terraform.tfvars.example terraform.tfvars
```

编辑 `terraform.tfvars`：

| 参数 | 内容 |
| --- | --- |
| `aws_region` | 你选择的 AWS 区域，示例是新加坡 `ap-southeast-1` |
| `aws_account_id` | `aws sts get-caller-identity` 输出的 12 位 Account；防止用错账号创建资源 |
| `admin_cidr` | 你当前出口公网 IPv4 加 `/32`，不是 Mac 的局域网 IP |
| `ssh_public_key_path` | SSH 公钥 `.pub` 的本机路径 |

可用 `curl -fsS https://checkip.amazonaws.com` 查看当前出口公网 IPv4。若使用代理，确认这里得到的 IP 与 SSH 实际出口一致。其余实例规格、磁盘大小可在同一文件调整。

```bash
umask 077
terraform init
terraform plan

# 核对账号、区域和规格后执行；输入 yes 才会创建资源。
terraform apply
```

不要添加 `-auto-approve`。无需自己查询 AMI ID：配置只从 Canonical 官方账号查找所选区域的 Ubuntu 24.04 amd64 镜像。当前设计面向 AWS 商业区域，不适用于中国区。所选可用区若没有 T3 容量，AWS 可能拒绝创建，需调整配置后重试。

Terraform state 保存在本地，须保留并备份；不要把它、tfvars 或计划文件提交到 Git。`.terraform.lock.hcl` 应保留在版本库。不要用丢失 state 的目录反复 apply，否则可能创建重复资源。

## 等待首次部署完成

`terraform apply` 成功表示云资源已创建，并不表示 Docker 镜像构建、Jenkins 启动已经结束。获取地址并连接：

```bash
terraform output -raw public_ip
ssh -i ~/.ssh/grpctest-aws "$(terraform output -raw ssh_destination)"
```

首次 SSH 连接先核验服务器指纹。进入服务器后执行：

```bash
sudo cloud-init status --wait
sudo test -f /var/lib/grpctest/infra-ready && echo '基础设施初始化成功'
sudo bash /opt/grpctest/deploy/scripts/stack.sh status
```

只有部署脚本全部成功才创建 `infra-ready` 标记。失败时查看 `sudo tail -n 100 /var/log/cloud-init-output.log`，修复后按上一级 README 重试失败阶段，已有 `.env` 时不要再次运行 `init-env.py`。

Terraform 通过 gzip 压缩的 EC2 user data 写入明确列出的部署源码，不上传本机 `.env`、`runtime/`、私钥或 Terraform state。应用密码、JWT 密钥和 Jenkins token 都在服务器上首次生成。

## 后续边界

- 这一步会自动开机器、装中间件，不会创建业务表或完成订单交易链路，也不会自动填写两个 GitHub 仓库的 Secrets。订单 RPC 当前基础构建已修复，但下单仍返回 `FailedPrecondition`，不能据此启用自动部署。后续 CI/CD 步骤见上一级 README。
- 安全组只允许 `admin_cidr` 的 SSH；MySQL/Nacos/Jenkins 等端口不向公网开放。GitHub 托管 runner 不能直接使用仅允许 Mac IP 的规则。启用自动发布前，需要确定固定出口或私网 runner 的访问方案，不能把机器创建成功当成 Actions 已经可达。
- 当前未使用 Elastic IP；停止后再启动 EC2 时公网 IP 可能变化，届时需更新连接地址和 GitHub Secret。单纯操作系统重启不等同于 EC2 stop/start。
- 首次部署后的 AMI 与 user data 变更被忽略，避免因为更新脚本自动替换装有数据库的主机。后续应用更新走 CI/CD；部署脚本变更另行同步服务器。
- 已启用 Terraform `prevent_destroy` 和 EC2 终止保护。根磁盘 `delete_on_termination=false`，即使你另外解除保护并终止实例，磁盘仍会保留并继续产生存储费用。需要清理环境时，先备份数据，再有意识地解除保护、销毁资源并确认遗留磁盘；此配置不是一条 destroy 就无残留费用的环境。

验证：历史离线配置校验见下方记录；本轮没有执行真实云端 plan/apply。具体检查结果见 `../VALIDATION.md`。

参考：[AWS CLI 创建 EC2](https://docs.aws.amazon.com/cli/latest/userguide/cli-services-ec2-instances.html)、[AWS CLI 浏览器登录](https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-sign-in.html)、[Terraform 创建 AWS 基础设施](https://developer.hashicorp.com/terraform/tutorials/aws-get-started/aws-create)、[EC2 user data](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/user-data.html)、[Terraform 安装](https://developer.hashicorp.com/terraform/install)。
