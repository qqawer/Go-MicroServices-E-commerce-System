#!/usr/bin/env bash
set -euo pipefail
if [[ $(uname -s) != Linux || ! -f /etc/os-release ]]; then
  echo 'This installer is for Ubuntu 24.04 or 26.04.' >&2; exit 1
fi
# shellcheck source=/dev/null
source /etc/os-release
[[ $ID == ubuntu ]] || { echo 'Ubuntu required.' >&2; exit 1; }
case "$VERSION_ID" in
  24.04) docker_suite=noble ;;
  26.04) docker_suite=resolute ;;
  *) echo 'Supported Ubuntu versions: 24.04 and 26.04.' >&2; exit 1 ;;
esac
[[ $EUID == 0 ]] || { echo 'Run with sudo.' >&2; exit 1; }
if command -v docker >/dev/null && docker compose version >/dev/null 2>&1; then
  systemctl enable --now docker
  echo 'Docker and Compose already installed.'; exit 0
fi
for pkg in docker.io docker-compose docker-compose-v2 podman-docker containerd runc; do
  if dpkg-query -W -f='${Status}' "$pkg" 2>/dev/null | grep -q 'install ok installed'; then
    echo "Conflicting package $pkg exists; this installer will not remove it automatically." >&2; exit 1
  fi
done
apt-get update
apt-get install -y ca-certificates curl python3
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
chmod a+r /etc/apt/keyrings/docker.asc
cat > /etc/apt/sources.list.d/docker.sources <<EOF
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: $docker_suite
Components: stable
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/docker.asc
EOF
apt-get update
apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
systemctl enable --now docker
docker compose version
