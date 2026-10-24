
# RESTful API 接入指南

本文档面向所有接口调用方（Web、移动端、第三方系统与服务间集成），描述接入本 API 所需的地址、认证、请求头、响应结构、错误模型、分页查询、速率限制与版本演进策略。

所有接口遵循 REST 语义：请求与响应均为 `application/json`（编码 `UTF-8`），接口通过 URI 路径进行版本控制。

---

## 1. 快速开始与服务地址 (Quick Start & Endpoints)

### 1.1 快速开始 (Quick Start)

**第 1 步**：调用认证接口获取访问令牌。

```bash
curl -X POST https://api.example.com/v1/auth/sign-in/email \
  -H "Content-Type: application/json" \
  -H "Accept: application/json" \
  -d '{"email":"user@example.com","password":"<your_password>"}'
```

**第 2 步**：携带令牌调用受保护资源。

```bash
curl https://api.example.com/v1/users/me \
  -H "Accept: application/json" \
  -H "Authorization: Bearer <your_access_token>"
```

各端点的完整参数、请求示例与响应结构，请在本参考文档中按标签（Tag）浏览；每个端点均提供可直接运行的示例。

### 1.2 服务地址 (Base URL)

| 环境                         | API Base URL                           | 说明                   |
| :--------------------------- | :------------------------------------- | :--------------------- |
| **开发环境 (Dev)**     | `http://localhost:{port}/v1`         | 本地开发与联调         |
| **测试环境 (Staging)** | `https://staging-api.example.com/v1` | 集成测试与验收         |
| **生产环境 (Prod)**    | `https://api.example.com/v1`         | 线上环境（强制 HTTPS） |

- 版本号位于路径首段：`/{version}/{resources}`，当前稳定版本为 `v1`。
- 生产环境仅接受 **HTTPS** 请求，请勿在生产环境使用明文 HTTP。
- 请求与响应统一使用 `application/json`，字符集固定为 `UTF-8`。

---

## 2. 数据格式与交互约定 (Data Conventions)

### 2.1 命名约定 (Naming)

- **资源路径**：复数名词、全小写短横线，不使用动词（如 `/{version}/{resources}`）。
- **Query 参数与 JSON 键名**：小驼峰（`camelCase`），例如 `createdAt`、`sortBy`、`pageSize`。
- **布尔字段**：统一使用 `is` / `has` / `can` 驼峰前缀或清晰布尔语义，例如 `isActive`、`hasAttachments`、`banned`、`emailVerified`。
- **枚举值**：全小写，多词用下划线分隔，例如 `"user"`、`"admin"`、`"active"`、`"in_progress"`（与 AGENTS.md §5.3 的示范一致）。

### 2.2 时间与数值精度 (Timestamp & Precision)

- **时间**：统一采用 ISO 8601 / RFC 3339 格式，**UTC 时区并以 `Z` 结尾**，例如 `"2026-08-25T14:28:10Z"`。不会返回无时区标记或裸数值形式的时间。
- **大整数**：超出 JavaScript 安全整数范围（大于 2^53 - 1）的数值，例如 64 位整型标识、雪花 ID，在 JSON 中**以字符串（`string`）形式传输**。请按字符串解析并存储，避免精度丢失。
- **金额**：以货币最小整数单位（如分 `cents`）表示，或采用定点字符串（如 `"199.99"`）；请勿使用浮点数参与金额计算。

### 2.3 空值与更新语义 (Nullability & Update Semantics)

- **空集合**：无数据或空列表时，字段一律返回 `[]`（空数组），不会返回 `null`，可直接安全遍历。
- **PATCH 局部更新三态语义**：
  - **字段缺省（不传该字段）**：保持原值不变；
  - **显式传递 `null`（`"field": null`）**：明确置空或解绑该字段；
  - **传递非空具体值**：覆盖更新为该值。

### 2.4 输入校验与失败反馈 (Validation)

