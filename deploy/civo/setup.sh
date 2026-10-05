#!/usr/bin/env bash
# DOW Mind — one-shot Civo VPS setup.
#
# Installs: Go toolchain, Qdrant (static binary, no Docker needed),
# builds dow-mind from source, and registers both as systemd services.
# DOW Mind binds 127.0.0.1:7777 by default (secure); reach it from your
# laptop with an SSH tunnel (see README.md).
#
#   curl -fsSL https://raw.githubusercontent.com/RivaldiMurpia/dow-mind/main/deploy/civo/setup.sh | sudo bash
#
set -euo pipefail

QDRANT_VERSION="${QDRANT_VERSION:-v1.19.2}"
GO_VERSION="${GO_VERSION:-1.27.1}"
REPO="${DOWMIND_REPO:-https://github.com/RivaldiMurpia/dow-mind.git}"

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root (use sudo)" >&2
  exit 1
fi

echo "==> [1/7] base packages"
apt-get update -qq
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq curl ca-certificates git openssl cron tar > /dev/null

echo "==> [2/7] Go toolchain (${GO_VERSION})"
if ! /usr/local/go/bin/go version 2>/dev/null | grep -q "${GO_VERSION}"; then
  curl -fsSL -o /tmp/go.tgz "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz"
  rm -rf /usr/local/go
  tar -C /usr/local -xzf /tmp/go.tgz
  rm -f /tmp/go.tgz
fi
ln -sf /usr/local/go/bin/go /usr/local/bin/go
export PATH=/usr/local/go/bin:$PATH
go version

echo "==> [3/7] Qdrant ${QDRANT_VERSION} (static binary)"
mkdir -p /opt/qdrant /var/lib/qdrant
if [ ! -x /opt/qdrant/qdrant ]; then
  curl -fsSL -o /tmp/qdrant.tgz \
    "https://github.com/qdrant/qdrant/releases/download/${QDRANT_VERSION}/qdrant-x86_64-unknown-linux-gnu.tar.gz"
  tar -xzf /tmp/qdrant.tgz -C /opt/qdrant
  rm -f /tmp/qdrant.tgz
  # Be tolerant of tarballs that nest the binary one level deep.
  if [ ! -x /opt/qdrant/qdrant ]; then
    found="$(find /opt/qdrant -name qdrant -type f | head -1)"
    [ -n "$found" ] && mv "$found" /opt/qdrant/qdrant
  fi
  chmod +x /opt/qdrant/qdrant
fi
/opt/qdrant/qdrant --version 2>/dev/null | head -1 || true

echo "==> [4/7] dow-mind source + build"
if [ -d /opt/dow-mind/.git ]; then
  git -C /opt/dow-mind pull --ff-only -q || true
else
  git clone -q "$REPO" /opt/dow-mind
fi
export GOCACHE=/root/.cache/go-build GOPATH=/root/.gopath
mkdir -p "$GOCACHE" "$GOPATH"
(
  cd /opt/dow-mind/mcp-server
  go build -trimpath -o /usr/local/bin/dow-mind ./cmd/mcp-server
)
chmod +x /usr/local/bin/dow-mind
/usr/local/bin/dow-mind version

echo "==> [5/7] config + admin token"
mkdir -p /etc/dow-mind
if [ ! -f /etc/dow-mind/env ]; then
  TOKEN="$(openssl rand -hex 32)"
  cat > /etc/dow-mind/env <<EOF
# DOW Mind environment. Fill in your Voyage key, then:
#   sudo systemctl restart dow-mind
DOWMIND_QDRANT_URL=http://127.0.0.1:6333
DOWMIND_VOYAGE_API_KEY=
DOWMIND_ADMIN_TOKEN=${TOKEN}
EOF
  chmod 600 /etc/dow-mind/env
  echo "    generated admin token (save this!): ${TOKEN}"
else
  echo "    /etc/dow-mind/env already exists, kept as-is"
fi

echo "==> [6/7] systemd services"
cat > /etc/systemd/system/qdrant.service <<'EOF'
[Unit]
Description=Qdrant vector database (DOW Mind backend)
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/var/lib/qdrant
Environment=QDRANT__SERVICE__HOST=127.0.0.1
Environment=QDRANT__SERVICE__HTTP_PORT=6333
ExecStart=/opt/qdrant/qdrant
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

cat > /etc/systemd/system/dow-mind.service <<'EOF'
[Unit]
Description=DOW Mind RAG memory server
After=network.target qdrant.service
Wants=qdrant.service

[Service]
Type=simple
User=root
EnvironmentFile=/etc/dow-mind/env
ExecStart=/usr/local/bin/dow-mind --serve
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

# Repo mirror: clone the repos you want indexed under /opt/dow-mind/repos/.
# A cron job git-pulls them every 10 minutes; mind_watch_project picks up
# the changes and re-indexes automatically.
mkdir -p /opt/dow-mind/repos
cat > /usr/local/bin/dow-mind-sync-repos <<'EOF'
#!/usr/bin/env bash
# Pull every repo mirror so the DOW Mind watcher re-indexes fresh code.
for d in /opt/dow-mind/repos/*/; do
  [ -d "$d/.git" ] || continue
  git -C "$d" pull --ff-only -q 2>/dev/null || true
done
EOF
chmod +x /usr/local/bin/dow-mind-sync-repos
( crontab -l 2>/dev/null | grep -v dow-mind-sync-repos; echo "*/10 * * * * /usr/local/bin/dow-mind-sync-repos" ) | crontab -

systemctl daemon-reload
systemctl enable -q qdrant dow-mind
systemctl restart qdrant
sleep 3
systemctl restart dow-mind
sleep 2

echo "==> [7/7] status"
systemctl is-active -q qdrant && echo "    qdrant: active" || echo "    qdrant: FAILED (journalctl -u qdrant)"
systemctl is-active -q dow-mind && echo "    dow-mind: active" || echo "    dow-mind: FAILED (journalctl -u dow-mind)"
echo
echo "Next steps (see deploy/civo/README.md):"
echo "  1. Put your Voyage API key in /etc/dow-mind/env, then: sudo systemctl restart dow-mind"
echo "  2. Clone repos to index into /opt/dow-mind/repos/"
echo "  3. From your laptop: ssh -L 7777:127.0.0.1:7777 <user>@<vps-ip>"
echo "     then open http://localhost:7777 (admin token is in /etc/dow-mind/env)"
