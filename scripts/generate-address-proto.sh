#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
[[ "$(protoc --version)" == "libprotoc 30.2" ]]
[[ "$(protoc-gen-go --version)" == "protoc-gen-go v1.36.5" ]]
[[ "$(protoc-gen-go-grpc --version)" == "protoc-gen-go-grpc 1.5.1" ]]

cp user_srv/model/proto/address.proto order_srv/proto/address.proto
for directory in user_srv/model/proto order_srv/proto; do
  (
    cd -- "$directory"
    protoc -I . --go_out=. --go_opt=paths=source_relative \
      --go-grpc_out=. --go-grpc_opt=paths=source_relative address.proto
  )
done
