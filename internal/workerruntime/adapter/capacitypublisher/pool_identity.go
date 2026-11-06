package capacitypublisher

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"path"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
)

var runtimeIdentityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,159}$`)

var cidPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

func label(s string) bool {
	return len(s) > 0 && len(s) <= 512 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n\t")
}
func absolute(s string) bool       { return label(s) && path.IsAbs(s) && path.Clean(s) == s && s != "/" }
func inside(s, parent string) bool { return s == parent || strings.HasPrefix(s, parent+"/") }
func decimal(s string, max uint64) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, e := strconv.ParseUint(s, 10, 64)
	return n, e == nil && n <= max
}
func uuid(b [16]byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
func window(o capacityprobe.PoolObservation, seq int64, now time.Time) error {
	if seq <= 0 || o.Started.IsZero() || o.Ended.IsZero() || !o.Ended.After(o.Started) || o.Ended.Sub(o.Started) > 30*time.Second {
		return errInvalid
	}
	if now.Sub(o.Started) > 30*time.Second || o.Started.After(now.Add(2*time.Second)) || o.Ended.After(now.Add(2*time.Second)) {
		return errStale
	}
	if o.BootBefore == ([16]byte{}) || o.BootBefore != o.BootAfter || o.NamespaceBefore == 0 || o.NamespaceBefore != o.NamespaceAfter || !bytes.Equal(o.MountInfoBefore, o.MountInfoAfter) {
		return errDrift
	}
	if len(o.MountInfoBefore) == 0 || len(o.MountInfoBefore) > capacityprobe.MaxResponse {
		return errInvalid
	}
	return nil
}
func census(o capacityprobe.PoolObservation) (map[string]capacityprobe.PoolContainerInspect, error) {
	before, after := o.EngineBefore, o.EngineAfter
	valid := func(e capacityprobe.PoolEngineReadback) bool {
		return uuidPattern.MatchString(e.ID) && e.ID != "00000000-0000-0000-0000-000000000000" && e.APIVersion == "1.56" && len(e.Version) <= 64 && versionPattern.MatchString(e.Version) && absolute(e.DockerRoot) && e.Driver == "overlay2" && e.CPUCount > 0 && e.CPUCount <= math.MaxInt64/1000 && e.MemoryBytes > 0 && e.MemoryBytes <= math.MaxInt64 && len(e.Containers) <= capacityprobe.MaxContainers
	}
	if !valid(before) || !valid(after) {
		return nil, errInvalid
	}
	a, e := inventory(before.Containers)
	if e != nil {
		return nil, e
	}
	b, e := inventory(after.Containers)
	if e != nil {
		return nil, e
	}
	before.Containers = nil
	after.Containers = nil
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(a, b) {
		return nil, errDrift
	}
	return a, nil
}
func inventory(items []capacityprobe.PoolContainerInspect) (map[string]capacityprobe.PoolContainerInspect, error) {
	out := make(map[string]capacityprobe.PoolContainerInspect, len(items))
	for _, i := range items {
		if !cidPattern.MatchString(i.ID) || !digestPattern.MatchString(i.ImageDigest) || len(i.Labels) > 32 {
			return nil, errInventory
		}
		if _, ok := out[i.ID]; ok {
			return nil, errInventory
		}
		if !label(i.Status) || len(i.CgroupParent) > 512 || strings.ContainsAny(i.CgroupParent, "\x00\r\n\t") || !label(i.NetworkMode) || len(i.GraphDriver) > 512 || strings.ContainsAny(i.GraphDriver, "\x00\r\n\t") || len(i.UpperDir) > 512 || strings.ContainsAny(i.UpperDir, "\x00\r\n\t") {
			return nil, errInventory
		}
		labels := make(map[string]string, len(i.Labels))
		for k, v := range i.Labels {
			if !label(k) || !label(v) {
				return nil, errInventory
			}
			labels[k] = v
		}
		i.Labels = labels
		out[i.ID] = i
	}
	return out, nil
}

type mountRecord struct {
	id                          uint64
	device, root, point, fstype string
	options                     []string
}

func decodeMount(s string) (string, bool) {
	var b strings.Builder
	for n := 0; n < len(s); n++ {
		if s[n] != '\\' {
			b.WriteByte(s[n])
			continue
		}
		if n+3 >= len(s) {
			return "", false
		}
		switch s[n+1 : n+4] {
		case "040":
			b.WriteByte(' ')
		case "011":
			b.WriteByte('\t')
		case "012":
			b.WriteByte('\n')
		case "134":
			b.WriteByte('\\')
		default:
			return "", false
		}
		n += 3
	}
	decoded := b.String()
	return decoded, (decoded == "/" || absolute(decoded))
}
func mounts(raw []byte) ([]mountRecord, error) {
	if len(raw) == 0 || len(raw) > capacityprobe.MaxResponse || raw[len(raw)-1] != '\n' || bytes.IndexByte(raw, 0) >= 0 {
		return nil, errInvalid
	}
	out := []mountRecord{}
	ids := map[uint64]bool{}
	for _, line := range strings.Split(string(raw[:len(raw)-1]), "\n") {
		if strings.Count(line, " - ") != 1 {
			return nil, errInvalid
		}
		halves := strings.SplitN(line, " - ", 2)
		pre, post := strings.Split(halves[0], " "), strings.Split(halves[1], " ")
		if len(pre) < 6 || len(post) < 3 {
			return nil, errInvalid
		}
		for _, s := range append(append([]string{}, pre...), post...) {
			if s == "" || !label(s) {
				return nil, errInvalid
			}
		}
		id, ok := decimal(pre[0], math.MaxInt64)
		parent, pok := decimal(pre[1], math.MaxInt64)
		if !ok || !pok || id == 0 || parent == 0 || ids[id] {
			return nil, errInvalid
		}
		ids[id] = true
		dev := strings.Split(pre[2], ":")
		if len(dev) != 2 {
			return nil, errInvalid
		}
		major, mok := decimal(dev[0], math.MaxUint32)
		minor, nok := decimal(dev[1], math.MaxUint32)
		if !mok || !nok || pre[2] != fmt.Sprintf("%d:%d", major, minor) {
			return nil, errInvalid
		}
		root, rok := decodeMount(pre[3])
		point, tok := decodeMount(pre[4])
		if !rok || !tok {
			return nil, errInvalid
		}
		opts := strings.Split(pre[5], ",")
		for _, op := range opts {
			if !label(op) {
				return nil, errInvalid
			}
		}
		// XFS quota flags are normally superblock options. Preserve the
		// ordered union of actual per-mount and superblock options.
		for _, op := range strings.Split(post[2], ",") {
			if !label(op) {
				return nil, errInvalid
			}
			found := false
			for _, have := range opts {
				if have == op {
					found = true
				}
			}
			if !found {
				opts = append(opts, op)
			}
		}
		out = append(out, mountRecord{id: id, device: pre[2], root: root, point: point, fstype: post[0], options: opts})
	}
	return out, nil
}
func hasQuota(options []string) bool {
	for _, s := range options {
		if s == "prjquota" || s == "pquota" {
			return true
		}
	}
	return false
}

type directoryFacts struct {
	path, fsuuid, device          string
	mount, project, xflags, inode uint64
}

func (m *Mapper) directory(d capacityprobe.CreatedLayerDirectory, expectedPath string, records []mountRecord) (directoryFacts, error) {
	bad := func() (directoryFacts, error) { return directoryFacts{}, errDrift }
	i := d.Before
	if !absolute(expectedPath) || i.CanonicalPath != expectedPath || i != d.After || i.Inode == 0 || i.Mode&0xf000 != 0x4000 || !d.NoFollowWalk || !d.ReopenedNoFollow || !d.IdentityRechecked || !d.FilesystemUUIDSameFD || i.MountID != m.trust.Identity.MountID || fmt.Sprintf("%d:%d", i.DeviceMajor, i.DeviceMinor) != m.trust.Identity.Device {
		return bad()
	}
	chosen := -1
	depth := -1
	matches := 0
	for n, r := range records {
		if r.point == "/" || inside(expectedPath, r.point) {
			if len(r.point) == depth {
				matches++
			}
			if len(r.point) > depth {
				chosen = n
				depth = len(r.point)
				matches = 1
			}
		}
	}
	if chosen < 0 || matches != 1 {
		return bad()
	}
	r := records[chosen]
	if r.id != i.MountID || r.device != m.trust.Identity.Device || r.root != "/" || r.fstype != "xfs" || !hasQuota(r.options) {
		return bad()
	}
	for _, g := range [][256]byte{d.Geometry, d.GeometryAfter} {
		bs := binary.LittleEndian.Uint32(g[0:4])
		if binary.LittleEndian.Uint32(g[88:92]) != 5 || bs < 512 || bs > 65536 || bs&(bs-1) != 0 || binary.LittleEndian.Uint32(g[12:16]) == 0 || binary.LittleEndian.Uint64(g[32:40]) == 0 {
			return bad()
		}
		for _, b := range g[120:] {
			if b != 0 {
				return bad()
			}
		}
		var fs [16]byte
		copy(fs[:], g[64:80])
		if fs == ([16]byte{}) || fs != i.XFSUUID || uuid(fs) != m.trust.Identity.FSUUID {
			return bad()
		}
	}
	if d.Geometry != d.GeometryAfter || binary.LittleEndian.Uint32(d.FSXAttr[0:4]) != binary.LittleEndian.Uint32(d.FSXAttrAfter[0:4]) || binary.LittleEndian.Uint32(d.FSXAttr[12:16]) != binary.LittleEndian.Uint32(d.FSXAttrAfter[12:16]) {
		return bad()
	}
	for _, a := range [][28]byte{d.FSXAttr, d.FSXAttrAfter} {
		for _, b := range a[20:28] {
			if b != 0 {
				return bad()
			}
		}
	}
	flags, project := uint64(binary.LittleEndian.Uint32(d.FSXAttr[0:4])), uint64(binary.LittleEndian.Uint32(d.FSXAttr[12:16]))
	if flags&0x101 != 0 {
		return bad()
	}
	return directoryFacts{path: expectedPath, fsuuid: uuid(i.XFSUUID), device: r.device, mount: i.MountID, project: project, xflags: flags, inode: i.Inode}, nil
}
func (m *Mapper) anchor(a capacityprobe.PoolAnchorReadback, d directoryFacts, records []mountRecord) (capacitysnapshot.Anchor, error) {
	if d.project != 0 || d.xflags&0x200 != 0 || len(a.MountOptions) == 0 || len(a.MountOptions) > 32 || !hasQuota(a.MountOptions) {
		return capacitysnapshot.Anchor{}, errQuota
	}
	for _, s := range a.MountOptions {
		if !label(s) || strings.Contains(s, ",") {
			return capacitysnapshot.Anchor{}, errInvalid
		}
	}
	// Mount options are compared to the selected actual mount, not caller policy.
	for _, r := range records {
		if r.id == d.mount && !reflect.DeepEqual(a.MountOptions, r.options) {
			return capacitysnapshot.Anchor{}, errDrift
		}
	}
	b, z := a.Before, a.After
	if b.Type != 0x58465342 || b.Type != z.Type || b.BlockSize == 0 || b.BlockSize != b.FragmentSize || b.BlockSize != z.BlockSize || b.FragmentSize != z.FragmentSize || b.Blocks == 0 || b.Blocks != z.Blocks {
		return capacitysnapshot.Anchor{}, errResource
	}
	available := uint64(math.MaxInt64)
	for _, s := range []capacityprobe.PoolStatFSReadback{b, z} {
		if s.Available > s.Free || s.Free > s.Blocks || s.Blocks > math.MaxInt64/s.BlockSize {
			return capacitysnapshot.Anchor{}, errResource
		}
		v := s.Available * s.BlockSize
		if v < available {
			available = v
		}
	}
	return capacitysnapshot.Anchor{Path: d.path, FSUUID: d.fsuuid, MountID: d.mount, Device: d.device, FSType: "xfs", MountOptions: append([]string(nil), a.MountOptions...), ProjectID: d.project, XFlags: d.xflags, AvailableBytes: available}, nil
}
func storageDomain(id string, d directoryFacts, q capacitysnapshot.StorageQuota) capacitysnapshot.StorageDomain {
	return capacitysnapshot.StorageDomain{ID: id, Path: d.path, FSUUID: d.fsuuid, Device: d.device, MountID: d.mount, FSType: "xfs", XFlags: d.xflags, ProjectID: d.project, HardBytes: q.HardBlocks512 * 512, UsedBytes: q.UsedBlocks512 * 512, Quota: q}
}
func running(i capacityprobe.PoolContainerInspect, started time.Time) bool {
	return i.Status == "running" && i.Running && i.PID > 0 && !i.Paused && !i.Restarting && !i.Dead && !i.Privileged && i.NetworkMode != "host" && !i.StartedAt.IsZero() && !i.StartedAt.After(started)
}
func workerLabels(i capacityprobe.PoolContainerInspect, profile config.RuntimeResourceProfile, network config.RuntimeNetworkProfile) (uint64, error) {
	l := i.Labels
	for _, k := range []string{"worker-session-id", "worker-run-id", "runtime-instance-id"} {
		if !runtimeIdentityPattern.MatchString(l["com.switchback."+k]) {
			return 0, errInventory
		}
	}
	generation, ok := decimal(l["com.switchback.runtime-generation"], math.MaxInt64)
	if !ok || generation == 0 || strconv.FormatUint(generation, 10) != l["com.switchback.runtime-generation"] || l["com.switchback.managed"] != "worker-runtime" || l["com.switchback.resource-profile"] != profile.ID+"@"+strconv.FormatUint(profile.Revision, 10)+":"+profile.Digest || l["com.switchback.network-policy-id"] != network.ID || l["com.switchback.network-policy-revision"] != strconv.FormatUint(network.Revision, 10) || l["com.switchback.network-policy-digest"] != network.Digest || l["com.switchback.network-enforcement-status"] != config.NetworkEnforcementActive {
		return 0, errInventory
	}
	agent := l["com.switchback.agent-profile"]
	parts := strings.SplitN(agent, "@", 2)
	if len(parts) != 2 || !label(parts[0]) {
		return 0, errInventory
	}
	ref := strings.SplitN(parts[1], ":", 2)
	if len(ref) != 2 || !digestPattern.MatchString(ref[1]) {
		return 0, errInventory
	}
	rev, ok := decimal(ref[0], math.MaxInt64)
	if !ok || rev == 0 || strconv.FormatUint(rev, 10) != ref[0] {
		return 0, errInventory
	}
	return generation, nil
}
