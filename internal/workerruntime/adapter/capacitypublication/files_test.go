//go:build linux || darwin

package capacitypublication

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// temporaryFiles is SYNTHETIC metadata over real temporary files. Test-owned
// directory roots replace the production ancestor trust anchor: this cannot prove
// Linux safe ancestors, root ownership, XFS, mount identity or power-loss durability.
// Only uid/gid/XFS/device-name/mount-id are synthesized. Real device/inode/type,
// mode/nlink/size, all bytes, writes, rename, fsync, and flock remain actual syscalls.
type temporaryFiles struct {
	nativeFiles
	mu            sync.Mutex
	names         map[int]string
	events        []string
	hook          func(string) error
	short         int
	zero          bool
	metadata      func(fileMeta) fileMeta
	opens, closes int
}

func (f *temporaryFiles) synthetic(m fileMeta) fileMeta {
	m.uid = 0
	m.gid = 0
	m.fsType = xfsType
	m.mountID = 73
	m.deviceName = "8:7"
	if f.metadata != nil {
		m = f.metadata(m)
	}
	return m
}
func (f *temporaryFiles) event(e string) error {
	f.mu.Lock()
	f.events = append(f.events, e)
	h := f.hook
	f.mu.Unlock()
	if h != nil {
		return h(e)
	}
	return nil
}
func (f *temporaryFiles) name(fd int) string { f.mu.Lock(); defer f.mu.Unlock(); return f.names[fd] }
func (f *temporaryFiles) owned(fd int, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.names == nil {
		f.names = map[int]string{}
	}
	if _, ok := f.names[fd]; ok {
		panic("duplicate live descriptor")
	}
	f.names[fd] = name
	f.opens++
}
func (f *temporaryFiles) openDirectory(p string) (int, fileMeta, error) {
	ev := "directory:" + filepath.Base(p)
	if e := f.event(ev + ".before"); e != nil {
		return -1, fileMeta{}, e
	}
	fd, e := unix.Open(p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return -1, fileMeta{}, e
	}
	f.owned(fd, filepath.Base(p))
	m, e := f.stat(fd)
	if e == nil {
		e = f.event(ev + ".after")
	}
	if e != nil {
		_ = f.close(fd)
		return -1, fileMeta{}, e
	}
	return fd, m, nil
}
func (f *temporaryFiles) canonical(p string, w fileMeta) error {
	ev := "canonical:" + filepath.Base(p)
	if e := f.event(ev + ".before"); e != nil {
		return e
	}
	fd, e := unix.Open(p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return e
	}
	m, e := f.nativeFiles.stat(fd)
	ce := unix.Close(fd)
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if !sameDirectory(f.synthetic(m), w) {
		return ErrPublicationStateUnavailable
	}
	return f.event(ev + ".after")
}
func (f *temporaryFiles) stat(fd int) (fileMeta, error) {
	ev := "stat:" + f.name(fd)
	if e := f.event(ev + ".before"); e != nil {
		return fileMeta{}, e
	}
	m, e := f.nativeFiles.stat(fd)
	if e != nil {
		return m, e
	}
	if e = f.event(ev + ".after"); e != nil {
		return fileMeta{}, e
	}
	return f.synthetic(m), nil
}
func (f *temporaryFiles) openLeaf(dir int, n string) (int, fileMeta, error) {
	ev := "open:" + n
	if e := f.event(ev + ".before"); e != nil {
		return -1, fileMeta{}, e
	}
	fd, m, e := f.nativeFiles.openLeaf(dir, n)
	if e != nil {
		return -1, fileMeta{}, e
	}
	f.owned(fd, n)
	if e = f.event(ev + ".after"); e != nil {
		_ = f.close(fd)
		return -1, fileMeta{}, e
	}
	return fd, f.synthetic(m), nil
}
func (f *temporaryFiles) entry(dir int, n string) (fileMeta, error) {
	ev := "entry:" + n
	if e := f.event(ev + ".before"); e != nil {
		return fileMeta{}, e
	}
	m, e := f.nativeFiles.entry(dir, n)
	if e != nil {
		return m, e
	}
	if e = f.event(ev + ".after"); e != nil {
		return fileMeta{}, e
	}
	return f.synthetic(m), nil
}
func (f *temporaryFiles) create(dir int, n string) (int, error) {
	ev := "create:" + n
	if e := f.event(ev + ".before"); e != nil {
		return -1, e
	}
	fd, e := f.nativeFiles.create(dir, n)
	if e != nil {
		return -1, e
	}
	f.owned(fd, n)
	if e = f.event(ev + ".after"); e != nil {
		_ = f.close(fd)
		return -1, e
	}
	return fd, nil
}
func (f *temporaryFiles) read(fd, limit int) ([]byte, error) {
	ev := "read:" + f.name(fd)
	if e := f.event(ev + ".before"); e != nil {
		return nil, e
	}
	b, e := f.nativeFiles.read(fd, limit)
	if e == nil {
		e = f.event(ev + ".after")
	}
	return b, e
}
func (f *temporaryFiles) write(fd int, b []byte) (int, error) {
	ev := "write:" + f.name(fd)
	if e := f.event(ev + ".before"); e != nil {
		return 0, e
	}
	if f.zero {
		return 0, nil
	}
	if f.short > 0 && len(b) > f.short {
		b = b[:f.short]
	}
	n, e := f.nativeFiles.write(fd, b)
	if e == nil {
		e = f.event(ev + ".after")
	}
	return n, e
}
func (f *temporaryFiles) chmod(fd int, m uint32) error {
	ev := "chmod:" + f.name(fd)
	if e := f.event(ev + ".before"); e != nil {
		return e
	}
	if e := f.nativeFiles.chmod(fd, m); e != nil {
		return e
	}
	return f.event(ev + ".after")
}
func (f *temporaryFiles) lock(fd int) error {
	ev := "lock:" + f.name(fd)
	if e := f.event(ev + ".before"); e != nil {
		return e
	}
	if e := f.nativeFiles.lock(fd); e != nil {
		return e
	}
	return f.event(ev + ".after")
}
func (f *temporaryFiles) sync(fd int) error {
	ev := "sync:" + f.name(fd)
	if e := f.event(ev + ".before"); e != nil {
		return e
	}
	if e := f.nativeFiles.sync(fd); e != nil {
		return e
	}
	return f.event(ev + ".after")
}
func (f *temporaryFiles) rename(dir int, from, to string) error {
	ev := "rename:" + to
	if e := f.event(ev + ".before"); e != nil {
		return e
	}
	if e := f.nativeFiles.rename(dir, from, to); e != nil {
		return e
	}
	return f.event(ev + ".after")
}
func (f *temporaryFiles) unlink(dir int, n string) error {
	ev := "unlink:" + n
	if e := f.event(ev + ".before"); e != nil {
		return e
	}
	if e := f.nativeFiles.unlink(dir, n); e != nil {
		return e
	}
	return f.event(ev + ".after")
}
func (f *temporaryFiles) list(dir int) ([]string, error) {
	ev := "list:" + f.name(dir)
	if e := f.event(ev + ".before"); e != nil {
		return nil, e
	}
	v, e := f.nativeFiles.list(dir)
	if e == nil {
		e = f.event(ev + ".after")
	}
	return v, e
}
func (f *temporaryFiles) close(fd int) error {
	ev := "close:" + f.name(fd)
	before := f.event(ev + ".before")
	f.mu.Lock()
	if _, ok := f.names[fd]; !ok {
		f.mu.Unlock()
		panic("descriptor closed twice")
	}
	delete(f.names, fd)
	f.closes++
	f.mu.Unlock()
	e := f.nativeFiles.close(fd)
	after := f.event(ev + ".after")
	if before != nil {
		return before
	}
	if e != nil {
		return e
	}
	return after
}
func (f *temporaryFiles) reset() { f.mu.Lock(); f.events = nil; f.hook = nil; f.mu.Unlock() }
func (f *temporaryFiles) assertReleased(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.names) != 0 || f.opens != f.closes {
		t.Fatalf("descriptor leak: live=%v opened=%d closed=%d", f.names, f.opens, f.closes)
	}
}
func injected() error { return errors.New("SYNTHETIC /secret/path private IO failure") }

