# 分类创建与分类品牌关联修复

## 原来的问题

- 创建分类用 map 插入，却从另一个空 Category 对象返回 ID；数据库拒绝写入时也返回成功。现有 category.url 必填且无默认值，旧模型没有这个列。
- 关联列表把 category_id 当作关联记录 id 返回；用列表 ID 修改、删除时可能选错行。
- 关联创建、更新、删除和部分查询没有完整检查数据库错误，重复组合或数据库故障可能被当作成功或不存在。

## 本批行为

- `handler/category.go` 的 CreateCategory 使用实际写入的对象返回自增 ID，以及已保存的名称、层级、父级、展示状态。校验分类名称、层级及父级层级；一级分类父级保存为 SQL NULL。
- `model/goods.go` 补充现有 URL 列映射，创建时写空字符串；API 没有 URL 字段，因此不在分类 JSON 中新增字段。不修改已应用的 SQL 基线或线上表结构。
- `handler/category_brand.go` 返回关联自身 ID，分别保留 category.id、brand.id；列表使用 COUNT 和稳定的 ID 排序分页。
- 创建只插入关系外键；更新在事务中锁定指定关联，明确更新两个外键，不用 Save 回退插入。重复提交相同更新成功；目标不存在返回 NotFound；无效分类、品牌返回 InvalidArgument。
- `handler/category_errors.go` 将数据库唯一约束冲突转换为 AlreadyExists，其他数据库故障返回通用 Internal，支持请求取消。Web 配套把 AlreadyExists 转成 HTTP 409 和可读提示。

示例：关联记录 501 指向分类 10、品牌 3，列表返回的外层 id 是 501。PUT/DELETE 使用 501；按分类获取品牌的 GetCategoryBrandList 仍使用分类 ID 10。

沿用现有软删除和唯一索引：已软删除的分类品牌组合仍占用唯一键，再次创建同一组合返回 AlreadyExists。本批不引入关系恢复、分类修改/删除改造、商品创建或库存初始化；这些边界继续留在后续计划。

## 验证

`tests/integration/goods/category_test.go` 使用真实隔离 MySQL 和 gRPC，验证三级分类创建后按返回 ID 读取、用列表 ID 修改/删除准确关系、重复创建/冲突更新拒绝且原关系不变、无效层级和目标拒绝。事务结束回滚测试数据。

`handler/category_errors_test.go` 在数据库边界注入查询、写入、提交故障，验证不会误报成功或不存在，也不把 SQL 错误传给客户端。两组都由既有 CI 执行，无需新增测试环境。
