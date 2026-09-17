# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.25.6-alpine@sha256:98e6cffc31ccc44c7c15d83df1d69891efee8115a5bb7ede2bf30a38af3e3c92 AS builder
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG SERVICE
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN case "$SERVICE" in user_srv|goods_srv|inventory_srv|order_srv) ;; *) echo "Invalid SERVICE" >&2; exit 1;; esac
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/server "./$SERVICE"

FROM alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40
ARG SERVICE
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -g 10001 app && adduser -D -u 10001 -G app app && \
    mkdir -p "/app/$SERVICE" /app/tmp && chown -R app:app /app
WORKDIR /app/${SERVICE}
COPY --from=builder /out/server /usr/local/bin/server
COPY --from=ghcr.io/grpc-ecosystem/grpc-health-probe:v0.4.38@sha256:cacececcc3e94fde691141b80c34e288bc2743323d4f450274a0c636d07400a9 /ko-app/grpc-health-probe /usr/local/bin/grpc_health_probe
USER 10001:10001
ENV MXSHOP_DEBUG=true
ENTRYPOINT ["/usr/local/bin/server"]
