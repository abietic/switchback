package capacityprobe

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"strings"
)

// RolePrivateDirectory is an unsigned current readback, not qualification.
type RolePrivateDirectory struct {
	Path, Device, Root             string
	Inode, MountID, Mode, UID, GID uint64
}
type rolePrivateBackend interface {
	open(context.Context, string) (int, error)
	stat(context.Context, int) (createdLayerMetadata, error)
	mountinfo(context.Context) ([]byte, error)
	close(int)
}

func observeRolePrivateDirectories(ctx context.Context, k rolePrivateBackend) ([]RolePrivateDirectory, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, ErrDeploymentCancelled
	}
	raw, e := k.mountinfo(ctx)
	if e != nil {
		return nil, e
	}
	records, e := poolMountRecords(raw)
	if e != nil {
		return nil, e
	}
	out := []RolePrivateDirectory{}
	for _, p := range []string{"/mnt/lima-sbquota/core/capacity-publication-private", "/mnt/lima-sbquota/core/capacity-publisher-keys"} {
		v, e := observeRolePrivateDirectory(ctx, k, records, p)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	if ctx.Err() != nil {
		return nil, ErrDeploymentCancelled
	}
	after, e := k.mountinfo(ctx)
	if e != nil || !bytes.Equal(raw, after) {
		return nil, ErrDeploymentInvalid
	}
	return out, nil
}
func observeRolePrivateDirectory(ctx context.Context, k rolePrivateBackend, records []poolMountRecord, p string) (RolePrivateDirectory, error) {
	var zero RolePrivateDirectory
	if ctx.Err() != nil {
		return zero, ErrDeploymentCancelled
	}
	fd, e := k.open(ctx, p)
	if fd >= 0 {
		defer k.close(fd)
	}
	if e != nil {
		return zero, e
	}
	before, e := k.stat(ctx, fd)
	if e != nil {
		return zero, e
	}
	if before.Mode != 0040700 || before.UID != 0 || before.GID != 0 || before.Inode == 0 || before.FSType != 0x58465342 {
		return zero, ErrDeploymentInvalid
	}
	selected, e := poolMountAt(records, p)
	if e != nil || selected.id != before.MountID || selected.major != before.Major || selected.minor != before.Minor || selected.kind != "xfs" {
		return zero, ErrDeploymentInvalid
	}
	if ctx.Err() != nil {
		return zero, ErrDeploymentCancelled
	}
	fresh, e := k.open(ctx, p)
	if fresh >= 0 {
		defer k.close(fresh)
	}
	if e != nil {
		return zero, e
	}
	reopened, e := k.stat(ctx, fresh)
	if e != nil || !poolStable(before, reopened) {
		return zero, ErrDeploymentInvalid
	}
	after, e := k.stat(ctx, fd)
	if e != nil || !poolStable(before, after) {
		return zero, ErrDeploymentInvalid
	}
	if ctx.Err() != nil {
		return zero, ErrDeploymentCancelled
	}
	return RolePrivateDirectory{Path: p, Device: fmt.Sprintf("%d:%d", before.Major, before.Minor), Root: path.Join(selected.root, strings.TrimPrefix(strings.TrimPrefix(p, selected.point), "/")), Inode: before.Inode, MountID: before.MountID, Mode: uint64(before.Mode), UID: uint64(before.UID), GID: uint64(before.GID)}, nil
}
