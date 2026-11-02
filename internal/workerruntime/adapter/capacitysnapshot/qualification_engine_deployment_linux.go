//go:build linux && (amd64 || arm64)

package capacitysnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/abietic/switchback/internal/platform/capacityprobe/procstatus"
	"github.com/moby/moby/api/types/network"
	"golang.org/x/sys/unix"
)

func qualificationVerifyEngineDeployment(ctx context.Context, kit qualificationKit) error {
	// This cap only shortens the caller's existing context. Historical receipt
	// elapsed_ms is not a TTL and neither qualification window is reset here.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var current qualificationEngineReceiptV2
	return qualificationVerifyEngineDeploymentWith(ctx, kit, qualificationEngineDeploymentReaders{
		read: qualificationEngineReadFile,
		native: func(ctx context.Context, r qualificationEngineReceiptV2) (qualificationEngineNative, error) {
			n, e := qualificationEngineCollectNative(ctx, r)
			if e == nil {
				current = r
			}
			return n, e
		},
		info: func(ctx context.Context) (qualificationEngineLiveInfo, error) {
			return qualificationEngineReadInfo(ctx, current)
		},
		network: func(ctx context.Context, index int, id string) (qualificationEngineNetworkIdentity, error) {
			return qualificationEngineReadNetwork(ctx, current, index, id)
		},
	})
}
func qualificationEngineLeafStat(s unix.Stat_t, mode uint64, limit int64) error {
	return qualificationEngineCheckLeaf(uint64(s.Mode), uint64(s.Nlink), uint64(s.Uid), uint64(s.Gid), s.Size, mode, limit)
}
func qualificationEngineReadFile(ctx context.Context, name string, limit int64, mode uint64) (raw []byte, err error) {
	if limit <= 0 || limit > 1<<20 || !absolute(name) {
		return nil, errQualification
	}
	dir, ids, e := qualificationEngineLinuxOpenDirectory(ctx, filepath.Dir(name))
	if e != nil {
		return nil, e
	}
	defer func() { err = qualificationEngineCleanup(err, qualificationEngineCloseFile(ctx, dir)) }()
	beforeDir, e := qualificationStat(ctx, int(dir.Fd()))
	if e != nil {
		return nil, e
	}
	if (filepath.Dir(name) == "/etc/switchback/m1-engine" || filepath.Dir(name) == "/var/lib/switchback/m1-engine" || filepath.Dir(name) == qualificationEngineBundleRoot) && beforeDir.Mode&07777 != 0700 {
		return nil, errQualification
	}
	fd, e := unix.Openat(int(dir.Fd()), filepath.Base(name), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() { err = qualificationEngineCleanup(err, qualificationEngineCloseFile(ctx, f)) }()
	before, e := qualificationStat(ctx, fd)
	if e != nil || qualificationEngineLeafStat(before, mode, limit) != nil {
		return nil, errQualification
	}
	var data bytes.Buffer
	b := make([]byte, 32768)
	for {
		if e = qualificationContext(ctx); e != nil {
			return nil, e
		}
		n, readErr := f.Read(b)
		data.Write(b[:n])
		if int64(data.Len()) > limit {
			return nil, errQualification
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
		if n == 0 {
			return nil, errQualification
		}
	}
	after, e := qualificationStat(ctx, fd)
	var named unix.Stat_t
	if e != nil || unix.Fstatat(int(dir.Fd()), filepath.Base(name), &named, unix.AT_SYMLINK_NOFOLLOW) != nil || qualificationIdentity(before) != qualificationIdentity(after) || qualificationIdentity(before) != qualificationIdentity(named) || int64(data.Len()) != before.Size {
		return nil, fmt.Errorf("Engine file changed: %s", name)
	}
	fresh, newIDs, e := qualificationEngineLinuxOpenDirectory(ctx, filepath.Dir(name))
	if e != nil {
		return nil, e
	}
	defer func() { err = qualificationEngineCleanup(err, qualificationEngineCloseFile(ctx, fresh)) }()
	if !reflect.DeepEqual(ids, newIDs) {
		return nil, errQualification
	}
	return data.Bytes(), qualificationContext(ctx)
}
func qualificationEngineProc(ctx context.Context, name string, limit int64) (raw []byte, err error) {
	if !strings.HasPrefix(name, "/proc/") || strings.Contains(name, "..") || limit <= 0 || limit > 1<<20 {
		return nil, errQualification
	}
	fd, e := unix.Open(name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() { err = qualificationEngineCleanup(err, qualificationEngineCloseFile(ctx, f)) }()
	var b bytes.Buffer
	buf := make([]byte, 32768)
	for {
		if e = qualificationContext(ctx); e != nil {
			return nil, e
		}
		n, err := f.Read(buf)
		b.Write(buf[:n])
		if int64(b.Len()) > limit {
			return nil, errQualification
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, errQualification
		}
	}
	return b.Bytes(), qualificationContext(ctx)
}

type qualificationEngineCommandOutput struct{ bytes.Buffer }

func (b *qualificationEngineCommandOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 16384 {
		return 0, errQualification
	}
	return b.Buffer.Write(p)
}
func qualificationEngineUnit(ctx context.Context, index int) (map[string]string, error) {
	properties := "Id,ActiveState,SubState,Result,MainPID,ControlGroup,FragmentPath,DropInPaths,NeedDaemonReload,Type,Restart,KillMode,TimeoutStartUSec,TimeoutStopUSec,Slice,User,Group,UMask,Delegate,Environment,EnvironmentFiles"
	cmd := exec.CommandContext(ctx, "/usr/bin/systemctl", "show", "--no-pager", "--property="+properties, qualificationEngineUnits[index])
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C"}
	var stdout, stderr qualificationEngineCommandOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = time.Second
	if e := cmd.Run(); e != nil {
		return nil, fmt.Errorf("Engine unit read failed: %w", e)
	}
	m := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, errQualification
		}
		if _, ok = m[key]; ok {
			return nil, errQualification
		}
		m[key] = value
	}
	if e := qualificationEngineUnitCheck(m, index); e != nil {
		return nil, e
	}
	return m, qualificationContext(ctx)
}
func qualificationEngineObserveDaemon(ctx context.Context, index int, boot string) (qualificationEngineDaemon, error) {
	var d qualificationEngineDaemon
	props, e := qualificationEngineUnit(ctx, index)
	if e != nil {
		return d, e
	}
	pid, e := strconv.ParseUint(props["MainPID"], 10, 31)
	if e != nil || pid == 0 {
		return d, errQualification
	}
	raw, e := qualificationEngineLinuxReadProc(ctx, pid, "stat", 4096)
	if e != nil {
		return d, e
	}
	actual, _, start, e := qualificationProcStat(raw)
	if e != nil || actual != pid || start == 0 {
		return d, errQualification
	}
	exe, e := qualificationEngineLinuxReadlink(ctx, unix.AT_FDCWD, qualificationProcPath(pid, "exe"), "")
	want := []string{"/usr/bin/containerd", "/usr/bin/dockerd"}[index]
	if e != nil || exe != want {
		return d, errQualification
	}
	status, e := qualificationEngineLinuxReadProc(ctx, pid, "status", 65536)
	if e != nil {
		return d, e
	}
	parsed, e := procstatus.Parse(status)
	if e != nil {
		return d, e
	}
	// root UID/GID must hold in every status column, not merely filesystem UID.
	for _, v := range parsed.UIDs {
		if v != 0 {
			return d, errQualification
		}
	}
	for _, v := range parsed.GIDs {
		if v != 0 {
			return d, errQualification
		}
	}
	group, e := qualificationEngineLinuxReadProc(ctx, pid, "cgroup", 4096)
	if e != nil {
		return d, e
	}
	if string(group) != "0::"+props["ControlGroup"]+"\n" {
		return d, errQualification
	}
	argv := qualificationEngineArgv(index)
	cmdline, e := qualificationEngineProc(ctx, qualificationProcPath(pid, "cmdline"), 16384)
	if e != nil {
		return d, e
	}
	if string(cmdline) != strings.Join(argv, "\x00")+"\x00" {
		return d, fmt.Errorf("Engine daemon argv differs")
	}
	// The running executable and installed regular root-owned binary must be
	// the same inode; a same-byte replacement or writable executable is rejected.
	binaryDir, _, e := qualificationEngineLinuxOpenDirectory(ctx, "/usr/bin")
	if e != nil {
		return d, e
	}
	var installed, running unix.Stat_t
	statErr := unix.Fstatat(int(binaryDir.Fd()), filepath.Base(want), &installed, unix.AT_SYMLINK_NOFOLLOW)
	statErr = qualificationEngineCleanup(statErr, qualificationEngineCloseFile(ctx, binaryDir))
	if statErr != nil {
		return d, statErr
	}
	if qualificationEngineLeafStat(installed, 0755, 512<<20) != nil || unix.Stat(qualificationProcPath(pid, "exe"), &running) != nil || qualificationIdentity(installed) != qualificationIdentity(running) {
		return d, errQualification
	}
	hash, e := qualificationEngineLinuxHashExecutable(ctx, pid)
	if e != nil {
		return d, e
	}
	raw, e = qualificationEngineLinuxReadProc(ctx, pid, "stat", 4096)
	if e != nil {
		return d, e
	}
	actual, _, again, e := qualificationProcStat(raw)
	if e != nil || actual != pid || again != start {
		return d, errQualification
	}
	return qualificationEngineDaemon{Unit: qualificationEngineUnits[index], PID: pid, Start: start, Exe: exe, Digest: hash, Cgroup: props["ControlGroup"], Boot: boot}, qualificationContext(ctx)
}
func qualificationEngineFilesystemRead(ctx context.Context) (fs qualificationEngineFilesystem, parent qualificationEngineRuntimeParent, err error) {
	raw, e := qualificationEngineLinuxReadProc(ctx, uint64(os.Getpid()), "mountinfo", 65536)
	if e != nil {
		return fs, parent, e
	}
	mounts, e := qualificationMountInfo(raw)
	if e != nil {
		return fs, parent, e
	}
	data, ok := mounts["/mnt/lima-sbquota"]
	runtime, rOK := mounts["/run/switchback-engine"]
	if !ok || !rOK || data.Root != "/" || runtime.Root != "/" || data.ReadOnly || runtime.ReadOnly {
		return fs, parent, errQualification
	}
	foundData, foundRuntime := false, false
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		fields := strings.Fields(line)
		sep := 0
		for i, s := range fields {
			if s == "-" {
				sep = i
				break
			}
		}
		if sep < 6 || len(fields) != sep+4 {
			return fs, parent, errQualification
		}
		if fields[4] == "/mnt/lima-sbquota" {
			opts := "," + fields[5] + "," + fields[sep+3] + ","
			foundData = fields[sep+1] == "xfs" && strings.Contains(opts, ",prjquota,")
		}
		if fields[4] == "/run/switchback-engine" {
			foundRuntime = fields[sep+1] == "tmpfs"
		}
	}
	if !foundData || !foundRuntime {
		return fs, parent, errQualification
	}
	dir, _, e := qualificationEngineLinuxOpenDirectory(ctx, "/mnt/lima-sbquota")
	if e != nil {
		return fs, parent, e
	}
	defer func() { err = qualificationEngineCleanup(err, qualificationEngineCloseFile(ctx, dir)) }()
	identity, e := qualificationNativeFD(ctx, int(dir.Fd()), "/mnt/lima-sbquota")
	if e != nil || identity.Device != data.Device || identity.MountID != data.MountID {
		return fs, parent, errQualification
	}
	if _, e = qualificationQuotaState(ctx, int(dir.Fd())); e != nil {
		return fs, parent, e
	}
	var major, minor uint32
	if n, _ := fmt.Sscanf(identity.Device, "%d:%d", &major, &minor); n != 2 {
		return fs, parent, errQualification
	}
	fs = qualificationEngineFilesystem{Mountpoint: "/mnt/lima-sbquota", FSType: "xfs", UUID: identity.FSUUID, Major: major, Minor: minor, RootInode: identity.Inode, QuotaEnforced: true}
	rd, _, e := qualificationEngineLinuxOpenDirectory(ctx, "/run/switchback-engine")
	if e != nil {
		return fs, parent, e
	}
	defer func() { err = qualificationEngineCleanup(err, qualificationEngineCloseFile(ctx, rd)) }()
	s, e := qualificationStat(ctx, int(rd.Fd()))
	var v unix.Statfs_t
	if e != nil || s.Gid != 0 || s.Mode&07777 != 0700 || unix.Fstatfs(int(rd.Fd()), &v) != nil || v.Type != unix.TMPFS_MAGIC || v.Bsize <= 0 {
		return fs, parent, errQualification
	}
	if fmt.Sprintf("%d:%d", unix.Major(uint64(s.Dev)), unix.Minor(uint64(s.Dev))) != runtime.Device {
		return fs, parent, errQualification
	}
	parent = qualificationEngineRuntimeParent{Path: "/run/switchback-engine", Device: uint64(s.Dev), Inode: s.Ino, UID: uint64(s.Uid), GID: uint64(s.Gid), Mode: uint64(s.Mode & 07777), FSType: "tmpfs", SizeBytes: uint64(v.Bsize) * v.Blocks, Inodes: v.Files}
	return fs, parent, qualificationContext(ctx)
}

