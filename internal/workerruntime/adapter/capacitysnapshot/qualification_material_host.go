package capacitysnapshot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const qualificationHostVolume = "/Volumes/SwitchbackCore"
const qualificationLimaExecutable = qualificationHostVolume + "/toolchain/lima-2.2.1/bin/limactl"
const qualificationLimaHome = qualificationHostVolume + "/lima"
const qualificationLimaConfig = qualificationLimaHome + "/switchback-core/lima.yaml"

func qualificationGenerateHostMaterial(ctx context.Context) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("host material requires Darwin")
	}
	home, e := os.UserHomeDir()
	if e != nil {
		return e
	}
	d, e := openQualificationCandidate(ctx, filepath.Join(home, ".switchback", "qualification-candidate"), uint32(os.Getuid()))
	if e != nil {
		return e
	}
	defer d.close()
	if e = d.claim(ctx, "host-material"); e != nil {
		return e
	}
	infoRaw, e := qualificationHostExec(ctx, "/usr/sbin/diskutil", "info", "-plist", qualificationHostVolume)
	if e != nil {
		return e
	}
	info, e := qualificationPlist(infoRaw)
	if e != nil {
		return e
	}
	listRaw, e := qualificationHostExec(ctx, "/usr/sbin/diskutil", "apfs", "list", "-plist")
	if e != nil {
		return e
	}
	list, e := qualificationPlist(listRaw)
	if e != nil {
		return e
	}
	h := qualificationHostTarget{VolumeUUID: plistText(info, "VolumeUUID"), Mount: qualificationHostVolume, QuotaBytes: 120259084288, ReserveBytes: 120259084288}
	if h.VolumeUUID == "" {
		h.VolumeUUID = plistText(info, "DiskUUID")
	}
	matches := 0
	for _, entry := range plistArray(list, "Containers") {
		c, ok := entry.(map[string]any)
		if !ok {
			return errQualification
		}
		if plistText(c, "ContainerReference") != plistText(info, "APFSContainerReference") {
			continue
		}
		matches++
		h.ContainerUUID = plistText(c, "APFSContainerUUID")
		stores := plistArray(c, "PhysicalStores")
		if len(stores) != 1 {
			return errQualification
		}
		p, ok := stores[0].(map[string]any)
		if !ok {
			return errQualification
		}
		h.PhysicalStore = plistText(p, "DeviceIdentifier")
		h.PhysicalStoreUUID = plistText(p, "DiskUUID")
		volumes := 0
		for _, v := range plistArray(c, "Volumes") {
			m, ok := v.(map[string]any)
			if !ok {
				return errQualification
			}
			if plistText(m, "APFSVolumeUUID") == h.VolumeUUID {
				q, qok := plistNumber(m, "CapacityQuota")
				r, rok := plistNumber(m, "CapacityReserve")
				if !qok || !rok || q != h.QuotaBytes || r != h.ReserveBytes {
					return errQualification
				}
				volumes++
			}
		}
		if volumes != 1 {
			return errQualification
		}
	}
	if matches != 1 {
		return errQualification
	}
	exe, e := qualificationHashFile(ctx, qualificationLimaExecutable, 512<<20)
	if e != nil {
		return e
	}
	conf, e := qualificationHashFile(ctx, qualificationLimaConfig, MaxBytes)
	if e != nil {
		return e
	}
	st, e := os.Stat(qualificationLimaConfig)
	if e != nil {
		return e
	}
	m := qualificationHostMaterial{Schema: "switchback.qualification.host_material.v1", Host: h, ExecutableDigest: exe, Config: qualificationSourceFile{Path: "host-config/lima.yaml", Type: "regular", Mode: uint64(st.Mode().Perm()), Bytes: uint64(st.Size()), Digest: conf}}
	m.Toolchain, e = qualificationWriterToolchain(qualificationLimaExecutable)
	if e != nil {
		return e
	}
	paths := map[string]string{"system-image": qualificationLimaHome + "/switchback-core/disk", "data-image": qualificationLimaHome + "/_disks/sbquota/datadisk", "lima-state": qualificationLimaHome + "/switchback-core", "lima-log": qualificationLimaHome + "/switchback-core/ha.stdout.log", "vz-helper": qualificationLimaHome + "/switchback-core", "cache": qualificationHostVolume + "/cache", "temp": qualificationHostVolume + "/tmp", "qualification": qualificationHostVolume + "/qualification"}
	for _, id := range qualificationWriterIDs {
		a := qualificationWriterArtifact{ID: id, Executable: qualificationLimaExecutable, Config: qualificationLimaConfig, ExecutableDigest: exe, ConfigDigest: conf}
		policy, p := "volume_mapped", paths[id]
		if p == "" {
			policy = "forbidden_during_frozen_deployment"
		}
		m.Writers = append(m.Writers, a)
		m.Host.Writers = append(m.Host.Writers, qualificationWriter{ID: id, Path: p, Policy: policy, ArtifactDigest: qualificationDigest(a)})
	}
	if e = validateQualificationHostTarget(m.Host); e != nil {
		return e
	}
	// The actual stats/allocation remain in this preflight readback and later host
	// receipt, never in the static writer identity used by Create labels.
	staged := qualificationStaged{Source: qualificationSource{Files: []qualificationSourceFile{m.Config}, Inputs: qualificationInputs{Host: m.Host}}, Writers: m.Writers}
	for _, w := range m.Writers {
		staged.Source.Builds = append(staged.Source.Builds, qualificationBuild{Role: "writer-" + w.ID, BinaryDigest: exe, InputRoot: qualificationDigest(staged.Source.Files)})
	}
	pathsReadback, e := qualificationHostObservePaths(ctx, staged)
	if e != nil {
		return e
	}
	if e = d.put(ctx, "host-material-readback.json", pathsReadback); e != nil {
		return e
	}
	return d.put(ctx, "host-material.json", m)
}
