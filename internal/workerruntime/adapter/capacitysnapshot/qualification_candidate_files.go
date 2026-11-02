package capacitysnapshot

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type qualificationCandidateDirectory struct {
	file *os.File
	uid  uint32
}

func (d qualificationCandidateDirectory) close() {
	if d.file != nil {
		_ = d.file.Close()
	}
}
func openQualificationCandidate(ctx context.Context, p string, uid uint32) (qualificationCandidateDirectory, error) {
	var out qualificationCandidateDirectory
	if !absolute(p) || qualificationContext(ctx) != nil {
		return out, errQualification
	}
	fd, e := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return out, e
	}
	for _, part := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
		var s unix.Stat_t
		if e = unix.Fstat(fd, &s); e != nil || s.Mode&0022 != 0 || (s.Uid != 0 && s.Uid != uid) {
			unix.Close(fd)
			return out, fmt.Errorf("candidate ancestor unsafe")
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			return out, err
		}
		fd = next
	}
	var s unix.Stat_t
	if e = unix.Fstat(fd, &s); e != nil || s.Uid != uid || s.Mode&0777 != 0700 {
		unix.Close(fd)
		return out, fmt.Errorf("candidate root unsafe")
	}
	out = qualificationCandidateDirectory{file: os.NewFile(uintptr(fd), p), uid: uid}
	return out, qualificationContext(ctx)
}
func (d qualificationCandidateDirectory) read(ctx context.Context, name string) ([]byte, error) {
	if !qualificationLeaf(name) || d.file == nil {
		return nil, errQualification
	}
	fd, e := unix.Openat(int(d.file.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	s, e := qualificationStat(ctx, fd)
	if e != nil || s.Uid != d.uid || s.Mode&unix.S_IFMT != unix.S_IFREG || s.Mode&0222 != 0 || s.Nlink != 1 || s.Size <= 0 || s.Size > MaxBytes {
		return nil, fmt.Errorf("candidate leaf unsafe: %s", name)
	}
	var b strings.Builder
	buf := make([]byte, 32768)
	for {
		if e = qualificationContext(ctx); e != nil {
			return nil, e
		}
		n, err := f.Read(buf)
		if b.Len()+n > MaxBytes {
			return nil, errQualification
		}
		b.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	after, e := qualificationStat(ctx, fd)
	if e != nil || qualificationIdentity(after) != qualificationIdentity(s) || int64(b.Len()) != s.Size {
		return nil, fmt.Errorf("candidate leaf changed")
	}
	fresh, e := unix.Openat(int(d.file.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	defer unix.Close(fresh)
	again, e := qualificationStat(ctx, fresh)
	if e != nil || qualificationIdentity(again) != qualificationIdentity(s) {
		return nil, fmt.Errorf("candidate leaf replaced")
	}
	return []byte(b.String()), qualificationContext(ctx)
}
func (d qualificationCandidateDirectory) decode(ctx context.Context, name string, v any) error {
	b, e := d.read(ctx, name)
	if e != nil {
		return e
	}
	return strictJSON(b, v)
}
func (d qualificationCandidateDirectory) write(ctx context.Context, name string, b []byte) error {
	if !qualificationLeaf(name) || len(b) == 0 || len(b) > MaxBytes || qualificationContext(ctx) != nil {
		return errQualification
	}
	fd, e := unix.Openat(int(d.file.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0400)
	if e != nil {
		return e
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	for len(b) > 0 {
		if e = qualificationContext(ctx); e != nil {
			return e
		}
		chunk := b
		if len(chunk) > 32768 {
			chunk = chunk[:32768]
		}
		n, err := f.Write(chunk)
		if err != nil {
			return err
		}
		if n != len(chunk) {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	if e = f.Chmod(0444); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = d.file.Sync(); e != nil {
		return e
	}
	return qualificationContext(ctx)
}
func (d qualificationCandidateDirectory) put(ctx context.Context, name string, v any) error {
	b, e := qualificationEncode(v)
	if e != nil {
		return e
	}
	return d.write(ctx, name, b)
}
func (d qualificationCandidateDirectory) claim(ctx context.Context, phase string) error {
	return d.put(ctx, "claimed-"+phase+".json", struct{ Schema, Phase string }{"switchback.qualification.phase_claim.v1", phase})
}
func qualificationHashFile(ctx context.Context, p string, limit int64) (string, error) {
	if !absolute(p) || limit <= 0 {
		return "", errQualification
	}
	parent, e := qualificationOpenArtifactParent(ctx, filepath.Dir(p))
	if e != nil {
		return "", e
	}
	defer parent.Close()
	fd, e := unix.Openat(int(parent.Fd()), filepath.Base(p), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return "", e
	}
	f := os.NewFile(uintptr(fd), p)
	defer f.Close()
	before, e := qualificationStat(ctx, fd)
	if e != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Mode&0022 != 0 || before.Nlink != 1 || before.Size <= 0 || before.Size > limit {
		return "", fmt.Errorf("artifact unsafe")
	}
	h := sha256.New()
	buf := make([]byte, 32768)
	var size int64
	for {
		if e = qualificationContext(ctx); e != nil {
			return "", e
		}
		n, err := f.Read(buf)
		size += int64(n)
		if size > limit {
			return "", errQualification
		}
		h.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	after, e := qualificationStat(ctx, fd)
	if e != nil || qualificationIdentity(before) != qualificationIdentity(after) || size != before.Size {
		return "", fmt.Errorf("artifact changed")
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil)), qualificationContext(ctx)
}
func qualificationOpenArtifactParent(ctx context.Context, p string) (*os.File, error) {
	if p != "/" && !absolute(p) {
		return nil, errQualification
	}
	fd, e := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	if p != "/" {
		for _, part := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
			if qualificationContext(ctx) != nil {
				unix.Close(fd)
				return nil, ctx.Err()
			}
			next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			unix.Close(fd)
			if e != nil {
				return nil, e
			}
			fd = next
		}
	}
	return os.NewFile(uintptr(fd), p), nil
}
