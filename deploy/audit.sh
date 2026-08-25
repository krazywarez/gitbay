#!/bin/sh
# Security checks to run before a release (and periodically). Exits non-zero
# on any finding so it can gate CI.
set -eu
cd "$(dirname "$0")/.."

echo "== go vet =="
go vet ./...

echo "== govulncheck =="
go run golang.org/x/vuln/cmd/govulncheck@latest ./...

echo "== fuzz smoke (10s each parser) =="
go test -run xxx -fuzz FuzzReadPktLine -fuzztime 10s ./internal/gitd/
go test -run xxx -fuzz FuzzParseCommit -fuzztime 10s ./internal/sig/
go test -run xxx -fuzz FuzzDecodeArmorAndParseSSHSig -fuzztime 10s ./internal/sig/
go test -run xxx -fuzz FuzzParsePGPKey -fuzztime 10s ./internal/sig/
go test -run xxx -fuzz FuzzTokenizeNoPanic -fuzztime 10s ./internal/protocol/

echo "== all clear =="
