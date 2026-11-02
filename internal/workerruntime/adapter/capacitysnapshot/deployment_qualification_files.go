package capacitysnapshot

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"golang.org/x/sys/unix"
)

var qualificationLeaves = [...]string{"admission-manifest.json", "source-manifest.json", "deployment-inventory.json", "host-boundary-receipt.json", "native-enforcement-receipt.json", "trust.json", "publisher-bindings.json", "observer-deployment.env"}

type qualificationFile struct {
	identity string
	data     []byte
}
type qualificationFiles struct {
	path        string
	directories []string
	leaves      map[string]qualificationFile
}

func qualificationContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("qualification context missing")
	}
	return ctx.Err()
}

// Ignore access time only. Reading must not mask an equal-byte rewrite, chmod,
// link, replacement or directory rename. Fields are portable across unix targets.
func qualificationIdentity(s unix.Stat_t) string {
	v := reflect.ValueOf(s)
	var b strings.Builder
	for _, name := range []string{"Dev", "Ino", "Mode", "Uid", "Gid", "Nlink", "Size", "Mtim", "Ctim", "Mtimespec", "Ctimespec"} {
		f := v.FieldByName(name)
		if f.IsValid() {
			fmt.Fprintf(&b, "%s=%v;", name, f.Interface())
		}
	}
	return b.String()
}
func qualificationStat(ctx context.Context, fd int) (unix.Stat_t, error) {
	var s unix.Stat_t
	if e := qualificationContext(ctx); e != nil {
		return s, e
	}
	e := unix.Fstat(fd, &s)
	if e == nil {
		e = qualificationContext(ctx)
	}
	return s, e
}
func openQualificationDirectory(ctx context.Context, path string) (*os.File, []string, error) {
	if !absolute(path) {
		return nil, nil, fmt.Errorf("qualification directory invalid")
	}
	if e := qualificationContext(ctx); e != nil {
		return nil, nil, e
	}
	fd, e := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, nil, e
	}
	fail := func(e error) (*os.File, []string, error) { unix.Close(fd); return nil, nil, e }
	ids := []string{}
	parts := append([]string{""}, strings.Split(strings.TrimPrefix(path, "/"), "/")...)
	for i, part := range parts {
		if e := qualificationContext(ctx); e != nil {
			return fail(e)
		}
		if i > 0 {
			next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if e != nil {
				return fail(e)
			}
			unix.Close(fd)
			fd = next
		}
		s, e := qualificationStat(ctx, fd)
		if e != nil {
			return fail(e)
		}
		if s.Uid != 0 || s.Mode&0022 != 0 || s.Mode&unix.S_IFMT != unix.S_IFDIR {
			return fail(fmt.Errorf("qualification ancestor unsafe"))
		}
		ids = append(ids, qualificationIdentity(s))
	}
	return os.NewFile(uintptr(fd), path), ids, nil
}
func readQualificationLeaf(ctx context.Context, dir *os.File, name string) (qualificationFile, error) {
	return readQualificationLeafWithStat(ctx, dir, name, qualificationStat)
}

