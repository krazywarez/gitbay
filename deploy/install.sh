#!/bin/sh
# Push the gitbayd binary to a freshly cloud-inited host and start it.
# Usage: deploy/install.sh <host-or-ip> [ssh-port]
set -eu
host=${1:?usage: install.sh <host-or-ip> [ssh-port]}
port=${2:-2222}
bin=dist/gitbayd-linux-amd64

[ -f "$bin" ] || { echo "build first: CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $bin ./cmd/gitbayd" >&2; exit 1; }

# rsync resumes a transfer the host's load stalled and compresses the
# binary on the wire; scp -C is the fallback where the host has no rsync.
if ssh -p "$port" "root@$host" 'command -v rsync >/dev/null'; then
  rsync --partial --inplace -z -e "ssh -p $port" "$bin" "root@$host:/usr/local/bin/gitbayd.new"
else
  scp -C -P "$port" "$bin" "root@$host:/usr/local/bin/gitbayd.new"
fi
ssh -p "$port" "root@$host" '
  set -eu
  chmod 755 /usr/local/bin/gitbayd.new
  mv /usr/local/bin/gitbayd.new /usr/local/bin/gitbayd
  /usr/local/bin/gitbayd --config /etc/gitbay/config.toml check-config --no-host-checks
  systemctl restart gitbayd
  sleep 1
  systemctl --no-pager --lines=5 status gitbayd
'
