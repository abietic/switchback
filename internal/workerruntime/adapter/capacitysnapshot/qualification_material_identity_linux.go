//go:build linux && (amd64 || arm64)

package capacitysnapshot

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
)

// Explicit preparation, outside the qualification window. This is a real key /
// password initialization operation; the driver does not invoke it implicitly.
// Only pre-created fresh root-owned fixed directories are accepted.
func qualificationPreparePublicIdentity(ctx context.Context) error {
	d, e := openQualificationCandidate(ctx, qualificationGuestCandidateRoot, 0)
	if e != nil {
		return e
	}
	defer d.close()
	if e = qualificationRuntimePreflightFixed(ctx); e != nil {
		return e
	}
	if e = d.claim(ctx, "identity"); e != nil {
		return e
	}
	keydir, _, e := openQualificationDirectory(ctx, "/mnt/lima-sbquota/core/capacity-publisher-keys")
	if e != nil {
		return e
	}
	defer keydir.Close()
	if e = qualificationInstallNames(keydir, nil); e != nil {
		return e
	}
	observer, _, e := openQualificationDirectory(ctx, qualificationObserverDirectory)
	if e != nil {
		return e
	}
	defer observer.Close()
	if e = qualificationInstallNames(observer, nil); e != nil {
		return e
	}
	core, _, e := openQualificationDirectory(ctx, "/mnt/lima-sbquota/core")
	if e != nil {
		return e
	}
	defer core.Close()
	for _, dir := range []*os.File{keydir, observer} {
		s, e := qualificationStat(ctx, int(dir.Fd()))
		if e != nil || s.Uid != 0 || s.Gid != 0 || s.Mode&07777 != 0700 {
			return errQualification
		}
	}
	create := func(dir *os.File, name string, raw []byte, mode uint32) error {
		fd, e := unix.Openat(int(dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
		if e != nil {
			return e
		}
		f := os.NewFile(uintptr(fd), name)
		defer f.Close()
		n, e := f.Write(raw)
		if e != nil || n != len(raw) {
			return fmt.Errorf("identity preparation write unknown")
		}
		if e = f.Sync(); e != nil {
			return e
		}
		if e = f.Close(); e != nil {
			return e
		}
		return dir.Sync()
	}
	seed := make([]byte, 32)
	if _, e = rand.Read(seed); e != nil {
		return e
	}
	defer clear(seed)
	private := ed25519.NewKeyFromSeed(seed)
	defer clear(private)
	public := append([]byte(nil), private.Public().(ed25519.PublicKey)...)
	ipc := make([]byte, 32)
	if _, e = rand.Read(ipc); e != nil {
		return e
	}
	defer clear(ipc)
	password := make([]byte, 32)
	if _, e = rand.Read(password); e != nil {
		return e
	}
	defer clear(password)
	if e = create(keydir, "ed25519.seed", seed, 0400); e != nil {
		return e
	}
	if e = create(observer, "key", ipc, 0400); e != nil {
		return e
	}
	if e = create(core, "postgres-password", []byte(hex.EncodeToString(password)), 0600); e != nil {
		return e
	}
	return d.put(ctx, "public-identity.json", qualificationPublicIdentity{Schema: "switchback.qualification.public_identity.v1", KeyID: "qualification-" + hex.EncodeToString(public[:8]), PublicKey: public})
}
