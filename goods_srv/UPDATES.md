# 商品资料与状态更新

## 原来的问题

Web 的 PUT 复用 `CreateGoodsInfo`，没有传 `isNew/isHot/onSale`，RPC 却把这些默认的 false 覆盖到商品上。PATCH 也调用同一个 RPC，但不携带分类和品牌，因此在分类校验处失败。

旧 RPC 使用 `Save(&goods)`，还会自动保存分类、品牌关联；在当前基线数据库中，分类 `url` 必填但模型没有这个字段，会触发数据库错误。旧代码没有检查保存结果，仍返回成功。

## 更新契约

- `UpdateGoods(CreateGoodsInfo)`：只替换商品资料。保留原有方法、消息和字段编号，忽略这个操作中的 `isNew/isHot/onSale/stocks`。资料里的 `shipFree=false`、空详情图片列表可以正常保存。
- `UpdateGoodsStatus(UpdateGoodsStatusRequest)`：新增的状态专用 RPC。`id` 必须为正整数，`isNew/isHot/onSale` 是 proto3 optional bool，至少传一个。未传的状态保持不变，显式 false 则关闭该状态。
- 商品不存在返回 `NotFound`，无效 ID、空状态更新、分类/品牌不存在返回 `InvalidArgument`；数据库查询、写入、事务提交失败返回 `Internal`，不把 SQL 错误直接传给客户端。
- 每次更新在事务内锁定目标商品，只更新白名单中的列。不会重写商品的其他状态、计数和库存，不会自动保存分类/品牌表，也不会在商品消失后重新插入商品。

实现位于 `handler/goods_update.go`。保留现有表结构，不修改已执行的迁移基线。库存接入、商品创建、分类/品牌列表与详情关联等仍按后续计划处理。

## 协议同步与运行顺序

商品协议同步到四份副本：

- RPC：`goods_srv/model/proto`、`order_srv/proto`。
- Web：`goods-web/proto`、`order-web/proto`。

已有 RPC 方法及字段编号不变。`UpdateGoods` 的状态字段现在不再用于更新，状态调用方应切换到新方法。先发布新版商品 RPC，再启动新版 goods-web；旧 RPC 没有状态新方法，新版 Web 会返回 503 提示升级，不回退调用资料更新接口。只改本地 Web 无法修复 AWS 上旧 RPC 的资料更新行为。

生成工具固定为 protoc 30.2、protoc-gen-go v1.36.5、protoc-gen-go-grpc v1.5.1。安装 protoc 官方对应平台版本，将其 bin 和以下 Go 插件目录加入 PATH：

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.5
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
bash scripts/generate-goods-proto.sh
```

两个仓库各运行自己的生成脚本；脚本检查工具版本。修改协议时同步四份 proto 并重新生成，不手工编辑 `.pb.go`。订单模块只同步商品协议，不改订单业务。

## 回归

`tests/integration/goods/update_test.go` 使用独立 MySQL、真实 RPC 编解码和商品详情读取验证：资料编辑保留状态、单字段 false、全关/重开/重复请求、无效目标拒绝、真实数据库写入失败及原数据保留。

`handler/goods_update_test.go` 在数据库边界注入查询、写入和提交失败，确认不会误报成功或 404。已加入 `ci/check.sh`；商品集成用例由已有 `tests/integration/run.py` 自动执行。

```bash
bash ci/check.sh
python3 tests/integration/run.py
```

这些命令使用已有的隔离测试环境，不读取 AWS/Nacos 业务配置。
