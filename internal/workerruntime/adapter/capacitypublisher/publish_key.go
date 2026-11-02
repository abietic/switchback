package capacitypublisher

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	"path/filepath"
	"sort"
)

const publisherConfigPath = "/mnt/lima-sbquota/core/capacity-publisher-config"
const publisherKeyPath = "/mnt/lima-sbquota/core/capacity-publisher-keys"
const publisherSeedLeaf = "ed25519.seed"

// Finite file operations; no writes, arbitrary data-open, or pathname from JSON.
type publisherFiles interface {
	directory(string) (int, publisherMeta, error)
	leaf(int, string) (int, publisherMeta, error)
	stat(int) (publisherMeta, error)
	read(int, int) ([]byte, error)
	list(int) ([]string, error)
	close(int) error
}
type publisherMeta struct {
	device, inode  uint64
	mode, uid, gid uint32
	links          uint64
	size           int64
	stamp          string
}

func (m publisherMeta) directoryOK() bool { return m.mode == 0040700 && m.uid == 0 && m.gid == 0 }
func (m publisherMeta) leafOK(limit int) bool {
	return m.mode == 0100400 && m.uid == 0 && m.gid == 0 && m.links == 1 && m.size > 0 && m.size <= int64(limit)
}

type publisherFileSet struct {
	fs        publisherFiles
	path      string
	directory publisherMeta
	meta      map[string]publisherMeta
	data      map[string][]byte
}

func readPublisherSet(ctx context.Context, fs publisherFiles, path string, leaves []string, limit int) (out *publisherFileSet, err error) {
	if ctx == nil || fs == nil || !filepath.IsAbs(path) || filepath.Clean(path) != path || len(leaves) == 0 {
		return nil, errPublisherInvalid
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	dir, dm, e := fs.directory(path)
	if e != nil {
		return nil, errPublisherUnavailable
	}
	defer func() {
		if fs.close(dir) != nil && err == nil {
			if out != nil {
				out.clear()
			}
			out = nil
			err = errPublisherUnavailable
		}
	}()
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if !dm.directoryOK() {
		return nil, errPublisherInvalid
	}
	names, e := fs.list(dir)
	if e != nil || len(names) != len(leaves) {
		return nil, errPublisherInvalid
	}
	a, b := append([]string(nil), names...), append([]string(nil), leaves...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] || a[i] == "" || filepath.Base(a[i]) != a[i] {
			return nil, errPublisherInvalid
		}
	}
	out = &publisherFileSet{fs: fs, path: path, directory: dm, meta: map[string]publisherMeta{}, data: map[string][]byte{}}
	defer func() {
		if err != nil && out != nil {
			out.clear()
			out = nil
		}
		if e := ctx.Err(); e != nil {
			if out != nil {
				out.clear()
			}
			out = nil
			err = e
		}
	}()
	for _, name := range leaves {
		if e := ctx.Err(); e != nil {
			return out, e
		}
		fd, m, e := fs.leaf(dir, name)
		if e != nil {
			return out, errPublisherUnavailable
		}
		if !m.leafOK(limit) {
			_ = fs.close(fd)
			return out, errPublisherInvalid
		}
		raw, re := fs.read(fd, limit)
		after, se := fs.stat(fd)
		ce := fs.close(fd)
		if e := ctx.Err(); e != nil {
			clear(raw)
			return out, e
		}
		if re != nil || se != nil || ce != nil || !m.leafOK(limit) || m != after || int64(len(raw)) != m.size || len(raw) > limit {
			clear(raw)
			return out, errPublisherInvalid
		}
		check, cm, e := fs.leaf(dir, name)
		if e != nil {
			clear(raw)
			return out, errPublisherUnavailable
		}
		ce = fs.close(check)
		if ce != nil || m != cm {
			clear(raw)
			return out, errPublisherInvalid
		}
		out.meta[name] = m
		out.data[name] = raw
	}
	held, e := fs.stat(dir)
	if e != nil || held != dm {
		return out, errPublisherInvalid
	}
	check, canonical, e := fs.directory(path)
	if e != nil {
		return out, errPublisherUnavailable
	}
	ce := fs.close(check)
	if ce != nil || canonical != dm {
		return out, errPublisherInvalid
	}
	return out, nil
}
func (s *publisherFileSet) clear() {
	if s != nil {
		for _, b := range s.data {
			clear(b)
		}
	}
}
func (s *publisherFileSet) recheck(ctx context.Context) error {
	if s == nil {
		return errPublisherInvalid
	}
	names := make([]string, 0, len(s.data))
	for n := range s.data {
		names = append(names, n)
	}
	limit := capacitysnapshot.MaxBytes
	if len(names) == 1 && names[0] == publisherSeedLeaf {
		limit = ed25519.SeedSize
	}
	n, e := readPublisherSet(ctx, s.fs, s.path, names, limit)
	if e != nil {
		return e
	}
	defer n.clear()
	if s.directory != n.directory {
		return errPublisherInvalid
	}
	for k, b := range s.data {
		if s.meta[k] != n.meta[k] || !bytes.Equal(b, n.data[k]) {
			return errPublisherInvalid
		}
	}
	return nil
}

type publisherKey struct {
	files   *publisherFileSet
	private ed25519.PrivateKey
}

func openPublisherKey(ctx context.Context, fs publisherFiles, path string, trust capacitysnapshot.Trust) (*publisherKey, error) {
	set, e := readPublisherSet(ctx, fs, path, []string{publisherSeedLeaf}, ed25519.SeedSize)
	if e != nil {
		return nil, e
	}
	seed := set.data[publisherSeedLeaf]
	if len(seed) != ed25519.SeedSize {
		set.clear()
		return nil, errPublisherInvalid
	}
	private := ed25519.NewKeyFromSeed(seed)
	if !bytes.Equal(private.Public().(ed25519.PublicKey), trust.PublicKey) {
		clear(private)
		set.clear()
		return nil, errPublisherInvalid
	}
	return &publisherKey{set, private}, nil
}
func (k *publisherKey) recheck(ctx context.Context) error {
	if k == nil || len(k.private) != ed25519.PrivateKeySize {
		return errPublisherInvalid
	}
	return k.files.recheck(ctx)
}
func (k *publisherKey) clear() {
	if k != nil {
		clear(k.private)
		k.files.clear()
	}
}
