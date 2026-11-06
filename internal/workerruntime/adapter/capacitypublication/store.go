// Package capacitypublication persists burned sequences and publishes verified raw
// v1 bytes. It neither samples capacity nor holds keys or execution authority.
package capacitypublication

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
)

var (
	ErrPublicationInvalid          = errors.New("publication_invalid")
	ErrPublicationBusy             = errors.New("publication_busy")
	ErrPublicationStateUnavailable = errors.New("publication_state_unavailable")
	ErrPublicationIOFailed         = errors.New("publication_io_failed")
	ErrPublicationUncertain        = errors.New("publication_uncertain")
	ErrPublicationClosed           = errors.New("publication_closed")
)

type publicationFailure struct {
	reason       error
	contextCause error
	stage        string
}

func (e *publicationFailure) Error() string { return e.reason.Error() }
func (e *publicationFailure) Unwrap() []error {
	if e.contextCause == nil {
		return []error{e.reason}
	}
	return []error{e.reason, e.contextCause}
}
func failure(reason, cause error, stage string) error {
	return &publicationFailure{reason: reason, contextCause: cause, stage: stage}
}
func ioReason(e error) error {
	if errors.Is(e, ErrPublicationBusy) {
		return ErrPublicationBusy
	}
	if errors.Is(e, ErrPublicationInvalid) {
		return ErrPublicationInvalid
	}
	if errors.Is(e, ErrPublicationStateUnavailable) || errors.Is(e, os.ErrNotExist) {
		return ErrPublicationStateUnavailable
	}
	return ErrPublicationIOFailed
}

const (
	privatePath   = "/mnt/lima-sbquota/core/capacity-publication-private"
	publicPath    = "/mnt/lima-sbquota/core/capacity-publication-public"
	regularKind   = uint32(0100000)
	directoryKind = uint32(0040000)
	xfsType       = uint64(0x58465342)
)

type fileMeta struct {
	device, inode, mountID, fsType uint64
	deviceName                     string
	kind, mode, uid, gid           uint32
	nlink                          uint64
	size                           int64
}

func sameDirectory(a, b fileMeta) bool { // APFS also changes directory nlink for ordinary entries. Neither size nor
	// nlink identifies a directory across this Store's own file updates.
	a.size, b.size = 0, 0
	a.nlink, b.nlink = 0, 0
	return a == b
}
func fixedLeaf(n string) bool {
	return n == "writer.lock" || n == "sequence.bin" || n == "sequence.next" || n == "capacity.json" || n == "capacity.next"
}

// This finite seam models descriptor operations, not a general filesystem.
// Production construction is private and has no mutable hooks.
type fileSystem interface {
	openDirectory(string) (int, fileMeta, error)
	canonical(string, fileMeta) error
	list(int) ([]string, error)
	openLeaf(int, string) (int, fileMeta, error)
	entry(int, string) (fileMeta, error)
	create(int, string) (int, error)
	stat(int) (fileMeta, error)
	read(int, int) ([]byte, error)
	write(int, []byte) (int, error)
	chmod(int, uint32) error
	lock(int) error
	sync(int) error
	rename(int, string, string) error
	unlink(int, string) error
	close(int) error
}
type fileRecord struct {
	meta fileMeta
	raw  []byte
}

// Store serializes the durable journal and visibility protocol. A failed instance
// is fenced until Close; reopening reads actual durable state, never a retry hint.
type Store struct {
	mu                                sync.Mutex
	trust                             capacitysnapshot.Trust
	binding                           [32]byte
	fs                                fileSystem
	now                               func() time.Time
	private, public                   string
	privateFD, publicFD, lockFD       int
	privateMeta, publicMeta, lockMeta fileMeta
	journal                           fileRecord
	capacity                          *fileRecord
	possible                          *fileRecord // candidate at an uncertain rename boundary
	high, pending                     int64
	reservedAt                        time.Time
	configured, failed, closed        bool
	closeResult                       error
}

