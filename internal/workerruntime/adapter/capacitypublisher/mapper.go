// Package capacitypublisher maps finite observations to unsigned facts only.
// It performs no IO and cannot issue capacity or execution authority.
package capacitypublisher

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
)

var (
	errInvalid   = errors.New("observation_invalid")
	errStale     = errors.New("observation_stale")
	errDrift     = errors.New("identity_drift")
	errInventory = errors.New("inventory_incomplete")
	errQuota     = errors.New("quota_unproven")
	errResource  = errors.New("resource_mismatch")
)

type CoreContainerBinding struct{ ContainerID, ImageDigest, CoreID string }
type Result struct {
	Facts         capacitysnapshot.Facts
	StorageInodes map[string]capacitysnapshot.CreatedLayerInodeObservation
	LayerInodes   map[string]capacitysnapshot.CreatedLayerInodeObservation
}

// Mapper owns detached deployment configuration. Its zero value rejects inputs.
type Mapper struct {
	trust             capacitysnapshot.Trust
	core              []CoreContainerBinding
	workerImageDigest string
	profile           config.RuntimeResourceProfile
	network           config.RuntimeNetworkProfile
	configured        bool
}

func NewMapper(trust capacitysnapshot.Trust, core []CoreContainerBinding, workerImageDigest string) (*Mapper, error) {
	if capacitysnapshot.ValidateTrust(trust) != nil || trust.AdmissionPolicy == nil || len(trust.Core) > 32 || len(trust.Storage) > 32 || len(core) == 0 || len(core) > 32 || !digestPattern.MatchString(workerImageDigest) {
		return nil, errInvalid
	}
	g := trust.AdmissionPolicy
	if (config.RuntimeCapacityGuarantee{ID: g.ID, Revision: g.Revision, Digest: g.Digest}).Validate() != nil {
		return nil, errInvalid
	}
	p, ok := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
	n, e := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(config.SingleUserLocalProfileName)
	if !ok || p.ValidateDigest() != nil || p.ID != config.LocalStandardV1 || p.Revision != 1 || p.CPUUnits != 2 || p.MemoryBytes != 4<<30 || p.PIDsLimit != 512 || p.WritableLayerBytes != 10<<30 || e != nil || n.ValidateDigest() != nil || n.ID != config.TrustedFullNetworkV1 || n.Revision != 1 {
		return nil, errInvalid
	}
	ids, covered := map[string]bool{}, map[string]bool{}
	for _, b := range core {
		if !cidPattern.MatchString(b.ContainerID) || !digestPattern.MatchString(b.ImageDigest) || ids[b.ContainerID] {
			return nil, errInvalid
		}
		ids[b.ContainerID] = true
		found := false
		for _, c := range trust.Core {
			if c.ID == b.CoreID {
				found = true
			}
		}
		if !found {
			return nil, errInvalid
		}
		covered[b.CoreID] = true
	}
	if len(covered) != len(trust.Core) {
		return nil, errInvalid
	}
	trust.PublicKey = append([]byte(nil), trust.PublicKey...)
	trust.Core = append([]capacitysnapshot.CoreDomain(nil), trust.Core...)
	trust.Storage = append([]capacitysnapshot.StorageDomain(nil), trust.Storage...)
	trust.Coverage = append([]capacitysnapshot.StorageCoverage(nil), trust.Coverage...)
	policy := *trust.AdmissionPolicy
	trust.AdmissionPolicy = &policy
	return &Mapper{trust: trust, core: append([]CoreContainerBinding(nil), core...), workerImageDigest: workerImageDigest, profile: p, network: n, configured: true}, nil
}
func (m *Mapper) Map(ctx context.Context, o capacityprobe.PoolObservation, seq int64) (Result, error) {
	return m.mapAt(ctx, o, seq, time.Now())
}

