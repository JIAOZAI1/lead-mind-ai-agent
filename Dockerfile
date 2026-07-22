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
COPY --from=build /out/server /app/server

EXPOSE 8080

ENTRYPOINT ["/app/server"]