func qualificationEnginePIDs(ctx context.Context) (result []uint64, err error) {
	if e := qualificationContext(ctx); e != nil {
		return nil, e
	}
	f, e := os.Open("/proc")
	if e != nil {
		return nil, e
	}
	defer func() {
		err = qualificationEngineCleanup(err, qualificationEngineCloseFile(ctx, f))
		if ce := qualificationContext(ctx); ce != nil {
			err = qualificationEngineCleanup(err, ce)
		}
	}()
	entries, e := f.Readdirnames(65537)
	if e != nil && e != io.EOF {
		return nil, e
	}
	if len(entries) > 65536 {
		return nil, errQualification
	}
	out := []uint64{}
	for _, name := range entries {
		if qualificationContext(ctx) != nil {
			return nil, ctx.Err()
		}
		pid, e := strconv.ParseUint(name, 10, 31)
		if e == nil && pid > 0 {
			out = append(out, pid)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}
func qualificationEngineProcesses(ctx context.Context) ([]qualificationEngineProcess, error) {
	return qualificationEngineProcessesWith(ctx, qualificationEngineLinuxProcessOps())
}

func qualificationEngineReadTables(ctx context.Context) ([]qualificationEngineListener, error) {
	out := []qualificationEngineListener{}
	for _, family := range []string{"unix", "tcp", "tcp6"} {
		raw, e := qualificationEngineProc(ctx, "/proc/net/"+family, 1<<20)
		if e != nil {
			return nil, e
		}
		rows, e := qualificationEngineParseListeners(raw, family)
		if e != nil {
			return nil, e
		}
		out = append(out, rows...)
	}
	return out, nil
}

// Hold each private chain directory open and stat every socket relative to its
// exact immediate-parent FD. Never follow a socket or directory symlink.
func qualificationEngineSampleSockets(ctx context.Context, paths []string, daemons []qualificationEngineDaemon) (out qualificationEngineSocketSample, err error) {
	if len(paths) != 6 || len(daemons) != 2 {
		return out, errQualification
	}
	dirs := map[string]*os.File{}
	stats := map[string]unix.Stat_t{}
	defer func() {
		for _, f := range dirs {
			err = qualificationEngineCleanup(err, qualificationEngineCloseFile(ctx, f))
		}
		if ce := qualificationContext(ctx); ce != nil {
			err = qualificationEngineCleanup(err, ce)
		}
	}()
	for i, name := range qualificationEngineSocketParentPaths {
		var dir *os.File
		if i == 0 {
			f, _, e := qualificationEngineLinuxOpenDirectory(ctx, name)
			if e != nil {
				return out, e
			}
			dir = f
		} else {
			if qualificationContext(ctx) != nil {
				return out, ctx.Err()
			}
			parent := dirs[filepath.Dir(name)]
			if parent == nil {
				return out, errQualification
			}
			fd, e := unix.Openat(int(parent.Fd()), filepath.Base(name), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if e != nil {
				return out, e
			}
			dir = os.NewFile(uintptr(fd), name)
		}
		dirs[name] = dir
		st, e := qualificationStat(ctx, int(dir.Fd()))
		if e != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != 0 || st.Gid != 0 || st.Mode&07777 != 0700 {
			return out, errQualification
		}
		stats[name] = st
		out.Parents = append(out.Parents, qualificationEngineSocketParent{Path: name, Device: uint64(st.Dev), Inode: st.Ino, Kind: "directory", UID: uint64(st.Uid), GID: uint64(st.Gid), Mode: uint64(st.Mode & 07777)})
		out.ParentLinks = append(out.ParentLinks, uint64(st.Nlink))
	}
	for i, name := range paths {
		parent := filepath.Dir(name)
		dir := dirs[parent]
		if dir == nil {
			return out, errQualification
		}
		if qualificationContext(ctx) != nil {
			return out, ctx.Err()
		}
		var st unix.Stat_t
		if unix.Fstatat(int(dir.Fd()), filepath.Base(name), &st, unix.AT_SYMLINK_NOFOLLOW) != nil || st.Mode&unix.S_IFMT != unix.S_IFSOCK {
			return out, errQualification
		}
		p := stats[parent]
		di := 0
		if i >= 2 {
			di = 1
		}
		d := daemons[di]
		out.Sockets = append(out.Sockets, qualificationEngineSocketIdentity{Path: name, Device: uint64(st.Dev), Inode: st.Ino, Kind: "socket", Nlink: uint64(st.Nlink), UID: uint64(st.Uid), GID: uint64(st.Gid), Mode: uint64(st.Mode & 07777), OwnerUnit: d.Unit, OwnerPID: d.PID, OwnerStart: d.Start, ParentPath: parent, ParentDevice: uint64(p.Dev), ParentInode: p.Ino, ParentUID: uint64(p.Uid), ParentGID: uint64(p.Gid), ParentMode: uint64(p.Mode & 07777)})
	}
	// The root path and every child entry must still designate the held object.
	for name, dir := range dirs {
		held, e := qualificationStat(ctx, int(dir.Fd()))
		original := stats[name]
		if e != nil || !qualificationEngineDirectoryStatEqual(held, original) {
			return out, errQualification
		}
		var linked unix.Stat_t
		if name == qualificationEngineSocketParentPaths[0] {
			again, _, e := qualificationEngineLinuxOpenDirectory(ctx, name)
			if e != nil {
				return out, e
			}
			linked, e = qualificationStat(ctx, int(again.Fd()))
			e = qualificationEngineCleanup(e, qualificationEngineCloseFile(ctx, again))
			if e != nil {
				return out, e
			}
		} else if unix.Fstatat(int(dirs[filepath.Dir(name)].Fd()), filepath.Base(name), &linked, unix.AT_SYMLINK_NOFOLLOW) != nil {
			return out, errQualification
		}
		if !qualificationEngineDirectoryStatEqual(linked, original) {
			return out, errQualification
		}
	}
	return out, qualificationContext(ctx)
}
func qualificationEngineDirectoryStatEqual(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode && a.Uid == b.Uid && a.Gid == b.Gid && a.Nlink == b.Nlink
}

// Three fixed GETs, each following its own native gate. Fixed API version avoids
// an SDK negotiation connection or retries hidden before the requested read.
func qualificationEngineReadAPI(ctx context.Context, receipt qualificationEngineReceiptV2, endpoint string) ([]byte, error) {
	allowed := endpoint == "/info"
	if strings.HasPrefix(endpoint, "/networks/") {
		allowed = containerIDPattern.MatchString(strings.TrimPrefix(endpoint, "/networks/"))
	}
	if !allowed {
		return nil, errQualification
	}
	dial := net.Dialer{Timeout: 2 * time.Second}
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 3 * time.Second, MaxResponseHeaderBytes: 8192, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		if _, e := qualificationEngineCollectNative(ctx, receipt); e != nil {
			return nil, e
		}
		return dial.DialContext(ctx, "unix", "/run/switchback-engine/docker.sock")
	}}
	defer tr.CloseIdleConnections()
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/v1.56"+endpoint, nil)
	if e != nil {
		return nil, e
	}
	req.Close = true
	response, e := (&http.Client{Transport: qualificationBoundedEngineTransport{base: tr}, CheckRedirect: func(*http.Request, []*http.Request) error { return errQualification }}).Do(req)
	if e != nil {
		return nil, e
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, errQualification
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, qualificationEngineBodyLimit+1))
	if e != nil || len(raw) > qualificationEngineBodyLimit {
		return nil, errQualification
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if _, e = qualificationEngineJSONValue(d, 0, new(int)); e != nil {
		return nil, e
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, errQualification
	}
	return raw, qualificationContext(ctx)
}
func qualificationEngineReadInfo(ctx context.Context, receipt qualificationEngineReceiptV2) (qualificationEngineLiveInfo, error) {
	var result qualificationEngineLiveInfo
	raw, e := qualificationEngineReadAPI(ctx, receipt, "/info")
	if e != nil {
		return result, e
	}
	var info struct {
		ID, ServerVersion, DockerRootDir, Driver, CgroupDriver, CgroupVersion string
		NCPU, MemTotal                                                        int64
		DriverStatus                                                          [][]string
		LiveRestoreEnabled                                                    bool
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return result, errQualification
	}
	for _, name := range []string{"ID", "ServerVersion", "DockerRootDir", "Driver", "CgroupDriver", "CgroupVersion", "NCPU", "MemTotal", "DriverStatus", "LiveRestoreEnabled"} {
		if v, ok := fields[name]; !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return result, errQualification
		}
	}
	if json.Unmarshal(raw, &info) != nil || info.DriverStatus == nil || info.LiveRestoreEnabled {
		return result, errQualification
	}
	snapshotter := false
	for _, row := range info.DriverStatus {
		if len(row) != 2 {
			return result, errQualification
		}
		snapshotter = snapshotter || row[0] == "driver-type" && strings.Contains(row[1], "snapshotter")
	}
	return qualificationEngineLiveInfo{ID: info.ID, Version: info.ServerVersion, Root: info.DockerRootDir, Driver: info.Driver, Snapshotter: snapshotter, CgroupDriver: info.CgroupDriver, CgroupVersion: info.CgroupVersion, CPUs: info.NCPU, Memory: info.MemTotal}, qualificationContext(ctx)
}
func qualificationEngineReadNetwork(ctx context.Context, receipt qualificationEngineReceiptV2, index int, id string) (qualificationEngineNetworkIdentity, error) {
	var out qualificationEngineNetworkIdentity
	raw, e := qualificationEngineReadAPI(ctx, receipt, "/networks/"+id)
	if e != nil {
		return out, e
	}
	var actual network.Inspect
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return out, errQualification
	}
	for _, name := range []string{"Name", "Id", "Driver", "Scope", "Internal", "Labels", "EnableIPv6", "Attachable", "Ingress", "IPAM"} {
		if v, ok := fields[name]; !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return out, errQualification
		}
	}
	if json.Unmarshal(raw, &actual) != nil || actual.ConfigOnly || actual.ConfigFrom.Network != "" || !actual.EnableIPv4 {
		return out, errQualification
	}
	options := actual.Options
	if options == nil {
		options = map[string]string{}
	}
	ipamOptions := actual.IPAM.Options
	if ipamOptions == nil {
		ipamOptions = map[string]string{}
	}
	out = qualificationEngineNetworkIdentity{Name: actual.Name, ID: actual.ID, Driver: actual.Driver, Scope: actual.Scope, Internal: actual.Internal, Labels: actual.Labels, IPv6: actual.EnableIPv6, Attachable: actual.Attachable, Ingress: actual.Ingress, Options: options, IPAM: qualificationEngineIPAM{Driver: actual.IPAM.Driver, Options: ipamOptions, Config: []qualificationEngineAllocation{}}}
	for _, c := range actual.IPAM.Config {
		if c.IPRange.IsValid() || len(c.AuxAddress) != 0 {
			return out, errQualification
		}
		out.IPAM.Config = append(out.IPAM.Config, qualificationEngineAllocation{Subnet: c.Subnet.String(), Gateway: c.Gateway.String()})
	}
	return out, qualificationEngineNetworkCheck(out, index)
}
