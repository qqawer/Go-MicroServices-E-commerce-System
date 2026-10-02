# Elasticsearch 小数据实验（阶段 0、1）

本目录独立于商城运行配置与 Go 模块。目标是看见分词、匹配、排序和写入的实际结果，再决定如何接商品服务。

## 第 0 批：环境与版本

- ES：`8.19.22`，基础镜像固定多架构 digest，Mac ARM64 使用原生 arm64 镜像。
- 中文分词：官方 `analysis-smartcn 8.19.22`，随镜像构建安装；不是 IK，也不预设两者切词相同。
- 官方 Go 客户端：`github.com/elastic/go-elasticsearch/v8 v8.19.7`，锁定于本目录 `go.mod/go.sum`；本机 Go `1.25.6`。商城根模块原有 `olivere/elastic/v7` 不在本批迁移。
- ES 容器：2 CPU、2 GiB 内存上限、1 GiB JVM 堆；Docker 的全部内存不等于 ES 能任意使用的内存。
- ES 地址：`http://127.0.0.1:9201`；没有发布 9300，专用 bridge 网络 `grpctest-es-lab_lab`。
- ES 数据：专用卷 `grpctest-es-lab_es-data` → 容器 `/usr/share/elasticsearch/data`；Docker Desktop 的卷位于 Linux VM 内，不是 Mac 同名目录。
- 本地合成数据实验关闭 ES 身份认证/TLS。此配置仅限本机实验，不用于 AWS 部署。

在 RPC 仓库根目录执行：

```bash
cd labs/elasticsearch
docker compose up -d --build --wait --wait-timeout 180
go run .
curl -fsS 'http://127.0.0.1:9201/_cluster/health?pretty'
docker compose exec -T es bin/elasticsearch-plugin list
docker compose ps
```

生命周期：

```bash
docker compose stop          # 停止实验，保留数据和容器
docker compose start --wait  # 继续使用保留的数据
docker compose down         # 删除实验容器与网络，保留 ES 数据卷
docker compose down -v      # 清空本实验的数据卷；下次需重新 seed
```

不要改项目名、复用商城 Compose，或删除机器上其他旧 ES 容器/卷。

版本依据：[Docker 安装](https://www.elastic.co/guide/en/elasticsearch/reference/8.19/docker.html)、[SmartCN](https://www.elastic.co/docs/reference/elasticsearch/plugins/analysis-smartcn)、[官方 Go 8.19 客户端](https://www.elastic.co/guide/en/elasticsearch/client/go-api/8.19/installation.html)、[v8.19.7 源码](https://github.com/elastic/go-elasticsearch/tree/v8.19.7)。客户端与服务端同为 8.19 系列；补丁号并不要求相等，实际连接由 `go run .` 验证。

2026-10-02 实测：两业务仓库初始工作区干净；Mac ARM64 / 16 GiB，Docker Engine 29.2.1 / Compose 5.0.2，Docker VM 约 7.65 GiB，磁盘剩余约 93 GiB。ES `green`，SmartCN `8.19.22`，Go 连接通过；容器内存上限 2,147,483,648 字节，CPU 上限 2。旧 ES 7.x 容器保持停止，其他正在运行的容器未修改。

本机首次下载卡在 `docker-credential-desktop get`。只为公开镜像建立了不含凭据的 `.local/es-lab/docker/config.json`，没有修改原 `~/.docker/config.json`。若同样卡住，在 RPC 根目录执行以下一次性配置，然后回到本目录重试构建：

```bash
mkdir -p .local/es-lab/docker
python3 - <<'PY'
import json
from pathlib import Path
Path('.local/es-lab/docker/config.json').write_text(json.dumps({
    'cliPluginsExtraDirs': [str(Path.home() / '.docker/cli-plugins')]
}))
PY
# 先取得当前已确认是本机的 Docker endpoint，再切换客户端配置。
export DOCKER_HOST="$(docker context inspect --format '{{.Endpoints.docker.Host}}')"
export DOCKER_CONFIG="$PWD/.local/es-lab/docker"
```

本机 Docker Desktop 的 `internal: true` 网络未发布宿主机端口，因此最终用专用 bridge 网络加 `127.0.0.1` 端口绑定，并由宿主机 Go 客户端验证可达。
