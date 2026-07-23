# # syntax=docker/dockerfile:1

# FROM golang:1.25.6-alpine AS build
# WORKDIR /src

# RUN apk add --no-cache git

# COPY go.mod go.sum ./
# RUN go mod download

# COPY . .
# RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# FROM alpine:3.20 AS runtime
# WORKDIR /app

# COPY --from=build /out/server /app/server

# EXPOSE 8080

# ENTRYPOINT ["/app/server"]


# syntax=docker/dockerfile:1

# --platform=$BUILDPLATFORM 让这个 stage 始终在 runner 的原生架构上运行，
# 用 Go 自身交叉编译到目标架构，避免在 QEMU 里跑编译器（多架构慢的根因）。
FROM --platform=$BUILDPLATFORM golang:1.25.6-alpine AS build
WORKDIR /src

RUN apk add --no-cache git

# 由 buildx 自动注入
ARG TARGETOS
ARG TARGETARCH

# 依赖层单独缓存：只有 go.mod/go.sum 变化才会重新下载
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
# --mount=type=cache 复用 module 与 build cache，跨构建大幅提速
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM alpine:3.20 AS runtime
WORKDIR /app

# 常见运行时依赖：证书（HTTPS 调用）和时区库
RUN apk add --no-cache ca-certificates tzdata

# 统一容器时区为 Asia/Shanghai (+8:00)：软链 /etc/localtime 让 time.Local
# （因而 log/slog 默认 handler 打印的时间戳、以及未来任何直接用 time.Local
# 的代码）都以北京时间显示，避免日志时间戳和运维人员本地时间对不上。
# TZ 环境变量是保险：即使某个基础镜像/运行时没有正确读取 /etc/localtime，
# glibc/musl 的时区解析也会退回读 TZ。
# 注意：业务代码里凡是存储型时间戳（如 internal/memory/shortterm 写入
# Redis 的 last_active_at）必须继续显式 .UTC()，不得依赖进程时区——这个
# 时区设置只影响“人读”的日志展示，不改变任何持久化时间语义。
RUN ln -sf /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
    echo "Asia/Shanghai" > /etc/timezone
ENV TZ=Asia/Shanghai

COPY --from=build /out/server /app/server

EXPOSE 8080

ENTRYPOINT ["/app/server"]