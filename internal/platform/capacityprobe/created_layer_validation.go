package capacityprobe

import (
	"encoding/binary"
	"errors"
	"path"
	"time"
)

// ValidateCreatedLayerObservation validates received finite native evidence. It
// neither sanitizes the caller's samples nor interprets limits as authority.
// Zero/off limits remain complete observations for the downstream quota policy.
func ValidateCreatedLayerObservation(containerID string, c CreatedLayerCollection) error {
	invalid := errors.New("created-layer finite observation invalid")
	if !hexID.MatchString(containerID) || c.Schema != "switchback.capacity.untrusted_created_layer_collection.v1" || c.Status != "preparation_blocked" || c.Trust != "none" || c.ExecutionEnabled || c.CapacityReady || !c.ObservationComplete || c.Engine == nil || c.Inspect == nil || c.Physical == nil {
		return invalid
	}
	blockers := []string{"unsigned_diagnostic_only", "quota_compliance_and_admission_unproven", "ABA_and_external_writers_unexcluded", "all_time_capacity_unknown"}
	if len(c.Blockers) != len(blockers) {
		return invalid
	}
	for i, b := range blockers {
		if c.Blockers[i] != b {
			return invalid
		}
	}
	if c.Started.IsZero() || !c.Ended.After(c.Started) || c.Ended.Sub(c.Started) > createdLayerWindow {
		return invalid
	}
	e, i := c.Engine, c.Inspect
	if !engineID.MatchString(e.ID) || e.APIVersion != "1.56" || !version.MatchString(e.Version) || len(e.Version) > 64 || e.DockerRoot != createdLayerDockerRoot || e.Driver != "overlay2" || e.Source != "engine_get_v1.56_version_info" {
		return invalid
	}
	if i.ID != containerID || (i.Image != "" && !createdLayerImage.MatchString(i.Image)) || i.Status != "created" || i.PID != 0 || i.Running || i.Paused || i.Restarting || i.Dead || i.StartedAt != "0001-01-01T00:00:00Z" || i.Driver != "overlay2" || i.Source != "engine_get_v1.56_container_inspect" || !createdLayerUpperValid(i.UpperDir) || i.ParentDir != path.Dir(i.UpperDir) {
		return invalid
	}
	endpoint := "/v1.56/containers/" + containerID + "/json"
	if !createdLayerSamplesValid(c.Samples, []string{"/v1.56/version", "/v1.56/info", endpoint, "physical", endpoint, "/v1.56/version", "/v1.56/info"}, c.Started, c.Ended, false) {
		return invalid
	}
	p := *c.Physical
	// The callback sanitizer rewrites sequence/source. Validate the original first,
	// and give it an independent slice so evidence is never repaired in place.
	ops := []string{"boot_id_before", "mount_namespace_before", "mountinfo_before", "root_open", "root_stat", "root_geometry", "root_fsxattr", "parent_open", "parent_stat", "parent_geometry", "parent_fsxattr", "diff_open", "diff_stat", "diff_geometry", "diff_fsxattr", "qstatv", "target_quota", "default0_quota", "boot_id_after", "mount_namespace_after", "mountinfo_after", "root_stat", "root_geometry", "root_fsxattr", "root_reopen", "parent_stat", "parent_geometry", "parent_fsxattr", "parent_reopen", "diff_stat", "diff_geometry", "diff_fsxattr", "diff_reopen"}
	outer := c.Samples[3]
	if !createdLayerSamplesValid(p.Samples, ops, outer.Started, outer.Ended, true) {
		return invalid
	}
	p.Samples = append([]CreatedLayerSample(nil), p.Samples...)
	if !createdLayerPhysicalResult(&p, i, outer.Started, outer.Ended) || p.BootIDBefore == [16]byte{} || p.MountNamespaceBefore == 0 || p.MountInfoSHA256Before == [32]byte{} {
		return invalid
	}
	root := p.Root.Before
	for _, d := range []CreatedLayerDirectory{p.Root, p.Parent, p.Diff} {
		n := d.Before
		u, err := createdLayerGeometry(&d.Geometry)
		if err != nil || u != n.XFSUUID || d.Geometry != p.Root.Geometry || n.Inode == 0 || n.MountID == 0 || n.MountID != root.MountID || n.DeviceMajor != root.DeviceMajor || n.DeviceMinor != root.DeviceMinor || n.Mode&0170000 != 0040000 || n.Mode&07022 != 0 || n.UID != 0 || n.GID != 0 {
			return invalid
		}
	}
	if p.Parent.Before.Inode == p.Diff.Before.Inode {
		return invalid
	}
	project := binary.LittleEndian.Uint32(p.Diff.FSXAttr[12:16])
	if project == 0 || project == 100 || project == 101 || project == 999 || project != p.ProjectID || project != binary.LittleEndian.Uint32(p.Parent.FSXAttr[12:16]) {
		return invalid
	}
	for _, d := range []CreatedLayerDirectory{p.Parent, p.Diff} {
		flags := binary.LittleEndian.Uint32(d.FSXAttr[:4])
		if flags&createdLayerXFlagProjInherit == 0 || flags&(createdLayerXFlagRealtime|createdLayerXFlagRTInherit) != 0 {
			return invalid
		}
	}
	if p.QStatV[0] != 1 {
		return invalid
	}
	for j, q := range [][112]byte{p.TargetQuota, p.DefaultQuota} {
		id := project
		if j == 1 {
			id = 0
		}
		if q[0] != 1 || q[1] != 2 || binary.LittleEndian.Uint32(q[4:8]) != id {
			return invalid
		}
		for _, b := range q[72:104] {
			if b != 0 {
				return invalid
			}
		}
	}
	return nil
}

func createdLayerSamplesValid(samples []CreatedLayerSample, operations []string, start, end time.Time, physical bool) bool {
	if len(samples) != len(operations) {
		return false
	}
	previous := start
	for j, s := range samples {
		source := "linux_syscall"
		if !physical && j != 3 {
			source = "engine_get_v1.56"
		}
		if s.Sequence != j+1 || s.Operation != operations[j] || s.Source != source || s.Outcome != "observed" || s.Errno != nil || s.Started.IsZero() || s.Started.Before(previous) || !s.Ended.After(s.Started) || s.Ended.After(end) {
			return false
		}
		previous = s.Ended
	}
	return true
}