// Only mapAt assembles a Result. All failures discard accumulated local values.
func (m *Mapper) mapAt(ctx context.Context, o capacityprobe.PoolObservation, seq int64, now time.Time) (result Result, err error) {
	if ctx == nil {
		return Result{}, errInvalid
	}
	if e := ctx.Err(); e != nil {
		return Result{}, e
	}
	defer func() {
		if e := ctx.Err(); e != nil {
			result, err = Result{}, e
		}
	}()
	if m == nil || !m.configured {
		return Result{}, errInvalid
	}
	if e := window(o, seq, now); e != nil {
		return Result{}, e
	}
	if len(o.Core) > 32 || len(o.Storage) > 32 || len(o.Workers) > 32 || len(o.Core) != len(m.trust.Core) || len(o.Storage) != len(m.trust.Storage) {
		return Result{}, errInventory
	}
	inventory, e := census(o)
	if e != nil {
		return Result{}, e
	}
	records, e := mounts(o.MountInfoBefore)
	if e != nil {
		return Result{}, e
	}
	anchorDir, e := m.directory(o.Anchor.Directory, m.trust.AnchorPath, records)
	if e != nil {
		return Result{}, e
	}
	anchor, e := m.anchor(o.Anchor, anchorDir, records)
	if e != nil {
		return Result{}, e
	}
	identity := capacitysnapshot.Identity{EngineID: o.EngineBefore.ID, DockerRoot: o.EngineBefore.DockerRoot, FSUUID: anchorDir.fsuuid, GuestBootID: uuid(o.BootBefore), MountNamespace: fmt.Sprint(o.NamespaceBefore), MountID: anchorDir.mount, Device: anchorDir.device}
	if identity != m.trust.Identity {
		return Result{}, errDrift
	}
	directoryIDs := map[string]bool{}
	markDirectory := func(d directoryFacts) bool {
		key := fmt.Sprintf("%s:%d", d.device, d.inode)
		if directoryIDs[key] {
			return false
		}
		directoryIDs[key] = true
		return true
	}
	markDirectory(anchorDir)
	cgroupIDs := map[string]bool{}
	markCgroup := func(c capacityprobe.PoolCgroupReadback) bool {
		key := fmt.Sprintf("%d:%d", c.Before.Device, c.Before.Inode)
		if cgroupIDs[key] {
			return false
		}
		cgroupIDs[key] = true
		return true
	}
	coreFacts := make([]capacitysnapshot.CoreDomain, 0, len(o.Core))
	seenCore := map[string]bool{}
	seenCID := map[string]bool{}
	for _, c := range o.Core {
		if seenCore[c.CoreID] || len(c.Members) > 32 {
			return Result{}, errInventory
		}
		seenCore[c.CoreID] = true
		actual, e := cgroup(c.Cgroup)
		if e != nil {
			return Result{}, e
		}
		actual.ID = c.CoreID
		known := false
		for _, want := range m.trust.Core {
			if actual == want {
				known = true
			}
		}
		if !known || !markCgroup(c.Cgroup) {
			return Result{}, errResource
		}
		expected := map[string]CoreContainerBinding{}
		for _, b := range m.core {
			if b.CoreID == c.CoreID {
				expected[b.ContainerID] = b
			}
		}
		if len(c.Members) != len(expected) {
			return Result{}, errInventory
		}
		for _, member := range c.Members {
			b, ok := expected[member.ContainerID]
			i, exists := inventory[member.ContainerID]
			if !ok || !exists || seenCID[i.ID] || b.ImageDigest != i.ImageDigest || !running(i, o.Started) {
				return Result{}, errInventory
			}
			for _, key := range []string{"worker-session-id", "worker-run-id", "runtime-instance-id", "runtime-generation"} {
				if _, ok := i.Labels["com.switchback."+key]; ok {
					return Result{}, errInventory
				}
			}
			if i.Labels["com.switchback.managed"] == "worker-runtime" || process(member, i, c.Cgroup.Path) != nil {
				return Result{}, errInventory
			}
			seenCID[i.ID] = true
		}
		coreFacts = append(coreFacts, actual)
	}
	storageFacts := make([]capacitysnapshot.StorageDomain, 0, len(o.Storage))
	storageInodes := map[string]capacitysnapshot.CreatedLayerInodeObservation{}
	projects := map[uint64]bool{}
	for _, s := range o.Storage {
		if _, ok := storageInodes[s.DomainID]; ok {
			return Result{}, errInventory
		}
		var want capacitysnapshot.StorageDomain
		found := false
		for _, d := range m.trust.Storage {
			if d.ID == s.DomainID {
				want = d
				found = true
			}
		}
		if !found {
			return Result{}, errInventory
		}
		d, e := m.directory(s.Directory, want.Path, records)
		if e != nil {
			return Result{}, e
		}
		if !markDirectory(d) || d.project != want.ProjectID || d.xflags&0x200 == 0 || projects[d.project] {
			return Result{}, errQuota
		}
		q, inodes, e := quota(s.Quota, d.project, false, o.Started, o.Ended)
		if e != nil {
			return Result{}, e
		}
		actual := storageDomain(s.DomainID, d, q)
		expected := want
		expected.UsedBytes = actual.UsedBytes
		expected.Quota.UsedBlocks512 = actual.Quota.UsedBlocks512
		if actual != expected {
			return Result{}, errQuota
		}
		projects[d.project] = true
		storageFacts = append(storageFacts, actual)
		storageInodes[s.DomainID] = inodes
	}
	containers := make([]capacitysnapshot.Container, 0, len(o.Workers))
	layerInodes := map[string]capacitysnapshot.CreatedLayerInodeObservation{}
	for _, w := range o.Workers {
		i, ok := inventory[w.ContainerID]
		if !ok || seenCID[w.ContainerID] || !running(i, o.Started) || i.ImageDigest != m.workerImageDigest {
			return Result{}, errInventory
		}
		seenCID[w.ContainerID] = true
		generation, e := workerLabels(i, m.profile, m.network)
		if e != nil {
			return Result{}, e
		}
		if i.NanoCPUs != uint64(m.profile.CPUUnits)*1e9 || i.MemoryBytes != uint64(m.profile.MemoryBytes) || i.MemorySwapBytes != i.MemoryBytes || i.PIDsMax != uint64(m.profile.PIDsLimit) {
			return Result{}, errResource
		}
		cg, e := cgroup(w.Cgroup)
		if e != nil {
			return Result{}, e
		}
		if !markCgroup(w.Cgroup) || cg.CPUQuotaMicros/cg.CPUPeriodMicros != 2 || cg.CPUQuotaMicros%cg.CPUPeriodMicros != 0 || cg.MemoryMax != uint64(m.profile.MemoryBytes) || cg.PIDsMax != uint64(m.profile.PIDsLimit) {
			return Result{}, errResource
		}
		if process(w.Process, i, w.Cgroup.Path) != nil {
			return Result{}, errInventory
		}
		prefix := m.trust.Identity.DockerRoot + "/overlay2/"
		if i.GraphDriver != "overlay2" || !absolute(i.UpperDir) || !strings.HasPrefix(i.UpperDir, prefix) {
			return Result{}, errDrift
		}
		parts := strings.Split(strings.TrimPrefix(i.UpperDir, prefix), "/")
		if len(parts) != 2 || parts[0] == "l" || parts[0] == "." || parts[0] == ".." || !label(parts[0]) || parts[1] != "diff" {
			return Result{}, errDrift
		}
		d, e := m.directory(w.Layer, i.UpperDir, records)
		if e != nil {
			return Result{}, e
		}
		if !markDirectory(d) || d.project == 0 || d.xflags&0x200 == 0 || projects[d.project] {
			return Result{}, errQuota
		}
		projects[d.project] = true
		q, inodes, e := quota(w.Quota, d.project, true, o.Started, o.Ended)
		if e != nil {
			return Result{}, e
		}
		runtime := i.Labels["com.switchback.runtime-instance-id"]
		containers = append(containers, capacitysnapshot.Container{ContainerID: i.ID, RuntimeID: runtime, SessionID: i.Labels["com.switchback.worker-session-id"], Generation: generation, ProfileID: m.profile.ID, ProfileRevision: m.profile.Revision, ProfileDigest: m.profile.Digest, NanoCPUs: i.NanoCPUs, MemoryMax: cg.MemoryMax, PIDsMax: cg.PIDsMax, CgroupPath: cg.CgroupPath, CPUQuotaMicros: cg.CPUQuotaMicros, CPUPeriodMicros: cg.CPUPeriodMicros, Layer: storageDomain("layer-"+runtime, d, q)})
		layerInodes[i.ID] = inodes
	}
	if len(seenCID) != len(inventory) {
		return Result{}, errInventory
	}
	sort.Slice(coreFacts, func(i, j int) bool { return coreFacts[i].ID < coreFacts[j].ID })
	sort.Slice(storageFacts, func(i, j int) bool { return storageFacts[i].ID < storageFacts[j].ID })
	sort.Slice(containers, func(i, j int) bool { return containers[i].ContainerID < containers[j].ContainerID })
	facts := capacitysnapshot.Facts{Sequence: seq, ObservedAt: o.Started, Identity: identity, ManifestDigest: m.trust.ManifestDigest, EngineCPUMilli: o.EngineBefore.CPUCount * 1000, EngineMemoryBytes: o.EngineBefore.MemoryBytes, Anchor: anchor, Core: coreFacts, Storage: storageFacts, Containers: containers, Removals: []capacitysnapshot.Removal{}}
	if capacitysnapshot.CheckFacts(facts, m.trust) != nil {
		return Result{}, errResource
	}
	return Result{Facts: facts, StorageInodes: storageInodes, LayerInodes: layerInodes}, nil
}
