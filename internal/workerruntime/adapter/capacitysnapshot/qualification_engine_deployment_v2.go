package capacitysnapshot

import (
	"fmt"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The v1 DTO and decoder remain an explicit historical route. Live readers
// decode this complete v2 object directly, including all six roles and parents.
type qualificationEngineReceiptV2 struct {
	qualificationEngineReceipt
	SocketParents []qualificationEngineSocketParent `json:"socket_parents"`
}

type qualificationEngineSocketParent struct {
	Path   string `json:"path"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Kind   string `json:"kind"`
	UID    uint64 `json:"uid"`
	GID    uint64 `json:"gid"`
	Mode   uint64 `json:"mode"`
}

var qualificationEngineSocketPathsV2 = [...]string{
	"/run/switchback-engine/m1-resident/containerd.sock",
	"/run/switchback-engine/m1-resident/containerd.ttrpc",
	"/run/switchback-engine/docker.sock",
	"/run/switchback-engine/m1-resident/docker/extensions/callback.sock",
	"/run/switchback-engine/m1-resident/docker/metrics.sock",
}
var qualificationEngineSocketParentPaths = [...]string{
	"/run/switchback-engine", "/run/switchback-engine/m1-resident",
	"/run/switchback-engine/m1-resident/docker",
	"/run/switchback-engine/m1-resident/docker/extensions",
	"/run/switchback-engine/m1-resident/docker/libnetwork",
}
var qualificationEngineLibnetworkPath = regexp.MustCompile(`^/run/switchback-engine/m1-resident/docker/libnetwork/[0-9a-f]{12}\.sock$`)

func qualificationEngineJSONFields(t reflect.Type) []reflect.StructField {
	var fields []reflect.StructField
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous && f.Type == reflect.TypeOf(qualificationEngineReceipt{}) {
			fields = append(fields, qualificationEngineJSONFields(f.Type)...)
		} else {
			fields = append(fields, f)
		}
	}
	return fields
}

func qualificationEngineReceiptV2Decode(raw []byte) (qualificationEngineReceiptV2, error) {
	var r qualificationEngineReceiptV2
	if err := qualificationEngineDecode(raw, &r); err != nil {
		return r, err
	}
	_, err := qualificationEngineReceiptCheck(r.qualificationEngineReceipt, 2, r.SocketParents)
	return r, err
}

func qualificationEngineDaemonsCheck(ds []qualificationEngineDaemon, boot string, count int) error {
	if !qualificationUUIDPattern.MatchString(boot) || len(ds) != count || count > 2 {
		return errQualification
	}
	seen := map[uint64]bool{}
	for i, d := range ds {
		if d.Unit != qualificationEngineUnits[i] || d.Boot != boot || d.PID == 0 || d.PID > 1<<31-1 || d.Start == 0 || d.Start > 1<<63-1 || d.Exe != []string{"/usr/bin/containerd", "/usr/bin/dockerd"}[i] || d.Cgroup != "/switchback.slice/switchback-core.slice/"+d.Unit || !digestPattern.MatchString(d.Digest) || seen[d.PID] {
			return errQualification
		}
		seen[d.PID] = true
	}
	return nil
}

func qualificationEngineV2Sockets(sockets []qualificationEngineSocketIdentity, daemons []qualificationEngineDaemon, parents []qualificationEngineSocketParent, runtime *qualificationEngineRuntimeParent) error {
	count := len(sockets)
	parentCount := 0
	switch count {
	case 0:
		parentCount = 0
	case 2:
		parentCount = 2
	case 6:
		parentCount = 5
	default:
		return errQualification
	}
	if len(parents) != parentCount || len(daemons) != map[int]int{0: 0, 2: 1, 6: 2}[count] {
		return errQualification
	}
	fsIDs := map[[2]uint64]bool{}
	kernelIDs := map[uint64]bool{}
	byPath := map[string]qualificationEngineSocketParent{}
	device := uint64(0)
	identity := func(dev, ino uint64) bool {
		if dev == 0 || dev > 1<<63-1 || ino == 0 || ino > 1<<63-1 || fsIDs[[2]uint64{dev, ino}] || device != 0 && dev != device {
			return false
		}
		device = dev
		fsIDs[[2]uint64{dev, ino}] = true
		return true
	}
	for i, p := range parents {
		if p.Path != qualificationEngineSocketParentPaths[i] || p.Kind != "directory" || p.UID != 0 || p.GID != 0 || p.Mode != 0700 || !identity(p.Device, p.Inode) {
			return errQualification
		}
		byPath[p.Path] = p
	}
	if runtime != nil && count != 0 {
		p := parents[0]
		if p.Path != runtime.Path || p.Device != runtime.Device || p.Inode != runtime.Inode || p.UID != runtime.UID || p.GID != runtime.GID || p.Mode != runtime.Mode {
			return errQualification
		}
	}
	for i, s := range sockets {
		pathOK := false
		if i == 5 {
			pathOK = qualificationEngineLibnetworkPath.MatchString(s.Path)
		} else {
			pathOK = s.Path == qualificationEngineSocketPathsV2[i]
		}
		mode := uint64(0660)
		if i >= 3 {
			mode = 0777
		}
		if i == 5 {
			mode = 0600
		}
		di := 0
		if i >= 2 {
			di = 1
		}
		d := daemons[di]
		p, ok := byPath[path.Dir(s.Path)]
		if !pathOK || !ok || s.Kind != "socket" || s.Nlink != 1 || s.UID != 0 || s.GID != 0 || s.Mode != mode || s.OwnerUnit != d.Unit || s.OwnerPID != d.PID || s.OwnerStart != d.Start || !identity(s.Device, s.Inode) || s.KernelInode == 0 || s.KernelInode > 1<<63-1 || kernelIDs[s.KernelInode] || s.ParentPath != p.Path || s.ParentDevice != p.Device || s.ParentInode != p.Inode || s.ParentUID != p.UID || s.ParentGID != p.GID || s.ParentMode != p.Mode {
			return errQualification
		}
		kernelIDs[s.KernelInode] = true
	}
	return nil
}

// Wire DTOs retain strict JSON field/type checking before conversion into the
// native readback policy; no extra role or owner can be dropped in conversion.
type qualificationEngineListenerOwnerDTO struct {
	PID    uint64 `json:"main_pid"`
	Start  uint64 `json:"start_tick"`
	Exe    string `json:"exe"`
	Cgroup string `json:"cgroup"`
	FD     uint64 `json:"fd"`
}
type qualificationEngineUnixDTO struct {
	Path     string                                `json:"path"`
	Inode    uint64                                `json:"inode"`
	Listener bool                                  `json:"listener"`
	Owners   []qualificationEngineListenerOwnerDTO `json:"owners"`
}
type qualificationEngineTCPDTO struct {
	Address string                                `json:"address"`
	Port    uint64                                `json:"port"`
	Inode   uint64                                `json:"inode"`
	Owners  []qualificationEngineListenerOwnerDTO `json:"owners"`
}
type qualificationEngineListenersV2 struct {
	Schema      string `json:"schema"`
	Phase       string `json:"phase"`
	Boot        string `json:"boot_id"`
	CurrentBoot string `json:"current_boot_id"`
	Complete    struct {
		Unix      bool `json:"unix"`
		TCP       bool `json:"tcp"`
		TCP6      bool `json:"tcp6"`
		FDOwners  bool `json:"fd_owners"`
		Processes bool `json:"processes"`
	} `json:"complete"`
	Daemons []qualificationEngineDaemon         `json:"daemons"`
	Before  []qualificationEngineDaemon         `json:"identity_before"`
	After   []qualificationEngineDaemon         `json:"identity_after"`
	Sockets []qualificationEngineSocketIdentity `json:"sockets"`
	Parents []qualificationEngineSocketParent   `json:"socket_parents"`
	Unix    []qualificationEngineUnixDTO        `json:"unix"`
	TCP     []qualificationEngineTCPDTO         `json:"tcp"`
	TCP6    []qualificationEngineTCPDTO         `json:"tcp6"`
}

func qualificationEngineListenersV2Decode(raw []byte) (qualificationEngineListenersV2, error) {
	var f qualificationEngineListenersV2
	if err := qualificationEngineDecode(raw, &f); err != nil {
		return f, err
	}
	count, ok := map[string]int{"preflight": 0, "containerd_ready": 1, "engine_ready": 2}[f.Phase]
	if !ok || f.Schema != "switchback.engine.listeners.v2" || f.Boot != f.CurrentBoot || !f.Complete.Unix || !f.Complete.TCP || !f.Complete.TCP6 || !f.Complete.FDOwners || !f.Complete.Processes || qualificationEngineDaemonsCheck(f.Daemons, f.Boot, count) != nil || !reflect.DeepEqual(f.Before, f.Daemons) || !reflect.DeepEqual(f.After, f.Daemons) || len(f.Sockets) != []int{0, 2, 6}[count] || qualificationEngineV2Sockets(f.Sockets, f.Daemons, f.Parents, nil) != nil || len(f.Unix) > 8192 || len(f.TCP) > 8192 || len(f.TCP6) > 8192 {
		return f, errQualification
	}
	_, err := qualificationEngineV2ListenerRows(f)
	return f, err
}
func qualificationEngineV2ListenerRows(f qualificationEngineListenersV2) ([]qualificationEngineListener, error) {
	rows := []qualificationEngineListener{}
	owners := func(in []qualificationEngineListenerOwnerDTO) []qualificationEngineOwner {
		out := make([]qualificationEngineOwner, len(in))
		for i, o := range in {
			out[i] = qualificationEngineOwner{PID: o.PID, Start: o.Start, Exe: o.Exe, Cgroup: o.Cgroup, FD: o.FD}
		}
		return out
	}
	for _, u := range f.Unix {
		if !qualificationEngineText(u.Path, 512, true) {
			return nil, errQualification
		}
		rows = append(rows, qualificationEngineListener{Family: "unix", Path: u.Path, Inode: u.Inode, Listener: u.Listener, Owners: owners(u.Owners)})
	}
	for i, table := range [][]qualificationEngineTCPDTO{f.TCP, f.TCP6} {
		for _, u := range table {
			if !qualificationEngineText(u.Address, 128, false) || u.Port == 0 || u.Port > 65535 {
				return nil, errQualification
			}
			rows = append(rows, qualificationEngineListener{Family: []string{"tcp", "tcp6"}[i], Address: u.Address, Port: u.Port, Inode: u.Inode, Listener: true, Owners: owners(u.Owners)})
		}
	}
	return rows, qualificationEngineListenerOwnership(f.Daemons, f.Sockets, rows)
}

// Closure compares parsed full-table identities and every process/socket FD,
// including stable netlink/other families that are absent from these tables.
type qualificationEngineProcessFD struct {
	FD                   uint64
	Target               string
	Flags, Device, Inode uint64
	Mount                uint64 `json:"-"`
}
type qualificationEngineProcess struct {
	PID, Start  uint64
	Exe, Cgroup string
	FDs         []qualificationEngineProcessFD
}

func qualificationEngineSocketFDInode(target string) (uint64, error) {
	if !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
		return 0, errQualification
	}
	n, err := strconv.ParseUint(target[len("socket:["):len(target)-1], 10, 63)
	if err != nil || n == 0 {
		return 0, errQualification
	}
	return n, nil
}
func qualificationEngineCollectionClosure(beforeTables, afterTables []qualificationEngineListener, beforeProcesses, afterProcesses []qualificationEngineProcess) error {
	tableIDs := func(rows []qualificationEngineListener) []qualificationEngineListener {
		out := append([]qualificationEngineListener{}, rows...)
		for i := range out {
			out[i].Owners = nil
		}
		// Keep table order: this mirrors the complete parsed Python inventory.
		return out
	}
	coverage := func(in []qualificationEngineProcess) ([]qualificationEngineProcess, error) {
		out := make([]qualificationEngineProcess, len(in))
		seen := map[uint64]bool{}
		fds := 0
		if len(in) > 65536 {
			return nil, errQualification
		}
		for i, p := range in {
			if p.PID == 0 || p.PID > 1<<31-1 || seen[p.PID] {
				return nil, errQualification
			}
			seen[p.PID] = true
			out[i] = p
			out[i].FDs = []qualificationEngineProcessFD{}
			used := map[uint64]bool{}
			for _, fd := range p.FDs {
				fds++
				if fds > 131072 || fd.FD > 1<<20-1 || used[fd.FD] {
					return nil, errQualification
				}
				used[fd.FD] = true
				if strings.HasPrefix(fd.Target, "socket:") {
					if _, err := qualificationEngineSocketFDInode(fd.Target); err != nil {
						return nil, err
					}
					out[i].FDs = append(out[i].FDs, fd)
				}
			}
			sort.Slice(out[i].FDs, func(a, b int) bool { return out[i].FDs[a].FD < out[i].FDs[b].FD })
		}
		sort.Slice(out, func(a, b int) bool { return out[a].PID < out[b].PID })
		return out, nil
	}
	before, err := coverage(beforeProcesses)
	if err != nil {
		return err
	}
	after, err := coverage(afterProcesses)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(tableIDs(beforeTables), tableIDs(afterTables)) || !reflect.DeepEqual(before, after) {
		return &qualificationEngineCoverageChanged{}
	}
	return nil
}

func qualificationEngineFDFlags(raw []byte) (uint64, error) {
	if len(raw) == 0 || len(raw) > 16384 {
		return 0, errQualification
	}
	found := false
	var flags uint64
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "flags:") {
			if found {
				return 0, errQualification
			}
			found = true
			text := strings.TrimSpace(strings.TrimPrefix(line, "flags:"))
			if text == "" || strings.Trim(text, "01234567") != "" {
				return 0, errQualification
			}
			var err error
			flags, err = strconv.ParseUint(text, 8, 64)
			if err != nil {
				return 0, err
			}
		}
	}
	if !found {
		return 0, errQualification
	}
	return flags, nil
}

func qualificationEngineProcessPolicy(processes []qualificationEngineProcess, daemons []qualificationEngineDaemon) error {
	byPID := map[uint64]qualificationEngineDaemon{}
	for _, d := range daemons {
		byPID[d.PID] = d
	}
	for _, p := range processes {
		d, known := byPID[p.PID]
		base := path.Base(strings.TrimSuffix(p.Exe, " (deleted)"))
		daemonish := base == "dockerd" || base == "containerd" || strings.HasPrefix(base, "containerd-shim")
		if known || daemonish {
			if !known || p.Start != d.Start || p.Exe != d.Exe || p.Cgroup != d.Cgroup {
				return fmt.Errorf("foreign Engine process")
			}
		}
		for _, fd := range p.FDs {
			access := fd.Flags & 3
			if (access == 1 || access == 2) && (fd.Target == "/mnt/lima-sbquota/docker" || strings.HasPrefix(fd.Target, "/mnt/lima-sbquota/docker/")) && !known {
				return fmt.Errorf("foreign Engine data-root writer")
			}
		}
	}
	return nil
}

func qualificationEngineDiscoverSocketPaths(rows []qualificationEngineListener) ([]string, error) {
	paths := append([]string{}, qualificationEngineSocketPathsV2[:]...)
	var dynamic string
	for _, row := range rows {
		if row.Family == "unix" && row.Listener && qualificationEngineLibnetworkPath.MatchString(row.Path) {
			if dynamic != "" {
				return nil, errQualification
			}
			dynamic = row.Path
		}
	}
	if dynamic == "" {
		return nil, errQualification
	}
	return append(paths, dynamic), nil
}
func qualificationEnginePairSocketInodes(sockets []qualificationEngineSocketIdentity, rows []qualificationEngineListener) error {
	for i, s := range sockets {
		found := uint64(0)
		for _, row := range rows {
			if row.Family == "unix" && row.Listener && row.Path == s.Path {
				if found != 0 || row.Inode == 0 {
					return errQualification
				}
				found = row.Inode
			}
		}
		if found == 0 {
			return errQualification
		}
		sockets[i].KernelInode = found
	}
	return nil
}
func qualificationEngineAttachOwners(rows []qualificationEngineListener, processes []qualificationEngineProcess) ([]qualificationEngineListener, error) {
	out := append([]qualificationEngineListener{}, rows...)
	byInode := map[uint64]int{}
	for i, l := range out {
		if l.Inode == 0 {
			return nil, errQualification
		}
		if _, ok := byInode[l.Inode]; ok {
			return nil, errQualification
		}
		byInode[l.Inode] = i
		out[i].Owners = []qualificationEngineOwner{}
	}
	for _, p := range processes {
		for _, fd := range p.FDs {
			if !strings.HasPrefix(fd.Target, "socket:") {
				continue
			}
			ino, err := qualificationEngineSocketFDInode(fd.Target)
			if err != nil {
				return nil, err
			}
			if i, ok := byInode[ino]; ok {
				if p.Exe == "" || len(out[i].Owners) >= 64 {
					return nil, errQualification
				}
				out[i].Owners = append(out[i].Owners, qualificationEngineOwner{PID: p.PID, Start: p.Start, Exe: p.Exe, Cgroup: p.Cgroup, FD: fd.FD})
			}
		}
	}
	return out, nil
}

type qualificationEngineProcessStat struct {
	PID, Start, Flags uint64
	State             string
}

// Kernel threads (and PID1 very early in boot) can legitimately have start_tick
// zero. This complete inventory parser does not weaken the daemon start policy.
func qualificationEngineParseProcessStat(raw []byte) (qualificationEngineProcessStat, error) {
	var out qualificationEngineProcessStat
	if len(raw) == 0 || len(raw) > 4096 || raw[len(raw)-1] != '\n' || strings.ContainsRune(string(raw), 0) {
		return out, errQualification
	}
	text := string(raw)
	left, right := strings.Index(text, " ("), strings.LastIndex(text, ") ")
	if left <= 0 || right <= left {
		return out, errQualification
	}
	pid, err := strconv.ParseUint(text[:left], 10, 31)
	if err != nil || pid == 0 {
		return out, errQualification
	}
	fields := strings.Fields(text[right+2:])
	if len(fields) < 20 || len(fields[0]) != 1 {
		return out, errQualification
	}
	start, err := strconv.ParseUint(fields[19], 10, 63)
	if err != nil {
		return out, errQualification
	}
	flags, err := strconv.ParseUint(fields[6], 10, 64)
	if err != nil {
		return out, errQualification
	}
	return qualificationEngineProcessStat{PID: pid, Start: start, Flags: flags, State: fields[0]}, nil
}
func qualificationEngineProcessStatStable(before, after qualificationEngineProcessStat, exe string, fds int) error {
	kernel := before.Flags&0x00200000 != 0
	if exe == "" && (!kernel || fds != 0) || before.Start == 0 && before.PID != 1 && !(exe == "" && fds == 0 && kernel) {
		return errQualification
	}
	sameState := before.State == after.State
	ordinary := (before.State == "R" || before.State == "S") && (after.State == "R" || after.State == "S")
	if before.PID != after.PID || before.Start != after.Start || before.Flags != after.Flags || !sameState && !ordinary {
		return errQualification
	}
	return nil
}

// A process may legitimately belong to the root cgroup. Use the Engine wire
// path grammar (which permits "/"), not storage-domain absolute(), here.
func qualificationEngineOwnerPath(value string) bool {
	return qualificationEngineText(value, 512, false) && strings.HasPrefix(value, "/") && path.Clean(value) == value && !strings.Contains(value, "//")
}
