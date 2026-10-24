# OpenTelemetry Collector Contrib 与 OpenObserve 接入最佳实践

本文档说明如何将本工程（Gin + Bun ORM）与已运行的 `otel/opentelemetry-collector-contrib` 以及 `OpenObserve` 可观测性平台联动配合。

---

## 1. 架构总览 (Architecture)

```
[ Gin Web API (bgin) ]
        │
        ▼  OTLP/gRPC (默认 :4317，经单连接多路复用高效推流)
[ otel/opentelemetry-collector-contrib ]
        │  内存熔断保护 (memory_limiter) + 批量缓冲 (batch)
        ▼  OTLP/HTTP (POST /api/{org}/v1/{traces|metrics|logs})
[ OpenObserve (:5080) ]
```

---

## 2. OpenObserve 认证配置 (Authentication)

OpenObserve 的 OTLP 接入依赖 HTTP Basic 认证：

1. **组织名 (Organization)**：默认通常为 `default`。
2. **凭据生成**：将你的 OpenObserve 登录账号与密码（或 ingestion 专用 token）转换为 Base64：
   ```bash
   echo -n "admin@gmail.com:Oo!ae13cf5850664c9a398194334d60383d" | base64
   # 输出例如: YWRtaW5AZ21haWwuY29tOk9vIWFlMTNjZjU4NTA2NjRjOWEzOTgxOTQzMzRkNjAzODNk
   ```
3. 在 Docker Compose 环境变量中注入：
   ```bash
   O2_AUTH="Basic YWRtaW5AZ21haWwuY29tOk9vIWFlMTNjZjU4NTA2NjRjOWEzOTgxOTQzMzRkNjAzODNk"
   ```

---

## 3. 在 bgin 应用中开启遥测 (Application Activation)

在 `.env` 或容器环境变量中设置：

```env
# 启用 OpenTelemetry 收集与上报
OTEL_ENABLED=true

# OTel Collector 目标地址（遵循 RFC URL 标准，若本地运行为 http://127.0.0.1:4317，容器内为 http://otel-collector:4317）
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4317

# 是否不使用 TLS 加密（内网互联或 Collector 在同一主机设为 true）
OTEL_INSECURE=true

# 服务标识（在 OpenObserve 中呈现的服务名）
OTEL_SERVICE_NAME=bgin

# 追踪采样率（1.0 表示 100% 全量采样，生产高吞吐环境建议根据需要调整如 0.2）
OTEL_TRACES_SAMPLE_RATIO=1.0

# 指标上报周期
OTEL_METRIC_INTERVAL=15s
```

---

## 4. 关键特性与链路排障优势

1. **Trace-to-Log 双向无缝穿梭**：
   - 所有的 Zap 结构化日志会自动提取当次请求的 `trace_id` 与 `span_id`；
   - 在 OpenObserve 的 **Logs** 页面中，带有 `trace_id` 的日志行可以直接点击一键跳转到具体的调用链甘特图；
   - 在 OpenObserve 的 **Traces** 页面中，点击任一 Span 可以直接查看该 Span 产生的所有关联日志。
2. **SQL 级深度下钻**：
   - Bun ORM 执行的每一条 SQL 操作自动生成名为 `db.<operation>` 的子 Span，并在属性中记录 `db.statement`（SQL 模板或语句）及耗时，轻松定位慢查询瓶颈。
3. **心跳探针自动过滤**：
   - `/livez`、`/readyz`、`/healthz` 等高频心跳路由默认不会生成 Span，不污染 OpenObserve 索引与存储。
4. **Go 运行时指标 (Runtime Metrics)**：
   - 自动采集并推送 Go GC 停顿、Heap 分配、活跃 Goroutine 数量，在 OpenObserve 可直接搭配 Dashboard 监控应用健康。
