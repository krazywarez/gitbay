#!/bin/sh
# Prepare a runner host for container-isolated builds (#144).
#
# Run this on the runner host as root BEFORE deploying a gitbay-runner
# that requires isolation. The runner refuses to start without a working
# podman rather than falling back to running builds unsandboxed, so the
# order matters: prepare the host, then `make deploy-runner`.
#
#   ssh -p 2222 root@bay1 'sh -s' < deploy/runner-podman-setup.sh
#
# Idempotent: safe to re-run.
set -eu

RUNNER_USER="${RUNNER_USER:-ci-runner}"

# The runner's home is wherever the account was created with; podman's
# store lives under it and the systemd drop-in names the same path.

if ! id "$RUNNER_USER" >/dev/null 2>&1; then
    echo "no such user: $RUNNER_USER" >&2
    exit 1
fi

echo "==> installing podman"
if ! command -v podman >/dev/null 2>&1; then
    apt-get update
    DEBIAN_FRONTEND=noninteractive apt-get install -y podman uidmap
fi
podman --version

# Rootless podman maps container uids into a range delegated to the user.
# Without these the runner's `podman run` fails with a mapping error.
echo "==> subuid/subgid for $RUNNER_USER"
for f in /etc/subuid /etc/subgid; do
    if ! grep -q "^$RUNNER_USER:" "$f" 2>/dev/null; then
        echo "$RUNNER_USER:200000:65536" >>"$f"
        echo "   added to $f"
    else
        echo "   already in $f"
    fi
done

# User namespaces are what rootless podman is built on. Debian 13 enables
# them by default; check rather than assume, because a build silently
# running as the host user is exactly what this is meant to prevent.
echo "==> kernel support"
max_ns=$(cat /proc/sys/user/max_user_namespaces 2>/dev/null || echo 0)
if [ "$max_ns" -lt 1 ]; then
    echo "user namespaces are disabled (user.max_user_namespaces=$max_ns);" >&2
    echo "rootless podman cannot work until they are enabled" >&2
    exit 1
fi
echo "   max_user_namespaces=$max_ns"

# Lingering keeps the user's systemd session — and so podman's storage
# and any running container — alive when nobody is logged in.
echo "==> lingering for $RUNNER_USER"
loginctl enable-linger "$RUNNER_USER"

echo "==> verifying rootless podman as $RUNNER_USER"
su - "$RUNNER_USER" -s /bin/sh -c 'podman info --format "{{.Host.Security.Rootless}}"'

echo
echo "host is ready; now: make deploy-runner"
