package capacityobserver

import (
	"encoding/binary"
	"fmt"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"github.com/abietic/switchback/internal/platform/createdlayeripc"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
)

// The authenticated request binds owner/profile/manifest metadata. Every physical
// deployment identity below comes from received observations, never pinned Trust.
func observationFacts(b createdlayeripc.Binding, c capacityprobe.CreatedLayerCollection) (capacitysnapshot.CreatedLayerFacts, error) {
	if err := capacityprobe.ValidateCreatedLayerObservation(b.ContainerID, c); err != nil {
		return capacitysnapshot.CreatedLayerFacts{}, err
	}
	p, i := c.Physical, c.Inspect
	root := p.Root.Before
	identity := capacitysnapshot.Identity{EngineID: c.Engine.ID, DockerRoot: c.Engine.DockerRoot, FSUUID: uuid(root.XFSUUID), GuestBootID: uuid(p.BootIDBefore), MountNamespace: fmt.Sprintf("mnt:[%d]", p.MountNamespaceBefore), MountID: root.MountID, Device: device(root)}
	f := capacitysnapshot.CreatedLayerFacts{SampleStartedAt: c.Started, ObservedAt: c.Ended, Identity: identity, ManifestDigest: b.ManifestDigest, ContainerID: i.ID, RuntimeID: b.RuntimeID, SessionID: b.SessionID, WorkerRunID: b.RunID, Generation: b.Generation, ProfileID: b.ProfileID, ProfileRevision: b.ProfileRevision, ProfileDigest: b.ProfileDigest,
		// Translate the validated Engine inspect provenance into the existing facts ABI.
		InspectSource: "docker_engine_inspect_v1", GraphDriver: i.Driver, State: capacitysnapshot.CreatedLayerState{Status: i.Status, PID: uint64(i.PID), Running: i.Running, Paused: i.Paused, Restarting: i.Restarting, Dead: i.Dead}, UpperDir: i.UpperDir, Parent: observedNode(p.Parent), Diff: observedNode(p.Diff), Quota: capacitysnapshot.CreatedLayerQuota{Format: "linux_xfs_quotactl_v1", StateSource: "Q_XGETQSTATV", StateVersion: uint64(p.QStatV[0]), StateFlags: uint64(binary.LittleEndian.Uint16(p.QStatV[2:4])), QuotaSource: "Q_XGETQUOTA", ABI: capacitysnapshot.CreatedLayerQuotaABI, FSUUID: uuid(p.Parent.Before.XFSUUID), MountID: p.Parent.Before.MountID, Device: device(p.Parent.Before), Target: append([]byte(nil), p.TargetQuota[:]...), Default0: append([]byte(nil), p.DefaultQuota[:]...)}}
	return f, nil
}

func observedNode(d capacityprobe.CreatedLayerDirectory) capacitysnapshot.CreatedLayerNode {
	n := d.Before
	return capacitysnapshot.CreatedLayerNode{Path: n.CanonicalPath, FSUUID: uuid(n.XFSUUID), MountID: n.MountID, Device: device(n), FSType: "xfs", Inode: n.Inode, DeviceMajor: uint64(n.DeviceMajor), DeviceMinor: uint64(n.DeviceMinor), Mode: uint64(n.Mode), UID: uint64(n.UID), GID: uint64(n.GID), NoFollow: d.NoFollowWalk, NoSymlinkAncestors: d.NoFollowWalk, MountStable: d.IdentityRechecked, RawFSXAttr: append([]byte(nil), d.FSXAttr[:]...)}
}
func uuid(b [16]byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func device(n capacityprobe.CreatedLayerIdentity) string {
	return fmt.Sprintf("%d:%d", n.DeviceMajor, n.DeviceMinor)
}
