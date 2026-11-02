package capacitypublisher

import (
	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	"testing"
)

// The expected identities are copied from qualification, never seeded from
// current startup reads. Even a root-owned 0700 replacement must reject.
func TestPublisherPrivateDirectoriesMatchQualificationOnFirstRead(t *testing.T) {
	expected := []capacitysnapshot.DeploymentPrivateDirectoryBinding{
		{Path: "/mnt/lima-sbquota/core/capacity-publication-private", Device: "8:7", Root: "/core/capacity-publication-private", Inode: 101, MountID: 73, Mode: 0040700},
		{Path: "/mnt/lima-sbquota/core/capacity-publisher-keys", Device: "8:7", Root: "/core/capacity-publisher-keys", Inode: 102, MountID: 73, Mode: 0040700},
	}
	actual := make([]capacityprobe.RolePrivateDirectory, len(expected))
	for j, e := range expected {
		actual[j] = capacityprobe.RolePrivateDirectory{Path: e.Path, Device: e.Device, Root: e.Root, Inode: e.Inode, MountID: e.MountID, Mode: e.Mode, UID: e.UID, GID: e.GID}
	}
	if e := validatePublisherPrivateDirectories(expected, actual); e != nil {
		t.Fatal(e)
	}
	for _, field := range []string{"inode", "mount", "device", "root", "mode", "uid", "gid", "duplicate", "missing"} {
		t.Run(field, func(t *testing.T) {
			changed := append([]capacityprobe.RolePrivateDirectory(nil), actual...)
			switch field {
			case "inode":
				changed[0].Inode++
			case "mount":
				changed[0].MountID++
			case "device":
				changed[0].Device = "8:8"
			case "root":
				changed[0].Root = "/other"
			case "mode":
				changed[0].Mode = 0040777
			case "uid":
				changed[0].UID = 10002
			case "gid":
				changed[0].GID = 10002
			case "duplicate":
				changed[1] = changed[0]
			case "missing":
				changed = changed[:1]
			}
			if e := validatePublisherPrivateDirectories(expected, changed); e == nil {
				t.Fatal("unqualified startup baseline accepted")
			}
			// A rejected startup cannot overwrite the qualified baseline.
			if e := validatePublisherPrivateDirectories(expected, actual); e != nil {
				t.Fatal(e)
			}
		})
	}
}
