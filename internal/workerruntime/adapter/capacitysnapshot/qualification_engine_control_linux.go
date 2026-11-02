//go:build linux && (amd64 || arm64)

package capacitysnapshot

import (
	"context"
	"os"
	"path/filepath"

	"github.com/abietic/switchback/internal/platform/qualificationio"
	"golang.org/x/sys/unix"
)

// The control directory is an exact new child of the 0700 candidate root.
// A helper sees only this 0755 directory through a read-only bind, never the
// candidate root or a writer alias. The sole control leaf is written after CID
// Create/Inspect and before the one Start.
func qualificationCreateControlDirectory(ctx context.Context, root qualificationCandidateDirectory, testCase string) (string, error) {
	if !qualificationHelperCase(testCase) || root.file == nil || root.uid != 0 || qualificationContext(ctx) != nil {
		return "", errQualification
	}
	name := "control-" + testCase
	if err := unix.Mkdirat(int(root.file.Fd()), name, 0755); err != nil {
		return "", err
	}
	fd, err := unix.Openat(int(root.file.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Uid != 0 || stat.Gid != 0 || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0777 != 0755 {
		return "", errQualification
	}
	path := filepath.Join(qualificationGuestCandidateRoot, name)
	return path, qualificationContext(ctx)
}

func qualificationWriteControl(ctx context.Context, path string, control qualificationio.Control) error {
	if !absolute(path) || filepath.Dir(path) != qualificationGuestCandidateRoot || qualificationContext(ctx) != nil {
		return errQualification
	}
	data, err := qualificationio.EncodeControl(control)
	if err != nil {
		return err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	d := qualificationCandidateDirectory{file: os.NewFile(uintptr(fd), path), uid: 0}
	defer d.close()
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil || stat.Uid != 0 || stat.Gid != 0 || stat.Mode&0777 != 0755 {
		return errQualification
	}
	return d.write(ctx, "control.json", data)
}
