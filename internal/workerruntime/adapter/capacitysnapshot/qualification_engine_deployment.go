package capacitysnapshot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

const qualificationEngineReceiptPath = "/var/lib/switchback/m1-engine/deployment-receipt.json"
const qualificationEngineBundleRoot = "/opt/switchback/engine-bundle"
const qualificationEngineSourcePrefix = "deploy/experience/engine/"

var qualificationEngineUnits = [...]string{"switchback-m1-containerd.service", "switchback-m1-engine.service"}
var qualificationEngineSocketPaths = [...]string{"/run/switchback-engine/m1-resident/containerd.sock", "/run/switchback-engine/m1-resident/containerd.ttrpc", "/run/switchback-engine/docker.sock"}
var qualificationEngineSteps = [...]string{"preflight", "claim_stage", "install_files", "daemon_reload", "effective_configuration", "containerd_start", "containerd_ready", "engine_start", "engine_ready", "networks", "final_identity", "write_receipt"}
var qualificationEngineInstalls = map[string]struct {
	target string
	mode   uint64
}{
	"switchback-m1-containerd.service": {"/etc/systemd/system/switchback-m1-containerd.service", 0644},
	"switchback-m1-engine.service":     {"/etc/systemd/system/switchback-m1-engine.service", 0644},
	"containerd.toml":                  {"/etc/switchback/m1-engine/containerd.toml", 0600},
	"daemon.json":                      {"/etc/switchback/m1-engine/daemon.json", 0600},
}

