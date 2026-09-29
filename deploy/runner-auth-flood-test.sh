#!/bin/sh
# Scratch-repository test for #260: a build fails SSH logins while the
# runner works, and the runner must not be locked out with it.
#
# Run from a machine with an admin gitbay identity, after the Admin
# page's scratch procedure: the runner's key attached to the scratch
# repository and the runner scoped to it with -repos. The script
# replaces the repository's .gitbay/ci.yml.
#
#   deploy/runner-auth-flood-test.sh cmc/runner-scratch              # trusted: a push to main
#   deploy/runner-auth-flood-test.sh cmc/runner-scratch --untrusted  # a merge request from a fork
#
# The build's logins use a git-scoped key registered with --ttl 1s and
# expired by the time the build runs. With registration open an
# unknown key is admitted to run register and never counts against the
# SSH auth limiter; an expired key counts (internal/sshd/sshd.go, authenticate).
# The key is removed from the account when the script exits.
#
# The step waits a minute, so the operator can find pasta's cgroup
# (Admin page), probes what the build reaches, then makes 12 logins
# without pause, so the limiter (ssh_auth_rate, 10 a minute per address) locks
# the address those logins come from for most of the next minute. The
# runner reports the result right after the step; its report retries
# for half a minute.
#
# Before #260 a build reached the host's loopback through pasta, and its
# logins arrived from 127.0.0.1, where the runner polls: the report is
# refused ("too many authentication attempts" in the runner's journal),
# the build is failed by the server two minutes after its log stream
# ended, the runner's last-seen stops advancing for up to a minute, and
# the audit log has auth.throttled for 127.0.0.1.
#
# With the fix, trusted: GITBAY_SSH is git@169.254.1.2, the logins are
# denied and arrive from the host's public address, auth.throttled
# names that address, the build succeeds and the runner keeps polling.
# Untrusted: every login is refused by the builds table before it
# reaches sshd, and no auth.* entry comes from the build at all.
#
# The script also requires lines in the build log, so a missing ssh or
# bash in the image, or a table that matches nothing, fails rather than
# passes. Trusted: "logins 12 denied" (every login reached sshd) and
# 10.0.0.1:80 refused, not timed out. Untrusted: 169.254.1.2:22 and
# github.com:22 refused and "12 refused". The untrusted lines are the
# proof that pasta's sockets are in the build's cgroup: the uid table
# lets ci-runner reach both.
set -eu

repo=${1:-}
[ -n "$repo" ] || { echo "usage: $0 <owner/name> [--untrusted]" >&2; exit 2; }
mode=trusted
[ "${2:-}" = --untrusted ] && mode=untrusted
host=${GITBAY_HOST:-gitbay.org}
account=${RUNNER_ACCOUNT:-ci}
job=flood260

tmp=$(mktemp -d)
fp=
cleanup() {
    if [ -n "$fp" ]; then gitbay auth keys remove "$fp" >/dev/null || echo "remove key $fp by hand" >&2; fi
    fp=
    rm -rf "$tmp"
}
trap cleanup EXIT
trap 'cleanup; exit 130' INT TERM

echo "==> an expired key"
ssh-keygen -q -t ed25519 -N '' -C auth-flood-260 -f "$tmp/key"
gitbay auth keys add --scope git --label auth-flood-260 --ttl 1s <"$tmp/key.pub" >/dev/null
fp=$(ssh-keygen -lf "$tmp/key.pub" | awk '{print $2}')
sleep 2

if [ "$mode" = untrusted ]; then
    src="${repo%/*}/${repo#*/}-fork260"
    if ! gitbay repo show "$src" --json >/dev/null 2>&1; then
        echo "==> forking $repo to $src"
        gitbay repo fork "$repo" --name "${repo#*/}-fork260" >/dev/null
    fi
    branch="flood-260-$(date +%s)"
else
    src=$repo
    branch=main
fi

echo "==> committing the $job job to $src ($branch)"
git clone -q "ssh://git@$host/$src.git" "$tmp/repo"
cd "$tmp/repo"
[ "$branch" = main ] || git checkout -q -b "$branch"
mkdir -p .gitbay
cp "$tmp/key" .gitbay/flood.key
cat >.gitbay/ci.yml <<EOF
jobs:
  $job:
    steps:
      - echo "GITBAY_SSH=\$GITBAY_SSH"
      - sh .gitbay/flood.sh
