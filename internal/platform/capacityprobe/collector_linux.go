//go:build linux

package capacityprobe

import (
	"errors"
	"io"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// UAPI layout source: Linux v6.8 include/uapi/linux/{fs.h,dqblk_xfs.h,quota.h}.
// Only little-endian Linux arm64/amd64 ABI is supported. Exact Ubuntu SRU remains unproven.
const fsGetXAttr = 0x801c581f                       // _IOR('X',31,28-byte fsxattr), no SET operation.
const getProjectQuota = ((('X' << 8) + 3) << 8) | 2 // QCMD(Q_XGETQUOTA, XQM_PRJQUOTA)

func readFixed(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, kernelReadError("fixed kernel path unavailable", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, kernelReadError("fixed kernel read failed", err)
	}
	if int64(len(b)) > limit {
		return nil, errors.New("fixed kernel read oversized")
	}
	return b, nil
}
func identities() (string, string, error) {
	b, err := readFixed("/proc/sys/kernel/random/boot_id", 64)
	if err != nil {
		return "", "", err
	}
	boot := strings.TrimSpace(string(b))
	ns, err := os.Readlink("/proc/self/ns/mnt")
	if err != nil {
		return "", "", kernelReadError("mount namespace unavailable", err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$`).MatchString(boot) || !regexp.MustCompile(`^mnt:\[[0-9]+\]$`).MatchString(ns) {
		return "", "", errors.New("kernel identity invalid")
	}
	return boot, ns, nil
}
func ReadLinux(c *Collection) (*LinuxFacts, error) {
	if runtime.GOARCH != "arm64" && runtime.GOARCH != "amd64" {
		return nil, errors.New("unsupported Linux ABI")
	}
	boot, ns, err := identities()
	if err != nil {
		return nil, err
	}
	f := &LinuxFacts{BootID: boot, MountNamespace: ns, Cgroups: map[string]string{}}
	mount, err := readFixed("/proc/self/mountinfo", MaxResponse)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(mount), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 || fields[4] != Root {
			continue
		}
		sep := strings.Index(line, " - ")
		if sep < 0 {
			continue
		}
		tail := strings.Fields(line[sep+3:])
		if len(tail) < 3 || tail[0] != "xfs" || !regexp.MustCompile(`^[0-9]+:[0-9]+$`).MatchString(fields[2]) {
			continue
		}
		if _, err := strconv.ParseUint(fields[0], 10, 64); err != nil {
			continue
		}
		if f.MountID != "" {
			return nil, errors.New("ambiguous pool mount")
		}
		f.MountID = fields[0]
		f.Device = fields[2]
		f.FilesystemType = tail[0]
	}
	if f.MountID == "" {
		c.Blockers = append(c.Blockers, "exact_xfs_mount_missing")
	}
	expected := map[string]uint32{Root: 0, Root + "/core": 100, Root + "/product": 101, Root + "/docker": 999}
	for _, path := range []string{Root, Root + "/core", Root + "/product", Root + "/docker"} {
		start := time.Now().UTC()
		dir, err := openDirectory(path)
		if err != nil {
			c.sample("open:"+path, start, err)
			c.Blockers = append(c.Blockers, "domain_unavailable")
			continue
		}
		var st unix.Statfs_t
		err = unix.Fstatfs(int(dir.Fd()), &st)
		c.sample("statfs:"+path, start, err)
		if err != nil {
			dir.Close()
			c.Blockers = append(c.Blockers, "statfs_unavailable")
			continue
		}
		x := Filesystem{Path: path, Type: st.Type, BlockSize: st.Bsize, FragmentSize: st.Frsize, Blocks: st.Blocks, Free: st.Bfree, Available: st.Bavail, Files: st.Files, FreeFiles: st.Ffree, FSID: st.Fsid.Val,
			Quota: Quota{Status: "unavailable", ABI: "linux_v6.8_fs_disk_quota_112_le", UnitBytes: 512}}
		if st.Type != 0x58465342 || st.Bsize <= 0 || st.Frsize <= 0 || st.Bavail > st.Bfree || st.Bfree > st.Blocks {
			c.Blockers = append(c.Blockers, "statfs_geometry_invalid")
		}
		if _, e := Scale(st.Bavail, uint64(st.Bsize)); e != nil {
			c.Blockers = append(c.Blockers, "statfs_unit_overflow")
		}
		var attrs [7]uint32
		start = time.Now().UTC()
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, dir.Fd(), fsGetXAttr, uintptr(unsafe.Pointer(&attrs[0])))
		var attrErr error
		if errno != 0 {
			attrErr = errno
		}
		c.sample("FSGETXATTR:"+path, start, attrErr)
		if errno == 0 {
			flags, id := attrs[0], attrs[3]
			x.XFlags = &flags
			x.ProjectID = &id
			if id != expected[path] || (path == Root && flags&0x200 != 0) || (path != Root && flags&0x200 == 0) {
				c.Blockers = append(c.Blockers, "domain_project_inheritance_mismatch")
			}
			// Fixed deployment project IDs only, never arbitrary IDs read from a directory.
			if id == expected[path] {
				var aligned [14]uint64 // 112 bytes; 64-bit aligned per dqblk_xfs.h.
				start = time.Now().UTC()
				_, _, errno = unix.Syscall6(unix.SYS_QUOTACTL_FD, dir.Fd(), getProjectQuota, uintptr(id), uintptr(unsafe.Pointer(&aligned[0])), 0, 0)
				var quotaErr error
				if errno != 0 {
					quotaErr = errno
				}
				c.sample("Q_XGETQUOTA:"+path, start, quotaErr)
				if errno == 0 {
					raw := unsafe.Slice((*byte)(unsafe.Pointer(&aligned[0])), 112)
					q, e := parseQuota(raw, id)
					if e == nil {
						x.Quota = q
					} else {
						c.Blockers = append(c.Blockers, "quota_abi_invalid")
					}
				}
			}
		}
		if x.Quota.Status != "observed" {
			c.Blockers = append(c.Blockers, "quota_or_inode_readback_missing")
		}
		dir.Close()
		f.Filesystems = append(f.Filesystems, x)
	}
	for _, group := range []string{"switchback.slice/switchback-core.slice", "switchback.slice/switchback-workers.slice"} {
		for _, leaf := range []string{"cpu.max", "memory.max", "memory.swap.max", "pids.max", "cgroup.procs"} {
			path := "/sys/fs/cgroup/" + group + "/" + leaf
			start := time.Now().UTC()
			b, e := readFixed(path, 16384)
			c.sample("cgroup:"+group+"/"+leaf, start, e)
			if e != nil || !regexp.MustCompile(`^[0-9 max\n]+$`).Match(b) {
				c.Blockers = append(c.Blockers, "aggregate_cgroup_unavailable")
				continue
			}
			f.Cgroups[group+"/"+leaf] = string(b)
		}
	}
	bootEnd, nsEnd, e := identities()
	mountEnd, me := readFixed("/proc/self/mountinfo", MaxResponse)
	if e != nil {
		return nil, kernelReadError("sampling identity unavailable", e)
	}
	if me != nil {
		return nil, kernelReadError("sampling mount read unavailable", me)
	}
	if boot != bootEnd || ns != nsEnd || string(mount) != string(mountEnd) {
		return nil, errors.New("sampling identity changed")
	}
	return f, nil
}
