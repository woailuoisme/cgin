# ADR-0014: 用户生命周期收敛为 shared/user 深模块

## 背景

同一套用户生命周期规则被多个 feature 各写一遍：

- “校验当前口令 → 哈希新口令 → 更新凭据 → 作废会话”在 `client/auth` 与 `admin/auth` 中共有三份实现，缺失凭据与口令错误的错误身份也各不相同。
- “归一姓名/邮箱/用户名 → 补默认角色状态 → 口令策略 → 哈希 → 事务创建用户与凭据”在客户端注册与管理端创建用户中各写一遍，共享的 `CreateWithCredential` 只覆盖最后一步写入。
- `sqlc.User` 在共享内核与两个 feature 中分别投影，客户端的 `role` 是 `string`，共享 `Profile` 是 `enums.Role`，同一契约出现类型漂移。

## 决策

1. **凭据生命周期归 `shared/user.Provider`**：`VerifyPassword`、`ChangePassword`、`ResetPassword` 在模块内完成哈希读取、口令强度校验、`hasher.Check/Make`、凭据写入与 `pgx.ErrNoRows` 归一。缺失凭据返回哨兵 `ErrNoCredential`，不泄漏账号是否存在。
2. **缺失凭据语义由调用方决定**：客户端维持 404，管理端维持 422（映射为 `errs.PasswordMismatch`）。会话作废仍留在 feature，通过 Guard 完成，避免 shared 反向依赖认证编排。
3. **用户创建归 `shared/user.Service.Register`**：一次事务写用户与本地凭据，统一姓名/邮箱/用户名归一、username/displayUsername 镜像、默认 `RoleUser`/`StatusActive`、口令策略与唯一冲突映射。删除只覆盖最后一步的 `CreateWithCredential`。
4. **投影单点定义**：`PublicProfile` 是客户端字段的唯一来源，`Profile` 在其上扩展 `Status`；客户端 `UserResource` 通过一次类型转换复用该投影，JSON 字段集合不变。
5. **删除不可达装配分支**：Guard 与 AuthManager 均由 bootstrap 显式注入，删除 client/admin auth 的 Guard 自建回退与 middleware 的令牌降级回退；缺失依赖 fail-closed 返回 500。
6. **删除 admin 导出的并行死子系统**：只保留 `ExportChunkedStream`、`UserFetcher` 与行映射；移除零调用的 slice/chunked 导出类型。
7. **重复通知合并**：注册验证与找回密码共用 `AuthOTPNotification`，仅标题由构造函数注入。

## 后果

- 正向：凭据与创建规则只有一个实现；feature 只保留用例编排；新增认证入口时无需再复制口令规则；客户端与管理端的 `role` 类型一致。
- 负向：`shared/user.Service` 现在依赖 `hasher`，装配必须注入同一实例；`Provider` 成为认证与凭据的双重深模块，修改口令规则会影响所有入口。
- 约束：生成类型仍是实体，不引入 Repository；客户端仍保留本地 `UserResource` 名称以稳定 Swagger 组件；对外 JSON 字段与状态码不变。
