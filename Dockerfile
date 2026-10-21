# Build Stage
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS builder

WORKDIR /src

# 安装编译依赖
RUN apk add --no-cache git ca-certificates tzdata

# 预先拉取模块依赖
COPY go.mod go.sum ./
RUN go mod download

# 复制源码
COPY . .

ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -ldflags="-s -w -X main.Version=0.1.0 -X main.BuildTime=$(date +%Y-%m-%d)" \
    -o /bin/storeluna-gogo ./cmd/storeluna-gogo

# Final Stage
FROM alpine:3.20

WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata
ENV TZ=Asia/Shanghai

COPY --from=builder /bin/storeluna-gogo /app/storeluna-gogo

# 暴露公共服务端口与管理控制台端口
EXPOSE 55150
EXPOSE 65150

VOLUME ["/app/data"]

ENTRYPOINT ["/app/storeluna-gogo"]
CMD ["-config", "/app/config.yaml"]
