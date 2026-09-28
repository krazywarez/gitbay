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
    su -s /bin/bash "$RUNNER_USER" -c "timeout 5 bash -c 'exec 3<>/dev/tcp/$1/$2'" 2>/dev/null
}

nft list table inet gitbay_runner >/dev/null

for dest in 127.0.0.1:22 "$public:22"; do
    if ! probe "${dest%:*}" "${dest##*:}"; then
        echo "$RUNNER_USER cannot reach $dest: the egress rule would stop the runner" >&2
        exit 1
    fi
done
for dest in 127.0.0.1:2222 "$public:2222"; do
    if probe "${dest%:*}" "${dest##*:}"; then
        echo "$RUNNER_USER reaches $dest: the egress rule is not in force" >&2
        exit 1
    fi
done
echo "egress for $RUNNER_USER: 127.0.0.1:22 and $public:22 open, 2222 refused"
