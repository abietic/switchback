//go:build linux && (amd64 || arm64)

package main

import (
	"context"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"strings"
)

func readInstalledObserverEnvironment(ctx context.Context) ([]byte, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, fmt.Errorf("observer context unavailable")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { unix.Close(fd) }()
	for _, part := range strings.Split("etc/switchback/created-layer-observer", "/") {
		var s unix.Stat_t
		if err = unix.Fstat(fd, &s); err != nil || s.Uid != 0 || s.Mode&0022 != 0 {
			return nil, fmt.Errorf("observer ancestor unsafe")
		}
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e != nil {
			return nil, e
		}
		unix.Close(fd)
		fd = next
	}
	var d unix.Stat_t
	if unix.Fstat(fd, &d) != nil || d.Uid != 0 || d.Mode&0022 != 0 {
		return nil, fmt.Errorf("observer directory unsafe")
	}
	leaf, err := unix.Openat(fd, "deployment.env", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(leaf), observerInstalledEnvironment)
	defer f.Close()
	var before, after unix.Stat_t
	if unix.Fstat(leaf, &before) != nil || before.Uid != 0 || before.Mode&0222 != 0 || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || before.Size < 1 || before.Size > 1024 {
		return nil, fmt.Errorf("observer environment unsafe")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 1025))
	if err != nil || unix.Fstat(leaf, &after) != nil || len(raw) > 1024 || int64(len(raw)) != before.Size {
		return nil, fmt.Errorf("observer environment read failed")
	}
	before.Atim = after.Atim
	if before != after || ctx.Err() != nil {
		return nil, fmt.Errorf("observer environment changed")
	}
	return raw, nil
}
