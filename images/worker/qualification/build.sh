#!/bin/sh
# Build integration only. Never execute the compat artifact or use emulation.
set -eu
CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -ldflags='-s -w' -o /out/switchback-qualification-native ./cmd/switchback-qualification-ioctl
case "$(go env GOARCH)" in
  arm64) CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -buildvcs=false -ldflags='-s -w' -o /out/switchback-qualification-compat ./cmd/switchback-qualification-ioctl ;;
  amd64) CGO_ENABLED=0 GOOS=linux GOARCH=386 go build -trimpath -buildvcs=false -ldflags='-s -w' -o /out/switchback-qualification-compat ./cmd/switchback-qualification-ioctl ;;
  *) exit 1 ;;
esac
