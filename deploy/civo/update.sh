#!/usr/bin/env bash
# DOW Mind — rebuild from the latest pushed code and restart.
# Run on the VPS as root after pushing to RivaldiMurpia/dow-mind.
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root (use sudo)" >&2
  exit 1
fi

export PATH=/usr/local/go/bin:$PATH
export GOCACHE=/root/.cache/go-build GOPATH=/root/.gopath

git -C /opt/dow-mind pull --ff-only
(
  cd /opt/dow-mind/mcp-server
  go build -trimpath -o /usr/local/bin/dow-mind ./cmd/mcp-server
)
chmod +x /usr/local/bin/dow-mind
systemctl restart dow-mind
sleep 2
systemctl is-active -q dow-mind && echo "dow-mind: active ($(dow-mind version 2>/dev/null || /usr/local/bin/dow-mind version))" \
  || { echo "dow-mind FAILED"; journalctl -u dow-mind --no-pager -n 20; exit 1; }