func TestFilesLockContentionAndReacquisition(t *testing.T) {
	f := newFixture(t)
	s := f.open(t)
	before := directoryImage(t, f)
	other, e := openStore(f.trust, f.privateDirectory, f.publicDirectory, f.fs, s.now)
	if other != nil || !errors.Is(e, ErrPublicationBusy) {
		t.Fatalf("second open=%v %v", other, e)
	}
	if got := directoryImage(t, f); got != before {
		t.Fatal("busy open modified directory")
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s = f.open(t)
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	f.fs.assertReleased(t)
}
func directoryImage(t *testing.T, f *fixture) string {
	t.Helper()
	var image strings.Builder
	for _, d := range []string{f.privateDirectory, f.publicDirectory} {
		entries, e := os.ReadDir(d)
		if e != nil {
			t.Fatal(e)
		}
		for _, entry := range entries {
			b, e := os.ReadFile(filepath.Join(d, entry.Name()))
			if e != nil {
				t.Fatal(e)
			}
			fmt.Fprintf(&image, "%s:%x;", entry.Name(), b)
		}
	}
	return image.String()
}
func TestFilesEntryRejectionWithoutDeletion(t *testing.T) {
	for _, kind := range []string{"unknown_private", "unknown_public", "many_entries", "symlink_lock", "hardlink_lock", "lock_mode", "journal_mode", "journal_missing", "temporary_symlink", "temporary_hardlink", "temporary_mode", "temporary_oversize", "capacity_unknown", "capacity_ahead", "capacity_wrong_binding", "capacity_symlink", "capacity_hardlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			p := func(n string) string { return filepath.Join(f.privateDirectory, n) }
			q := func(n string) string { return filepath.Join(f.publicDirectory, n) }
			switch kind {
			case "unknown_private":
				mustWrite(t, p("unknown"), []byte("x"), 0600)
			case "unknown_public":
				mustWrite(t, q("unknown"), []byte("x"), 0600)
			case "many_entries":
				for i := 0; i < 9; i++ {
					mustWrite(t, q(fmt.Sprint(i)), []byte("x"), 0600)
				}
			case "symlink_lock":
				mustRemove(t, p("writer.lock"))
				if e := os.Symlink("sequence.bin", p("writer.lock")); e != nil {
					t.Fatal(e)
				}
			case "hardlink_lock":
				if e := os.Link(p("writer.lock"), p("alias")); e != nil {
					t.Fatal(e)
				}
			case "lock_mode":
				mustChmod(t, p("writer.lock"), 0640)
			case "journal_mode":
				mustChmod(t, p("sequence.bin"), 0600)
			case "journal_missing":
				mustRemove(t, p("sequence.bin"))
			case "temporary_symlink":
				if e := os.Symlink("sequence.bin", p("sequence.next")); e != nil {
					t.Fatal(e)
				}
			case "temporary_hardlink":
				if e := os.Link(p("sequence.bin"), p("sequence.next")); e != nil {
					t.Fatal(e)
				}
			case "temporary_mode":
				mustWrite(t, p("sequence.next"), nil, 0644)
			case "temporary_oversize":
				mustWrite(t, p("sequence.next"), make([]byte, 97), 0400)
			case "capacity_unknown":
				mustWrite(t, q("capacity.json"), []byte("unrecognized"), 0440)
			case "capacity_ahead":
				mustWrite(t, q("capacity.json"), f.signedFacts(t, 1, sampleTime), 0440)
			case "capacity_wrong_binding":
				facts := f.facts(1, sampleTime)
				facts.ManifestDigest = "sha256:" + strings.Repeat("f", 64)
				mustWrite(t, q("capacity.json"), f.signed(t, facts), 0440)
			case "capacity_symlink":
				if e := os.Symlink(p("sequence.bin"), q("capacity.json")); e != nil {
					t.Fatal(e)
				}
			case "capacity_hardlink":
				mustWrite(t, q("capacity.json"), []byte("x"), 0440)
				if e := os.Link(q("capacity.json"), q("capacity.next")); e != nil {
					t.Fatal(e)
				}
			case "fifo":
				mustRemove(t, p("writer.lock"))
				if e := unix.Mkfifo(p("writer.lock"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			before := entryImage(t, f)
			s, e := openStore(f.trust, f.privateDirectory, f.publicDirectory, f.fs, func() time.Time { return sampleTime })
			if e == nil || s != nil {
				t.Fatal("unsafe entry accepted")
			}
			if entryImage(t, f) != before {
				t.Fatal("unrecognized entry changed")
			}
			f.fs.assertReleased(t)
		})
	}
}

// lstat-only image also covers symlinks, FIFOs and missing files without opening them.
func entryImage(t *testing.T, f *fixture) string {
	t.Helper()
	var b strings.Builder
	for _, d := range []string{f.privateDirectory, f.publicDirectory} {
		es, e := os.ReadDir(d)
		if e != nil {
			t.Fatal(e)
		}
		for _, x := range es {
			i, e := os.Lstat(filepath.Join(d, x.Name()))
			if e != nil {
				t.Fatal(e)
			}
			fmt.Fprintf(&b, "%s:%v:%d;", x.Name(), i.Mode(), i.Size())
			if i.Mode().IsRegular() {
				raw, e := os.ReadFile(filepath.Join(d, x.Name()))
				if e != nil {
					t.Fatal(e)
				}
				fmt.Fprintf(&b, "%x;", raw)
			}
		}
	}
	return b.String()
}
func TestFilesMetadataDrift(t *testing.T) {
	for _, kind := range []string{"uid", "gid", "mount", "device", "fs", "type", "mode", "nlink"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.fs.metadata = func(m fileMeta) fileMeta {
				switch kind {
				case "uid":
					m.uid = 1
				case "gid":
					m.gid = 1
				case "mount":
					m.mountID++
				case "device":
					m.deviceName = "8:8"
				case "fs":
					m.fsType = 1
				case "type":
					m.kind = 0120000
				case "mode":
					m.mode |= 0022
				case "nlink":
					if m.kind == regularKind {
						m.nlink++
					}
				}
				return m
			}
			before := entryImage(t, f)
			s, e := openStore(f.trust, f.privateDirectory, f.publicDirectory, f.fs, time.Now)
			if s != nil || e == nil {
				t.Fatal("bad metadata accepted")
			}
			if entryImage(t, f) != before {
				t.Fatal("bad metadata changed files")
			}
			f.fs.assertReleased(t)
		})
	}
}
func TestFilesStaleCleanupAndRestartWithdrawal(t *testing.T) {
	f := newFixture(t)
	s := f.open(t)
	if _, e := s.ReserveSequence(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	mustWrite(t, filepath.Join(f.privateDirectory, "sequence.next"), []byte("partial"), 0600)
	mustWrite(t, filepath.Join(f.publicDirectory, "capacity.next"), []byte("partial"), 0440)
	raw := f.signedFacts(t, 1, sampleTime.Add(-time.Hour))
	mustWrite(t, filepath.Join(f.publicDirectory, "capacity.json"), raw, 0440)
	s = f.open(t)
	defer s.Close()
	for _, p := range []string{filepath.Join(f.privateDirectory, "sequence.next"), filepath.Join(f.publicDirectory, "capacity.next"), filepath.Join(f.publicDirectory, "capacity.json")} {
		if _, e := os.Lstat(p); !os.IsNotExist(e) {
			t.Fatalf("not withdrawn %v", e)
		}
	}
	n, e := s.ReserveSequence(context.Background())
	if n != 2 || e != nil {
		t.Fatalf("burned sequence %d %v", n, e)
	}
}
func TestFilesNativeLeafSafetyAndNoFollow(t *testing.T) {
	n := nativeFiles{}
	root := t.TempDir()
	fd, e := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer unix.Close(fd)
	mustWrite(t, filepath.Join(root, "sequence.bin"), []byte("x"), 0400)
	if e := os.Symlink("sequence.bin", filepath.Join(root, "writer.lock")); e != nil {
		t.Fatal(e)
	}
	if got, _, e := n.openLeaf(fd, "writer.lock"); e == nil {
		n.close(got)
		t.Fatal("symlink accepted")
	}
	mustRemove(t, filepath.Join(root, "writer.lock"))
	if e = unix.Mkfifo(filepath.Join(root, "writer.lock"), 0600); e != nil {
		t.Fatal(e)
	}
	if got, _, e := n.openLeaf(fd, "writer.lock"); e == nil {
		n.close(got)
		t.Fatal("FIFO accepted")
	}
	if _, _, e := n.openDirectory(root); e == nil {
		t.Fatal("unsafe test ancestors accepted")
	}
}
func TestFilesCloseDescriptorOrder(t *testing.T) {
	f := newFixture(t)
	s := f.open(t)
	f.fs.reset()
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	var closes []string
	for _, ev := range f.fs.events {
		if strings.HasPrefix(ev, "close:") && strings.HasSuffix(ev, ".after") {
			closes = append(closes, ev)
		}
	}
	if len(closes) < 3 || strings.Join(closes[len(closes)-3:], ",") != "close:public.after,close:private.after,close:writer.lock.after" {
		t.Fatalf("lock not released last: %v", closes)
	}
	f.fs.assertReleased(t)
}
func TestFilesUnsupportedProductionPlatform(t *testing.T) {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		f := newFixture(t)
		s, e := Open(f.trust)
		if s != nil || !errors.Is(e, ErrPublicationInvalid) {
			t.Fatalf("unsupported open=%v %v", s, e)
		}
	}
}

