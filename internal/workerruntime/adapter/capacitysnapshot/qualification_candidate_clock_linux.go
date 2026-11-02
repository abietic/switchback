//go:build linux

package capacitysnapshot

import (
	"context"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func qualificationGuestBoot(ctx context.Context) (string, error) {
	if err := qualificationContext(ctx); err != nil {
		return "", err
	}
	f, err := os.Open("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 65))
	if err != nil || len(b) > 64 {
		return "", errQualification
	}
	boot := strings.TrimSpace(string(b))
	if !qualificationUUIDPattern.MatchString(boot) || qualificationContext(ctx) != nil {
		return "", errQualification
	}
	return boot, nil
}

func qualificationMonotonicNS() (int64, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil || ts.Sec < 0 || ts.Nsec < 0 {
		return 0, errQualification
	}
	return ts.Nano(), nil
}
