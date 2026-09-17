#!/usr/bin/env bash
set -euo pipefail
DEPLOY_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
[[ -f "$DEPLOY_DIR/.env" ]] || { echo 'Run python3 scripts/init-env.py first.' >&2; exit 1; }
compose=(docker compose --env-file "$DEPLOY_DIR/.env" -f "$DEPLOY_DIR/compose.yaml" --profile ci)
case "${1:-}" in
  up) "${compose[@]}" up -d --build --wait --wait-timeout 600 ;;
  status) "${compose[@]}" ps ;;
  logs) "${compose[@]}" logs --tail 100 "${@:2}" ;;
  stop) "${compose[@]}" stop ;;
  check) "${compose[@]}" config --quiet ;;
  *) echo 'Usage: bash scripts/stack.sh {up|status|logs [service]|stop|check}' >&2; exit 2 ;;
esac