func TestFilesOpenFaultClosesAllDescriptors(t *testing.T) {
	f := newFixture(t)
	s := f.open(t)
	trace := append([]string(nil), f.fs.events...)
	s.Close()
	f.fs.assertReleased(t)
	for index, event := range trace {
		t.Run(fmt.Sprintf("%03d_%s", index, event), func(t *testing.T) {
			f := newFixture(t)
			seen := 0
			hit := false
			cause := injected()
			f.fs.hook = func(ev string) error {
				i := seen
				seen++
				if i == index {
					hit = true
					if ev != event {
						t.Fatalf("trace mismatch %s != %s", ev, event)
					}
					return cause
				}
				return nil
			}
			s, e := openStore(f.trust, f.privateDirectory, f.publicDirectory, f.fs, time.Now)
			f.fs.hook = nil
			if !hit || e == nil || s != nil {
				t.Fatalf("Open failure not fenced %v %v", s, e)
			}
			fixedError(t, e)
			if errors.Is(e, cause) {
				t.Fatal("nested IO cause exposed")
			}
			f.fs.assertReleased(t)
		})
	}
}
func TestFilesOpenWithdrawalFaultVisibility(t *testing.T) {
	for _, event := range []string{"unlink:capacity.json.before", "unlink:capacity.json.after", "sync:public.before", "sync:public.after"} {
		t.Run(event, func(t *testing.T) {
			f := newFixture(t)
			s := f.open(t)
			requireReserve(t, s, 1)
			s.Close()
			mustWrite(t, filepath.Join(f.publicDirectory, "capacity.json"), f.signedFacts(t, 1, sampleTime), 0440)
			hit := false
			f.fs.hook = func(ev string) error {
				if ev == event && !hit {
					hit = true
					return injected()
				}
				return nil
			}
			s, e := openStore(f.trust, f.privateDirectory, f.publicDirectory, f.fs, time.Now)
			f.fs.hook = nil
			if s != nil || !hit || !errors.Is(e, ErrPublicationUncertain) {
				t.Fatalf("restart visibility %v %v hit=%v", s, e, hit)
			}
			f.fs.assertReleased(t)
		})
	}
}
