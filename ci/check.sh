#!/usr/bin/env bash
set -euo pipefail

cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
export GOWORK=off

# Match the Go version installed by release.yml and used by Dockerfile.
go version
python3 -m unittest discover -s ci -p 'test_*.py'

# Build production entry points only; example programs are not release targets.
# Multiple packages populate Go's build cache without leaving binaries in the repo.
CGO_ENABLED=0 go build -mod=readonly -trimpath \
  ./user_srv ./goods_srv ./inventory_srv ./order_srv ./cmd/payment-review

# These regressions use local HTTP/gRPC fixtures and sqlmock. Do not replace this
# allowlist with ./...: old example packages may initialize external services.
CGO_ENABLED=1 go test -mod=readonly -race -count=1 -timeout=90s \
  ./user_srv/handler ./user_srv/initialize ./order_srv ./order_srv/initialize ./order_srv/handler ./order_srv/payment ./goods_srv/handler ./inventory_srv ./inventory_srv/initialize ./inventory_srv/handler ./internal/mqruntime ./internal/tracing ./cmd/goods-es-demo ./cmd/payment-review

# Keep the reproduced third-party cache race covered without a broker.
CGO_ENABLED=1 go test -mod=readonly -race -count=1 -timeout=30s \
  github.com/apache/rocketmq-client-go/v2/consumer -run '^TestLocal'
