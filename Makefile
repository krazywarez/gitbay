# gitbay build and deploy.
#
#   make test          run the full suite
#   make deploy        build the server, push it to HOST, rebuild the local CLI
#   make deploy-runner update the CI runner on HOST (only when cmd/gitbay-runner changed)
#
# Override the target host with: make deploy HOST=example.org PORT=22
# Deploy an uncommitted build on purpose with: ALLOW_DIRTY=1 make deploy

HOST     ?= gitbay.org
PORT     ?= 2222
CLI_DEST ?= /opt/homebrew/bin/gitbay

CROSS   := CGO_ENABLED=0 GOOS=linux GOARCH=amd64

# Stamp the commit into every binary so a deployed artifact can say where it
# came from. The -dirty suffix only appears under ALLOW_DIRTY=1, since
# preflight otherwise refuses to build an uncommitted tree.
COMMIT  := $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)$(shell [ -n "$$(git status --porcelain 2>/dev/null)" ] && echo -dirty)
LDFLAGS := -s -w -X gitbay.org/gitbay/internal/buildinfo.Commit=$(COMMIT)
SERVER_BIN := dist/gitbayd-linux-amd64
RUNNER_BIN := dist/gitbay-runner-linux-amd64

.PHONY: help test server cli deploy deploy-runner preflight

help:
	@sed -n 's/^#   //p' $(MAKEFILE_LIST)

# The e2e suite runs 400-700s against go's 600s per-package default, so a
# loaded machine turns a passing tree into a goroutine dump that reads as
# an unrelated failure. A real hang still fails, just later.
test:
	go test ./... -count=1 -timeout 30m

# Fail in seconds on an unreachable host or a wedged ssh-agent, rather
# than hanging on a credential prompt mid-deploy.
#
# Also refuse a dirty tree. Every build target compiles the working tree, not
# HEAD, so uncommitted work ships silently -- and migrations/ is embedded, so a
# migration file that exists only on disk still migrates the production
# database on restart. That happened once: 0027 reached gitbay.org inside an
# unrelated deploy, an hour before it merged. Untracked counts; go:embed does
# not consult the index.
preflight:
	@[ -n "$(ALLOW_DIRTY)" ] || [ -z "$$(git status --porcelain)" ] \
	  || { echo "working tree is dirty; deploy builds the tree, not HEAD:" >&2; \
	       git status --short >&2; \
	       echo "commit first, or ALLOW_DIRTY=1 make deploy to ship it anyway." >&2; \
	       exit 1; }
	@echo "==> checking $(HOST):$(PORT)"
	@ssh -p $(PORT) -o BatchMode=yes -o ConnectTimeout=10 root@$(HOST) true \
	  || { echo "cannot reach root@$(HOST):$(PORT) without a prompt." >&2; \
	       echo "if ssh-add -l says 'invalid format', the agent is wedged: killall ssh-agent" >&2; \
	       exit 1; }

server:
	@echo "==> building $(SERVER_BIN)"
	$(CROSS) go build -trimpath -ldflags='$(LDFLAGS)' -o $(SERVER_BIN) ./cmd/gitbayd

cli:
	@echo "==> installing CLI to $(CLI_DEST)"
	go build -o $(CLI_DEST) ./cmd/gitbay

deploy: preflight server
	@echo "==> pushing to $(HOST) (~24MB, then restart)"
	./deploy/install.sh $(HOST) $(PORT)
	@$(MAKE) --no-print-directory cli
	@echo "==> deployed $$(git rev-parse --short HEAD)"

deploy-runner: preflight
	@echo "==> building $(RUNNER_BIN)"
	$(CROSS) go build -trimpath -ldflags='$(LDFLAGS)' -o $(RUNNER_BIN) ./cmd/gitbay-runner
	@echo "==> pushing runner to $(HOST)"
	./deploy/copy.sh $(HOST) $(PORT) $(RUNNER_BIN) /usr/local/bin/gitbay-runner.new
	ssh -p $(PORT) root@$(HOST) 'mkdir -p /etc/systemd/system/gitbay-runner.service.d'
	./deploy/copy.sh $(HOST) $(PORT) deploy/gitbay-runner.override.conf /etc/systemd/system/gitbay-runner.service.d/override.conf
	ssh -p $(PORT) root@$(HOST) 'set -eu; \
	  chmod 755 /usr/local/bin/gitbay-runner.new; \
	  mv /usr/local/bin/gitbay-runner.new /usr/local/bin/gitbay-runner; \
	  systemctl daemon-reload; \
	  systemctl restart gitbay-runner; \
	  systemctl --no-pager --lines=3 status gitbay-runner'
