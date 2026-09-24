# 订单支付与安全关闭

2026-09-24，第 06 批已部署：RPC `fc64b6a`、Web `fca45be`，迁移 0005 已备份应用，现有 AWS 继续 `rocket.mode=demo`、支付宝关闭。Nacos `orders-srv.json / dev` 已配置同套沙箱应用及收款商户 ID；从 AWS 只读查不存在的沙箱订单得到预期结果，未创建交易。Nginx → order-web 通知路径在服务器内验证通过，但公网 80 连接仍超时，待核对安全组后再启用、验收真实付款及通知。

## 调用与数据

Web 的原有 `POST /g/v1/pay/alipay/notify` 通过新增 RPC `ProcessPaymentNotification` 转交原始签名表单。RPC 校验 RSA2 签名、AppID、SellerID、订单、交易号、状态与精确金额，避免信任调用方直接给出的 `TRADE_SUCCESS`。原 `UpdateOrderStatus` 拒绝裸支付状态更新。

新迁移 `0005_order_payment.sql` 新增 `order_payment` 支付流水。流水与订单状态在同一个数据库事务更新，订单号、渠道交易号各自唯一；同一订单行锁串行化付款、关单和重复通知。数据库不存私钥、完整回调 body 或付款人资料。

金额沿用第 05 批的 `total_cents`。新支付宝订单受页面支付上限 1e10 分限制，`demo` 保留原范围。旧订单没有可靠 `total_cents` 或到期时间时，不补猜测值来开放付款。到期时间沿用 `order_transaction.expires_at`，通过 `OrderInfoResponse.expiresAt` 给 Web 生成固定截止时间的链接；列表不为此逐条查库。

## 状态与恢复

- `WAIT_BUYER_PAY`：可付款，收到经验证的成功结果才入账。
- `CLOSING`：到期后先持久化，再在事务外查询渠道；未知结果不能返库。
- `TRADE_SUCCESS / TRADE_FINISHED`：已付款，不允许旧通知或超时任务回退。
- `TRADE_CLOSED`：确认未付款关闭后写入，和已有 `order-reback` outbox 同事务提交。
- 流水 `review=true`：晚付或疑似付款后退款的异常，暂停自动处理。原订单已关闭则保持关闭，其他情况进入 `PAYMENT_REVIEW`；不会直接恢复成正常履约订单或自动退款。

复用原有 5 秒恢复循环和 RocketMQ。查询显示待付款时调用渠道关单，然后再次查询。关闭调用返回成功本身不足以返库；必须有可信最终结果。超时、未知业务错误、`ACQ.TRADE_NOT_EXIST` 都保留待确认。页面支付生成链接不代表已在渠道建单，不能把“查无此单”当作“以后无法付款”。当前此类订单可能长期待确认，需后续沙箱验收和明确人工处理规则，未宣称已实现有期限的自动释放。

扫描记录上次尝试时间并轮转，避免长期未知订单或失败消息挡住后续任务。已进 MQ 的消息不能靠本地 review 撤回。

若返库已经发送或完成后才收到真实付款，保留已关闭状态并记录待核对，不重新扣库存或自动发货。库存操作仍使用上一批的幂等记录。

## Nacos 配置

namespace `ce4b3f78-1db4-4418-9cb9-a3e1bd406c88`，Data ID **`orders-srv.json`**，Group **`dev`**，只追加 `alipay` 对象，保留其他配置：

```json
"alipay": {
  "enabled": false,
  "appid": "",
  "seller_id": "",
  "private_key": "",
  "alipaypublic_key": ""
}
```

`seller_id` 是自己的沙箱收款商户 ID。启用后检查配置及 RSA 密钥格式，SDK 固定沙箱模式；客户端在原有启动流程里构造并复用，没有第二套服务入口。查询/关单请求时间使用 UTC+8，避免 AWS UTC 环境产生错误 timestamp。

Web 的 `order-web.json` 还保留支付链接所需配置；AppID、应用私钥、支付宝公钥应与 RPC 一致。沙箱准备完成后才同时启用两端并把 `rocket.mode` 改为 `alipay`，当前不切换。仅开启开关不代表沙箱已联调成功。

## 检查及部署边界

```bash
bash ci/check.sh
python3 tests/integration/run.py
```

第一项离线构建/race/SDK真实RSA签验与模拟网关；第二项创建带所有权标记的临时 MySQL/Redis，跑迁移、RPC业务、支付/关闭竞争和故障回归，不连接 AWS 数据库。具体用例在 `order_srv/payment/alipay_test.go` 和 `tests/integration/order/payment_test.go`。

需要先应用迁移并升级 RPC，再升级 Web。原来的演示订单继续工作。真实回调送达、截止时间边界、沙箱正常付款/关单与退款规则仍待凭据及公网回调就绪后验收。待核对记录可通过 `order_payment WHERE review=1` 查看；尚未新增后台退款操作页面。

渠道业务语义和 Web 参数说明见 [Web 支付说明](https://github.com/qqawer/grpctest_web/blob/main/PAYMENT.md)（随本批提交发布）。官方依据：[通知业务校验](https://developer.alibaba.com/docs/doc.htm?articleId=105902&docType=1&treeId=193)、[关单接口](https://aipay.alipay.com/docs/vibe-pay/ai-web-app-payment-qianyi/api-list/alipay-trade-close.html)、[页面支付参数](https://developer.alibaba.com/docs/api.htm?apiId=63600)。
