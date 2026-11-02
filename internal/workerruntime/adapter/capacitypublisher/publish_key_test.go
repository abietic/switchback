package capacitypublisher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Portable temporary-file source. Metadata is explicitly synthetic and mutable;
// it exercises guard decisions without pretending the host grants root ownership.
type publisherTestFiles struct {
	files          map[int]*os.File
	paths          map[int]string
	next           int
	mutate         func(string, *publisherMeta)
	fail           string
	opened, closed int
}

func newPublisherTestFiles(t *testing.T) *publisherTestFiles {
	t.Helper()
	f := &publisherTestFiles{files: map[int]*os.File{}, paths: map[int]string{}}
	t.Cleanup(func() {
		if len(f.files) != 0 {
			t.Errorf("leaked fixture descriptors %d", len(f.files))
			for _, v := range f.files {
				v.Close()
			}
		}
	})
	return f
}
func (f *publisherTestFiles) meta(p string) (publisherMeta, error) {
	i, e := os.Lstat(p)
	if e != nil {
		return publisherMeta{}, e
	}
	m := publisherMeta{device: 1, inode: 1, mode: 0100000 | uint32(i.Mode().Perm()), links: 1, size: i.Size(), stamp: i.ModTime().String()}
	if i.IsDir() {
		m.mode = 0040700
		m.size = 0
		m.stamp = "synthetic-directory"
	}
	for _, b := range []byte(p) {
		m.inode = m.inode*31 + uint64(b)
	}
	if i.Mode()&os.ModeSymlink != 0 {
		m.mode = 0120000
	}
	if i.Mode()&os.ModeNamedPipe != 0 {
		m.mode = 0010000
	}
	if f.mutate != nil {
		f.mutate(p, &m)
	}
	return m, nil
}
func (f *publisherTestFiles) open(p string, dir bool) (int, publisherMeta, error) {
	m, e := f.meta(p)
	if e != nil {
		return 0, m, e
	}
	if !dir && m.mode&0170000 != 0100000 {
		return 0, m, errPublisherInvalid
	}
	v, e := os.Open(p)
	if e != nil {
		return 0, m, e
	}
	f.next++
	f.files[f.next] = v
	f.paths[f.next] = p
	f.opened++
	return f.next, m, nil
}
func (f *publisherTestFiles) directory(p string) (int, publisherMeta, error) { return f.open(p, true) }
func (f *publisherTestFiles) leaf(d int, n string) (int, publisherMeta, error) {
	return f.open(filepath.Join(f.paths[d], n), false)
}
func (f *publisherTestFiles) stat(d int) (publisherMeta, error) { return f.meta(f.paths[d]) }
func (f *publisherTestFiles) read(d, n int) ([]byte, error) {
	if f.fail == "read" {
		return nil, errors.New("synthetic-secret-path")
	}
	return io.ReadAll(io.LimitReader(f.files[d], int64(n+1)))
}
func (f *publisherTestFiles) list(d int) ([]string, error) { return f.files[d].Readdirnames(20) }
func (f *publisherTestFiles) close(d int) error {
	v, ok := f.files[d]
	if !ok {
		return errPublisherInvalid
	}
	delete(f.files, d)
	delete(f.paths, d)
	f.closed++
	return v.Close()
}

