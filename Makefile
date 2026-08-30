# gitbay build and deploy.
#
#   make test          run the full suite
#   make deploy        build the server, push it to HOST, rebuild the local CLI
#   make deploy-runner update the CI runner on HOST (only when cmd/gitbay-runner changed)
#
# Override the target host with: make deploy HOST=example.org PORT=22

HOST     ?= 46.232.248.67
PORT     ?= 2222
CLI_DEST ?= /opt/homebrew/bin/gitbay

CROSS   := CGO_ENABLED=0 GOOS=linux GOARCH=amd64
LDFLAGS := -s -w
SERVER_BIN := dist/gitbayd-linux-amd64
RUNNER_BIN := dist/gitbay-runner-linux-amd64

.PHONY: help test server cli deploy deploy-runner preflight

help:
	@sed -n 's/^#   //p' $(MAKEFILE_LIST)

test:
	go test ./... -count=1

# Fail in seconds on an unreachable host or a wedged ssh-agent, rather
# than hanging on a credential prompt mid-deploy.
preflight:
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
	scp -P $(PORT) $(RUNNER_BIN) root@$(HOST):/usr/local/bin/gitbay-runner.new
	ssh -p $(PORT) root@$(HOST) 'set -eu; \
	  chmod 755 /usr/local/bin/gitbay-runner.new; \
	  mv /usr/local/bin/gitbay-runner.new /usr/local/bin/gitbay-runner; \
	  systemctl restart gitbay-runner; \
	  systemctl --no-pager --lines=3 status gitbay-runner'