func Open(trust capacitysnapshot.Trust) (*Store, error) {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return nil, failure(ErrPublicationInvalid, nil, "unsupported_platform")
	}
	return openStore(trust, privatePath, publicPath, newNativeFiles(), time.Now)
}
func openStore(trust capacitysnapshot.Trust, private, public string, fs fileSystem, now func() time.Time) (s *Store, err error) {
	t, e := cloneTrust(trust)
	if e != nil {
		return nil, failure(ErrPublicationInvalid, nil, "trust")
	}
	if fs == nil || now == nil || !filepath.IsAbs(private) || !filepath.IsAbs(public) || filepath.Clean(private) != private || filepath.Clean(public) != public || private == public || strings.HasPrefix(private, public+"/") || strings.HasPrefix(public, private+"/") {
		return nil, failure(ErrPublicationInvalid, nil, "configuration")
	}
	s = &Store{trust: t, binding: trustBinding(t), fs: fs, now: now, private: private, public: public, privateFD: -1, publicFD: -1, lockFD: -1}
	defer func() {
		if err != nil {
			s.release()
			s = nil
		}
	}()
	s.privateFD, s.privateMeta, e = fs.openDirectory(private)
	if e != nil {
		return s, failure(ioReason(e), nil, "private_directory")
	}
	s.publicFD, s.publicMeta, e = fs.openDirectory(public)
	if e != nil {
		return s, failure(ioReason(e), nil, "public_directory")
	}
	if !s.validDirectory(s.privateMeta, 0700) || !s.validDirectory(s.publicMeta, 0750) || (s.privateMeta.device == s.publicMeta.device && s.privateMeta.inode == s.publicMeta.inode) {
		return s, failure(ErrPublicationStateUnavailable, nil, "directories")
	}
	if e = s.checkDirectories(); e != nil {
		return s, failure(ioReason(e), nil, "directories")
	}
	s.lockFD, s.lockMeta, e = fs.openLeaf(s.privateFD, "writer.lock")
	if e != nil {
		return s, failure(ioReason(e), nil, "lock_open")
	}
	if !s.validLeaf(s.lockMeta, 0600, 0, 0) {
		return s, failure(ErrPublicationStateUnavailable, nil, "lock_metadata")
	}
	if e = fs.lock(s.lockFD); e != nil {
		return s, failure(ioReason(e), nil, "lock")
	}
	if e = s.checkLock(); e != nil {
		return s, failure(ioReason(e), nil, "lock_entry")
	}
	s.journal, e = s.readRecord(s.privateFD, "sequence.bin", 0400, journalSize, journalSize)
	if e != nil {
		return s, failure(ioReason(e), nil, "journal_read")
	}
	s.high, e = decodeJournal(s.journal.raw, s.binding)
	if e != nil {
		return s, failure(ErrPublicationStateUnavailable, nil, "journal")
	}
	// Recognition precedes any cleanup. Unknown public bytes are never withdrawn.
	cap, e := s.readOptional(s.publicFD, "capacity.json", 0440, 1, capacitysnapshot.MaxBytes)
	if e != nil {
		return s, failure(ioReason(e), nil, "capacity_read")
	}
	if cap != nil {
		facts, e := s.validateEnvelope(cap.raw)
		if e != nil || facts.Sequence > s.high {
			return s, failure(ErrPublicationStateUnavailable, nil, "capacity_binding")
		}
		s.capacity = cap
	}
	if e = s.cleanNext(); e != nil {
		return s, failure(ioReason(e), nil, "stale_temporary")
	}
	if e = s.checkState(); e != nil {
		return s, failure(ioReason(e), nil, "open_recheck")
	}
	if s.capacity != nil {
		if e = s.withdraw(); e != nil {
			return s, failure(ErrPublicationUncertain, nil, "restart_withdraw")
		}
	}
	s.configured = true
	return s, nil
}
func (s *Store) validDirectory(m fileMeta, mode uint32) bool {
	return m.kind == directoryKind && m.mode == mode && m.uid == 0 && m.gid == 0 && m.fsType == xfsType && m.mountID == s.trust.Identity.MountID && m.deviceName == s.trust.Identity.Device && m.inode != 0
}
func (s *Store) validLeaf(m fileMeta, mode uint32, min, max int) bool {
	return m.kind == regularKind && m.mode == mode && m.uid == 0 && m.gid == 0 && m.nlink == 1 && m.size >= int64(min) && m.size <= int64(max) && m.fsType == xfsType && m.mountID == s.trust.Identity.MountID && m.deviceName == s.trust.Identity.Device && m.device == s.privateMeta.device && m.inode != 0
}
func (s *Store) checkDirectories() error {
	if e := s.fs.canonical(s.private, s.privateMeta); e != nil {
		return e
	}
	if e := s.fs.canonical(s.public, s.publicMeta); e != nil {
		return e
	}
	for _, d := range []struct {
		fd      int
		private bool
	}{{s.privateFD, true}, {s.publicFD, false}} {
		names, e := s.fs.list(d.fd)
		if e != nil {
			return e
		}
		if len(names) > 8 {
			return ErrPublicationStateUnavailable
		}
		for _, n := range names {
			if d.private {
				if n != "writer.lock" && n != "sequence.bin" && n != "sequence.next" {
					return ErrPublicationStateUnavailable
				}
			} else if n != "capacity.json" && n != "capacity.next" {
				return ErrPublicationStateUnavailable
			}
		}
	}
	return nil
}
func (s *Store) checkLock() error {
	m, e := s.fs.stat(s.lockFD)
	if e != nil {
		return e
	}
	if m != s.lockMeta || !s.validLeaf(m, 0600, 0, 0) {
		return ErrPublicationStateUnavailable
	}
	m, e = s.fs.entry(s.privateFD, "writer.lock")
	if e != nil {
		return e
	}
	if m != s.lockMeta {
		return ErrPublicationStateUnavailable
	}
	return nil
}
func (s *Store) readRecord(dir int, name string, mode uint32, min, max int) (fileRecord, error) {
	fd, m, e := s.fs.openLeaf(dir, name)
	if e != nil {
		return fileRecord{}, e
	}
	// Always close this numeric descriptor exactly once, including failure paths.
	if !s.validLeaf(m, mode, min, max) {
		_ = s.fs.close(fd)
		return fileRecord{}, ErrPublicationStateUnavailable
	}
	raw, e := s.fs.read(fd, max)
	after, se := s.fs.stat(fd)
	ce := s.fs.close(fd)
	if e != nil {
		return fileRecord{}, e
	}
	if se != nil {
		return fileRecord{}, se
	}
	if ce != nil {
		return fileRecord{}, ce
	}
	if m != after || len(raw) != int(m.size) {
		return fileRecord{}, ErrPublicationStateUnavailable
	}
	entry, e := s.fs.entry(dir, name)
	if e != nil {
		return fileRecord{}, e
	}
	if entry != m {
		return fileRecord{}, ErrPublicationStateUnavailable
	}
	return fileRecord{meta: m, raw: raw}, nil
}
func (s *Store) readOptional(dir int, name string, mode uint32, min, max int) (*fileRecord, error) {
	r, e := s.readRecord(dir, name, mode, min, max)
	if errors.Is(e, os.ErrNotExist) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	return &r, nil
}
func recordEqual(a, b *fileRecord) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.meta == b.meta && bytes.Equal(a.raw, b.raw)
}
func (s *Store) checkJournal() error {
	r, e := s.readRecord(s.privateFD, "sequence.bin", 0400, journalSize, journalSize)
	if e != nil {
		return e
	}
	if !recordEqual(&r, &s.journal) {
		return ErrPublicationStateUnavailable
	}
	return nil
}
func (s *Store) checkCapacity() error {
	r, e := s.readOptional(s.publicFD, "capacity.json", 0440, 1, capacitysnapshot.MaxBytes)
	if e != nil {
		return e
	}
	if !recordEqual(r, s.capacity) {
		return ErrPublicationStateUnavailable
	}
	return nil
}
func (s *Store) checkState() error {
	if e := s.checkDirectories(); e != nil {
		return e
	}
	if e := s.checkLock(); e != nil {
		return e
	}
	if e := s.checkJournal(); e != nil {
		return e
	}
	return s.checkCapacity()
}
func (s *Store) cleanNext() error {
	for _, d := range []struct {
		fd   int
		name string
		mode uint32
		max  int
	}{{s.privateFD, "sequence.next", 0400, journalSize}, {s.publicFD, "capacity.next", 0440, capacitysnapshot.MaxBytes}} {
		m, e := s.fs.entry(d.fd, d.name)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return e
		}
		if !s.validLeaf(m, 0600, 0, d.max) && !s.validLeaf(m, d.mode, 0, d.max) {
			return ErrPublicationStateUnavailable
		}
		// Recheck exact entry and canonical authority immediately before unlink.
		if e = s.checkDirectories(); e != nil {
			return e
		}
		if e = s.checkLock(); e != nil {
			return e
		}
		if e = s.checkJournal(); e != nil {
			return e
		}
		again, e := s.fs.entry(d.fd, d.name)
		if e != nil {
			return e
		}
		if again != m {
			return ErrPublicationStateUnavailable
		}
		if e = s.fs.unlink(d.fd, d.name); e != nil {
			return e
		}
		if e = s.fs.sync(d.fd); e != nil {
			return e
		}
		if e = s.checkDirectories(); e != nil {
			return e
		}
		if e = s.checkLock(); e != nil {
			return e
		}
	}
	return nil
}
func (s *Store) usable() error {
	if !s.configured {
		return ErrPublicationInvalid
	}
	if s.closed {
		return ErrPublicationClosed
	}
	if s.failed {
		return ErrPublicationStateUnavailable
	}
	return nil
}
func (s *Store) poison(e error, ctx context.Context, stage string, uncertain bool) error {
	s.failed = true
	var cause error
	if ctx != nil {
		cause = ctx.Err()
	}
	if uncertain {
		return failure(ErrPublicationUncertain, cause, stage)
	}
	if cause != nil {
		return failure(ErrPublicationIOFailed, cause, stage)
	}
	return failure(ioReason(e), nil, stage)
}
func (s *Store) writeTemporary(ctx context.Context, dir int, name string, mode uint32, raw []byte) (rec fileRecord, err error) {
	if e := ctx.Err(); e != nil {
		return rec, e
	}
	fd, e := s.fs.create(dir, name)
	if e != nil {
		return rec, e
	}
	live := true
	defer func() {
		if live {
			ce := s.fs.close(fd)
			if err == nil {
				err = ce
			}
		}
	}()
	initial, e := s.fs.stat(fd)
	if e != nil {
		return rec, e
	}
	if !s.validLeaf(initial, 0600, 0, 0) {
		return rec, ErrPublicationStateUnavailable
	}
	for written := 0; written < len(raw); {
		if e = ctx.Err(); e != nil {
			return rec, e
		}
		n, e := s.fs.write(fd, raw[written:])
		if e != nil {
			return rec, e
		}
		if n <= 0 || n > len(raw)-written {
			return rec, ErrPublicationIOFailed
		}
		written += n
	}
	if e = s.fs.chmod(fd, mode); e != nil {
		return rec, e
	}
	if e = s.fs.sync(fd); e != nil {
		return rec, e
	}
	m, e := s.fs.stat(fd)
	if e != nil {
		return rec, e
	}
	if !s.validLeaf(m, mode, len(raw), len(raw)) || m.device != initial.device || m.inode != initial.inode {
		return rec, ErrPublicationStateUnavailable
	}
	live = false
	if e = s.fs.close(fd); e != nil {
		return rec, e
	}
	entry, e := s.fs.entry(dir, name)
	if e != nil {
		return rec, e
	}
	if entry != m {
		return rec, ErrPublicationStateUnavailable
	}
	return fileRecord{meta: m, raw: bytes.Clone(raw)}, nil
}
func (s *Store) ReserveSequence(ctx context.Context) (int64, error) {
	if s == nil {
		return 0, ErrPublicationInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.usable(); e != nil {
		return 0, e
	}
	if ctx == nil {
		return 0, ErrPublicationInvalid
	}
	if s.pending != 0 {
		return 0, ErrPublicationBusy
	}
	if e := ctx.Err(); e != nil {
		return 0, s.poison(e, ctx, "reserve_context", false)
	}
	if s.high == math.MaxInt64 {
		return 0, s.poison(ErrPublicationStateUnavailable, ctx, "exhausted", false)
	}
	if e := s.checkState(); e != nil {
		return 0, s.poison(e, ctx, "reserve_state", false)
	}
	n := s.high + 1
	raw, _ := encodeJournal(s.binding, n)
	rec, e := s.writeTemporary(ctx, s.privateFD, "sequence.next", 0400, raw[:])
	if e != nil {
		return 0, s.poison(e, ctx, "journal_temporary", false)
	}
	if e = ctx.Err(); e != nil {
		return 0, s.poison(e, ctx, "journal_context", false)
	}
	if e = s.checkState(); e != nil {
		return 0, s.poison(e, ctx, "journal_recheck", false)
	}
	// The new entry must still be the exact inode whose contents were synced.
	if e = s.checkTemporary(s.privateFD, "sequence.next", rec); e != nil {
		return 0, s.poison(e, ctx, "journal_entry", false)
	}
	if e = s.fs.rename(s.privateFD, "sequence.next", "sequence.bin"); e != nil {
		return 0, s.poison(e, ctx, "journal_rename", false)
	}
	s.journal = rec
	s.high = n
	if e = s.fs.sync(s.privateFD); e != nil {
		return 0, s.poison(e, ctx, "journal_directory_sync", false)
	}
	if e = s.checkState(); e != nil {
		return 0, s.poison(e, ctx, "journal_durable_recheck", false)
	}
	if e = ctx.Err(); e != nil {
		return 0, s.poison(e, ctx, "reserve_return", false)
	}
	s.reservedAt = s.now()
	if e = ctx.Err(); e != nil {
		return 0, s.poison(e, ctx, "reserve_clock_return", false)
	}
	s.pending = n
	return n, nil
}
func (s *Store) checkTemporary(dir int, name string, r fileRecord) error {
	got, e := s.readRecord(dir, name, r.meta.mode, len(r.raw), len(r.raw))
	if e != nil {
		return e
	}
	if !recordEqual(&got, &r) {
		return ErrPublicationStateUnavailable
	}
	return nil
}
func (s *Store) validateEnvelope(raw []byte) (capacitysnapshot.Facts, error) {
	if len(raw) == 0 || len(raw) > capacitysnapshot.MaxBytes {
		return capacitysnapshot.Facts{}, ErrPublicationInvalid
	}
	v, e := capacitysnapshot.Verify(raw, s.trust)
	if e != nil {
		return capacitysnapshot.Facts{}, ErrPublicationInvalid
	}
	f := v.Facts()
	if capacitysnapshot.CheckFacts(f, s.trust) != nil || len(f.Containers) > 32 || len(f.Removals) != 0 {
		return capacitysnapshot.Facts{}, ErrPublicationInvalid
	}
	return f, nil
}
func (s *Store) fresh(f capacitysnapshot.Facts) bool {
	now := s.now()
	return !f.ObservedAt.Before(s.reservedAt) && now.Sub(f.ObservedAt) <= 30*time.Second && f.ObservedAt.Sub(now) <= 2*time.Second
}
func (s *Store) Commit(ctx context.Context, envelope []byte) error {
	if s == nil {
		return ErrPublicationInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.usable(); e != nil {
		return e
	}
	if ctx == nil {
		return s.poison(ErrPublicationInvalid, nil, "commit_context", false)
	}
	if e := ctx.Err(); e != nil {
		return s.poison(e, ctx, "commit_context", false)
	}
	if s.pending == 0 || len(envelope) == 0 || len(envelope) > capacitysnapshot.MaxBytes {
		return s.poison(ErrPublicationInvalid, ctx, "commit_input", false)
	}
	raw := bytes.Clone(envelope)
	f, e := s.validateEnvelope(raw)
	if e != nil || f.Sequence != s.pending || !s.fresh(f) {
		return s.poison(ErrPublicationInvalid, ctx, "commit_envelope", false)
	}
	if e = s.checkState(); e != nil {
		return s.poison(e, ctx, "commit_state", false)
	}
	rec, e := s.writeTemporary(ctx, s.publicFD, "capacity.next", 0440, raw)
	if e != nil {
		return s.poison(e, ctx, "capacity_temporary", false)
	}
	if e = ctx.Err(); e != nil {
		return s.poison(e, ctx, "capacity_context", false)
	}
	if !s.fresh(f) {
		return s.poison(ErrPublicationInvalid, ctx, "capacity_clock", false)
	}
	if e = s.checkState(); e != nil {
		return s.poison(e, ctx, "capacity_recheck", false)
	}
	if e = s.checkTemporary(s.publicFD, "capacity.next", rec); e != nil {
		return s.poison(e, ctx, "capacity_entry", false)
	}
	if e = ctx.Err(); e != nil {
		return s.poison(e, ctx, "capacity_rename_context", false)
	}
	if !s.fresh(f) {
		return s.poison(ErrPublicationInvalid, ctx, "capacity_rename_clock", false)
	}
	// Visibility may change as soon as rename is called, even if it returns an error.
	s.possible = &rec
	if e = s.fs.rename(s.publicFD, "capacity.next", "capacity.json"); e != nil {
		return s.poison(e, ctx, "capacity_rename", true)
	}
	s.capacity = &rec
	s.possible = nil
	if e = s.fs.sync(s.publicFD); e != nil {
		return s.poison(e, ctx, "capacity_directory_sync", true)
	}
	if e = s.checkState(); e != nil {
		return s.poison(e, ctx, "capacity_visible_recheck", true)
	}
	if e = ctx.Err(); e != nil {
		return s.poison(e, ctx, "capacity_return", true)
	}
	if !s.fresh(f) {
		return s.poison(ErrPublicationInvalid, ctx, "capacity_return_clock", true)
	}
	if e = ctx.Err(); e != nil {
		return s.poison(e, ctx, "capacity_final_context", true)
	}
	s.pending = 0
	return nil
}
func (s *Store) withdraw() error {
	if e := s.checkDirectories(); e != nil {
		return e
	}
	if e := s.checkLock(); e != nil {
		return e
	}
	if e := s.checkJournal(); e != nil {
		return e
	}
	actual, e := s.readOptional(s.publicFD, "capacity.json", 0440, 1, capacitysnapshot.MaxBytes)
	if e != nil {
		return e
	}
	if actual == nil {
		if s.capacity != nil {
			return ErrPublicationStateUnavailable
		}
		return nil
	}
	if !recordEqual(actual, s.capacity) && !(s.possible != nil && recordEqual(actual, s.possible)) {
		return ErrPublicationStateUnavailable
	}
	if e = s.fs.unlink(s.publicFD, "capacity.json"); e != nil {
		return e
	}
	if e = s.fs.sync(s.publicFD); e != nil {
		return e
	}
	if e = s.checkDirectories(); e != nil {
		return e
	}
	if e = s.checkLock(); e != nil {
		return e
	}
	if e = s.checkJournal(); e != nil {
		return e
	}
	actual, e = s.readOptional(s.publicFD, "capacity.json", 0440, 1, capacitysnapshot.MaxBytes)
	if e != nil {
		return e
	}
	if actual != nil {
		return ErrPublicationStateUnavailable
	}
	s.capacity = nil
	s.possible = nil
	return nil
}

// release closes directory descriptors before the lock. A reported close failure
// never causes a retry on a possibly recycled numeric descriptor.
func (s *Store) release() error {
	var first error
	for _, p := range []*int{&s.publicFD, &s.privateFD, &s.lockFD} {
		if *p >= 0 {
			fd := *p
			*p = -1
			if e := s.fs.close(fd); e != nil && first == nil {
				first = e
			}
		}
	}
	return first
}
func (s *Store) Close() error {
	if s == nil {
		return ErrPublicationInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeResult
	}
	if !s.configured {
		return ErrPublicationInvalid
	}
	s.closed = true
	var e error
	if e = s.withdraw(); e != nil {
		s.closeResult = failure(ErrPublicationUncertain, nil, "close_withdraw")
	} else if e = s.cleanNext(); e != nil {
		s.closeResult = failure(ioReason(e), nil, "close_temporary")
	}
	if e = s.release(); e != nil && s.closeResult == nil {
		s.closeResult = failure(ErrPublicationIOFailed, nil, "close_descriptors")
	}
	return s.closeResult
}

// AbandonSequence burns a reserved number without withdrawing the last sample.
func (s *Store) AbandonSequence(ctx context.Context, expectedSequence int64) error {
	if s == nil {
		return ErrPublicationInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(); err != nil {
		return err
	}
	if ctx == nil || expectedSequence <= 0 || s.pending == 0 || expectedSequence != s.pending {
		return ErrPublicationInvalid
	}
	if err := ctx.Err(); err != nil {
		return s.poison(err, ctx, "abandon_context", false)
	}
	if err := s.checkState(); err != nil {
		return s.poison(err, ctx, "abandon_state", false)
	}
	if err := ctx.Err(); err != nil {
		return s.poison(err, ctx, "abandon_return", false)
	}
	s.pending = 0
	s.reservedAt = time.Time{}
	return nil
}