EOF
cat >.gitbay/flood.sh <<'EOF'
#!/bin/sh
# Written by deploy/runner-auth-flood-test.sh (#260).
set -u
sleep 60
key=/tmp/flood.key
cp .gitbay/flood.key "$key"
chmod 600 "$key"
dest=${GITBAY_SSH#*@}
host=${dest%:*}
port=${dest##*:}
[ "$host" = "$dest" ] && port=22

probe() {
    timeout 5 bash -c "exec 3<>/dev/tcp/$1/$2" 2>/dev/null
    case $? in
    0) echo "open     $1:$2" ;;
    124) echo "timeout  $1:$2" ;;
    *) echo "refused  $1:$2" ;;
    esac
}
getent hosts proxy.golang.org >/dev/null && echo "dns      ok" || echo "dns      failed"
for t in "$host:22" "$host:80" "$host:443" "$host:2222" \
    10.0.0.1:80 192.168.0.1:80 proxy.golang.org:443 github.com:22; do
    probe "${t%:*}" "${t##*:}"
done

denied=0 refused=0 other=0 i=0
while [ $i -lt 12 ]; do
    i=$((i + 1))
    out=$(ssh -F /dev/null -i "$key" -o IdentitiesOnly=yes -o BatchMode=yes \
        -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 \
        -p "$port" "git@$host" whoami 2>&1)
    case $out in
    *"Permission denied"*) denied=$((denied + 1)) ;;
    *"Connection refused"*) refused=$((refused + 1)) ;;
    *) other=$((other + 1)); echo "login $i: $out" ;;
    esac
done
echo "logins   $denied denied, $refused refused, $other other"
EOF
git add .gitbay
git commit -q -m "ci: auth flood test (#260)"
git push -q origin "$branch"
cd - >/dev/null

if [ "$mode" = untrusted ]; then
    gitbay mr create "$repo" --source "$src:$branch" --target main --title "#260 auth flood, untrusted" >/dev/null
fi

# One gitbay call per tick: the CLI shares one connection, and a burst of
# logins is what the limiter is for.
echo "==> waiting for the build"
n=
for _ in $(seq 1 12); do
    sleep 5
    n=$(gitbay build list "$repo" --job "$job" --limit 1 --json | jq -r '.data | (.items // .) | .[0].number // empty')
    [ -n "$n" ] && break
done
[ -n "$n" ] || { echo "no $job build queued on $repo" >&2; exit 1; }
echo "   build $n"
status=
for _ in $(seq 1 60); do
    sleep 10
    status=$(gitbay build show "$repo" "$n" --json | jq -r .data.status)
    case $status in success | failure) break ;; esac
done
echo "   $status"

echo "==> the runner after the build"
seen() { gitbay admin runners --json | jq -r --arg a "$account" '[.data.runners[] | select(.username == $a) | .last_seen] | max // empty'; }
first=$(seen)
sleep 15
second=$(seen)
echo "   $account last seen $first, then $second"

echo "==> build log"
log=$(gitbay build log "$repo" "$n")
printf '%s\n' "$log" | sed -n '/dns /,$p'

echo "==> auth audit, last 15 minutes"
gitbay audit --action auth. --since 15m --json |
    jq -r '.data[] | "\(.action) \(.data | fromjson | .ip // "-")"' | sort | uniq -c

echo
fail=0
[ "$status" = success ] || { echo "FAIL: build $n is $status; the runner could not report it"; fail=1; }
[ -n "$second" ] && [ "$second" != "$first" ] || { echo "FAIL: the runner did not poll in 15 seconds after the build"; fail=1; }
if gitbay audit --action auth.throttled --since 15m --json | jq -e '.data[] | select((.data | fromjson | .ip) == "127.0.0.1")' >/dev/null; then
    echo "FAIL: 127.0.0.1, the runner's address, was throttled"
    fail=1
fi
need() {
    printf '%s\n' "$log" | grep -Eq "$1" || { echo "FAIL: the build log lacks \"$2\""; fail=1; }
}
if [ "$mode" = trusted ]; then
    need 'logins +12 denied' "logins 12 denied"
    need 'refused +10\.0\.0\.1:80( |$)' "refused 10.0.0.1:80"
else
    need 'refused +169\.254\.1\.2:22( |$)' "refused 169.254.1.2:22"
    need 'refused +github\.com:22( |$)' "refused github.com:22"
    need '12 refused' "12 refused"
fi
[ $fail = 0 ] && echo "PASS ($mode): the build's failed logins did not lock the runner out"
exit $fail
