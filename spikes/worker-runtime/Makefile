.PHONY: fmt fmt-check test test-scripts check-fixture-baseline proto proto-lint proto-check build lock-images preflight compose-up compose-down purge spike-integration spike-e2e

IMAGE_LOCK := image-lock.json
BUF_IMAGE = $$(jq -er '.images[] | select(.name == "buf") | .runtime_ref' $(IMAGE_LOCK))
COMPOSE_ENV = "$${SWITCHBACK_SPIKE_HOST_DATA_ROOT:-$(CURDIR)/.spike-data}/env/compose.env"

fmt:
	gofmt -w $$(rg --files -g '*.go')

fmt-check:
	@test -z "$$(gofmt -l $$(rg --files -g '*.go'))"

test:
	go test ./...

test-scripts:
	./scripts/test-deployment

lock-images:
	./scripts/lock-images

preflight:
	./scripts/preflight

build:
	./scripts/generate-env
	docker build --platform linux/arm64 --file images/backend/Dockerfile --build-arg GO_IMAGE="$$(jq -er '.images[] | select(.name == "golang") | .runtime_ref' $(IMAGE_LOCK))" --build-arg DEBIAN_IMAGE="$$(jq -er '.images[] | select(.name == "debian") | .runtime_ref' $(IMAGE_LOCK))" --tag switchback-spike-backend:local .
	docker build --platform linux/arm64 --file images/worker/Dockerfile --build-arg GO_IMAGE="$$(jq -er '.images[] | select(.name == "golang") | .runtime_ref' $(IMAGE_LOCK))" --build-arg NODE_IMAGE="$$(jq -er '.images[] | select(.name == "node") | .runtime_ref' $(IMAGE_LOCK))" --build-arg PYTHON_IMAGE="$$(jq -er '.images[] | select(.name == "python") | .runtime_ref' $(IMAGE_LOCK))" --tag switchback-spike-worker:local .
	SWITCHBACK_SPIKE_WORKER_IMAGE="$$(docker image inspect --format '{{.Id}}' switchback-spike-worker:local)" ./scripts/generate-env

compose-up: preflight build
	docker compose --env-file $(COMPOSE_ENV) -f compose.yaml up -d

compose-down:
	docker compose --env-file $(COMPOSE_ENV) -f compose.yaml down

purge:
	@echo "Deleting Switchback Worker Runtime Spike Compose volumes."
	docker compose --env-file $(COMPOSE_ENV) -f compose.yaml down -v

spike-integration:
	@set -eu; \
	integration_root="$$(mktemp -d "$(CURDIR)/.spike-integration.XXXXXX")"; \
	export SWITCHBACK_SPIKE_HOST_DATA_ROOT="$$integration_root"; \
	compose=""; \
	cleanup() { status=$$?; if [ -n "$$compose" ]; then if [ "$$status" -ne 0 ]; then $$compose logs spike-backend; fi; $$compose down --remove-orphans; fi; rm -rf "$$integration_root"; return "$$status"; }; \
	trap cleanup EXIT INT TERM; \
	export SWITCHBACK_SPIKE_UPSTREAM_MODEL="openai/switchback-integration-fake"; \
	export SWITCHBACK_SPIKE_UPSTREAM_API_BASE="http://fake-openai:8080/v1"; \
	export SWITCHBACK_SPIKE_UPSTREAM_API_KEY="integration-upstream-key"; \
	$(MAKE) build; \
	compose="docker compose --env-file $(COMPOSE_ENV) -f compose.yaml -f compose.integration.yaml"; \
	$$compose down --remove-orphans >/dev/null 2>&1 || true; \
	$$compose up -d --wait --no-deps spike-backend fake-openai; \
	SWITCHBACK_SPIKE_INTEGRATION=1 \
	SWITCHBACK_SPIKE_INTEGRATION_BACKEND_URL="http://127.0.0.1:18080" \
	SWITCHBACK_SPIKE_INTEGRATION_DATA_ROOT="$$integration_root" \
	go test ./test/integration -run 'TestRuntimeFullPath|TestNoManagedWorkersRemain' -v -count=1; \
	SWITCHBACK_SPIKE_DOCKER_FAULT_INTEGRATION=1 \
	SWITCHBACK_SPIKE_INTEGRATION_DATA_ROOT="$$integration_root" \
	go test ./test/e2e -run '^TestDockerCleanupFaultProxyAgainstRealDaemon$$' -v -count=1; \
	SWITCHBACK_SPIKE_CHECKSUM_FAULT_INTEGRATION=1 \
	SWITCHBACK_SPIKE_INTEGRATION_BACKEND_URL="http://127.0.0.1:18080" \
	SWITCHBACK_SPIKE_INTEGRATION_DATA_ROOT="$$integration_root" \
	go test ./test/e2e -run '^TestChecksum(FaultProxyPrivateNetworkTransport|MismatchFaultAgainstFakeProfile)$$' -v -count=1

spike-e2e:
	./scripts/run-e2e

check-fixture-baseline:
	./scripts/check-fixture-baseline

proto:
	docker run --rm --user "$$(id -u):$$(id -g)" --env HOME=/tmp --volume "$(CURDIR):/workspace" --workdir /workspace $(BUF_IMAGE) generate

proto-lint:
	docker run --rm --user "$$(id -u):$$(id -g)" --env HOME=/tmp --volume "$(CURDIR):/workspace" --workdir /workspace $(BUF_IMAGE) lint

proto-check:
	@tmp_dir="$$(mktemp -d)"; \
	trap 'rm -rf "$$tmp_dir"' EXIT; \
	cp -R gen "$$tmp_dir/gen"; \
	$(MAKE) proto; \
	diff -ru "$$tmp_dir/gen" gen
