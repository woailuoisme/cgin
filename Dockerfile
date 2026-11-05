# ==============================================================================
# 阶段一：构建二进制 (Alpine 3.24 + 纯 Go 静态编译，零 CGO)
# ==============================================================================
FROM golang:1.27.3-alpine3.24 AS builder

WORKDIR /src

# 1. 预先复制依赖清单，利用 BuildKit 缓存挂载加速 go mod 下载
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
  go mod download

# 2. 复制全部源代码并利用 Go 编译缓存加速纯静态构建
COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

RUN --mount=type=cache,target=/go/pkg/mod \
  --mount=type=cache,target=/root/.cache/go-build \
  CGO_ENABLED=0 go build -trimpath \
  -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE} -X main.builtBy=docker" \
  -o /out/cgin ./cmd/api

# ==============================================================================
# 阶段二：生产运行环境 (Alpine 3.24 极致精简镜像)
# ==============================================================================
FROM alpine:3.24

# 安装基础 CA 证书与时区数据
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# 复制编译后的纯静态二进制文件 (静态资源与权限策略已全部内嵌)
COPY --from=builder /out/cgin /app/cgin

# 预先创建可写入的 storage 结构并递归赋予系统内置 nobody 用户权限
RUN mkdir -p /app/storage/logs \
  /app/storage/app/uploads \
  /app/storage/cache \
  && chown -R nobody:nobody /app

ENV APP_ENV=production \
  APP_PORT=3300 \
  APP_HOST=0.0.0.0 \
  TZ=Asia/Shanghai

EXPOSE 3300

# 以 Alpine 系统内置非 root 用户 (nobody:nobody / 65534:65534) 运行应用
USER nobody:nobody

# 支持直接传参执行不同子命令（如 serve, db:seed）
ENTRYPOINT ["/app/cgin"]
CMD ["serve"]
