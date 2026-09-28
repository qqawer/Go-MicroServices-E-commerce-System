# 支付关闭核对工具（仅沙箱）

本工具处理已过期却无法确认支付宝终态的订单。`ACQ.TRADE_NOT_EXIST` 不会被当成未付款证明；多次查询、创建交易成功、关单请求成功也都不会单独触发返库。

先部署迁移 `0009_order_close_review.sql` 和支持核对的订单 RPC。工具只连接 `grpctest_orders_srv`，不会自动建表，不启动 HTTP/RPC/MQ，不直接修改库存。渠道确认 `TRADE_CLOSED` 后，订单状态与返库 outbox 一起提交，由已部署的恢复任务或 MQ 执行原有幂等返库。

工具明确按 UTC 读取和写入时间，只适配当前 AWS 的 UTC 订单库；不继承 Mac 等操作者电脑的本地时区。它没有迁移或转换其他部署的历史时间数据。

## 查看和预演

准备权限为 `0600`、位于 Git 忽略目录的私有配置文件；内容只有 `mysql` 和 `alipay` 两部分。`mysql` 包含 `host`、字符串 `port`、`user`、`password`、`name`（固定为 `grpctest_orders_srv`）。`alipay` 使用订单 RPC 的现有沙箱配置。通过标准输入提供文件，不把密码或私钥放进命令行、日志或 Git。

```sh
go build -o .local/payment-review ./cmd/payment-review
.local/payment-review inspect --order-id 1 < .local/payment-review-config.json
.local/payment-review fence-and-close --order-id 1 --request-id close-order-1 --operator your-name --buyer-id REAL_SANDBOX_BUYER_ID < .local/payment-review-config.json
```

第二条操作未加 `--execute`，也只读数据库，不请求支付宝。`REAL_SANDBOX_BUYER_ID` 必须换为该沙箱的实际买家支付宝用户 ID（`2088` 开头的 16 位数字），不能用商城用户 ID 或收款方 ID。

## 显式收尾

核对订单、金额、商户与买家后，在同一条 `fence-and-close` 命令后追加 `--execute`。工具会记录操作者和原始请求；查询发现渠道没有交易时，才使用同一商户订单号、同一金额和指定沙箱买家调用 `alipay.trade.create`，随后关单并查询真实终态。创建不会调用扣款接口，也不会生成或发出收银台链接。创建的绝对有效期由首次持久化的操作决定，重试不续期。

这需要当前沙箱应用具有 `alipay.trade.create` 相应产品权限。权限不足、超时、响应丢失、渠道仍查不到交易等情况都会保留库存等待核对，不会改产品码、换订单号、强制标记已关闭或自行返库。

若命令失败，先运行 `inspect`。重试必须沿用相同的订单、请求编号、操作者和买家 ID；不要重新下单，也不要直接改数据库状态。出现已支付或退款迹象时，应按实际支付证据处理，而不是强制走未支付关单。

依据：[支付宝关闭交易与订单号唯一性说明](https://aipay.alipay.com/docs/vibe-pay/ai-web-app-payment-qianyi/ai-web-app-payment-integration-guide.html)，[官方 EasySDK 创建交易与买家 ID 参数](https://github.com/alipay/alipay-easysdk/blob/master/APIDoc.md)。此处的安全条件是核验真实渠道终态，不是依赖“过期后首次提交必然失败”或“查不到交易即永久不存在”等未经确认的保证。
