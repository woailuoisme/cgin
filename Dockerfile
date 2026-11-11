FROM golang:1.27.4-alpine3.24 AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
  go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

# 纯静态零 CGO 编译并注入版本元数据
RUN --mount=type=cache,target=/go/pkg/mod \
  --mount=type=cache,target=/root/.cache/go-build \
  CGO_ENABLED=0 go build -trimpath \
  -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE} -X main.builtBy=docker" \
  -o /out/cgin ./cmd/api

# ── 生产运行镜像 ─────────────────────────────────────────────────────────────
FROM alpine:3.24

RUN apk add --no-cache ca-certificates tzdata tini

WORKDIR /app

COPY --from=builder /out/cgin /app/cgin

# 初始化运行时存储目录并赋予非 root (nobody) 权限
RUN mkdir -p /app/storage/logs \
  /app/storage/app/uploads \
  /app/storage/cache \
  && chown -R nobody:nobody /app

ENV APP_ENV=production \
  APP_PORT=3300 \
  APP_HOST=0.0.0.0 \
  TZ=Asia/Shanghai

EXPOSE 3300

USER nobody:nobody

ENTRYPOINT ["tini", "--", "/app/cgin"]
CMD ["serve"]