func TestPublisherKeyLengthsAndIdentity(t *testing.T) {
	for _, n := range []int{0, 31, 32, 33, 64} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			c, _, _ := publisherFixture(t, false)
			fs := newPublisherTestFiles(t)
			dir := t.TempDir()
			raw := bytes.Repeat([]byte{0x11}, n)
			if e := os.WriteFile(filepath.Join(dir, publisherSeedLeaf), raw, 0400); e != nil {
				t.Fatal(e)
			}
			key, e := openPublisherKey(context.Background(), fs, dir, c.trust)
			if (e == nil) != (n == 32) {
				t.Fatalf("length %d accepted=%v", n, e == nil)
			}
			if key != nil {
				key.clear()
			}
			if fs.opened != fs.closed {
				t.Fatal("FD leak", fs.opened, fs.closed)
			}
		})
	}
	for _, kind := range []string{"wrong_public", "zero_seed", "null_bytes", "symlink", "fifo", "hardlink", "owner", "group", "mode", "directory_mode", "replace", "bytes", "read_failure", "canonical"} {
		t.Run(kind, func(t *testing.T) {
			c, _, _ := publisherFixture(t, false)
			fs := newPublisherTestFiles(t)
			dir := t.TempDir()
			p := filepath.Join(dir, publisherSeedLeaf)
			raw := bytes.Repeat([]byte{0x11}, 32)
			if e := os.WriteFile(p, raw, 0400); e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "wrong_public":
				c.trust.PublicKey = make([]byte, 32)
			case "zero_seed":
				os.Remove(p)
				os.WriteFile(p, make([]byte, 32), 0400)
			case "null_bytes":
				os.Remove(p)
				os.WriteFile(p, []byte("null"), 0400)
			case "symlink":
				os.Remove(p)
				os.Symlink("missing", p)
			case "fifo":
				os.Remove(p)
				if e := unix.Mkfifo(p, 0400); e != nil {
					t.Fatal(e)
				}
			case "hardlink":
				fs.mutate = func(p string, m *publisherMeta) {
					if filepath.Base(p) == publisherSeedLeaf {
						m.links = 2
					}
				}
			case "owner":
				fs.mutate = func(p string, m *publisherMeta) {
					if filepath.Base(p) == publisherSeedLeaf {
						m.uid = 501
					}
				}
			case "group":
				fs.mutate = func(p string, m *publisherMeta) {
					if filepath.Base(p) == publisherSeedLeaf {
						m.gid = 501
					}
				}
			case "mode":
				os.Chmod(p, 0600)
			case "directory_mode":
				fs.mutate = func(p string, m *publisherMeta) {
					if p == dir {
						m.mode = 0040755
					}
				}
			case "read_failure":
				fs.fail = "read"
			case "canonical":
				calls := 0
				fs.mutate = func(p string, m *publisherMeta) {
					if p == dir {
						calls++
						if calls > 2 {
							m.inode++
						}
					}
				}
			}
			if kind == "replace" || kind == "bytes" {
				k, e := openPublisherKey(context.Background(), fs, dir, c.trust)
				if e != nil {
					t.Fatal(e)
				}
				defer k.clear()
				if kind == "replace" {
					fs.mutate = func(p string, m *publisherMeta) {
						if filepath.Base(p) == publisherSeedLeaf {
							m.inode++
						}
					}
				} else {
					os.Remove(p)
					os.WriteFile(p, bytes.Repeat([]byte{0x12}, 32), 0400)
				}
				if e = k.recheck(context.Background()); e == nil {
					t.Fatal("rotation accepted")
				}
				return
			}
			k, e := openPublisherKey(context.Background(), fs, dir, c.trust)
			if k != nil {
				k.clear()
			}
			if e == nil {
				t.Fatal("unsafe key accepted")
			}
			if strings.Contains(e.Error(), "synthetic-secret-path") || strings.Contains(e.Error(), dir) {
				t.Fatal("secret/path leaked")
			}
			if fs.opened != fs.closed {
				t.Fatal("FD leak")
			}
		})
	}
}
func TestPublisherKeyClearsOwnedBuffers(t *testing.T) {
	_, k, _ := publisherFixture(t, false)
	seed := k.files.data[publisherSeedLeaf]
	private := k.private
	k.clear()
	if !bytes.Equal(seed, make([]byte, 32)) || !bytes.Equal(private, make([]byte, 64)) {
		t.Fatal("owned buffers retained")
	}
}
func TestPublisherKeyContextAndNil(t *testing.T) {
	c, _, _ := publisherFixture(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := openPublisherKey(ctx, newPublisherTestFiles(t), t.TempDir(), c.trust); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if _, e := openPublisherKey(nil, newPublisherTestFiles(t), t.TempDir(), c.trust); e == nil {
		t.Fatal("nil context")
	}
	if e := (*publisherKey)(nil).recheck(context.Background()); e == nil {
		t.Fatal("nil key")
	}
}

type publisherFaultFiles struct {
	publisherFiles
	counts    map[string]int
	op        string
	ordinal   int
	afterRead func()
}

func (f *publisherFaultFiles) failure(op string) bool {
	f.counts[op]++
	return f.op == op && f.counts[op] == f.ordinal
}
func (f *publisherFaultFiles) directory(p string) (int, publisherMeta, error) {
	if f.failure("directory") {
		return -1, publisherMeta{}, errPublisherUnavailable
	}
	return f.publisherFiles.directory(p)
}
func (f *publisherFaultFiles) leaf(d int, n string) (int, publisherMeta, error) {
	if f.failure("leaf") {
		return -1, publisherMeta{}, errPublisherUnavailable
	}
	return f.publisherFiles.leaf(d, n)
}
func (f *publisherFaultFiles) stat(d int) (publisherMeta, error) {
	if f.failure("stat") {
		return publisherMeta{}, errPublisherUnavailable
	}
	return f.publisherFiles.stat(d)
}
func (f *publisherFaultFiles) list(d int) ([]string, error) {
	if f.failure("list") {
		return nil, errPublisherUnavailable
	}
	return f.publisherFiles.list(d)
}
func (f *publisherFaultFiles) read(d, n int) ([]byte, error) {
	if f.failure("read") {
		return nil, errPublisherUnavailable
	}
	b, e := f.publisherFiles.read(d, n)
	if f.afterRead != nil {
		f.afterRead()
	}
	return b, e
}
func (f *publisherFaultFiles) close(d int) error {
	e := f.publisherFiles.close(d)
	if f.failure("close") {
		return errPublisherUnavailable
	}
	return e
}
func TestPublisherKeyFiniteFaultsAndLateCancellation(t *testing.T) {
	c, k, _ := publisherFixture(t, false)
	// Count only this finite guarded read, then reject every operation in turn.
	base := &publisherFaultFiles{publisherFiles: k.files.fs, counts: map[string]int{}}
	v, e := openPublisherKey(context.Background(), base, k.files.path, c.trust)
	if e != nil {
		t.Fatal(e)
	}
	v.clear()
	for op, total := range base.counts {
		for ordinal := 1; ordinal <= total; ordinal++ {
			t.Run(fmt.Sprintf("%s_%d", op, ordinal), func(t *testing.T) {
				f := &publisherFaultFiles{publisherFiles: k.files.fs, counts: map[string]int{}, op: op, ordinal: ordinal}
				v, e := openPublisherKey(context.Background(), f, k.files.path, c.trust)
				if v != nil {
					v.clear()
				}
				if e == nil {
					t.Fatal("finite file failure ignored")
				}
			})
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &publisherFaultFiles{publisherFiles: k.files.fs, counts: map[string]int{}, afterRead: cancel}
	if v, e := openPublisherKey(ctx, f, k.files.path, c.trust); v != nil || !errors.Is(e, context.Canceled) {
		t.Fatal("late key read accepted", e)
	}
	// A lying/changed stat length cannot hide byte 33 from the bounded read.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, publisherSeedLeaf), bytes.Repeat([]byte{0x11}, 33), 0400)
	fs := newPublisherTestFiles(t)
	fs.mutate = func(p string, m *publisherMeta) {
		if filepath.Base(p) == publisherSeedLeaf {
			m.size = 32
		}
	}
	if v, e := openPublisherKey(context.Background(), fs, dir, c.trust); v != nil || e == nil {
		t.Fatal("overflow byte accepted", e)
	}
}
