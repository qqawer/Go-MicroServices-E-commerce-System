#!/usr/bin/env bash
set -euo pipefail

cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
export GOWORK=off

# Match the Go version installed by release.yml and used by Dockerfile.
go version

# Build production entry points only; example programs are not release targets.
# Multiple packages populate Go's build cache without leaving binaries in the repo.
CGO_ENABLED=0 go build -mod=readonly -trimpath \
  ./user_srv ./goods_srv ./inventory_srv ./order_srv

# These regressions use local HTTP/gRPC fixtures and sqlmock. Do not replace this
# allowlist with ./...: old example packages may initialize external services.
CGO_ENABLED=1 go test -mod=readonly -race -count=1 -timeout=90s \
  ./order_srv ./order_srv/initialize ./order_srv/handler