// Private syscall seam is never exposed through OpenDeploymentQualification.
func readQualificationLeafWithStat(ctx context.Context, dir *os.File, name string, stat func(context.Context, int) (unix.Stat_t, error)) (qualificationFile, error) {
	var out qualificationFile
	if e := qualificationContext(ctx); e != nil {
		return out, e
	}
	fd, e := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return out, e
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	s, e := stat(ctx, fd)
	if e != nil {
		return out, e
	}
	if s.Uid != 0 || s.Mode&0222 != 0 || s.Mode&unix.S_IFMT != unix.S_IFREG || s.Nlink != 1 || s.Size <= 0 || s.Size > MaxBytes {
		return out, fmt.Errorf("qualification leaf unsafe")
	}
	out.identity = qualificationIdentity(s)
	// Check cancellation between bounded chunks; no blocking FIFO/device reads.
	var b bytes.Buffer
	buf := make([]byte, 32768)
	for {
		if e := qualificationContext(ctx); e != nil {
			return out, e
		}
		n, e := f.Read(buf)
		b.Write(buf[:n])
		if b.Len() > MaxBytes {
			return out, fmt.Errorf("qualification leaf oversized")
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return out, e
		}
	}
	after, e := stat(ctx, fd)
	if e != nil || qualificationIdentity(after) != out.identity || int64(b.Len()) != s.Size {
		return out, fmt.Errorf("qualification leaf changed")
	}
	if e := qualificationContext(ctx); e != nil {
		return out, e
	}
	fresh, e := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return out, e
	}
	defer unix.Close(fresh)
	again, e := stat(ctx, fresh)
	if e != nil || qualificationIdentity(again) != out.identity {
		return out, fmt.Errorf("qualification leaf replaced")
	}
	out.data = b.Bytes()
	return out, nil
}
func readQualificationFiles(ctx context.Context, manifestPath string) (qualificationFiles, error) {
	var out qualificationFiles
	if !absolute(manifestPath) || filepath.Base(manifestPath) != "admission-manifest.json" {
		return out, fmt.Errorf("qualification manifest path invalid")
	}
	dir, ids, e := openQualificationDirectory(ctx, filepath.Dir(manifestPath))
	if e != nil {
		return out, e
	}
	defer dir.Close()
	out = qualificationFiles{path: manifestPath, directories: ids, leaves: map[string]qualificationFile{}}
	for _, name := range qualificationLeaves {
		if name == "observer-deployment.env" {
			continue
		}
		leaf, e := readQualificationLeaf(ctx, dir, name)
		if e != nil {
			return qualificationFiles{}, fmt.Errorf("qualification fixed leaf unreadable: %s", name)
		}
		out.leaves[name] = leaf
	}
	fresh, after, e := openQualificationDirectory(ctx, filepath.Dir(manifestPath))
	if e != nil {
		return qualificationFiles{}, e
	}
	defer fresh.Close()
	if !reflect.DeepEqual(ids, after) {
		return qualificationFiles{}, fmt.Errorf("qualification directory changed")
	}
	for _, name := range qualificationLeaves {
		if name == "observer-deployment.env" {
			continue
		}
		leaf, e := readQualificationLeaf(ctx, fresh, name)
		if e != nil || leaf.identity != out.leaves[name].identity || !bytes.Equal(leaf.data, out.leaves[name].data) {
			return qualificationFiles{}, fmt.Errorf("qualification set changed")
		}
	}
	envDir, envIDs, e := openQualificationDirectory(ctx, "/etc/switchback/created-layer-observer")
	if e != nil {
		return qualificationFiles{}, e
	}
	defer envDir.Close()
	env, e := readQualificationLeaf(ctx, envDir, "deployment.env")
	if e != nil {
		return qualificationFiles{}, e
	}
	envFresh, envAfter, e := openQualificationDirectory(ctx, "/etc/switchback/created-layer-observer")
	if e != nil {
		return qualificationFiles{}, e
	}
	defer envFresh.Close()
	envAgain, e := readQualificationLeaf(ctx, envFresh, "deployment.env")
	if e != nil || !reflect.DeepEqual(envIDs, envAfter) || env.identity != envAgain.identity || !bytes.Equal(env.data, envAgain.data) {
		return qualificationFiles{}, fmt.Errorf("observer installed config changed")
	}
	out.directories = append(out.directories, envIDs...)
	out.leaves["observer-deployment.env"] = env
	return out, qualificationContext(ctx)
}
func (f qualificationFiles) recheck(ctx context.Context) error {
	if e := qualificationContext(ctx); e != nil {
		return e
	}
	if f.path == "" || len(f.leaves) != len(qualificationLeaves) {
		return fmt.Errorf("qualification handle invalid")
	}
	current, e := readQualificationFiles(ctx, f.path)
	if e != nil {
		return e
	}
	if !reflect.DeepEqual(f, current) {
		return fmt.Errorf("qualification frozen files changed")
	}
	return nil
}
