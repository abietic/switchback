// Pool readbacks are unsigned finite observations, never native provenance.
package capacityprobe

import "time"

type PoolObservation struct {
	Started, Ended                  time.Time
	BootBefore, BootAfter           [16]byte
	NamespaceBefore, NamespaceAfter uint64
	MountInfoBefore, MountInfoAfter []byte
	EngineBefore, EngineAfter       PoolEngineReadback
	Anchor                          PoolAnchorReadback
	Core                            []PoolCoreReadback
	Storage                         []PoolStorageReadback
	Workers                         []PoolWorkerReadback
}
type PoolEngineReadback struct {
	ID, APIVersion, Version, DockerRoot, Driver string
	CPUCount, MemoryBytes                       uint64
	Containers                                  []PoolContainerInspect
}
type PoolContainerInspect struct {
	ID, ImageDigest, Status, GraphDriver, UpperDir  string
	PID                                             uint64
	Running, Paused, Restarting, Dead               bool
	StartedAt                                       time.Time
	Labels                                          map[string]string
	CgroupParent                                    string
	NanoCPUs, MemoryBytes, MemorySwapBytes, PIDsMax uint64
	Privileged                                      bool
	NetworkMode                                     string
}
type PoolStatFSReadback struct {
	Type, BlockSize, FragmentSize, Blocks, Free, Available uint64
}
type PoolAnchorReadback struct {
	Directory     CreatedLayerDirectory
	Before, After PoolStatFSReadback
	MountOptions  []string
}
type PoolQuotaReadback struct {
	QStatV                [160]byte
	StateSamples          []CreatedLayerSample
	StateSamplesTruncated bool
	Target                Quota
	Default0              [112]byte
}
type PoolStorageReadback struct {
	DomainID  string
	Directory CreatedLayerDirectory
	Quota     PoolQuotaReadback
}
type PoolCgroupIdentity struct{ FSType, Device, Inode uint64 }
type PoolCgroupFiles struct{ CPU, Memory, Swap, PIDs, Type string }
type PoolCgroupReadback struct {
	Path                    string
	Before, After           PoolCgroupIdentity
	FilesBefore, FilesAfter PoolCgroupFiles
}
type PoolProcessReadback struct {
	ContainerID                                                    string
	PIDBefore, PIDAfter, StartTimeTicksBefore, StartTimeTicksAfter uint64
	CgroupPathBefore, CgroupPathAfter                              string
}
type PoolCoreReadback struct {
	CoreID  string
	Cgroup  PoolCgroupReadback
	Members []PoolProcessReadback
}
type PoolWorkerReadback struct {
	ContainerID string
	Process     PoolProcessReadback
	Cgroup      PoolCgroupReadback
	Layer       CreatedLayerDirectory
	Quota       PoolQuotaReadback
}
