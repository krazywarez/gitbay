#!/bin/sh
# Check the CI runner's host egress rule (#260) as the runner's user:
# the forge over loopback on 22 must answer (the runner polls there),
# and the admin sshd on 2222 must not, on loopback or the public
# address. `make deploy-runner` runs this after loading the rule and
# before restarting the runner, and stops on a failure.
#
#   ssh -p 2222 root@bay1 'sh -s' < deploy/runner-egress-check.sh
set -eu

RUNNER_USER="${RUNNER_USER:-ci-runner}"
# hostname -I lists the host's addresses, IPv4 first on bay1; the first
# is the public one there.
public=$(hostname -I | awk '{print $1}')

probe() {
    su -s /bin/bash "$RUNNER_USER" -c "timeout 5 bash -c 'exec 3<>/dev/tcp/$1/$2'" </dev/null 2>/dev/null
}

nft list table inet gitbay_runner >/dev/null

# The runner polls 127.0.0.1:22. If the rule blocks that, the running
# runner is already cut off, so remove the table: CI keeps polling as it
# did before the deploy, and the exit still stops make before the restart.
if ! probe 127.0.0.1 22; then
    nft destroy table inet gitbay_runner
    echo "$RUNNER_USER cannot reach 127.0.0.1:22 with the egress rule loaded;" >&2
    echo "removed table inet gitbay_runner so the runner keeps polling. Fix the rule and deploy again." >&2
    exit 1
fi
if ! probe "$public" 22; then
    echo "$RUNNER_USER cannot reach $public:22: builds would not reach the forge" >&2
    exit 1
fi
for dest in 127.0.0.1:2222 "$public:2222"; do
    if probe "${dest%:*}" "${dest##*:}"; then
        echo "$RUNNER_USER reaches $dest: the egress rule is not in force" >&2
        exit 1
    fi
done
echo "egress for $RUNNER_USER: 127.0.0.1:22 and $public:22 open, 2222 refused"
