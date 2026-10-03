#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

# Run after building the image, with the lab ES stopped.
lab_storage="$(cd ../.. && pwd)/.local/es-lab/elasticsearch"
mkdir -p "$lab_storage/config" "$lab_storage/data"

# Preserve existing configuration. UID 501 etc. remains the Mac file owner;
# the ES container runs as UID 1000, GID 0 and accesses the shared group.
docker run --rm --user 0 --entrypoint /bin/bash \
  --mount "type=bind,source=$lab_storage,target=/lab" \
  grpctest-es-lab:8.19.22-smartcn -c '
    if [ ! -f /lab/config/elasticsearch.yml ]; then
      cp -a /usr/share/elasticsearch/config/. /lab/config/
    fi
    chown -R "$1":0 /lab/config /lab/data
    chmod -R u+rwX,g+rwX,o-rwx /lab/config /lab/data
  ' -- "$(id -u)"

printf 'ES config: %s/config\nES data:   %s/data\n' "$lab_storage" "$lab_storage"
