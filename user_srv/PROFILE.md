# 用户资料读取与部分修改

Web 提供 `GET /api/v1/profile` 和 `PATCH /api/v1/profile`，目标用户 ID 只取已验证的 JWT。普通用户与管理员都可访问自己的资料；现有管理员列表 `GET /api/v1/getUserList` 继续限制管理员。

| 数据库内容 | 原 HTTP 返回 | 修复后 HTTP 返回 |
| --- | --- | --- |
| mobile 已填写 | `"mobile": ""` | 返回实际手机号 |
| birthday 为 NULL | 被当作时间戳 0，显示 1970 年日期 | `"birthday": ""` |
| birthday 已填写 | 日期字符串 | 保留日期字符串，包括真实的 1970 年日期 |

`handler/user.go` 的 `ModelToResponse` 补上 `Mobile`。`UserInfoResponse` 第 5 字段改为 `optional uint64`，仅在数据库生日非 NULL 时赋值。新增第 8 字段 `birthdayDate`，直接返回数据库中的 `YYYY-MM-DD` 日历日期，Web 优先使用它，避免跨进程时区转换改变生日。生日为 NULL 时两个字段均省略。

HTTP 列表仍返回用户数组，单人资料返回对象，`birthday` 都为字符串。RPC 的密码哈希字段保留供现有登录校验使用，HTTP 用户资料不会返回该字段。数据库已使用可空生日列，无需迁移。

## 修改语义

原 `Updateuser` 无条件赋值昵称、性别、生日并调用 `Save`，仅改昵称也可能清空性别、把生日写为时间戳 0；整行保存还可能覆盖并发修改。现在更新逻辑位于 `handler/profile.go`，只把明确提交的字段加入更新列。

新 `PatchUser` RPC 使用 `PatchUserInfo`：`id`、可选的 `nickName`、`gender`、`birthdayDate`。缺省字段保持原值；生日明确传空字符串表示清空，对应 HTTP 的 `{"birthday":null}`。HTTP 不接受空日期字符串，以免把误填与主动清空混淆。

- 昵称去除首尾空白后为 1–20 个 Unicode 字符，性别仅允许 `male` / `female`。
- 生日严格使用 `YYYY-MM-DD`，范围 `1000-01-01` 至 UTC 今天。Web 与 RPC 使用相同的上限，避免不同进程的本地日期边界不一致。
- 新生日以日期字符串参数写入 MySQL DATETIME，避免驱动将 `time.Time` 转为 DSN 时区而偏移一天；清空写入 SQL NULL。
- 空补丁或任一无效字段使整次请求失败。手机号、密码和角色不属于可修改字段。
- 数据库事务中锁定该用户行并检查存在性，再执行精确列 `Updates(map)`。软删除或不存在用户返回 `NotFound`，相同值重复更新仍成功；不使用整行 `Save`，不同字段的并发修改不会互相覆盖。
- `GetUserById` 和修改操作使用请求 context；数据库故障返回通用内部错误，不误报用户不存在或暴露 SQL 细节。

旧 `Updateuser` 的请求字段 2/3/4 改为 optional，保留名称、编号和类型，复用相同的更新函数。旧时间戳继续按原解释方式处理，显式时间戳 0 可以保存；旧协议不提供清空生日操作。旧客户端未序列化的默认值无法恢复，新 Web 统一使用新方法。

## 验证与发布

- RPC `handler/user_test.go` 验证手机号、两种生日字段存在性及 protobuf 序列化；`handler/profile_test.go` 验证数据库错误、取消请求、精确更新列和跨时区日历日期。
- `tests/integration/user/profile_test.go` 通过真实 MySQL 和 gRPC 验证部分修改、重复请求、并发不同字段、清空/保留日期、非法请求无写入、软删除与旧方法的存在性语义。它由现有 `tests/integration/run.py` 集成入口执行。
- Web 使用实际路由验证资料权限、JWT 身份绑定、输入校验、日期显示和密码字段隔离，既有登录、JWT 与管理员权限测试继续运行。
- 两仓在各自根目录执行 `bash ci/check.sh`。协议通过各仓的 `scripts/generate-user-proto.sh` 生成，沿用固定工具版本。
- 发布时先更新用户 RPC，再更新 user-web。新 Web 调到旧 RPC 时，`PatchUser` 返回 `Unimplemented`，HTTP 返回 `503` 提示升级，绝不回退调用会覆盖字段的旧方法。

旧响应没有 `birthdayDate` 时 Web 保留原时间戳显示方式；旧 RPC 已省略的时间戳 0 无法恢复，完整修复需要两端更新。本次不自动更改数据库中已有的 1970 年日期，也不把直接访问 RPC 当作已完成 HTTP 身份验证。
