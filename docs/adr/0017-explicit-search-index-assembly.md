# ADR-0017: 检索索引改为显式装配

## 背景

全文检索只有 `user` 一个模型，却配了一整套全局机制：

- `platform/search` 维护包级注册表（`RegisterModel` / `GetModel` / `RegisteredModels`），导入器签名 `BatchImporter(ctx, db *database.DB, ...)` 直接吃数据访问层。
- `shared/indexes` 通过 `init()` 调用 `RegisterAll()`，把模型写入全局 map。
- `console/scout.go` 用空白导入 `_ "cgin/internal/shared/indexes"` 触发注册，读代码无法看出模型从哪来。
- 结果是 `platform/search` 反向依赖 `internal/database`（平台层依赖数据访问层），且注册顺序成为隐式约束。

## 决策

1. **导入器不再持有数据库**：`BatchImporter` 与 `RecordCounter` 去掉 `*database.DB` 参数，数据源由装配方在构造闭包时捕获。
2. **删除全局注册表**：移除 `RegisterModel` / `GetModel` / `RegisteredModels` 与包级 map、互斥锁；删除 `shared/indexes/search.go` 的 `init()` 副作用注册。
3. **引入显式 Catalog**：`search.NewCatalog(models...)` 构建期一次性装配，`Lookup(name)` 支持按模型名或索引名检索，`All()` 按索引名排序且返回副本。
4. **单一装配入口**：`indexes.Catalog(db)` 与 `indexes.Names()` 提供模型集合与名称列表，`scout` 命令显式构造，不再依赖空白导入。
5. **校验时机后移**："模型未注册"的判定需要 `db` 才能装配 Catalog，因此从 `Validate()` 移到 `Run()`。

## 后果

- 正向：`platform/search` 不再依赖 `internal/database`，分层复原；新增索引模型只需改装配点，不存在隐式注册顺序；`Catalog` 可在测试中直接构造，无需重置全局状态。
- 负向：`scout:import <unknown-model>` 的错误从参数校验阶段推迟到执行阶段，错误信息附带可用模型列表；CLI 退出码不变（仍为非零）。
- 约束：可搜索模型的名称集合由 `indexes.Names()` 单一维护，新增模型时必须同时更新该函数。
