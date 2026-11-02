ARG GO_IMAGE=docker.io/library/golang@sha256:b58a2431884c6dfc5ddcea953c7f426ad72c2ff9e94b4b8495fefa74e4dabcc0
ARG NODE_IMAGE=docker.io/library/node@sha256:a5dbaa51d1be96b3415b993320893038c7240d6dd901166ee597f3b1cc3259d1
ARG PYTHON_IMAGE=docker.io/library/python@sha256:56249d7a2f93306106f6d8bcdf6423afb73c1b747d874febcc778beee25cb8bb

FROM ${GO_IMAGE} AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/worker-shim ./worker/cmd/worker-shim \
    && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/fake-acp ./test/fakes/cmd/fake-acp \
    && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/fake-openai ./test/fakes/cmd/fake-openai \
    && go version | grep -F 'go1.26.4'

FROM ${NODE_IMAGE} AS node-deps
WORKDIR /opt/switchback
COPY images/worker/package.json images/worker/package-lock.json ./
RUN npm ci --omit=dev \
    && node --version | grep -F 'v24.17.0' \
    && test "$(node -p 'require("./node_modules/@agentclientprotocol/codex-acp/package.json").version')" = '1.1.2' \
    && test "$(node -p 'require("./node_modules/@openai/codex/package.json").version')" = '0.144.1'

FROM ${PYTHON_IMAGE}
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates git tini \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 litellm \
    && useradd --uid 10001 --gid litellm --create-home --home-dir /var/lib/switchback-litellm --shell /usr/sbin/nologin litellm \
    && groupadd --gid 10002 switchback-agent \
    && useradd --uid 10002 --gid switchback-agent --create-home --home-dir /home/switchback-agent --shell /usr/sbin/nologin switchback-agent
RUN pip install --no-cache-dir 'litellm[proxy]==1.93.0' \
    && python -c "import importlib.metadata as m; assert m.version('litellm') == '1.93.0'" \
    && python --version | grep -F 'Python 3.13' \
    && git --version \
    && litellm --version
COPY --from=go-build /usr/local/go /usr/local/go
COPY --from=go-build /out/worker-shim /usr/local/bin/worker-shim
COPY --from=go-build /out/fake-acp /opt/switchback/bin/fake-acp
COPY --from=go-build /out/fake-openai /opt/switchback/bin/fake-openai
COPY --from=node-deps /usr/local/bin/node /usr/local/bin/node
COPY --from=node-deps /opt/switchback/node_modules /opt/switchback/node_modules
COPY worker/litellm /opt/switchback/worker/litellm
RUN mkdir -p /workspace /agent-state /trace-spool /run/switchback-secrets \
    /opt/switchback/secret-input /opt/switchback/secret-output \
    /opt/switchback/attest/dir-0 /opt/switchback/attest/dir-1 /opt/switchback/attest/dir-2 /opt/switchback/attest/dir-3 /opt/switchback/attest/dir-4 \
    && touch /opt/switchback/attest/file-0 /opt/switchback/attest/file-1 /opt/switchback/attest/file-2 /opt/switchback/attest/file-3 /opt/switchback/attest/file-4 \
    && chown -R switchback-agent:switchback-agent /workspace /agent-state /trace-spool /home/switchback-agent \
    && chown -R litellm:litellm /var/lib/switchback-litellm \
    && chmod 755 /usr/local/bin/worker-shim /opt/switchback/bin/fake-acp /opt/switchback/bin/fake-openai
ENV PATH="/usr/local/go/bin:/opt/switchback/node_modules/.bin:/usr/local/bin:/usr/bin:/bin"
WORKDIR /workspace
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/worker-shim"]
