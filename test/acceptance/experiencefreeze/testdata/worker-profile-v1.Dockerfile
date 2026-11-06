ARG GO_IMAGE=docker.io/library/golang@sha256:b58a2431884c6dfc5ddcea953c7f426ad72c2ff9e94b4b8495fefa74e4dabcc0
ARG NODE_IMAGE=docker.io/library/node@sha256:a5dbaa51d1be96b3415b993320893038c7240d6dd901166ee597f3b1cc3259d1
ARG PYTHON_IMAGE=docker.io/library/python@sha256:56249d7a2f93306106f6d8bcdf6423afb73c1b747d874febcc778beee25cb8bb

FROM ${GO_IMAGE} AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags='-s -w' -o /out/worker-shim ./cmd/worker-shim \
    && go version | grep -F 'go1.26.4' \
    && /out/worker-shim --build-info | grep -F '"acp_go_sdk_version":"v0.13.5"'

FROM ${NODE_IMAGE} AS node-deps
WORKDIR /opt/switchback
COPY images/worker/package.json images/worker/package-lock.json ./
RUN npm ci --omit=dev \
    && node --version | grep -F 'v24.17.0' \
    && test "$(node -p 'require("./node_modules/@agentclientprotocol/codex-acp/package.json").version')" = '1.1.2' \
    && test "$(node -p 'require("./node_modules/@openai/codex/package.json").version')" = '0.144.1'

FROM ${PYTHON_IMAGE}
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl gh git jq openssh-client ripgrep tini \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 litellm \
    && useradd --uid 10001 --gid litellm --create-home --home-dir /var/lib/switchback-litellm --shell /usr/sbin/nologin litellm \
    && groupadd --gid 10002 switchback-agent \
    && useradd --uid 10002 --gid switchback-agent --create-home --home-dir /home/switchback-agent --shell /usr/sbin/nologin switchback-agent
RUN pip install --no-cache-dir 'litellm[proxy]==1.93.0' \
    && python -c "import importlib.metadata as m; assert m.version('litellm') == '1.93.0'"
COPY --from=go-build /usr/local/go /usr/local/go
COPY --from=go-build /out/worker-shim /usr/local/bin/worker-shim
COPY --from=node-deps /usr/local/bin/node /usr/local/bin/node
COPY --from=node-deps /opt/switchback/node_modules /opt/switchback/node_modules
COPY images/worker/litellm /opt/switchback/litellm
RUN mkdir -p /workspace /var/lib/switchback/agent-state /var/lib/switchback/trace-spool /run/switchback-secrets /run/switchback-runtime/app-server-logs /run/switchback-runtime/codex-sqlite \
    && chown -R switchback-agent:switchback-agent /workspace /var/lib/switchback/agent-state /var/lib/switchback/trace-spool /home/switchback-agent \
    && chown -R switchback-agent:switchback-agent /run/switchback-runtime \
    && chmod 0700 /run/switchback-runtime/app-server-logs /run/switchback-runtime/codex-sqlite \
    && chown -R litellm:litellm /var/lib/switchback-litellm \
    && chmod 0755 /usr/local/bin/worker-shim \
    && test "$(/opt/switchback/node_modules/.bin/codex-acp --version | awk '{print $NF}')" = '1.1.2' \
    && /usr/local/bin/worker-shim --build-info | grep -F '"litellm_version":"1.93.0"'
ENV PATH="/usr/local/go/bin:/opt/switchback/node_modules/.bin:/usr/local/bin:/usr/bin:/bin"
WORKDIR /workspace
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/worker-shim"]