所有输入在服务边界完成校验。校验失败时统一返回 `422 Unprocessable Entity`，并在响应的 `errors` 字段中按字段名归集失败原因，调用方可直接映射到表单控件（详见 [5.2 错误响应](#52-错误响应-error-response)）。

---

## 3. 认证、请求头与并发控制 (Authentication, Headers & Concurrency)

### 3.1 认证方式 (Authentication)

除公开端点外，所有受保护请求必须携带 Bearer Token：

```http
Authorization: Bearer <your_access_token>
```

| 状态码               | 含义                                   | 调用方处理                                         |
| :------------------- | :------------------------------------- | :------------------------------------------------- |
| `401 Unauthorized` | 未携带凭证，或令牌已过期、失效、被撤销 | 使用刷新令牌换取新令牌后重试；刷新失败引导重新登录 |
| `403 Forbidden`    | 凭证有效，但无权访问该资源或执行该操作 | 不要重试；提示用户权限不足或联系管理员             |

**安全提示**：请仅在请求头中传递令牌，不要将令牌拼接到 URL、写入日志或落盘到客户端明文存储。

**令牌载荷说明**：访问令牌除标准声明外还携带 `role`（系统角色）与 `tokenType`（令牌类型，取值 `access` / `refresh`）两项声明，授权判定直接依据 `role`。**角色变更不会作用于已签发的访问令牌**：旧令牌会继续携带旧角色直到过期；续签（`POST /v1/auth/token/refresh`）时角色取数据库中的最新值，因此改角色会在下一次续签后生效。如需让某枚令牌立即失效，请使用登出/撤销接口将其写入黑名单。`role` 属于服务端内部声明，客户端不得据此做任何权限判断，一切以接口返回的 `403 Forbidden` 为准。

**多实例部署说明**：令牌撤销与限流配额由服务端共享存储承载，任一副本上的登出/撤销对所有副本立即生效，无需会话粘性。

### 3.2 标准请求头 (Request Headers)

| 请求头                        | 必需 | 描述                                                     | 示例                                     |
| :---------------------------- | :--: | :------------------------------------------------------- | :--------------------------------------- |
| **`Content-Type`**    |  ✅  | 请求体媒体类型                                           | `application/json`                     |
| **`Accept`**          |  ✅  | 客户端期望的响应格式                                     | `application/json`                     |
| **`Authorization`**   | 条件 | 访问受保护资源的 Bearer Token                            | `Bearer eyJhbGci...`                   |
| **`x-request-id`**    |  ❌  | 全链路追踪 ID；未提供时由服务端生成，并在响应头回写      | `req_019572d4a6c878b18cf0`             |
| **`Idempotency-Key`** | 可选 | 非幂等写操作的去重键；携带则去重并回放首次响应，未携带行为不变 | `019572d4-a6c8-78b1-8cf0-281b94c032a1` |
| **`If-None-Match`**   | 未实现 | 条件读取：资源未变化时返回`304 Not Modified`，节省带宽 | `W/"019572d4a6c878b1"`                 |

> 上表标为「未实现」的请求头当前**会被服务端忽略**，不产生任何效果，详见 3.4。
>
> 本 API 只使用 `application/json`：不实现 415/406 内容协商——`Accept` 与 `Content-Type` 不匹配不会单独报错，请求体结构有问题时仍返回 400。

### 3.3 幂等请求 (Idempotency)

> **已接线（探测模式）。** 服务端已挂载幂等中间件：携带 `Idempotency-Key` 的写请求会按该键去重、并在重复提交时回放首次响应；**未携带时行为与从前完全一致**（不强制要求，避免破坏既有客户端与渠道回调）。以下语义适用于携带该头的请求。

对创建类、支付类等非幂等写操作，建议携带 `Idempotency-Key`：

- **Key 取值**：推荐使用 UUIDv7 或 ULID 等全局唯一、单调递增的字符串。
- **重试保持稳定**：同一业务意图的多次重试必须复用同一个 Key；每次重试生成新 Key 将失去去重保护。
- **服务端行为**：
  - 相同 Key、相同请求载荷：返回首次请求的结果，不会重复执行；
  - 相同 Key、不同请求载荷：拒绝执行并返回 `422 Unprocessable Entity`；
  - 相同 Key 的首次请求仍在处理中：返回 `409 Conflict`，请退避后重试；
  - Key 在约定的保留期内有效，覆盖常规重试与异步补偿周期。

### 3.4 条件读取与缓存协商 (Conditional Reads)

> **当前版本不支持。** 本版本不返回 `ETag`，也不处理 `If-None-Match`，`304 Not Modified` 不会被返回；所有 `GET` 均返回完整响应体。以下为规划中的语义。

- `GET /{version}/{resources}/{id}` 的响应头携带实体标签：`ETag: W/"<hash>"`。
- 后续请求可携带 `If-None-Match: W/"<hash>"`；资源未变化时服务端返回 **`304 Not Modified`**（无响应体），调用方可直接复用本地缓存以节省带宽。

> **关于并发更新**
>
> 本版本**不提供** `If-Match` / `412 Precondition Failed` 的条件更新能力。并发写同一资源时为
> **后写覆盖（last-write-wins）**，服务端不会返回冲突信号。
>
> 调用方如需避免并发覆盖，需在应用层自行处理：读取时记录 `updatedAt`，提交前比对服务端最新值，
> 不一致时由业务侧决定合并或提示用户。请勿依赖 `If-Match` —— 该请求头当前被忽略。

---

## 4. HTTP 方法与状态码 (HTTP Methods & Status Codes)

### 4.1 HTTP 动词语义

|         方法         | 语义                         |     幂等性     |                    成功返回                    | 典型场景                       |
| :------------------: | :--------------------------- | :------------: | :---------------------------------------------: | :----------------------------- |
|  **`GET`**  | 获取单个资源或列表           |    ✅ 幂等    |                   `200 OK`                   | 读取详情、分页查询             |
|  **`POST`**  | 创建新资源或执行非 CRUD 操作 |   ❌ 非幂等   | `201 Created` / `202 Accepted` / `200 OK` | 创建实体、提交认证、异步长任务 |
|  **`PUT`**  | 全量覆盖替换目标资源         |    ✅ 幂等    |                   `200 OK`                   | 完整替换目标配置               |
| **`PATCH`** | 局部修改目标资源属性         | 视具体端点而定 |                   `200 OK`                   | 局部更新字段、变更状态         |
| **`DELETE`** | 销毁或注销目标资源           |    ✅ 幂等    |         `204 No Content` / `200 OK`         | 删除资源、解绑关联             |

### 4.2 响应状态码速查

| 状态码       | `code` 字段 / 状态语义        | 说明                        |
| :-------- | :---------------------- | :------------------------ |
| **`200`** | `OK`                    | 请求处理成功                    |
| **`201`** | `CREATED`               | 资源创建成功                    |
| **`202`** | `ACCEPTED`              | 异步长任务已受理排队（当前版本不返回，见 5.1） |
| **`204`** | `NO_CONTENT`            | 请求成功，无返回数据体（0 字节）         |
| **`304`** | `NOT_MODIFIED`          | 资源未变更（当前版本不返回，见 3.4）      |
| **`400`** | `BAD_REQUEST`           | 请求格式无效、缺少必填参数或语法错误        |
| **`401`** | `AUTH_TOKEN_INVALID` 等  | 未携带凭证，或令牌无效、已过期、已被撤销      |
| **`403`** | `FORBIDDEN` 等           | 凭证有效但无权执行该操作（权限不足或账号受限）   |
| **`404`** | `NOT_FOUND`             | 请求的资源或 URI 不存在            |
| **`405`** | `METHOD_NOT_ALLOWED`    | 请求的 HTTP 方法不受支持          |
| **`409`** | `CONFLICT` 等            | 资源状态冲突（如唯一键重复）            |
| **`413`** | `REQUEST_TOO_LARGE`     | 请求体超过服务端上限（普通请求 1 MiB；multipart 上传按 `MEDIA_MAX_FILE_SIZE`） |
| **`422`** | `VALIDATION_FAILED` 等   | 请求体字段语义或参数验证规则校验失败        |
| **`429`** | `RATE_LIMITED`          | 请求频率超出速率配额限制              |
| **`500`** | `INTERNAL_SERVER_ERROR` | 服务器内部异常，未捕获错误统一转为通用提示     |
| **`504`** | `GATEWAY_TIMEOUT`       | 请求处理超时（上游数据库或依赖未在时限内响应） |

> 关于第二列：2xx 与 304 的成功响应**不含 `code` 字段**（成功响应直接返回业务对象本身，见 5.1），该列对这些行给出的是状态语义名；4xx 与 5xx 行给出的是错误响应体里 `code` 字段的字面取值，标为「等」表示该状态码对应多个具体错误码，完整清单见 5.2 的错误码清单。

---

## 5. 响应格式与错误处理 (Response Structure & Error Handling)

接口响应遵循简洁一致的契约：**仅分页列表**使用 `{"items": [...], "meta": {...}}` 信封，其余成功响应一律直接返回业务对象本身，不含 `success`、`code`、`message` 等外层包装。

### 5.1 成功响应 (Success Response)

#### 1. 分页列表响应

所有分页列表统一包裹在 `items` 与 `meta` 中：

```json
{
  "items": [
    {
      "id": "<resource_id>",
      "<attribute>": "<value>"
    }
  ],
  "meta": {
    "page": 1,
    "pageSize": 20,
    "total": 100,
    "totalPages": 5
  }
}
```

空列表同样保持信封结构，`items` 固定为 `[]`：

```json
{
  "items": [],
  "meta": {
    "page": 1,
    "pageSize": 20,
    "total": 0,
    "totalPages": 0
  }
}
```

#### 2. 单资源与操作结果

查询、创建、更新等非分页响应直接返回业务对象：

```json
{
  "id": "<resource_id>",
  "<attribute>": "<value>"
}
```

#### 3. 异步任务受理 (`202 Accepted`)

> **当前版本不支持。** 本版本没有异步任务端点，不会返回 `202 Accepted`；同步操作直接返回 `200` / `201` / `204`。以下为规划中的语义。

长耗时操作受理后返回 `202 Accepted`，并在 `Location` 响应头给出任务查询地址：

```http
HTTP/1.1 202 Accepted
Location: /{version}/tasks/<taskId>
```

```json
{
  "taskId": "<taskId>",
  "status": "PENDING"
}
```

#### 4. 无返回数据体 (`204 No Content`)

删除、登出、修改密码等无返回内容的操作统一返回 `204 No Content`，响应体为空（0 字节），请勿解析响应体。

### 5.2 错误响应 (Error Response)

所有错误响应统一采用扁平根级结构，包含机器可读的 `code` 与人类可读的 `message` 描述信息。

#### 1. 通用业务与客户端错误 (`400` / `401` / `403` / `404` / `409`)

```json
{
  "code": "<MACHINE_READABLE_ERROR_CODE>",
  "message": "<human_readable_error_description>"
}
```

#### 2. 字段级校验失败 (`422 Unprocessable Entity`)

按字段键归集错误文案数组，可直接用于表单控件报错：

```json
{
  "code": "VALIDATION_FAILED",
  "message": "Validation failed",
  "errors": {
    "teamName": [
      "The team name must be a string.",
      "The team name must be at least 1 characters."
    ],
    "authorization.role": [
      "The selected authorization.role is invalid."
    ],
    "users.0.email": [
      "The users.0.email field is required."
    ],
    "users.2.email": [
      "The users.2.email must be a valid email address."
    ]
  }
}
```

#### 3. 服务器内部异常 (`500 Internal Server Error`)

```json
{
    "code": "INTERNAL_SERVER_ERROR",
    "message": "An unexpected error occurred"
}
```

#### 4. 错误码清单

下表是 `code` 字段的全部取值。客户端应据 `code` 分支，`message` 仅供展示。

| `code`                      | 状态码 | 触发时机                                             |
| :-------------------------- | :----: | :--------------------------------------------------- |
| `BAD_REQUEST`               |  400   | 请求体不是合法 JSON，或路径/查询参数格式非法         |
| `VALIDATION_FAILED`         |  422   | 字段校验失败，`errors` 按字段路径归集文案            |
| `NOT_FOUND`                 |  404   | 目标资源不存在                                       |
| `METHOD_NOT_ALLOWED`        |  405   | 请求的 HTTP 方法不受支持（响应头包含 Allow 允许的动词列表） |
| `FORBIDDEN`                 |  403   | 凭证有效但无权执行该操作                             |
| `CONFLICT`                  |  409   | 资源状态冲突（如唯一键重复）                         |
| `RATE_LIMITED`              |  429   | 超出速率配额                                         |
| `REQUEST_TOO_LARGE`         |  413   | 请求体超过服务端上限（普通请求 1 MiB、multipart 上传按 `MEDIA_MAX_FILE_SIZE`），重试无用、须改小载荷 |
| `IDEMPOTENCY_REQUIRED`      |  400   | 该端点要求 `Idempotency-Key` 却未提供                |
| `IDEMPOTENCY_CONFLICT`      |  409   | 同一幂等键的请求正在处理中，请稍后重试               |
| `IDEMPOTENCY_MISMATCH`      |  422   | 同一幂等键重复提交但载荷不一致                       |
| `GATEWAY_TIMEOUT`           |  504   | 请求处理超时（上游数据库或外部依赖未在时限内响应）   |
| `CLIENT_CLOSED_REQUEST`     |  499   | 客户端在服务端响应完成前主动终止了连接（如关闭网页或切断网络） |
| `SERVICE_UNAVAILABLE`       |  503   | 外部依赖暂不可用或服务暂未启用                       |
| `INTERNAL_SERVER_ERROR`     |  500   | 未捕获的服务器内部异常                               |
| `AUTH_INVALID_CREDENTIALS`  |  401   | 邮箱/用户名或口令不正确（账号不存在返回同一码，防枚举） |
| `AUTH_EMAIL_ALREADY_EXISTS` |  409   | 邮箱已被占用                                         |
| `AUTH_USERNAME_TAKEN`       |  409   | 用户名已被占用                                       |
| `AUTH_PHONE_NUMBER_TAKEN`   |  409   | 手机号已被占用                                       |
| `AUTH_ACCOUNT_BANNED`       |  403   | 账号已被封禁                                         |
| `AUTH_EMAIL_NOT_VERIFIED`   |  403   | 邮箱未验证，且服务端开启了强制验证                   |
| `AUTH_OTP_INVALID`          |  422   | 邮箱验证码或密码重置凭据不正确                       |
| `AUTH_OTP_EXPIRED`          |  422   | 验证码或重置凭据已过期或已被使用                     |
| `AUTH_OTP_TOO_MANY_ATTEMPTS`|  429   | 同一凭据的失败尝试次数超限                           |
| `AUTH_OTP_RATE_LIMITED`     |  429   | 同一邮箱请求凭据过于频繁                             |
| `AUTH_TOKEN_INVALID`        |  401   | 访问令牌或刷新令牌无效                               |
| `AUTH_TOKEN_EXPIRED`        |  401   | 令牌已过期                                           |
| `AUTH_TOKEN_REVOKED`        |  401   | 令牌已被撤销（例如登出后再次使用）                   |
| `AUTH_PASSWORD_MISMATCH`    |  422   | 修改口令时当前口令不正确                             |
| `AUTH_PASSWORD_TOO_WEAK`    |  422   | 新口令不满足强度要求                                 |
| `EXCEL_FILE_TOO_LARGE`      |  413   | 导入文件超过 `MEDIA_MAX_FILE_SIZE` 或 excel 子系统上限 |
| `EXCEL_OPEN_ERROR`          |  400   | 导入文件无法被解析为合法工作簿（损坏或格式不符）     |
| `EXCEL_READ_ERROR`          |  400   | 读取上传流失败（连接中断或中途截断）                 |
| `EXCEL_MAP_ERROR`           |  422   | 行数据无法映射到目标字段类型                         |
| `EXCEL_ROWS_ERROR`          |  422   | 工作表行结构不合法（如缺少表头行）                   |
| `EXCEL_READ_ROW_ERROR`      |  422   | 逐行读取时该行内容不合法                             |
| `EXCEL_DUPLICATE_ROW`       |  409   | 导入数据行存在重复键（文件内重复或数据库已存在）     |
| `EMPTY_SHEETS`              |  400   | 工作簿内没有任何可导入的工作表                       |
| `CSV_READ_ERROR`            |  400   | CSV 输入流不合法或无法读取                           |
| `NOTIFICATION_MISSING_RECIPIENT`    |  400   | 通知未指定接收方实体                         |
| `NOTIFICATION_MISSING_RECIPIENT_ID` |  400   | 接收方实体缺少有效标识                       |
| `NOTIFICATION_UNSUPPORTED_SMS`      |  400   | 该通知未实现短信承载方式                     |
| `NOTIFICATION_UNSUPPORTED_MAIL`     |  400   | 该通知未实现邮件承载方式                     |
| `NOTIFICATION_UNSUPPORTED_DATABASE` |  400   | 该通知未实现数据库承载方式                   |
| `NOTIFICATION_UNSUPPORTED_REALTIME` |  400   | 该通知未实现实时广播承载方式                 |
| `NOTIFICATION_UNSUPPORTED_GOTIFY`   |  400   | 该通知未实现 Gotify 承载方式                 |
| `NOTIFICATION_UNSUPPORTED_CHAT`     |  400   | 该通知未实现即时通讯承载方式                 |
| `NOTIFICATION_CHANNEL_NOT_FOUND`    |  400   | 请求了未注册的通知渠道                       |
| `NOTIFICATION_RATE_LIMITED`         |  429   | 同一接收方在同一渠道的通知频次超限           |
| `NOTIFICATION_DEDUPLICATED`         |  409   | 相同幂等键的通知已投递过                     |
| `MAIL_MISSING_RECIPIENT`    |  400   | 邮件通知未提供收件地址                               |
| `SMS_MISSING_PHONE`         |  400   | 短信通知未提供手机号                                 |
| `INVALID_SMS_MESSAGE`       |  400   | 短信载荷缺少模板或参数                               |
| `INVALID_GOTIFY_MESSAGE`    |  400   | Gotify 载荷缺少必填字段                              |
| `INVALID_CHAT_MESSAGE`      |  400   | 即时通讯载荷缺少必填字段                             |
| `PAYMENT_CAPABILITY_NOT_SUPPORTED` |  400   | 该渠道/应用不支持所请求的支付能力            |
| `DATABASE_UNAVAILABLE`      |  503   | 数据库依赖暂不可用（可重试）                         |

> 本清单与服务端的错误码定义一一对应，新增错误码必须同时更新此处。
>
> 服务端内部还有一批"仅用于日志与堆栈定位"的失败码（如 `EXCEL_SPOOL_COPY_ERROR`、
> `TENCENT_HTTP_ERROR`）。它们**不会**出现在响应体里：这类失败一律按 `500` 返回
> `INTERNAL_SERVER_ERROR`，原始码只留在服务端日志中。因此上表即客户端可能收到的全部取值。

#### 5. 追踪与重试

- 每次响应的 `x-request-id` 响应头唯一标识该次请求，请在反馈问题时一并提供；
- 生产环境不会返回堆栈信息、源码路径或数据库错误细节；
- `429` 与 `5xx` 可重试，请遵循 [7. 速率限制与重试策略](#7-速率限制与重试策略-rate-limiting--retries) 中的退避建议；
- `4xx` 中的参数、权限与状态冲突类错误，请在修正请求后重试。

---

## 6. 分页、过滤与排序 (Pagination, Filtering & Sorting)

分页列表统一采用 **Offset 偏移分页**：通过 `page` 与 `pageSize` 指定位置与页大小，响应中的 `meta.total` 与 `meta.totalPages` 可用于渲染分页器。

### 6.1 请求与响应

- **请求参数**：`page`（默认 `1`）、`pageSize`（默认 `20`，最大 `100`）。
- **响应结构**：

```json
{
  "items": [
    { "id": "<resource_id>", "<attribute>": "<value>" }
  ],
  "meta": {
    "page": 1,
    "pageSize": 20,
    "total": 100,
    "totalPages": 5
  }
}
```

### 6.2 过滤、排序与检索

- **字段过滤**：
  - 精确匹配：`?filter[<field>]=<value>`
  - 多值匹配（IN 查询）：`?filter[<field>]=<val1>,<val2>`
  - 区间过滤：`?filter[<field>After]=<date>` 或 `?filter[<field>Min]=<num>`
- **链式排序**：
  - 升序：`?sort=<field>`；降序：`?sort=-<field>`；复合排序：`?sort=-<field1>,<field2>`
  - 服务端始终提供确定性的默认排序（如按创建时间倒序），分页遍历不会出现数据漂移。
- **全局搜索**：`?search=<keyword>`（在核心可检索列执行模糊匹配）。
- **关联加载与字段投影**：
  - 关联预加载：`?include=<relation1>,<relation2>`
  - 稀疏字段集：`?fields[<resource>]=<field1>,<field2>`（精简响应体积）
- **综合请求示例**：

```http
GET /{version}/{resources}?page=1&pageSize=20&search=<keyword>&filter[<field>]=<value>&sort=-<field>&include=<relation>
```

---

## 7. 速率限制与重试策略 (Rate Limiting & Retries)

接口在网关与中间件层实施多级配额控制：

- **已认证用户**：标准业务并发配额；
- **敏感认证端点**：防暴力破解的严格限制。

支付与实时回调端点**不做限流**：渠道服务端的阶梯重试是合法流量，限流会误伤、极端情况下导致漏单；这两个端点依靠签名校验（渠道验签 / HMAC）与幂等防重放抵御滥用。

超出配额时返回 `429 Too Many Requests`，并通过标准响应头给出配额状态：

```http
HTTP/1.1 429 Too Many Requests
Retry-After: 30
X-RateLimit-Limit: 120
X-RateLimit-Remaining: 0
X-RateLimit-Reset: 1756137600
```

**调用方建议**：

- 优先遵循 `Retry-After` 指定的等待秒数；
- 采用指数退避并加入随机抖动，避免同一时刻集中重试形成二次冲击；
- 写操作重试务必复用同一个 `Idempotency-Key`；
- 对 `429`、`5xx` 与网络超时进行退避重试；`4xx` 业务错误不应重试。

---

## 8. 版本演进、废弃策略与支持 (Versioning, Deprecation & Support)

### 8.1 版本演进

接口通过向前兼容的方式演进，同一大版本内不会引入破坏性变更：

- **非破坏性扩展**：新增可选入参、在响应中新增只读字段、新增专用端点；
- **破坏性变更**：修改字段类型、将可选参数改为必填、删除或改名既有字段、变更既有成功状态码——此类变更仅通过新版本（如 `/v2`）提供。

请调用方对响应中的新增字段保持宽容（忽略未知字段），以平滑获取新能力。

### 8.2 废弃通告

> **当前版本不支持。** 本版本不会被标记废弃的端点，也不会返回 `Deprecation` / `Sunset` / `Link` 响应头。以下为规划中的做法。

端点或字段确需下线时，会提前通过 RFC 8594 标准响应头通告：

```http
Deprecation: @1756137600
Sunset: Wed, 11 Nov 2026 00:00:00 GMT
Link: <https://api.example.com/v2/{resources}>; rel="successor-version"
```

- `Deprecation`：废弃生效时间（Unix 秒）；
- `Sunset`：该能力停止服务的时间，请在此之前完成迁移；
- `Link`：替代版本或端点地址。

### 8.3 支持与反馈

- **联系人**：Developer Support（`dev@example.com`）；
- **问题反馈**：请提供请求端点、时间点与响应头中的 `x-request-id`，以便快速定位链路。
