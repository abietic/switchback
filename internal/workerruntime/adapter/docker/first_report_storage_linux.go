//go:build linux

package docker

import (
	"fmt"
	"math"

	"golang.org/x/sys/unix"
)

func readFirstReportStorage() (int64, error) {
	fd, err := unix.Open(firstReportCapacityMount, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, fmt.Errorf("open first-report capacity mount: %w", err)
	}
	defer unix.Close(fd)
	var entry unix.Stat_t
	if err := unix.Fstat(fd, &entry); err != nil || entry.Uid != 0 || entry.Mode&0o022 != 0 {
		return 0, fmt.Errorf("first-report capacity bind must be owned by root and protected from writes")
	}
	var fs unix.Statfs_t
	if err := unix.Fstatfs(fd, &fs); err != nil {
		return 0, fmt.Errorf("read first-report XFS capacity: %w", err)
	}
	if fs.Type != unix.XFS_SUPER_MAGIC || fs.Flags&unix.ST_RDONLY == 0 || fs.Bsize <= 0 || fs.Bavail == 0 || fs.Bavail > uint64(math.MaxInt64/fs.Bsize) {
		return 0, fmt.Errorf("first-report capacity requires a read-only XFS bind with known free space")
	}
	return int64(fs.Bavail) * fs.Bsize, nil
}