type qualificationEngineFilesystem struct {
	Mountpoint    string `json:"mountpoint"`
	FSType        string `json:"fs_type"`
	UUID          string `json:"uuid"`
	Major         uint32 `json:"major"`
	Minor         uint32 `json:"minor"`
	RootInode     uint64 `json:"root_inode"`
	QuotaEnforced bool   `json:"quota_enforced"`
}
type qualificationEngineRuntimeParent struct {
	Path      string `json:"path"`
	Device    uint64 `json:"device"`
	Inode     uint64 `json:"inode"`
	UID       uint64 `json:"uid"`
	GID       uint64 `json:"gid"`
	Mode      uint64 `json:"mode"`
	FSType    string `json:"fs_type"`
	SizeBytes uint64 `json:"size_bytes"`
	Inodes    uint64 `json:"inodes"`
}
type qualificationEngineDaemon struct {
	Unit   string `json:"unit"`
	PID    uint64 `json:"main_pid"`
	Start  uint64 `json:"start_tick"`
	Exe    string `json:"exe"`
	Digest string `json:"exe_sha256"`
	Cgroup string `json:"cgroup"`
	Boot   string `json:"boot_id"`
}
type qualificationEngineInstalled struct {
	Source string `json:"source_path"`
	Target string `json:"target_path"`
	Digest string `json:"sha256"`
	Mode   uint64 `json:"mode"`
	UID    uint64 `json:"uid"`
	GID    uint64 `json:"gid"`
	Bytes  uint64 `json:"bytes"`
}
type qualificationEngineSocketIdentity struct {
	Path         string `json:"path"`
	Device       uint64 `json:"device"`
	Inode        uint64 `json:"inode"`
	KernelInode  uint64 `json:"kernel_inode"`
	Kind         string `json:"kind"`
	Nlink        uint64 `json:"nlink"`
	UID          uint64 `json:"uid"`
	GID          uint64 `json:"gid"`
	Mode         uint64 `json:"mode"`
	OwnerUnit    string `json:"owner_unit"`
	OwnerPID     uint64 `json:"owner_pid"`
	OwnerStart   uint64 `json:"owner_start_tick"`
	ParentPath   string `json:"parent_path"`
	ParentDevice uint64 `json:"parent_device"`
	ParentInode  uint64 `json:"parent_inode"`
	ParentUID    uint64 `json:"parent_uid"`
	ParentGID    uint64 `json:"parent_gid"`
	ParentMode   uint64 `json:"parent_mode"`
}
type qualificationEngineIdentity struct {
	ID            string `json:"id"`
	Version       string `json:"version"`
	DataRoot      string `json:"data_root"`
	StorageDriver string `json:"storage_driver"`
	Snapshotter   bool   `json:"containerd_snapshotter"`
	CgroupParent  string `json:"cgroup_parent"`
	Containers    uint64 `json:"containers"`
	Volumes       uint64 `json:"volumes"`
	Plugins       uint64 `json:"plugins"`
}
type qualificationEngineAllocation struct {
	Subnet  string `json:"subnet"`
	Gateway string `json:"gateway"`
}
type qualificationEngineIPAM struct {
	Driver  string                          `json:"driver"`
	Options map[string]string               `json:"options"`
	Config  []qualificationEngineAllocation `json:"config"`
}
type qualificationEngineNetworkIdentity struct {
	Name       string                  `json:"name"`
	ID         string                  `json:"id"`
	Driver     string                  `json:"driver"`
	Scope      string                  `json:"scope"`
	Internal   bool                    `json:"internal"`
	Labels     map[string]string       `json:"labels"`
	IPv6       bool                    `json:"enable_ipv6"`
	Attachable bool                    `json:"attachable"`
	Ingress    bool                    `json:"ingress"`
	Options    map[string]string       `json:"options"`
	IPAM       qualificationEngineIPAM `json:"ipam"`
}
type qualificationEngineStep struct {
	Name     string   `json:"name"`
	Status   string   `json:"status"`
	Started  int64    `json:"started_ms"`
	Finished int64    `json:"finished_ms"`
	Exit     int64    `json:"exit_code"`
	Stdout   string   `json:"stdout"`
	Stderr   string   `json:"stderr"`
	Known    []string `json:"known_effects"`
	Unknown  []string `json:"unknown_effects"`
}
type qualificationEngineReceipt struct {
	Schema               string                               `json:"schema"`
	Status               string                               `json:"status"`
	BundleDigest         string                               `json:"bundle_manifest_sha256"`
	KitDigest            string                               `json:"kit_manifest_sha256"`
	SourceRoot           string                               `json:"source_root"`
	TemplatesRoot        string                               `json:"templates_root"`
	Boot                 string                               `json:"boot_id"`
	Filesystem           qualificationEngineFilesystem        `json:"filesystem"`
	RuntimeParent        qualificationEngineRuntimeParent     `json:"runtime_parent"`
	Daemons              []qualificationEngineDaemon          `json:"daemons"`
	Installed            []qualificationEngineInstalled       `json:"installed_files"`
	Sockets              []qualificationEngineSocketIdentity  `json:"sockets"`
	Engine               qualificationEngineIdentity          `json:"engine"`
	Networks             []qualificationEngineNetworkIdentity `json:"networks"`
	Steps                []qualificationEngineStep            `json:"steps"`
	ElapsedMS            int64                                `json:"elapsed_ms"`
	Known                []string                             `json:"known_effects"`
	Unknown              []string                             `json:"unknown_effects"`
	RuntimeQualification bool                                 `json:"runtime_qualification"`
	CapacityReady        bool                                 `json:"capacity_ready"`
	ExecutionEnabled     bool                                 `json:"execution_enabled"`
	PaidConsumed         bool                                 `json:"paid_consumed"`
}

