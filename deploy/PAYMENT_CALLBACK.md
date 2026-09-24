# 支付宝沙箱通知入口

本入口只代理 `POST /g/v1/pay/alipay/notify`，其他路径返回 404，其他方法返回 405。三个 Web 的 8021–8023 仍绑定服务器回环地址；Mac 继续使用 `local` 分组运行自己的 Web。

```text
支付宝 → AWS 公网 TCP 80 → Nginx 精确通知路径
       → order-web:8023 → orders-srv → 验签/金额商户校验/数据库事务
```

当前通知地址已配置为 `http://98.83.120.128/g/v1/pay/alipay/notify`，`return_url` 可空。2026-09-24 已部署 Nginx、RPC 和三个 Web；服务器内通知 POST 返回预期 503（支付关闭），Mac 公网访问 80 仍超时，需核对实例 `i-06a10bd902160fade` 的安全组 `sg-01a6adef2222761c1`。现阶段是学习用沙箱 HTTP 入口；域名/TLS 后续另行配置。不要用 RPC 的 50054 或 Mac localhost 作为通知地址。

## 启动

先按 `migrate-business.py check/up` 备份并应用迁移 0005，再发布 RPC，最后发布完整 Web 镜像组。发布器不自动执行迁移。首次部署期间两端支付关闭、订单模式保持 `demo`。

将本目录的 `compose.payment.yaml` 与 `nginx/payment.conf` 放入服务器 `/opt/grpctest/deploy` 对应位置。默认只绑定回环；需要对外监听时在服务器私有 `.env` 中设置 `PAYMENT_BIND_ADDRESS=0.0.0.0`，然后：

```bash
cd /opt/grpctest/deploy
sudo docker compose --env-file .env -f compose.payment.yaml config --quiet
sudo docker compose --env-file .env -f compose.payment.yaml run --rm callback nginx -t
sudo docker compose --env-file .env -f compose.payment.yaml up -d --wait
```

AWS 安全组还须允许支付宝从公网到达 TCP 80；只允许 Mac 当前 IP 不足以接收支付宝通知。新增规则不需要删除既有规则，也不需要公开 8021–8023。安全组来源与实际开放范围应在变更时核对。

## 验证与启用

容器健康只表示进程存活，必须另外验证 HTTP 和 RPC：

```bash
# 非回调路径 404；回调的 GET 405。
curl -i http://98.83.120.128/
curl -i http://98.83.120.128/g/v1/pay/alipay/notify

# 无签名通知不得修改订单：关闭支付时 503；启用后 400 failure。
curl -i -H 'Content-Type: application/x-www-form-urlencoded' \
  --data 'out_trade_no=connectivity_probe' \
  http://98.83.120.128/g/v1/pay/alipay/notify
```

公网链路通过且沙箱凭据有效后，才开启 `orders-srv.json / dev` 和 `order-web.json / local,dev` 的 `alipay.enabled`，并把订单 RPC 的 `rocket.mode` 切为 `alipay`。配置在启动时读取，需重启订单 RPC、云端订单 Web；本机 Web 由使用者重启。旧 demo 订单不会变成可支付订单，需要重新下单测试。

产生 alipay 订单后，不能直接回滚到缺少渠道查单保护的旧 RPC。恢复旧代码可能误关已付款订单；数据库新增表/列兼容旧程序不代表支付流程可以安全回退。

网关不改写 POST body，限制 64 KiB，关闭代理自动重试。通过变量形式的 upstream 与 Docker DNS 定期重新解析，适应 order-web 容器更新。Nginx access log 不记录 body/query；现有 Web 请求日志仍可能记录 query，所以不把签名表单或密钥放入 URL。依据：[Nginx proxy_pass](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_pass)、[resolver](https://nginx.org/en/docs/http/ngx_http_core_module.html#resolver)。
