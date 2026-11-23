export PATH := $(CURDIR)/.tools/bin:$(CURDIR)/.tools/go/bin:$(CURDIR)/.tools/node/bin:$(PATH)
.PHONY: configure infra api worker web build
configure:
	./scripts/configure.sh
infra: configure
	docker compose --env-file .env.local -f deploy/compose.yaml up -d
api worker web tools preview retrieval: configure
	./scripts/run-local.sh $@
build:
	cd backend && go build ./...
	cd frontend && npm ci && npm run build

.PHONY: smoke tools preview retrieval
smoke: infra
	./scripts/build-smoke.sh

.PHONY: smoke-public
PYTHON_A2A ?= python3.12
smoke-public:
	$(PYTHON_A2A) -m venv .tools/public-agent-venv
	.tools/public-agent-venv/bin/pip install -r scripts/public-agent-requirements.txt
	PUBLIC_AGENT_PYTHON="$(CURDIR)/.tools/public-agent-venv/bin/python" $(MAKE) smoke

.PHONY: smoke-recovery
smoke-recovery:
	python3 scripts/smoke_recovery.py

.PHONY: smoke-oidc
smoke-oidc: infra
	python3 scripts/smoke_oidc.py
