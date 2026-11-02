//go:build linux && (amd64 || arm64)

package capacityprobe

import (
	"context"
	"runtime"
)

type rolePrivateLinux struct{ poolLinux }

func (rolePrivateLinux) mountinfo(ctx context.Context) ([]byte, error) {
	return poolNativeRead(ctx, "/proc/thread-self/mountinfo", MaxResponse)
}

// ObserveRolePrivateDirectories never opens key or journal leaves.
func ObserveRolePrivateDirectories(ctx context.Context) ([]RolePrivateDirectory, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	return observeRolePrivateDirectories(ctx, rolePrivateLinux{})
}
