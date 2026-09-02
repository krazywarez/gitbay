#!/bin/sh
# Copy one file to the host: rsync when the host has it, since a stalled
# transfer then resumes and the bytes are compressed on the wire; scp -C
# otherwise. Both deploy targets go through here so neither can assume a
# tool the host lacks.
#
#   deploy/copy.sh <host> <port> <local-file> <remote-path>
set -eu
host=$1 port=$2 src=$3 dst=$4
if ssh -p "$port" "root@$host" 'command -v rsync >/dev/null 2>&1'; then
  rsync --partial --inplace -z -e "ssh -p $port" "$src" "root@$host:$dst"
else
  scp -C -P "$port" "$src" "root@$host:$dst"
fi