// Decode the ready subset of the shared Python contract. Every property is
// required and nonnull, including false booleans, integer zero and empty arrays.
// Tokens are checked before typed decoding so duplicate-key evidence survives.
func qualificationEngineDecode(raw []byte, target any) error {
	if len(raw) == 0 || len(raw) > 1<<20 || !utf8.Valid(raw) {
		return errQualification
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	value, err := qualificationEngineJSONValue(d, 0, new(int))
	if err != nil {
		return err
	}
	if _, err = d.Token(); err != io.EOF {
		return errQualification
	}
	if err = qualificationEngineShape(value, reflect.TypeOf(target).Elem()); err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}
func qualificationEngineJSONValue(d *json.Decoder, depth int, nodes *int) (any, error) {
	(*nodes)++
	if depth > 64 || *nodes > 50000 {
		return nil, errQualification
	}
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch t := token.(type) {
	case json.Delim:
		if t == '{' {
			m := map[string]any{}
			for d.More() {
				key, e := d.Token()
				if e != nil {
					return nil, e
				}
				s, ok := key.(string)
				if !ok {
					return nil, errQualification
				}
				if _, ok = m[s]; ok {
					return nil, fmt.Errorf("duplicate Engine property %s", s)
				}
				v, e := qualificationEngineJSONValue(d, depth+1, nodes)
				if e != nil {
					return nil, e
				}
				m[s] = v
			}
			end, e := d.Token()
			if e != nil || end != json.Delim('}') {
				return nil, errQualification
			}
			return m, nil
		}
		if t == '[' {
			a := []any{}
			for d.More() {
				v, e := qualificationEngineJSONValue(d, depth+1, nodes)
				if e != nil {
					return nil, e
				}
				a = append(a, v)
			}
			end, e := d.Token()
			if e != nil || end != json.Delim(']') {
				return nil, errQualification
			}
			return a, nil
		}
		return nil, errQualification
	default:
		return token, nil
	}
}
func qualificationEngineShape(v any, t reflect.Type) error {
	if t == reflect.TypeOf(json.RawMessage{}) {
		return nil
	}
	if v == nil {
		return errQualification
	}
	switch t.Kind() {
	case reflect.Struct:
		m, ok := v.(map[string]any)
		fields := qualificationEngineJSONFields(t)
		if !ok || len(m) != len(fields) {
			return errQualification
		}
		for _, f := range fields {
			value, ok := m[f.Tag.Get("json")]
			if !ok {
				return errQualification
			}
			if e := qualificationEngineShape(value, f.Type); e != nil {
				return fmt.Errorf("Engine property %s: %w", f.Tag.Get("json"), e)
			}
		}
	case reflect.Slice:
		a, ok := v.([]any)
		if !ok {
			return errQualification
		}
		for _, value := range a {
			if e := qualificationEngineShape(value, t.Elem()); e != nil {
				return e
			}
		}
	case reflect.Map:
		m, ok := v.(map[string]any)
		if !ok {
			return errQualification
		}
		for _, value := range m {
			if e := qualificationEngineShape(value, t.Elem()); e != nil {
				return e
			}
		}
	case reflect.Bool:
		if _, ok := v.(bool); !ok {
			return errQualification
		}
	case reflect.String:
		if _, ok := v.(string); !ok {
			return errQualification
		}
	case reflect.Uint64, reflect.Uint32, reflect.Int64:
		n, ok := v.(json.Number)
		if !ok || strings.ContainsAny(string(n), ".eE") {
			return errQualification
		}
	default:
		return errQualification
	}
	return nil
}
func qualificationEngineText(s string, max int, empty bool) bool {
	return (empty || s != "") && len(s) <= max && !strings.ContainsRune(s, 0)
}
func qualificationEngineEffects(a []string) (map[string]bool, error) {
	if len(a) > 64 {
		return nil, errQualification
	}
	m := map[string]bool{}
	for _, s := range a {
		if !qualificationEngineText(s, 512, false) || m[s] {
			return nil, errQualification
		}
		m[s] = true
	}
	return m, nil
}

// qualificationEngineReceiptDecode validates historical v1 fixtures only. Every
// live receipt consumer must use qualificationEngineReceiptV2Decode instead.
func qualificationEngineReceiptDecode(raw []byte) (qualificationEngineReceipt, error) {
	var r qualificationEngineReceipt
	if err := qualificationEngineDecode(raw, &r); err != nil {
		return r, err
	}
	return qualificationEngineReceiptCheck(r, 1, nil)
}

func qualificationEngineReceiptCheck(r qualificationEngineReceipt, version int, socketParents []qualificationEngineSocketParent) (qualificationEngineReceipt, error) {
	if r.Schema != fmt.Sprintf("switchback.engine.deployment_receipt.v%d", version) || r.Status != "engine_ready" || r.RuntimeQualification || r.CapacityReady || r.ExecutionEnabled || r.PaidConsumed {
		return r, errQualification
	}
	for _, s := range []string{r.BundleDigest, r.KitDigest, r.SourceRoot, r.TemplatesRoot} {
		if !digestPattern.MatchString(s) {
			return r, errQualification
		}
	}
	if !qualificationUUIDPattern.MatchString(r.Boot) || r.Filesystem.Mountpoint != "/mnt/lima-sbquota" || r.Filesystem.FSType != "xfs" || !qualificationUUIDPattern.MatchString(r.Filesystem.UUID) || !r.Filesystem.QuotaEnforced || r.Filesystem.RootInode == 0 || r.Filesystem.RootInode > 1<<63-1 {
		return r, errQualification
	}
	p := r.RuntimeParent
	if p.Path != "/run/switchback-engine" || p.Device == 0 || p.Inode == 0 || p.Device > 1<<63-1 || p.Inode > 1<<63-1 || p.UID != 0 || p.GID != 0 || p.Mode != 0700 || p.FSType != "tmpfs" || p.SizeBytes != 128<<20 || p.Inodes != 4096 {
		return r, errQualification
	}
	socketCount := 3
	if version == 2 {
		socketCount = 6
	}
	if len(r.Daemons) != 2 || len(r.Sockets) != socketCount || len(r.Installed) != 4 || len(r.Networks) != 2 || len(r.Steps) != 12 || r.ElapsedMS < 0 || r.ElapsedMS > 120000 {
		return r, errQualification
	}
	if err := qualificationEngineDaemonsCheck(r.Daemons, r.Boot, 2); err != nil {
		return r, err
	}
	installs := map[string]bool{}
	for _, f := range r.Installed {
		name := strings.TrimPrefix(f.Source, qualificationEngineSourcePrefix)
		policy, ok := qualificationEngineInstalls[name]
		if !ok || f.Source != qualificationEngineSourcePrefix+name || installs[name] || f.Target != policy.target || f.Mode != policy.mode || f.UID != 0 || f.GID != 0 || f.Bytes == 0 || f.Bytes > 16384 || !digestPattern.MatchString(f.Digest) {
			return r, errQualification
		}
		installs[name] = true
	}
	if version == 2 {
		if err := qualificationEngineV2Sockets(r.Sockets, r.Daemons, socketParents, &r.RuntimeParent); err != nil {
			return r, err
		}
	} else {
		parents := map[string][5]uint64{p.Path: {p.Device, p.Inode, p.UID, p.GID, p.Mode}}
		inodes := map[uint64]bool{}
		paths := map[[2]uint64]bool{}
		for i, s := range r.Sockets {
			d := r.Daemons[0]
			parent := "/run/switchback-engine/m1-resident"
			if i == 2 {
				d = r.Daemons[1]
				parent = p.Path
			}
			id := [2]uint64{s.Device, s.Inode}
			pid := [5]uint64{s.ParentDevice, s.ParentInode, s.ParentUID, s.ParentGID, s.ParentMode}
			if s.Path != qualificationEngineSocketPaths[i] || s.Kind != "socket" || s.Nlink != 1 || s.UID != 0 || s.GID != 0 || s.Mode != 0660 || s.OwnerUnit != d.Unit || s.OwnerPID != d.PID || s.OwnerStart != d.Start || s.ParentPath != parent || s.ParentUID != 0 || s.ParentGID != 0 || s.ParentMode != 0700 || inodes[s.KernelInode] || paths[id] {
				return r, errQualification
			}
			for _, n := range []uint64{s.Device, s.Inode, s.KernelInode, s.ParentDevice, s.ParentInode} {
				if n == 0 || n > 1<<63-1 {
					return r, errQualification
				}
			}
			if old, ok := parents[parent]; ok && old != pid {
				return r, errQualification
			}
			parents[parent] = pid
			inodes[s.KernelInode] = true
			paths[id] = true
		}
	}
	e := r.Engine
	if !qualificationEngineText(e.ID, 128, false) || e.Version != "29.9.0" || e.DataRoot != "/mnt/lima-sbquota/docker" || e.StorageDriver != "overlay2" || e.Snapshotter || e.CgroupParent != "switchback-core.slice" || e.Containers != 0 || e.Volumes != 0 || e.Plugins != 0 {
		return r, errQualification
	}
	for i, n := range r.Networks {
		if err := qualificationEngineNetworkCheck(n, i); err != nil {
			return r, err
		}
	}
	a, _ := netip.ParsePrefix(r.Networks[0].IPAM.Config[0].Subnet)
	b, _ := netip.ParsePrefix(r.Networks[1].IPAM.Config[0].Subnet)
	if r.Networks[0].ID == r.Networks[1].ID || a.Overlaps(b) {
		return r, errQualification
	}
	known := map[string]bool{}
	previous := int64(0)
	for i, s := range r.Steps {
		if s.Name != qualificationEngineSteps[i] || s.Status != "succeeded" || s.Exit != 0 || s.Started < previous || s.Finished < s.Started || s.Finished > r.ElapsedMS || !qualificationEngineText(s.Stdout, 16384, true) || !qualificationEngineText(s.Stderr, 16384, true) || len(s.Unknown) != 0 {
			return r, errQualification
		}
		effects, err := qualificationEngineEffects(s.Known)
		if err != nil {
			return r, err
		}
		for x := range effects {
			known[x] = true
		}
		previous = s.Finished
	}
	aggregate, err := qualificationEngineEffects(r.Known)
	if err != nil || !reflect.DeepEqual(known, aggregate) || len(r.Unknown) != 0 {
		return r, errQualification
	}
	return r, nil
}
func qualificationEngineNetworkCheck(n qualificationEngineNetworkIdentity, index int) error {
	if n.Name != []string{"switchback-core1011_core", "switchback-m1-worker-control"}[index] || !containerIDPattern.MatchString(n.ID) || n.Driver != "bridge" || n.Scope != "local" || n.Internal != (index == 0) || n.IPv6 || n.Attachable || n.Ingress || len(n.Options) != 0 || len(n.Labels) != 2 || n.Labels["com.switchback.owner"] != "m1-resident-engine" || n.Labels["com.switchback.engine-profile"] != "m1-resident-engine-v1" || n.IPAM.Driver != "default" || len(n.IPAM.Options) != 0 || len(n.IPAM.Config) != 1 {
		return errQualification
	}
	a := n.IPAM.Config[0]
	subnet, err := netip.ParsePrefix(a.Subnet)
	gateway, e := netip.ParseAddr(a.Gateway)
	if err != nil || e != nil || !subnet.Addr().Is4() || subnet != subnet.Masked() || !subnet.Addr().IsPrivate() || !gateway.Is4() || !subnet.Contains(gateway) || gateway == subnet.Addr() {
		return errQualification
	}
	// The last address is the IPv4 broadcast address, including /31 and /32.
	raw := subnet.Addr().As4()
	ip := uint32(raw[0])<<24 | uint32(raw[1])<<16 | uint32(raw[2])<<8 | uint32(raw[3])
	mask := uint32(0xffffffff) >> subnet.Bits()
	last := ip | mask
	lastAddress := netip.AddrFrom4([4]byte{byte(last >> 24), byte(last >> 16), byte(last >> 8), byte(last)})
	if !lastAddress.IsPrivate() {
		return errQualification
	}
	g := gateway.As4()
	if last == uint32(g[0])<<24|uint32(g[1])<<16|uint32(g[2])<<8|uint32(g[3]) {
		return errQualification
	}
	return nil
}

// Match Python json.dumps(..., separators=(',', ':'), ensure_ascii=False),
// including record field order and HTML/U+2028 handling. No authority is added.
func qualificationEngineRecordBytes(records []qualificationSourceFile) []byte {
	var b bytes.Buffer
	b.WriteByte('[')
	quote := func(s string) {
		b.WriteByte('"')
		for _, r := range s {
			switch r {
			case '"', '\\':
				b.WriteByte('\\')
				b.WriteRune(r)
			case '\b':
				b.WriteString(`\b`)
			case '\f':
				b.WriteString(`\f`)
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				if r < 32 {
					fmt.Fprintf(&b, `\u%04x`, r)
				} else {
					b.WriteRune(r)
				}
			}
		}
		b.WriteByte('"')
	}
	for i, f := range records {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"path":`)
		quote(f.Path)
		b.WriteString(`,"type":`)
		quote(f.Type)
		b.WriteString(`,"mode":`)
		b.WriteString(strconv.FormatUint(f.Mode, 10))
		b.WriteString(`,"bytes":`)
		b.WriteString(strconv.FormatUint(f.Bytes, 10))
		b.WriteString(`,"digest":`)
		quote(f.Digest)
		b.WriteByte('}')
	}
	b.WriteByte(']')
	return b.Bytes()
}
