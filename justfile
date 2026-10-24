# bgin 常用工程任务

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

# 直接运行 Temporal 工作流 Worker（just work-temporal -q bgin-tasks）
work-temporal *ARGS:
    go run ./cmd/api temporal:work {{ARGS}}

# 运行 CLI（just cli route:list）
cli *ARGS:
    go run ./cmd/api {{ARGS}}

alias artisan := cli

# 释放端口（默认 APP_PORT）
free-port port="":
    @port="{{port}}"; port="${port:-${APP_PORT:-3300}}"; pids=$(lsof -ti tcp:$port -sTCP:LISTEN || true); if [ -z "$pids" ]; then echo "端口 $port 空闲"; else echo "端口 $port 被占用（PID $(echo $pids)），结束它"; kill $pids 2>/dev/null || true; sleep 1; kill -9 $(lsof -ti tcp:$port -sTCP:LISTEN 2>/dev/null || true) 2>/dev/null || true; echo "端口 $port 已释放"; fi

# 编译到 bin/bgin
build:
    go build -trimpath \
        -ldflags "-X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo dev) \
                  -X main.commit=$(git rev-parse --short HEAD 2>/dev/null || echo none) \
                  -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
        -o bin/bgin ./cmd/api

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
    go test -tags=integration -count=1 ./tests/integration/...

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
check: fmt-check lint test

# 完整 CI 门禁
ci: fmt-check lint test-all

# 压测（wrk）
bench path="/livez" *args="":
    wrk -t4 -c200 -d30s --latency {{args}} "http://localhost:${APP_PORT:-3300}{{path}}"

alias wrk := bench

# 应用未执行的 Atlas 迁移
migrate:
    atlas migrate apply --env app

# 应用未执行的 Atlas 迁移至测试库（bgin_test）
migrate-test:
    atlas migrate apply --env test

# 依 schemas/ 声明式 DDL 生成版本迁移
migrate-diff name:
    atlas migrate diff {{name}} --env local

# 重算迁移哈希校验链
migrate-hash:
    atlas migrate hash --dir file://migrations

# 幂等注入首个管理员
seed:
    go run ./cmd/api db:seed

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

# Redis/Valkey 终端
redis:
    docker exec -it -e REDISCLI_AUTH="$(echo "$REDIS_URL" | sed -E 's#^redis://[^@]*:([^@]*)@.*#\1#')" valkey valkey-cli

alias cache-shell := redis

# 生成前后台两套 OpenAPI 文档；客户端只收 client/*+realtime，管理端只收 admin/*，
# payment 是渠道回调端点，两套都不收录（AGENTS §5.2）
docs:
    swag init -d "$(find cmd internal -type f -name '*.go' ! -path '*/admin/*' ! -path '*/admin' ! -path '*/features/payment*' -exec dirname {} + | sort -u | paste -sd, -)" -g main.go -o internal/shared/apidocs --parseInternal --outputTypes json
    swag init -d "$(find cmd internal -type f -name '*.go' ! -path '*/features/client*' ! -path '*/features/realtime*' ! -path '*/features/payment*' -exec dirname {} + | sort -u | paste -sd, -)" -g admin_doc.go -o internal/shared/apidocs/admin_tmp --parseInternal --outputTypes json
    mv internal/shared/apidocs/admin_tmp/swagger.json internal/shared/apidocs/admin_swagger.json
    rm -rf internal/shared/apidocs/admin_tmp

# 静态检查 Helm Chart
helm-lint:
    helm lint ./helm/bgin

# 渲染测试 Helm Manifests（支持参数，如 just helm-template --set api.replicaCount=3）
helm-template *ARGS:
    helm template bgin ./helm/bgin {{ARGS}}

# 更新 Helm 子 Chart 依赖
helm-dep:
    helm dependency update ./helm/bgin

