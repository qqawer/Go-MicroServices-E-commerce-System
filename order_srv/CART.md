# 购物车记录与部分更新（2026-09-22）

## 对照 mxshop

参考提交 `58142016702831ed31a62b9ece431c935a24ba04`：

- [Web 购物车](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop-api/order-web/api/shop_cart/shop_cart.go)：列表的 id 是购物车行 ID，但 PATCH/DELETE 将 URL 参数放进 GoodsId。
- [RPC 购物车](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop_srvs/order_srv/handler/order.go)：更新按 goods + user 查询，无条件覆盖 Checked，未检查 Save 错误。
- [Web 表单](https://github.com/iceymoss/mxshop/blob/58142016702831ed31a62b9ece431c935a24ba04/mxshop-api/order-web/forms/shop_cart.go)：更新数量必填，不能单独改变 Checked。

本仓原实现沿用了这些问题。本批保留添加同一商品时累加数量的行为，修正身份定位、部分更新和失败传播。

## RPC 契约

- CartItemList：user ID 必须为正数，显式按 user 查询；不能用零值省略用户条件。
- CreateCarItem：传 UserId、GoodsId、正数 Nums。已有记录保留行 ID 和 Checked，累加数量；新记录 Checked=false。事务中锁定已有行，拒绝 int32 数量溢出，检查读取、写入及提交错误。
- UpdateCartItem：传 Id（购物车行 ID）和 UserId；Nums、Checked 都是 optional。只改提供的列；Nums 如提供必须大于 0；至少提供一项。显式 false 有效，未提供 Checked 保留现值。事务内先锁定自己的行再更新，更新同值成功；不用 Save，避免删除后回退插入。
- DeleteCartItem：按 Id + UserId 软删除。其他用户的行与不存在的行均返回 NotFound。SQL 失败返回 Internal，不误报不存在或成功。
- RPC 使用请求 context 执行 SQL；Web 用户 ID 来自现有 JWT 中间件。

没有新表、迁移、MQ、后台任务或请求幂等编号。购物车增加数量仍是每次请求累加，不保证网络超时后重复 POST 只加一次；本批没有接通下单扣库存。首次并发加入的数据库唯一约束与更完整的库存校验未包含在本次修复中。

## 协议与上线

现有字段编号、方法名不变，nums=4、checked=5 改为 optional 并同步两仓生成代码。Go 调用方用指针传入这两项（例如 protobuf/proto.Int32、Bool）。

先升级订单 RPC，再重启 Mac order-web。旧 Web 仍用 GoodsId 修改/删除，新 RPC 会因缺少行 ID 返回 InvalidArgument；不按商品 ID 自动回退，避免修改错误记录。Apifox 路径必须使用 GET 购物车列表里的 id。

固定工具版本：protoc 30.2、protoc-gen-go 1.36.5、protoc-gen-go-grpc 1.5.1。将工具放入 PATH 后运行 `bash scripts/generate-order-proto.sh`。

## 验证

- `order_srv/handler/cart_test.go`：非法用户/商品/购物车 ID、空更新、非正数量；读取、插入、更新、删除及提交故障均明确失败。
- `tests/integration/order/rpc_test.go`：真实 MySQL + gRPC 编解码；行 ID 与商品 ID 不同、两个用户的同一商品、只改数量保留勾选、显式 false、同值更新、他人记录拒绝、溢出拒绝、删除后不能更新复活、重新添加获得新 ID。
- 两仓 `ci/check.sh`，RPC `python3 tests/integration/run.py`。集成环境使用现有隔离脚本，不读取 AWS 数据或配置。
