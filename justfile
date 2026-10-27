# cgin 常用工程任务

set shell := ["bash", "-euo", "pipefail", "-c"]
set dotenv-load := true

# 列出全部任务
default:
    @just --list

# 并行热重载启动 API 与 Worker
dev: free-port
    concurrently -n "api,worker" -c "bgCyan.black.bold,bgYellow.black.bold" --kill-others \
        "just dev-api" \
        "just dev-worker"

# 仅热重载 API（Air）
dev-api: free-port
    air

# 仅热重载 Worker（Air）
dev-worker:
    air -c .air.worker.toml

# 直接运行 API（just run --port 3400）
run *ARGS:
    @just free-port "$(printf '%s' '{{ARGS}}' | sed -nE 's/.*(-p|--port)[= ]+([0-9]+).*/\2/p' | tail -1)"
    go run ./cmd/api serve {{ARGS}}

# 直接运行 Worker（just work -q high:3,default:1）
work *ARGS:
    go run ./cmd/api queue:work {{ARGS}}

# 运行 CLI（just cli route:list）
cli *ARGS:
    go run ./cmd/api {{ARGS}}

alias artisan := cli

# 初始化应用环境（.env、双密钥、数据库、迁移、数据播种与 S3 桶）
init *ARGS:
    go run ./cmd/api app:init {{ARGS}}

# 释放端口（默认 APP_PORT）
free-port port="":
    @port="{{port}}"; port="${port:-${APP_PORT:-3300}}"; pids=$(lsof -ti tcp:$port -sTCP:LISTEN || true); if [ -z "$pids" ]; then echo "端口 $port 空闲"; else echo "端口 $port 被占用（PID $(echo $pids)），结束它"; kill $pids 2>/dev/null || true; sleep 1; kill -9 $(lsof -ti tcp:$port -sTCP:LISTEN 2>/dev/null || true) 2>/dev/null || true; echo "端口 $port 已释放"; fi

# 编译到 bin/cgin
build:
    go build -trimpath \
        -ldflags "-X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo dev) \
                  -X main.commit=$(git rev-parse --short HEAD 2>/dev/null || echo none) \
                  -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
        -o bin/cgin ./cmd/api

# 整理 go.mod
tidy:
    go mod tidy

# 清理构建产物
clean:
    rm -rf bin tmp coverage.out coverage.html

# 单元测试
test:
    go test ./...

# 集成测试（需外部服务，-tags=integration）
test-int:
    go test -tags=integration -count=1 ./test/integration/...

# 单元 + 集成测试
test-all: test test-int

# 全量覆盖率报告
coverage:
    go test -tags=integration -count=1 -covermode=atomic -coverpkg=./internal/... -coverprofile=coverage.out ./...
    go tool cover -func=coverage.out | tail -1
    @echo "明细：go tool cover -html=coverage.out"

# 静态检查
lint:
    golangci-lint run

# 格式化并就地改写
fmt:
    golangci-lint fmt

# 仅检查格式
fmt-check:
    golangci-lint fmt --diff

# 提交前门禁
check: fmt-check lint sqlc-diff test

# 完整 CI 门禁
ci: fmt-check lint sqlc-diff test-all

# 由 database/schemas + database/queries 离线生成类型安全数据访问层（输出 internal/database/sqlc）
sqlc:
    sqlc generate

# 校验生成物与 SQL 事实源一致（生成物禁止手改）
sqlc-diff:
    sqlc diff

# 压测（wrk）
bench path="/livez" *args="":
    wrk -t4 -c200 -d30s --latency {{args}} "http://localhost:${APP_PORT:-3300}{{path}}"

alias wrk := bench

# 应用未执行的 Atlas 迁移
migrate:
    atlas migrate apply --env app

# 应用未执行的 Atlas 迁移至测试库（cgin_test）
migrate-test:
    atlas migrate apply --env test

# 依 database/schemas 声明式 DDL 生成版本迁移
migrate-diff name:
    atlas migrate diff {{name}} --env local

# 重算迁移哈希校验链
migrate-hash:
    atlas migrate hash --dir file://database/migrations

# 幂等注入首个管理员或运行种子数据
seed *ARGS:
    go run ./cmd/api db:seed {{ARGS}}

# 重建数据库并重新播种（需确认）
fresh:
    go run ./cmd/api db:fresh

# 重建数据库并重新播种（跳过确认）
fresh-force:
    go run ./cmd/api db:fresh --force

# 生成或更新 JWT 签名密钥
jwt-secret *ARGS:
    go run ./cmd/api jwt:secret {{ARGS}}

# 下载或更新离线 GeoIP 数据库
geoip-update *ARGS:
    go run ./cmd/api geoip:update {{ARGS}}

# PostgreSQL 终端
db:
    psql "$DB_URL"

alias db-shell := db


# 生成前后台两套 OpenAPI 文档。
# 分层扁平化后同层包混装两侧端点，无法再靠目录切分，改为按 Swagger Tag 收敛收录范围（AGENTS §5.2）：
#   客户端规范 = 认证中心 + 个人资料 + 实时通信，并排除 admin 子目录减少解析开销；
#   管理端规范 = 用户管理 + 管理员认证。
# 支付中心（渠道回调）两套都不收录。
docs:
    swag init -d "$(find cmd internal pkg shared -type f -name '*.go' -exec dirname {} + | sort -u | paste -sd, -)" -g main.go -o shared/apidocs --parseInternal --outputTypes json --exclude internal/handler/admin,internal/dto/admin,internal/service/admin,internal/route/admin -t "认证中心,个人资料,实时通信"
    swag init -d "$(find cmd internal pkg shared -type f -name '*.go' -exec dirname {} + | sort -u | paste -sd, -)" -g admin_doc.go -o shared/apidocs/admin_tmp --parseInternal --outputTypes json -t "用户管理,管理员认证"
    mv shared/apidocs/admin_tmp/swagger.json shared/apidocs/admin_swagger.json
    rm -rf shared/apidocs/admin_tmp

