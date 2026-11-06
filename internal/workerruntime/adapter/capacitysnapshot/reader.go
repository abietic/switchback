package capacitysnapshot

import (
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Reader is constructed once with a deployment-owned directory. Task input
// cannot choose a filename. Each read opens the directory entry again so atomic
// probe rename is visible; O_NOFOLLOW applies to both directory and snapshot.
type Reader struct{ directory string }

func NewReader(directory string) (*Reader, error) {
	if !absolute(directory) {
		return nil, fmt.Errorf("snapshot directory must be canonical absolute path")
	}
	// Reject symlink ancestors too; deployment mount paths must be stable.
	for p := directory; p != "/"; p = filepath.Dir(p) {
		s, e := os.Lstat(p)
		if e != nil {
			return nil, e
		}
		if !s.IsDir() || s.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("unsafe snapshot directory")
		}
	}
	return &Reader{directory: directory}, nil
}
func (r *Reader) Read() ([]byte, error) {
	if r == nil || r.directory == "" {
		return nil, fmt.Errorf("snapshot reader unconfigured")
	}
	return readBoundedFile(r.directory, "capacity.json")
}

func readBoundedFile(directory, leaf string) ([]byte, error) {
	dir, err := openSnapshotDirectory(directory)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	before, err := dir.Stat()
	if err != nil || !before.IsDir() {
		return nil, fmt.Errorf("snapshot directory unavailable")
	}
	filename := filepath.Join(directory, leaf)
	fd, err := unix.Openat(int(dir.Fd()), leaf, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	var file *os.File
	if err == nil {
		file = os.NewFile(uintptr(fd), filename)
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxBytes {
		return nil, fmt.Errorf("snapshot is not a bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil || len(raw) > MaxBytes || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil, fmt.Errorf("snapshot changed during read")
	}
	current, err := os.Lstat(filename)
	if err != nil || !os.SameFile(info, current) {
		return nil, fmt.Errorf("snapshot replaced during read")
	}
	currentDir, err := os.Lstat(directory)
	if err != nil || !os.SameFile(before, currentDir) {
		return nil, fmt.Errorf("snapshot directory replaced during read")
	}
	return raw, nil
}

func openSnapshotDirectory(path string) (*os.File, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if path == "/" {
		return os.NewFile(uintptr(fd), path), nil
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), path), nil
}
