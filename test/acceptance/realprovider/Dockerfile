ARG GO_IMAGE=docker.io/library/golang@sha256:b58a2431884c6dfc5ddcea953c7f426ad72c2ff9e94b4b8495fefa74e4dabcc0
ARG DEBIAN_IMAGE=docker.io/library/debian@sha256:9b67294679b30e5d6ab257b40594feeb4a4b81f7fcf4131f4decf0d6a212a9b0

FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal ./internal
COPY test/acceptance/realprovider ./test/acceptance/realprovider
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags='-s -w' -o /out/real-provider-acceptance ./test/acceptance/realprovider

FROM ${DEBIAN_IMAGE}
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/real-provider-acceptance /usr/local/bin/real-provider-acceptance
COPY test/fixtures/m1/real-provider/fixture.txt /opt/switchback-acceptance/fixture.txt
USER 0:0
ENTRYPOINT ["/usr/local/bin/real-provider-acceptance"]