# 登录 CNB 或指定 OCI Helm 制品库 (默认取环境变量或参数)
helm-login token="" user="" registry="":
    @reg="{{registry}}"; \
     reg="${reg:-${CNB_HELM_REGISTRY:-helm.cnb.cool}}"; \
     tok="{{token}}"; \
     tok="${tok:-${CNB_TOKEN}}"; \
     usr="{{user}}"; \
     usr="${usr:-${CNB_TOKEN_USER_NAME:-cnb}}"; \
     if [ -z "$tok" ]; then \
       echo "Error: Token required. Usage: just helm-login <token> [user] [registry] (or set CNB_TOKEN)"; exit 1; \
     fi; \
     helm registry login -u "$usr" -p "$tok" "$reg"

# 本地打包 Helm Chart 到 dist/ 目录
helm-package:
    mkdir -p ./helm/dist
    helm dependency build ./helm/bgin
    helm package ./helm/bgin -d ./helm/dist

# 本地推送 Helm Chart 至 CNB 或目标 OCI 制品库 (默认取 CNB_HELM_REGISTRY 与 CNB_REPO_SLUG_LOWERCASE)
helm-push version="0.1.0" registry="":
    @reg="{{registry}}"; \
     reg="${reg:-${CNB_HELM_REGISTRY:-helm.cnb.cool}/${CNB_REPO_SLUG_LOWERCASE:-ailuoga/gin-bun}}"; \
     mkdir -p ./helm/dist; \
     helm dependency build ./helm/bgin; \
     helm package ./helm/bgin --version "{{version}}" --app-version "{{version}}" -d ./helm/dist; \
     helm push "./helm/dist/bgin-{{version}}.tgz" "oci://${reg}"

# 初始化本地 age 密钥对并打印公钥（保存在 .local/age.key）
sops-init:
    @mkdir -p .local
    @if [ -f .local/age.key ]; then \
        echo "age key already exists at .local/age.key"; \
    else \
        age-keygen -o .local/age.key 2>/dev/null; \
        chmod 600 .local/age.key; \
        echo "Generated new age keypair at .local/age.key"; \
    fi
    @pub="$(age-keygen -y .local/age.key 2>/dev/null)"; \
     echo "=================================================="; \
     echo "Local age Public Key: $pub"; \
     echo "Please ensure this public key is added to .sops.yaml"; \
     echo "=================================================="

# 原地编辑或创建环境密文：helm/bgin/secrets.<env>.enc.yaml
sops-edit env="prod":
    @export SOPS_AGE_KEY_FILE="${SOPS_AGE_KEY_FILE:-.local/age.key}"; \
     sops "helm/bgin/secrets.{{env}}.enc.yaml"

# 查看解密后的环境敏感配置内容
sops-view env="prod":
    @export SOPS_AGE_KEY_FILE="${SOPS_AGE_KEY_FILE:-.local/age.key}"; \
     dst="helm/bgin/secrets.{{env}}.enc.yaml"; \
     if [ ! -f "$dst" ]; then \
       echo "Error: Encrypted file $dst not found."; exit 1; \
     fi; \
     sops -d "$dst"

# 从未加密模板加密生成环境密文：helm/bgin/secrets.<env>.yaml -> secrets.<env>.enc.yaml
sops-encrypt env="prod":
    @src="helm/bgin/secrets.{{env}}.yaml"; \
     dst="helm/bgin/secrets.{{env}}.enc.yaml"; \
     if [ ! -f "$src" ]; then \
       echo "Error: Source file $src not found. Copy from secrets.example.yaml first."; exit 1; \
     fi; \
     sops -e "$src" > "$dst" && echo "Successfully encrypted $src -> $dst"

# 使用 SOPS 解密后的环境配置进行本地测试渲染 (无需落地明文)
helm-template-env env="prod" *ARGS:
    @export SOPS_AGE_KEY_FILE="${SOPS_AGE_KEY_FILE:-.local/age.key}"; \
     src="helm/bgin/secrets.{{env}}.enc.yaml"; \
     if [ ! -f "$src" ]; then \
       echo "Error: $src not found. Create it with just sops-edit {{env}} first."; exit 1; \
     fi; \
     sops exec-file "$src" 'helm template bgin ./helm/bgin -f ./helm/bgin/values.yaml -f {} {{ARGS}}'

