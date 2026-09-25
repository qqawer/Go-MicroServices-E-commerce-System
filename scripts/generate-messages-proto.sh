#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../user_srv/model/proto"
[[ "$(protoc --version)" == "libprotoc 30.2" ]]
[[ "$(protoc-gen-go --version)" == "protoc-gen-go v1.36.5" ]]
[[ "$(protoc-gen-go-grpc --version)" == "protoc-gen-go-grpc 1.5.1" ]]
protoc -I . --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative messages.proto
