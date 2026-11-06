package capacityprobe

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func openDirectory(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("canonical absolute directory required")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, kernelReadError("directory unavailable", err)
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "" {
			continue
		}
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			return nil, kernelReadError("unsafe or unavailable directory", e)
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), path), nil
}
func randomLeaf(prefix string) (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", errors.New("archive identity unavailable")
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

// PublishBlocked requires exclusive deployment ownership and a private directory.
// External admission shutdown is a prerequisite for taking over a previously live directory.
// On error it makes NO statement about external Trust, cached samples, or Backend admission.
func PublishBlocked(directory string, c Collection) error { return publishBlocked(directory, c, nil) }
func publishBlocked(directory string, c Collection, beforeInvalidate func()) error {
	if c.Status != "preparation_blocked" || c.Trust != "none" {
		return errors.New("only blocked untrusted collection supported")
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil || len(data) > MaxResponse {
		return errors.New("collection invalid or oversized")
	}
	dir, err := openDirectory(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil || info.Mode().Perm() != 0700 {
		return errors.New("private directory required")
	}
	var st unix.Stat_t
	if unix.Fstat(int(dir.Fd()), &st) != nil || st.Uid != uint32(os.Geteuid()) {
		return errors.New("directory owner mismatch")
	}
	if st.Mode&0200 == 0 {
		return errors.New("directory not writable")
	}
	same := func() bool {
		current, e := openDirectory(directory)
		if e != nil {
			return false
		}
		defer current.Close()
		now, e := current.Stat()
		return e == nil && os.SameFile(info, now)
	}
	// Validate every existing publication entry before modifying the old signed file.
	for _, leaf := range []string{"probe-status.json", "capacity.json"} {
		var entry unix.Stat_t
		e := unix.Fstatat(int(dir.Fd()), leaf, &entry, unix.AT_SYMLINK_NOFOLLOW)
		if e != nil && e != unix.ENOENT {
			return errors.New("publication entry unavailable")
		}
		if e == nil && (entry.Mode&unix.S_IFMT != unix.S_IFREG || entry.Size > MaxResponse || entry.Nlink != 1) {
			return errors.New("unsafe publication entry")
		}
	}
	temp, err := randomLeaf(".blocked-")
	if err != nil {
		return err
	}
	fd, err := unix.Openat(int(dir.Fd()), temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return errors.New("status staging failed")
	}
	file := os.NewFile(uintptr(fd), temp)
	_, err = file.Write(append(data, '\n'))
	if err == nil {
		err = file.Sync()
	}
	file.Close()
	defer unix.Unlinkat(int(dir.Fd()), temp, 0)
	if err != nil {
		return errors.New("status staging failed")
	}
	if beforeInvalidate != nil {
		beforeInvalidate()
	}
	if !same() {
		return errors.New("publication directory replaced; external admission state unknown")
	}
	oldfd, err := unix.Openat(int(dir.Fd()), "capacity.json", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil && err != unix.ENOENT {
		return errors.New("old evidence unavailable")
	}
	if err == nil {
		old := os.NewFile(uintptr(oldfd), "capacity.json")
		defer old.Close()
		oldInfo, e := old.Stat()
		if e != nil || !oldInfo.Mode().IsRegular() || oldInfo.Size() > MaxResponse {
			return errors.New("unsafe old evidence")
		}
		raw, e := io.ReadAll(io.LimitReader(old, MaxResponse+1))
		if e != nil || len(raw) > MaxResponse {
			return errors.New("old evidence read failed")
		}
		archive, e := randomLeaf("quarantine-capacity-")
		if e != nil {
			return e
		}
		// Linkat is no-replace: archive contains exact original inode/bytes, never an overwrite.
		if !same() {
			return errors.New("publication directory replaced")
		}
		if unix.Linkat(int(dir.Fd()), "capacity.json", int(dir.Fd()), archive, 0) != nil {
			return errors.New("old evidence quarantine failed")
		}
		var original, archived unix.Stat_t
		if unix.Fstat(oldfd, &original) != nil || unix.Fstatat(int(dir.Fd()), archive, &archived, unix.AT_SYMLINK_NOFOLLOW) != nil || original.Ino != archived.Ino || original.Dev != archived.Dev || !same() {
			return errors.New("evidence identity changed; external admission state unknown")
		}
		if unix.Unlinkat(int(dir.Fd()), "capacity.json", 0) != nil {
			return errors.New("old evidence invalidation failed")
		}
		if dir.Sync() != nil {
			return errors.New("invalidation durability unknown")
		}
	}
	if !same() {
		return errors.New("publication directory replaced; external admission state unknown")
	}
	if unix.Renameat(int(dir.Fd()), temp, int(dir.Fd()), "probe-status.json") != nil {
		return errors.New("blocked status publication failed")
	}
	if dir.Sync() != nil || !same() {
		return errors.New("blocked status durability or identity unknown")
	}
	return nil
}

// LoadDeploymentManifest reads only the fixed deployment-owned manifest, bounded before allocation.
func LoadDeploymentManifest() (Manifest, error) {
	dir, err := openDirectory("/etc/switchback/capacity-probe")
	if err != nil {
		return Manifest{}, err
	}
	defer dir.Close()
	var parent unix.Stat_t
	if unix.Fstat(int(dir.Fd()), &parent) != nil || parent.Uid != 0 || parent.Mode&0022 != 0 {
		return Manifest{}, errors.New("deployment directory ownership rejected")
	}
	fd, err := unix.Openat(int(dir.Fd()), "collector-manifest.json", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return Manifest{}, errors.New("deployment manifest unavailable")
	}
	file := os.NewFile(uintptr(fd), "collector-manifest.json")
	defer file.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Size > 4096 || st.Uid != 0 || st.Mode&0022 != 0 {
		return Manifest{}, errors.New("deployment manifest ownership or size rejected")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(raw) > 4096 {
		return Manifest{}, errors.New("deployment manifest read failed")
	}
	return ParseManifest(raw)
}
