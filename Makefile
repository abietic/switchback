GO ?= go
OPENSPEC ?= npx --yes @fission-ai/openspec@1.6.0
CODEGRAPH ?= codegraph

.PHONY: build run test unit integration race fmt fmt-check vet migration-check generated-protocol-check worker-image-check secret-scan publication-check python-check openspec-check verify codegraph-init codegraph-status codegraph-smoke real-provider-acceptance m2-real-provider-acceptance runtime-recovery-acceptance failure-acceptance local-env local-config local-up local-down clean

build:
	$(GO) build -o bin/switchback-backend ./cmd/switchback-backend

run:
	$(GO) run ./cmd/switchback-backend

test:
	$(GO) test ./...

unit:
	$(GO) test -short ./...

integration:
	$(GO) test -tags=integration ./...

race:
	$(GO) test -race ./...

fmt:
	find cmd internal test -type f -name '*.go' -exec gofmt -w {} +

fmt-check:
	./scripts/check-format

vet:
	$(GO) vet ./...

migration-check:
	./scripts/check-migrations

generated-protocol-check:
	./scripts/check-generated-protocol

worker-image-check:
	$(GO) test ./internal/architecture -run TestWorkerImageProvenanceMatchesBuildAndDependencyLocks

secret-scan:
	./scripts/check-secrets

publication-check:
	./scripts/check-publication-ready
	./scripts/test-publication-ready
	./scripts/test-secrets

python-check:
	./scripts/test-python-suites

openspec-check:
	$(OPENSPEC) validate --all --strict

verify: fmt-check migration-check generated-protocol-check worker-image-check unit race vet integration secret-scan publication-check python-check openspec-check

codegraph-init:
	@if [ -d .codegraph ]; then \
		$(CODEGRAPH) sync .; \
	else \
		$(CODEGRAPH) init .; \
	fi

codegraph-status:
	$(CODEGRAPH) status .

codegraph-smoke: codegraph-status
	$(CODEGRAPH) explore "CreateTask Store.CreateTask internal/taskorchestration/application/create_task.go internal/taskorchestration/adapter/postgres/store.go StartWorkerRun internal/workerruntime/application/start_run.go internal/workerruntime/adapter/postgres/start_run.go"

real-provider-acceptance:
	./scripts/run-real-provider-acceptance

m2-real-provider-acceptance:
	./scripts/run-m2-real-provider-acceptance

runtime-recovery-acceptance:
	./scripts/run-runtime-recovery-acceptance

failure-acceptance:
	./scripts/run-failure-acceptance

local-env:
	./deploy/local/bootstrap-env.sh

local-config: local-env
	docker compose --env-file deploy/local/.env -f deploy/local/compose.yaml config --quiet

local-up: local-env
	docker compose --env-file deploy/local/.env -f deploy/local/compose.yaml --profile image-build build switchback-web switchback-backend assistant-litellm worker-image
	./deploy/local/pin-worker-image.sh
	docker compose --env-file deploy/local/.env -f deploy/local/compose.yaml up -d

local-down:
	docker compose --env-file deploy/local/.env -f deploy/local/compose.yaml down

clean:
	rm -rf bin
