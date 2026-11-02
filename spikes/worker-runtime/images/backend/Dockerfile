ARG GO_IMAGE=docker.io/library/golang@sha256:b58a2431884c6dfc5ddcea953c7f426ad72c2ff9e94b4b8495fefa74e4dabcc0
ARG DEBIAN_IMAGE=docker.io/library/debian@sha256:9b67294679b30e5d6ab257b40594feeb4a4b81f7fcf4131f4decf0d6a212a9b0

FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/spike-backend ./backend/cmd/spike-backend

FROM ${DEBIAN_IMAGE}
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates git \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/spike-backend /usr/local/bin/spike-backend
COPY fixtures/go-repository /opt/switchback/fixture
RUN chmod -R a-w /opt/switchback/fixture \
    && test -x /usr/local/bin/spike-backend \
    && git --version
EXPOSE 8080 9090
ENTRYPOINT ["/usr/local/bin/spike-backend"]
