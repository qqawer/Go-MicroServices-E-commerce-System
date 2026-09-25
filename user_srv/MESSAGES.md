# 用户留言

本批在现有 user-srv 中注册独立的 `mxshop.messages.v1.Messages` 服务，复用用户 RPC 端口与数据库。新增提交、本人分页列表、本人详情和本人删除；不依赖商品、库存或消息队列。

## 原实现与 mxshop 对照

当前工程此前没有留言协议、数据表或 HTTP 接口。参考 mxshop 固定版本 `2bbe1b3a0eaa59bf26a3a2047769979a0e206c11`：

- [RPC](https://github.com/iceymoss/mxshop/blob/2bbe1b3a0eaa59bf26a3a2047769979a0e206c11/mxshop_srvs/userop_srv/handler/message.go) 只有新增和列表，未检查 `Save`/`Find` 的数据库错误，没有分页；按结构体过滤时，零值用户 ID 会被 GORM 忽略。
- [Web](https://github.com/iceymoss/mxshop/blob/2bbe1b3a0eaa59bf26a3a2047769979a0e206c11/mxshop-api/userop-web/api/message/message.go) 普通用户传本人 ID，管理员列表留下零值。这次的“我的留言”对所有角色统一限定本人，不提供管理员查看所有留言的入口。
- [表单](https://github.com/iceymoss/mxshop/blob/2bbe1b3a0eaa59bf26a3a2047769979a0e206c11/mxshop-api/userop-web/forms/message.go) 要求 `file` 必填。本批先支持纯文字，附件留到 OSS 上传任务；不要求用户为了文字留言提交附件。

## 数据与接口规则

`0008_user_messages.sql` 新增用户库 `user_message`：用户 ID、类型、标题、正文、创建/更新时间及软删除标记。索引 `(user_id, deleted_at, id)` 支持本人分页；数据库约束要求用户 ID 为正、类型为 1–5。之前的迁移保持不变。

- 类型：1 留言、2 投诉、3 询问、4 售后、5 求购。
- 标题和正文去掉首尾空白，标题 1–100 个 Unicode 字符，正文 1–2000 个；HTTP 与 RPC 都校验，RPC 拒绝无效 UTF-8。
- 创建先在同一个数据库事务内确认用户存在且未被删除，再插入记录；数据库失败或提交失败均返回错误。
- 分页默认 `pn=1&psize=10`，每页最多 100 条，按记录 ID 倒序；计数和分页读取在同一个事务内完成。空列表返回 `data: []`。
- 详情与删除使用明确的 `id = ? AND user_id = ?` 条件。不存在、属于他人、已删除都返回 NotFound。删除为软删除，后续列表和详情不再返回该记录。
- 所有数据库操作继承调用方 context。错误使用明确的 gRPC 状态，不把数据库失败伪装成空列表或提交成功。

HTTP 从 JWT 取本人身份，不接受客户端提交 `user_id`。RPC 是现有受信任的服务间接口，用户 ID 由 Web 传入；这些 SQL 所属关系检查并不替代 RPC 网络的访问控制。

创建是普通 POST：重复提交会产生另一条留言。超时意味着调用方没有确认结果，应先查看本人列表再决定是否重新提交。这里没有新增请求幂等编号、自动重试或后台回复流程。

## 协议、验证与发布

协议源文件为 `user_srv/model/proto/messages.proto`，用固定版本工具执行 `bash scripts/generate-messages-proto.sh`。Web 同步协议及生成代码，在原用户 RPC 连接上调用 Messages。

验证命令：`bash ci/check.sh`、`python3 tests/integration/run.py`。真实 MySQL/gRPC 回归覆盖 Unicode 边界、分页排序、用户隔离、软删除、无效/已删除用户、数据库错误与 context 取消；Web 另验证实际 JWT 路由、严格请求参数和错误映射。

发布先运行既有生产迁移工具预检、备份并应用 `0008_user_messages.sql`，再部署 RPC，最后部署 Web。迁移只新增留言表，不修改既有业务数据，不需要新增 Nacos 配置或公网端口。Web 接口与 Apifox 示例见 Web 仓库 `MESSAGES.md`。
