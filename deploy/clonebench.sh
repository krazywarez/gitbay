#!/bin/sh
# clonebench.sh <clone-url> <n>: start n full bare clones of <clone-url>
# at once and print each one's wall time and outcome, then the total.
# Run from a machine other than the server, against a public repository.
set -eu
url=$1
n=$2
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
start=$(date +%s)
i=1
while [ "$i" -le "$n" ]; do
    (
        s=$(date +%s)
        if git clone --quiet --bare "$url" "$dir/$i.git" 2>"$dir/$i.err"; then
            echo "$i ok $(( $(date +%s) - s ))s"
        else
            echo "$i failed $(( $(date +%s) - s ))s: $(head -n 1 "$dir/$i.err")"
        fi
    ) &
    i=$((i + 1))
done
wait
echo "total $(( $(date +%s) - start ))s for $n clones"
