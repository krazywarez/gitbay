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

# podman's storage paths are pinned in storage.conf, both graphroot and
# runroot, under the runner's home. Left to podman, the run root is
# $XDG_RUNTIME_DIR or /tmp/storage-run-<uid>; the service runs with
# PrivateTmp, so that is a per-instance tmpfs, and podman's pause process
# (which the cgroupfs manager places outside the service cgroup) can
# outlive a restart holding a dead /tmp — after which every podman
# command, in any context, fails with "mkdir ...: no such file or
# directory". A run root under the home directory is valid in every
# namespace and needs neither lingering nor /tmp.
#
# podman records the run root at first use. Changing it later needs
# `podman system reset --force` as the runner user and a rebuild of the
# images; this script does not do that for you.
home=$(getent passwd "$RUNNER_USER" | cut -d: -f6)
conf="$home/.config/containers/storage.conf"
echo "==> storage config in $conf"
install -d -o "$RUNNER_USER" -g "$RUNNER_USER" -m 700 "$home/.config/containers"
printf '[storage]\ndriver = "overlay"\ngraphroot = "%s/.local/share/containers/storage"\nrunroot = "%s/.local/share/containers/run"\n' "$home" "$home" >"$conf"
chown "$RUNNER_USER:$RUNNER_USER" "$conf"
echo "   written"

# Lingering keeps the user's systemd session alive when nobody is logged
# in, which podman's pause process relies on.
echo "==> lingering for $RUNNER_USER"
loginctl enable-linger "$RUNNER_USER"

echo "==> verifying rootless podman as $RUNNER_USER"
# The verification fails rather than passing with || true: a host that
# reports ready and is not is the outage this script exists to prevent.
su - "$RUNNER_USER" -s /bin/sh -c "podman info --format 'rootless={{.Host.Security.Rootless}} runroot={{.Store.RunRoot}}'"

echo
echo "host is ready. Build the CI image (deploy/Containerfile.ci), then: make deploy-runner"
