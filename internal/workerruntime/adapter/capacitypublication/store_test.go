//go:build linux || darwin

package capacitypublication

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixture struct {
	privateDirectory, publicDirectory string
	trust                             capacitysnapshot.Trust
	signKey                           ed25519.PrivateKey
	fs                                *temporaryFiles
}

// SYNTHETIC only: this deterministic private seed never enters production Store.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{privateDirectory: filepath.Join(root, "private"), publicDirectory: filepath.Join(root, "public"), fs: &temporaryFiles{}}
	f.signKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x11}, 32))
	g := config.M1AccountedCapacityGuarantee()
	uuid := "01020304-0506-0708-090a-0b0c0d0e0f10"
	f.trust = capacitysnapshot.Trust{KeyID: "publication-synthetic", PublicKey: bytes.Clone(f.signKey.Public().(ed25519.PublicKey)), ManifestDigest: "sha256:" + strings.Repeat("e", 64), Identity: capacitysnapshot.Identity{EngineID: uuid, DockerRoot: "/mnt/lima-sbquota/docker", FSUUID: uuid, GuestBootID: uuid, MountNamespace: "17", MountID: 73, Device: "8:7"}, AnchorPath: "/mnt/lima-sbquota/anchor", Core: []capacitysnapshot.CoreDomain{{ID: "core", CgroupPath: "/sys/fs/cgroup/switchback.slice/switchback-core.slice", CPUQuotaMicros: 120000, CPUPeriodMicros: 100000, MemoryMax: 3 << 30, PIDsMax: 1024}}, AdmissionPolicy: &workerapp.CapacityGuaranteeRef{ID: g.ID, Revision: g.Revision, Digest: g.Digest}}
	for n, id := range []string{"core", "product"} {
		f.trust.Storage = append(f.trust.Storage, capacitysnapshot.StorageDomain{ID: id, Path: "/mnt/lima-sbquota/" + id, FSUUID: uuid, Device: "8:7", MountID: 73, FSType: "xfs", XFlags: 0x200, ProjectID: uint64(100 + n), HardBytes: 10 << 30, Quota: capacitysnapshot.StorageQuota{Format: "linux_xfs_quotactl_v1", StateVersion: 1, StateFlags: 0x30, Version: 1, Flags: 2, ProjectID: uint64(100 + n), HardBlocks512: 20971520}})
	}
	for _, role := range []string{"core", "images", "logs", "build"} {
		f.trust.Coverage = append(f.trust.Coverage, capacitysnapshot.StorageCoverage{Role: role, DomainID: "core"})
	}
	for _, role := range []string{"product", "working_copy", "cache"} {
		f.trust.Coverage = append(f.trust.Coverage, capacitysnapshot.StorageCoverage{Role: role, DomainID: "product"})
	}
	for _, d := range []struct {
		p string
		m os.FileMode
	}{{f.privateDirectory, 0700}, {f.publicDirectory, 0750}} {
		if e := os.Mkdir(d.p, d.m); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.WriteFile(filepath.Join(f.privateDirectory, "writer.lock"), nil, 0600); e != nil {
		t.Fatal(e)
	}
	j, _ := encodeJournal(trustBinding(f.trust), 0)
	if e := os.WriteFile(filepath.Join(f.privateDirectory, "sequence.bin"), j[:], 0400); e != nil {
		t.Fatal(e)
	}
	return f
}
func (f *fixture) open(t *testing.T) *Store {
	t.Helper()
	s, e := openStore(f.trust, f.privateDirectory, f.publicDirectory, f.fs, time.Now)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func (f *fixture) facts(n int64, at time.Time) capacitysnapshot.Facts {
	return capacitysnapshot.Facts{Sequence: n, ObservedAt: at, Identity: f.trust.Identity, ManifestDigest: f.trust.ManifestDigest, EngineCPUMilli: 8000, EngineMemoryBytes: 32 << 30, Anchor: capacitysnapshot.Anchor{Path: f.trust.AnchorPath, FSUUID: f.trust.Identity.FSUUID, Device: "8:7", MountID: 73, FSType: "xfs", MountOptions: []string{"rw", "prjquota"}, AvailableBytes: 16 << 30}, Core: append([]capacitysnapshot.CoreDomain(nil), f.trust.Core...), Storage: append([]capacitysnapshot.StorageDomain(nil), f.trust.Storage...), Containers: []capacitysnapshot.Container{}, Removals: []capacitysnapshot.Removal{}}
}
func (f *fixture) signed(t *testing.T, facts capacitysnapshot.Facts) []byte {
	t.Helper()
	p, e := json.Marshal(facts)
	if e != nil {
		t.Fatal(e)
	}
	sig := ed25519.Sign(f.signKey, append([]byte(capacitysnapshot.SignatureDomain), p...))
	raw, e := json.Marshal(capacitysnapshot.Envelope{Version: capacitysnapshot.Version, KeyID: f.trust.KeyID, Payload: p, Signature: sig})
	if e != nil {
		t.Fatal(e)
	}
	return raw
}
func (f *fixture) signedFacts(t *testing.T, n int64, at time.Time) []byte {
	return f.signed(t, f.facts(n, at))
}
func TestStoreExecutableRestart(t *testing.T) {
	f := newFixture(t)
	s := f.open(t)
	n, e := s.ReserveSequence(context.Background())
	if e != nil || n != 1 {
		t.Fatalf("reserve=%d err=%#v", n, e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s = f.open(t)
	defer s.Close()
	n, e = s.ReserveSequence(context.Background())
	if e != nil || n != 2 {
		t.Fatalf("restart=%d err=%v", n, e)
	}
}
func TestStoreExecutableCommit(t *testing.T) {
	f := newFixture(t)
	s := f.open(t)
	defer s.Close()
	n, e := s.ReserveSequence(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	raw := f.signedFacts(t, n, time.Now())
	if e = s.Commit(context.Background(), raw); e != nil {
		t.Fatal(e)
	}
	reader, e := capacitysnapshot.NewReader(f.publicDirectory)
	if e != nil {
		t.Fatal(e)
	}
	got, e := reader.Read()
	if e != nil || !bytes.Equal(raw, got) {
		t.Fatalf("read mismatch: %v", e)
	}
	v, e := capacitysnapshot.Verify(got, f.trust)
	if e != nil {
		t.Fatal(e)
	}
	if e = capacitysnapshot.CheckFacts(v.Facts(), f.trust); e != nil {
		t.Fatal(e)
	}
	if v.Facts().Sequence != 1 {
		t.Fatal("wrong sequence")
	}
}

var sampleTime = time.Date(2026, 10, 20, 1, 2, 3, 0, time.UTC)

func mustWrite(t *testing.T, p string, b []byte, mode os.FileMode) {
	t.Helper()
	if e := os.WriteFile(p, b, mode); e != nil {
		t.Fatal(e)
	}
	mustChmod(t, p, mode)
}
func mustChmod(t *testing.T, p string, mode os.FileMode) {
	t.Helper()
	if e := os.Chmod(p, mode); e != nil {
		t.Fatal(e)
	}
}
func mustRemove(t *testing.T, p string) {
	t.Helper()
	if e := os.Remove(p); e != nil {
		t.Fatal(e)
	}
}
func clockStore(t *testing.T, f *fixture, now *time.Time) *Store {
	t.Helper()
	s, e := openStore(f.trust, f.privateDirectory, f.publicDirectory, f.fs, func() time.Time { return *now })
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func requireReserve(t *testing.T, s *Store, want int64) {
	t.Helper()
	n, e := s.ReserveSequence(context.Background())
	if n != want || e != nil {
		t.Fatalf("reserve=%d want=%d err=%v", n, want, e)
	}
}
func requireFenced(t *testing.T, s *Store) {
	t.Helper()
	if n, e := s.ReserveSequence(context.Background()); n != 0 || e == nil {
		t.Fatalf("failed Store reused: %d %v", n, e)
	}
	if e := s.Commit(context.Background(), nil); e == nil {
		t.Fatal("failed Store committed")
	}
}
func fixedError(t *testing.T, e error) {
	t.Helper()
	if e == nil {
		t.Fatal("nil error")
	}
	for _, s := range []error{ErrPublicationInvalid, ErrPublicationBusy, ErrPublicationStateUnavailable, ErrPublicationIOFailed, ErrPublicationUncertain, ErrPublicationClosed} {
		if e.Error() == s.Error() {
			return
		}
	}
	t.Fatalf("unbounded error: %v", e)
}
func TestStoreBurnedGapAndBusy(t *testing.T) {
	f := newFixture(t)
	s := f.open(t)
	requireReserve(t, s, 1)
	f.fs.reset()
	if n, e := s.ReserveSequence(context.Background()); n != 0 || !errors.Is(e, ErrPublicationBusy) {
		t.Fatalf("busy=%d %v", n, e)
	}
	if len(f.fs.events) != 0 {
		t.Fatal("busy performed IO")
	}
	if e := s.Commit(context.Background(), f.signedFacts(t, 1, time.Now())); e != nil {
		t.Fatal(e)
	}
	requireReserve(t, s, 2)
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	s = f.open(t)
	defer s.Close()
	requireReserve(t, s, 3)
}
func TestStoreNilZeroClosedAndSentinels(t *testing.T) {
	var nilStore *Store
	for _, s := range []*Store{nilStore, {}} {
		if n, e := s.ReserveSequence(context.Background()); n != 0 || !errors.Is(e, ErrPublicationInvalid) {
			t.Fatalf("zero reserve %d %v", n, e)
		}
		if e := s.Commit(context.Background(), nil); !errors.Is(e, ErrPublicationInvalid) {
			t.Fatal(e)
		}
		if e := s.Close(); !errors.Is(e, ErrPublicationInvalid) {
			t.Fatal(e)
		}
	}
	f := newFixture(t)
	s := f.open(t)
	if n, e := s.ReserveSequence(nil); n != 0 || !errors.Is(e, ErrPublicationInvalid) {
		t.Fatal(e)
	}
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	if n, e := s.ReserveSequence(context.Background()); n != 0 || !errors.Is(e, ErrPublicationClosed) {
		t.Fatal(e)
	}
	if e := s.Commit(context.Background(), nil); !errors.Is(e, ErrPublicationClosed) {
		t.Fatal(e)
	}
	for _, e := range []error{ErrPublicationInvalid, ErrPublicationBusy, ErrPublicationStateUnavailable, ErrPublicationIOFailed, ErrPublicationUncertain, ErrPublicationClosed} {
		fixedError(t, e)
	}
	f.fs.assertReleased(t)
}
func TestStoreExhaustedAndJournalNeverCreated(t *testing.T) {
	f := newFixture(t)
	b, _ := encodeJournal(trustBinding(f.trust), math.MaxInt64)
	mustRemove(t, filepath.Join(f.privateDirectory, "sequence.bin"))
	mustWrite(t, filepath.Join(f.privateDirectory, "sequence.bin"), b[:], 0400)
	s := f.open(t)
	if n, e := s.ReserveSequence(context.Background()); n != 0 || !errors.Is(e, ErrPublicationStateUnavailable) {
		t.Fatalf("overflow %d %v", n, e)
	}
	requireFenced(t, s)
	s.Close()
	mustRemove(t, filepath.Join(f.privateDirectory, "sequence.bin"))
	s, e := openStore(f.trust, f.privateDirectory, f.publicDirectory, f.fs, time.Now)
	if s != nil || !errors.Is(e, ErrPublicationStateUnavailable) {
		t.Fatalf("missing state %v %v", s, e)
	}
	if _, e = os.Stat(filepath.Join(f.privateDirectory, "sequence.bin")); !os.IsNotExist(e) {
		t.Fatal("recreated state")
	}
	f.fs.assertReleased(t)
}
func TestStoreTrustDetachedAndPolicyValidation(t *testing.T) {
	for _, field := range []string{"key", "core", "storage", "coverage", "policy"} {
		t.Run(field, func(t *testing.T) {
			f := newFixture(t)
			original := f.trust
			now := sampleTime
			s := clockStore(t, f, &now)
			defer s.Close()
			switch field {
			case "key":
				f.trust.PublicKey[0] ^= 1
			case "core":
				f.trust.Core[0].MemoryMax++
			case "storage":
				f.trust.Storage[0].HardBytes++
			case "coverage":
				f.trust.Coverage[0].DomainID = "bad"
			case "policy":
				f.trust.AdmissionPolicy.Digest = "bad"
			}
			if s.binding != trustBinding(s.trust) || s.binding == trustBinding(f.trust) {
				t.Fatal("retained Trust alias")
			}
			requireReserve(t, s, 1)
			facts := f.facts(1, now)
			facts.Core = s.trust.Core
			facts.Storage = s.trust.Storage
			f.trust.KeyID = original.KeyID
			if e := s.Commit(context.Background(), f.signed(t, facts)); e != nil {
				t.Fatalf("detached commit %v", e)
			}
		})
	}
	for _, kind := range []string{"nil_policy", "wrong_policy", "wrong_digest", "bad_trust", "core_33", "storage_33"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			switch kind {
			case "nil_policy":
				f.trust.AdmissionPolicy = nil
			case "wrong_policy":
				f.trust.AdmissionPolicy.ID = "legacy"
			case "wrong_digest":
				f.trust.AdmissionPolicy.Digest = "sha256:" + strings.Repeat("0", 64)
			case "bad_trust":
				f.trust.Identity.MountID = 0
			case "core_33":
				f.trust.Core = make([]capacitysnapshot.CoreDomain, 33)
			case "storage_33":
				f.trust.Storage = make([]capacitysnapshot.StorageDomain, 33)
			}
			s, e := openStore(f.trust, f.privateDirectory, f.publicDirectory, f.fs, time.Now)
			if s != nil || !errors.Is(e, ErrPublicationInvalid) || len(f.fs.events) != 0 {
				t.Fatalf("invalid trust touched IO: %v %v", s, e)
			}
		})
	}
}
func TestStoreEnvelopeRejection(t *testing.T) {
	for _, kind := range []string{"nil", "oversize", "sequence", "key", "signature", "domain", "v2", "identity", "manifest", "facts", "containers_33", "removals", "null_containers", "null_removals", "null_core", "null_storage", "nil_context", "no_pending"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			now := sampleTime
			s := clockStore(t, f, &now)
			defer s.Close()
			if kind != "no_pending" {
				requireReserve(t, s, 1)
			}
			facts := f.facts(1, now)
			switch kind {
			case "sequence":
				facts.Sequence = 2
			case "identity":
				facts.Identity.Device = "8:8"
			case "manifest":
				facts.ManifestDigest = "sha256:" + strings.Repeat("f", 64)
			case "facts":
				facts.Anchor.FSType = "ext4"
			case "containers_33":
				facts.Containers = make([]capacitysnapshot.Container, 33)
			case "removals":
				facts.Removals = []capacitysnapshot.Removal{{}}
			case "null_containers":
				facts.Containers = nil
			case "null_removals":
				facts.Removals = nil
			case "null_core":
				facts.Core = nil
			case "null_storage":
				facts.Storage = nil
			}
			raw := f.signed(t, facts)
			switch kind {
			case "nil":
				raw = nil
			case "oversize":
				raw = make([]byte, capacitysnapshot.MaxBytes+1)
			case "key", "signature", "domain", "v2":
				var e capacitysnapshot.Envelope
				if err := json.Unmarshal(raw, &e); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "key":
					e.KeyID = "wrong"
				case "signature":
					e.Signature[0] ^= 1
				case "domain":
					e.Signature = ed25519.Sign(f.signKey, append([]byte("wrong-domain\x00"), e.Payload...))
				case "v2":
					e.Version = capacitysnapshot.VersionV2
				}
				raw, _ = json.Marshal(e)
			}
			before := bytes.Clone(raw)
			ctx := context.Background()
			if kind == "nil_context" {
				ctx = nil
			}
			e := s.Commit(ctx, raw)
			if !errors.Is(e, ErrPublicationInvalid) {
				t.Fatalf("accepted malformed envelope: %v", e)
			}
			fixedError(t, e)
			if !bytes.Equal(before, raw) {
				t.Fatal("caller bytes changed")
			}
			requireFenced(t, s)
			if _, e = os.Stat(filepath.Join(f.publicDirectory, "capacity.json")); !os.IsNotExist(e) {
				t.Fatal("invalid capacity visible")
			}
		})
	}
}
func TestStoreClockBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		age, future, reservedOffset time.Duration
		ok                          bool
	}{{"reserved_equal", 0, 0, 0, true}, {"before_reserved", 0, 0, -time.Nanosecond, false}, {"age_equal", 30 * time.Second, 0, 0, true}, {"age_excess", 30*time.Second + time.Nanosecond, 0, 0, false}, {"future_equal", 0, 2 * time.Second, 0, true}, {"future_excess", 0, 2*time.Second + time.Nanosecond, 0, false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			now := sampleTime
			s := clockStore(t, f, &now)
			defer s.Close()
			requireReserve(t, s, 1)
			observed := sampleTime.Add(tc.reservedOffset).Add(tc.future)
			now = sampleTime.Add(tc.age)
			e := s.Commit(context.Background(), f.signedFacts(t, 1, observed))
			if (e == nil) != tc.ok {
				t.Fatalf("clock acceptance %v", e)
			}
			if e != nil {
				requireFenced(t, s)
			}
		})
	}
}
func TestStoreJournalDrift(t *testing.T) {
	for _, operation := range []string{"reserve", "commit", "close"} {
		for _, kind := range []string{"bytes", "inode", "missing", "lock_replacement", "directory_replacement", "capacity_foreign"} {
			t.Run(operation+"_"+kind, func(t *testing.T) {
				f := newFixture(t)
				now := sampleTime
				s := clockStore(t, f, &now)
				if operation != "reserve" {
					requireReserve(t, s, 1)
				}
				p := filepath.Join(f.privateDirectory, "sequence.bin")
				switch kind {
				case "bytes":
					mustChmod(t, p, 0600)
					b, e := os.ReadFile(p)
					if e != nil {
						t.Fatal(e)
					}
					b[0] ^= 1
					mustWrite(t, p, b, 0400)
				case "inode":
					b, e := os.ReadFile(p)
					if e != nil {
						t.Fatal(e)
					}
					mustWrite(t, filepath.Join(f.privateDirectory, "sequence.next"), b, 0400)
					if e = os.Rename(filepath.Join(f.privateDirectory, "sequence.next"), p); e != nil {
						t.Fatal(e)
					}
				case "missing":
					mustRemove(t, p)
				case "lock_replacement":
					mustRemove(t, filepath.Join(f.privateDirectory, "writer.lock"))
					mustWrite(t, filepath.Join(f.privateDirectory, "writer.lock"), nil, 0600)
				case "directory_replacement":
					old := f.publicDirectory + "-old"
					if e := os.Rename(f.publicDirectory, old); e != nil {
						t.Fatal(e)
					}
					if e := os.Mkdir(f.publicDirectory, 0750); e != nil {
						t.Fatal(e)
					}
				case "capacity_foreign":
					mustWrite(t, filepath.Join(f.publicDirectory, "capacity.json"), f.signedFacts(t, 1, now), 0440)
				}
				before := entryImage(t, f)
				var e error
				switch operation {
				case "reserve":
					_, e = s.ReserveSequence(context.Background())
				case "commit":
					e = s.Commit(context.Background(), f.signedFacts(t, 1, now))
				case "close":
					e = s.Close()
				}
				if e == nil {
					t.Fatal("drift accepted")
				}
				fixedError(t, e)
				if entryImage(t, f) != before {
					t.Fatal("drifted files mutated")
				}
				s.Close()
				f.fs.assertReleased(t)
			})
		}
	}
}
func TestStoreReaderAtomicReplacementAndWithdrawal(t *testing.T) {
	f := newFixture(t)
	s := f.open(t)
	requireReserve(t, s, 1)
	first := f.signedFacts(t, 1, time.Now())
	if e := s.Commit(context.Background(), first); e != nil {
		t.Fatal(e)
	}
	old, e := os.Open(filepath.Join(f.publicDirectory, "capacity.json"))
	if e != nil {
		t.Fatal(e)
	}
	defer old.Close()
	requireReserve(t, s, 2)
	second := f.signedFacts(t, 2, time.Now())
	if e = s.Commit(context.Background(), second); e != nil {
		t.Fatal(e)
	}
	held, e := io.ReadAll(old)
	if e != nil || !bytes.Equal(held, first) {
		t.Fatal("old inode torn")
	}
	reader, e := capacitysnapshot.NewReader(f.publicDirectory)
	if e != nil {
		t.Fatal(e)
	}
	got, e := reader.Read()
	if e != nil || !bytes.Equal(got, second) {
		t.Fatal("new canonical bytes not exact")
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e = reader.Read(); e == nil {
		t.Fatal("Close failed to withdraw")
	}
	f.fs.assertReleased(t)
}
func TestStoreShortWritesAndZeroProgress(t *testing.T) {
	for _, kind := range []string{"short", "zero_journal", "zero_capacity"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			s := f.open(t)
			defer s.Close()
			if kind == "short" {
				f.fs.short = 7
			}
			if kind == "zero_journal" {
				f.fs.zero = true
			}
			n, e := s.ReserveSequence(context.Background())
			if kind == "zero_journal" {
				if n != 0 || !errors.Is(e, ErrPublicationIOFailed) {
					t.Fatalf("zero write=%d %v", n, e)
				}
				requireFenced(t, s)
				return
			}
			if n != 1 || e != nil {
				t.Fatal(e)
			}
			if kind == "zero_capacity" {
				f.fs.zero = true
			}
			e = s.Commit(context.Background(), f.signedFacts(t, n, time.Now()))
			if kind == "zero_capacity" {
				if !errors.Is(e, ErrPublicationIOFailed) {
					t.Fatal(e)
				}
				requireFenced(t, s)
			} else if e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestStoreCloseErrorIdempotency(t *testing.T) {
	for _, event := range []string{"unlink:capacity.json.before", "unlink:capacity.json.after", "sync:public.before", "sync:public.after", "close:public.before", "close:writer.lock.after"} {
		t.Run(event, func(t *testing.T) {
			f := newFixture(t)
			s := f.open(t)
			requireReserve(t, s, 1)
			if e := s.Commit(context.Background(), f.signedFacts(t, 1, time.Now())); e != nil {
				t.Fatal(e)
			}
			f.fs.reset()
			once := false
			f.fs.hook = func(ev string) error {
				if ev == event && !once {
					once = true
					return injected()
				}
				return nil
			}
			e := s.Close()
			if e == nil || !once {
				t.Fatalf("fault missed %v", e)
			}
			fixedError(t, e)
			if strings.HasPrefix(event, "unlink") || strings.HasPrefix(event, "sync") {
				if !errors.Is(e, ErrPublicationUncertain) {
					t.Fatal(e)
				}
			}
			count := len(f.fs.events)
			again := s.Close()
			if again != e || len(f.fs.events) != count {
				t.Fatal("Close result/repeated IO")
			}
			f.fs.assertReleased(t)
		})
	}
}

// Record actual finite syscall boundaries of a successful operation, then fail
// every occurrence independently. Expected outcomes are protocol invariants;
// this deliberately includes repeated rechecks rather than guessing one call count.
func operationTrace(t *testing.T, commit bool) []string {
	t.Helper()
	f := newFixture(t)
	s := f.open(t)
	if commit {
		requireReserve(t, s, 1)
	}
	f.fs.reset()
	var e error
	if commit {
		e = s.Commit(context.Background(), f.signedFacts(t, 1, time.Now()))
	} else {
		_, e = s.ReserveSequence(context.Background())
	}
	if e != nil {
		t.Fatal(e)
	}
	trace := append([]string(nil), f.fs.events...)
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	f.fs.assertReleased(t)
	return trace
}
func TestStoreFaultBoundaries(t *testing.T) {
	for _, commit := range []bool{false, true} {
		name := "reserve"
		if commit {
			name = "commit"
		}
		trace := operationTrace(t, commit)
		boundary := len(trace)
		for i, ev := range trace {
			if ev == "rename:capacity.json.before" {
				boundary = i
				break
			}
		}
		for index, event := range trace {
			t.Run(fmt.Sprintf("%s_%03d_%s", name, index, event), func(t *testing.T) {
				f := newFixture(t)
				s := f.open(t)
				if commit {
					requireReserve(t, s, 1)
				}
				f.fs.reset()
				seen := 0
				hit := false
				cause := injected()
				f.fs.hook = func(ev string) error {
					current := seen
					seen++
					if current == index {
						if ev != event {
							t.Fatalf("trace drift %s != %s", ev, event)
						}
						hit = true
						return cause
					}
					return nil
				}
				var e error
				if commit {
					e = s.Commit(context.Background(), f.signedFacts(t, 1, time.Now()))
				} else {
					var n int64
					n, e = s.ReserveSequence(context.Background())
					if n != 0 {
						t.Fatal("failed Reserve yielded usable number")
					}
				}
				f.fs.hook = nil
				if e == nil || !hit {
					t.Fatalf("fault unobserved index=%d hit=%v err=%v", index, hit, e)
				}
				fixedError(t, e)
				if errors.Is(e, cause) || strings.Contains(e.Error(), "secret") {
					t.Fatal("IO cause exposed")
				}
				if commit && index >= boundary && !errors.Is(e, ErrPublicationUncertain) {
					t.Fatalf("visibility boundary lost: %v", e)
				}
				if commit && index < boundary && errors.Is(e, ErrPublicationUncertain) {
					t.Fatalf("premature visibility classification: %v", e)
				}
				requireFenced(t, s)
				s.Close()
				f.fs.assertReleased(t)
				// Reopening after each failure must use only the formal journal. Any pending
				// next bytes are discarded, never promoted. The literal LE field is inspected
				// independently of the decoder used by Store.
				raw, err := os.ReadFile(filepath.Join(f.privateDirectory, "sequence.bin"))
				if err != nil {
					t.Fatal(err)
				}
				high := int64(binary.LittleEndian.Uint64(raw[48:56]))
				again := f.open(t)
				requireReserve(t, again, high+1)
				if err = again.Close(); err != nil {
					t.Fatal(err)
				}
				f.fs.assertReleased(t)
			})
		}
	}
}
func TestStoreJournalDurableBeforeCapacityVisible(t *testing.T) {
	f := newFixture(t)
	s := f.open(t)
	f.fs.reset()
	requireReserve(t, s, 1)
	if e := s.Commit(context.Background(), f.signedFacts(t, 1, time.Now())); e != nil {
		t.Fatal(e)
	}
	events := f.fs.events
	position := func(want string) int {
		for i, e := range events {
			if e == want {
				return i
			}
		}
		return -1
	}
	journalSync, visible := position("sync:private.after"), position("rename:capacity.json.before")
	if journalSync < 0 || visible <= journalSync {
		t.Fatalf("wrong durable-before-visible order %d %d", journalSync, visible)
	}
	for _, pair := range [][2]string{{"write:sequence.next.after", "chmod:sequence.next.before"}, {"chmod:sequence.next.after", "sync:sequence.next.before"}, {"sync:sequence.next.after", "close:sequence.next.before"}, {"close:sequence.next.after", "rename:sequence.bin.before"}, {"write:capacity.next.after", "chmod:capacity.next.before"}, {"chmod:capacity.next.after", "sync:capacity.next.before"}, {"sync:capacity.next.after", "close:capacity.next.before"}, {"close:capacity.next.after", "rename:capacity.json.before"}, {"rename:capacity.json.after", "sync:public.before"}} {
		a, b := position(pair[0]), position(pair[1])
		if a < 0 || b <= a {
			t.Fatalf("required literal order %v: %d %d", pair, a, b)
		}
	}
	s.Close()
}
func TestStoreCancellationBoundaries(t *testing.T) {
	for _, commit := range []bool{false, true} {
		name := "reserve"
		targets := []string{"pre", "create:sequence.next.after", "sync:sequence.next.after", "rename:sequence.bin.after", "sync:private.after", "late"}
		if commit {
			name = "commit"
			targets = []string{"pre", "create:capacity.next.after", "sync:capacity.next.after", "rename:capacity.json.before", "rename:capacity.json.after", "sync:public.before", "sync:public.after", "late"}
		}
		for _, target := range targets {
			t.Run(name+"_"+target, func(t *testing.T) {
				f := newFixture(t)
				now := sampleTime
				s := clockStore(t, f, &now)
				if commit {
					requireReserve(t, s, 1)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if target == "pre" {
					cancel()
				}
				f.fs.reset()
				hit := target == "pre"
				f.fs.hook = func(ev string) error {
					if ev == target {
						hit = true
						cancel()
					}
					return nil
				}
				if target == "late" {
					s.now = func() time.Time { hit = true; cancel(); return now }
				}
				var e error
				if commit {
					e = s.Commit(ctx, f.signedFacts(t, 1, now))
				} else {
					n, err := s.ReserveSequence(ctx)
					e = err
					if n != 0 {
						t.Fatal("cancelled Reserve exposed number")
					}
				}
				f.fs.hook = nil
				if !hit || !errors.Is(e, context.Canceled) {
					t.Fatalf("cancellation lost hit=%v error=%v", hit, e)
				}
				fixedError(t, e)
				afterBoundary := commit && (strings.HasPrefix(target, "rename:capacity.json") || strings.HasPrefix(target, "sync:public"))
				if afterBoundary && !errors.Is(e, ErrPublicationUncertain) {
					t.Fatalf("cancel after rename: %v", e)
				}
				requireFenced(t, s)
				events := len(f.fs.events)
				runtime.Gosched()
				if len(f.fs.events) != events {
					t.Fatal("detached IO after return")
				}
				s.Close()
				f.fs.assertReleased(t)
			})
		}
	}
}

// This context cancels on an exact Err check; it has no detached timer/goroutine.
type checkedCancelContext struct {
	context.Context
	checks, at int
	cause      error
}

func (c *checkedCancelContext) Err() error {
	c.checks++
	if c.checks >= c.at {
		return c.cause
	}
	return nil
}
func TestStoreLateReturnCancellation(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		for _, commit := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v_%v", cause, commit), func(t *testing.T) {
				f := newFixture(t)
				now := sampleTime
				s := clockStore(t, f, &now)
				if commit {
					requireReserve(t, s, 1)
				}
				probe := &checkedCancelContext{Context: context.Background(), at: 1000000, cause: cause}
				if commit {
					if e := s.Commit(probe, f.signedFacts(t, 1, now)); e != nil {
						t.Fatal(e)
					}
				} else {
					if _, e := s.ReserveSequence(probe); e != nil {
						t.Fatal(e)
					}
				}
				checks := probe.checks
				s.Close()
				f = newFixture(t)
				s = clockStore(t, f, &now)
				if commit {
					requireReserve(t, s, 1)
				}
				ctx := &checkedCancelContext{Context: context.Background(), at: checks, cause: cause}
				var e error
				if commit {
					e = s.Commit(ctx, f.signedFacts(t, 1, now))
				} else {
					n, err := s.ReserveSequence(ctx)
					e = err
					if n != 0 {
						t.Fatal("late cancelled number")
					}
				}
				if !errors.Is(e, cause) || (commit && !errors.Is(e, ErrPublicationUncertain)) {
					t.Fatalf("late result %v", e)
				}
				s.Close()
				f.fs.assertReleased(t)
			})
		}
	}
}
func TestStoreConcurrentReserve(t *testing.T) {
	f := newFixture(t)
	s := f.open(t)
	defer s.Close()
	var wg sync.WaitGroup
	results := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, e := s.ReserveSequence(context.Background())
			if e == nil && n != 1 {
				e = fmt.Errorf("unexpected successful number %d", n)
			}
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	success, busy := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, ErrPublicationBusy) {
			busy++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || busy != 19 {
		t.Fatalf("success=%d busy=%d", success, busy)
	}
	if e := s.Commit(context.Background(), f.signedFacts(t, 1, time.Now())); e != nil {
		t.Fatal(e)
	}
}
func TestStoreConcurrentClose(t *testing.T) {
	f := newFixture(t)
	s := f.open(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	f.fs.hook = func(ev string) error {
		if ev == "write:sequence.next.before" {
			once.Do(func() { close(entered); <-release })
		}
		return nil
	}
	reserved := make(chan error, 1)
	go func() { _, e := s.ReserveSequence(context.Background()); reserved <- e }()
	<-entered
	done := make(chan error, 12)
	for i := 0; i < 12; i++ {
		go func() { done <- s.Close() }()
	}
	select {
	case e := <-done:
		t.Fatalf("Close released in-flight descriptors: %v", e)
	default:
	}
	close(release)
	if e := <-reserved; e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 12; i++ {
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	}
	f.fs.hook = nil
	if n, e := s.ReserveSequence(context.Background()); n != 0 || !errors.Is(e, ErrPublicationClosed) {
		t.Fatalf("queued call crossed Close fence %d %v", n, e)
	}
	f.fs.assertReleased(t)
}
func TestStoreConcurrentCommitClose(t *testing.T) {
	f := newFixture(t)
	s := f.open(t)
	requireReserve(t, s, 1)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	f.fs.hook = func(ev string) error {
		if ev == "rename:capacity.json.before" {
			once.Do(func() { close(entered); <-release })
		}
		return nil
	}
	committed := make(chan error, 1)
	go func() { committed <- s.Commit(context.Background(), f.signedFacts(t, 1, time.Now())) }()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case e := <-closed:
		t.Fatalf("Close raced rename %v", e)
	default:
	}
	close(release)
	if e := <-committed; e != nil {
		t.Fatal(e)
	}
	if e := <-closed; e != nil {
		t.Fatal(e)
	}
	f.fs.hook = nil
	if _, e := os.Stat(filepath.Join(f.publicDirectory, "capacity.json")); !os.IsNotExist(e) {
		t.Fatal("Close left capacity")
	}
	f.fs.assertReleased(t)
}
func snapshotFixture(t *testing.T, f *fixture) *fixture {
	t.Helper()
	g := newFixture(t)
	g.trust = f.trust
	g.signKey = f.signKey
	for _, pair := range [][2]string{{f.privateDirectory, g.privateDirectory}, {f.publicDirectory, g.publicDirectory}} {
		existing, e := os.ReadDir(pair[1])
		if e != nil {
			t.Fatal(e)
		}
		for _, x := range existing {
			mustRemove(t, filepath.Join(pair[1], x.Name()))
		}
		entries, e := os.ReadDir(pair[0])
		if e != nil {
			t.Fatal(e)
		}
		for _, entry := range entries {
			p := filepath.Join(pair[0], entry.Name())
			info, e := os.Stat(p)
			if e != nil {
				t.Fatal(e)
			}
			raw, e := os.ReadFile(p)
			if e != nil {
				t.Fatal(e)
			}
			mustWrite(t, filepath.Join(pair[1], entry.Name()), raw, info.Mode().Perm())
		}
	}
	return g
}
func TestStoreFrozenCrashImages(t *testing.T) {
	for _, cut := range []string{"create:sequence.next.after", "write:sequence.next.after", "chmod:sequence.next.after", "sync:sequence.next.after", "close:sequence.next.after", "rename:sequence.bin.after", "sync:private.after", "create:capacity.next.after", "write:capacity.next.after", "chmod:capacity.next.after", "sync:capacity.next.after", "close:capacity.next.after", "rename:capacity.json.after", "sync:public.after"} {
		t.Run(cut, func(t *testing.T) {
			f := newFixture(t)
			s := f.open(t)
			commit := strings.Contains(cut, "capacity") || cut == "sync:public.after"
			if commit {
				requireReserve(t, s, 1)
			}
			var image *fixture
			f.fs.reset()
			f.fs.hook = func(ev string) error {
				if ev == cut && image == nil {
					image = snapshotFixture(t, f)
					return injected()
				}
				return nil
			}
			if commit {
				s.Commit(context.Background(), f.signedFacts(t, 1, time.Now()))
			} else {
				s.ReserveSequence(context.Background())
			}
			f.fs.hook = nil
			if image == nil {
				t.Fatal("crash cut missed")
			}
			s.Close()
			f.fs.assertReleased(t)
			raw, e := os.ReadFile(filepath.Join(image.privateDirectory, "sequence.bin"))
			if e != nil {
				t.Fatal(e)
			}
			high := int64(binary.LittleEndian.Uint64(raw[48:56]))
			if high < 0 || high > 1 {
				t.Fatal("formal journal not 0 or 1")
			}
			reopened := image.open(t)
			for _, p := range []string{filepath.Join(image.privateDirectory, "sequence.next"), filepath.Join(image.publicDirectory, "capacity.next"), filepath.Join(image.publicDirectory, "capacity.json")} {
				if _, e = os.Stat(p); !os.IsNotExist(e) {
					t.Fatalf("temporary promoted or stale publication retained: %v", e)
				}
			}
			requireReserve(t, reopened, high+1)
			if e = reopened.Close(); e != nil {
				t.Fatal(e)
			}
			image.fs.assertReleased(t)
		})
	}
}

func TestStoreSameNumberEnvelopeNeverRetried(t *testing.T) {
	for _, afterSuccess := range []bool{false, true} {
		t.Run(fmt.Sprint(afterSuccess), func(t *testing.T) {
			f := newFixture(t)
			now := sampleTime
			s := clockStore(t, f, &now)
			requireReserve(t, s, 1)
			first := f.signedFacts(t, 1, now)
			if afterSuccess {
				if e := s.Commit(context.Background(), first); e != nil {
					t.Fatal(e)
				}
			} else {
				bad := bytes.Clone(first)
				bad[0] = '!'
				if e := s.Commit(context.Background(), bad); e == nil {
					t.Fatal("malformed accepted")
				}
			}
			different := f.facts(1, now)
			different.Anchor.AvailableBytes--
			if e := s.Commit(context.Background(), f.signed(t, different)); e == nil {
				t.Fatal("same number retried")
			}
			if afterSuccess {
				raw, e := os.ReadFile(filepath.Join(f.publicDirectory, "capacity.json"))
				if e != nil || !bytes.Equal(raw, first) {
					t.Fatal("same-number bytes replaced")
				}
			}
			s.Close()
			f.fs.assertReleased(t)
		})
	}
}
func TestStoreLateFreshnessRecheck(t *testing.T) {
	for _, boundary := range []string{"sync:capacity.next.after", "rename:capacity.json.after", "sync:public.after"} {
		t.Run(boundary, func(t *testing.T) {
			f := newFixture(t)
			now := sampleTime
			s := clockStore(t, f, &now)
			requireReserve(t, s, 1)
			f.fs.hook = func(ev string) error {
				if ev == boundary {
					now = sampleTime.Add(30*time.Second + time.Nanosecond)
				}
				return nil
			}
			e := s.Commit(context.Background(), f.signedFacts(t, 1, sampleTime))
			f.fs.hook = nil
			if e == nil {
				t.Fatal("expired facts published successfully")
			}
			if boundary == "sync:capacity.next.after" {
				if !errors.Is(e, ErrPublicationInvalid) {
					t.Fatal(e)
				}
			} else if !errors.Is(e, ErrPublicationUncertain) {
				t.Fatal(e)
			}
			requireFenced(t, s)
			s.Close()
			f.fs.assertReleased(t)
		})
	}
}
func TestStoreContainerBoundAndEmptyRemovals(t *testing.T) {
	for _, n := range []int{32, 33} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			f := newFixture(t)
			now := sampleTime
			s := clockStore(t, f, &now)
			defer s.Close()
			requireReserve(t, s, 1)
			facts := f.facts(1, now)
			for i := 0; i < n; i++ {
				layer := f.trust.Storage[0]
				layer.ID = fmt.Sprintf("layer-%d", i)
				layer.Path = fmt.Sprintf("/mnt/lima-sbquota/docker/overlay2/worker-%d/diff", i)
				layer.ProjectID = uint64(200 + i)
				layer.Quota.ProjectID = layer.ProjectID
				facts.Containers = append(facts.Containers, capacitysnapshot.Container{ContainerID: fmt.Sprintf("cid-%d", i), RuntimeID: fmt.Sprintf("runtime-%d", i), SessionID: fmt.Sprintf("session-%d", i), Generation: 1, ProfileID: "local_standard_v1", ProfileRevision: 1, ProfileDigest: "sha256:" + strings.Repeat("a", 64), NanoCPUs: 2000000000, MemoryMax: 4 << 30, PIDsMax: 512, Layer: layer, CgroupPath: fmt.Sprintf("/sys/fs/cgroup/switchback.slice/switchback-workers.slice/worker-%d.scope", i), CPUQuotaMicros: 200000, CPUPeriodMicros: 100000})
			}
			if e := capacitysnapshot.CheckFacts(facts, f.trust); e != nil {
				t.Fatalf("fixture must pass original CheckFacts %v", e)
			}
			e := s.Commit(context.Background(), f.signed(t, facts))
			if (e == nil) != (n == 32) {
				t.Fatalf("container bound n=%d err=%v", n, e)
			}
		})
	}
	f := newFixture(t)
	now := sampleTime
	s := clockStore(t, f, &now)
	defer s.Close()
	requireReserve(t, s, 1)
	facts := f.facts(1, now)
	facts.Removals = []capacitysnapshot.Removal{{EngineID: f.trust.Identity.EngineID, ContainerID: "cid", RuntimeID: "runtime", SessionID: "session", SecretVolumeID: "secret-volume", Generation: 1, ChannelEpoch: 1, ContainerInspectHTTPStatus: 404, SecretVolumeInspectHTTPStatus: 404}}
	if e := capacitysnapshot.CheckFacts(facts, f.trust); e != nil {
		t.Fatal(e)
	}
	if e := s.Commit(context.Background(), f.signed(t, facts)); !errors.Is(e, ErrPublicationInvalid) {
		t.Fatal(e)
	}
}
func TestStoreOwnedCapacityDriftNeverDeleted(t *testing.T) {
	for _, kind := range []string{"content", "inode", "missing", "mode", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			s := f.open(t)
			requireReserve(t, s, 1)
			raw := f.signedFacts(t, 1, time.Now())
			if e := s.Commit(context.Background(), raw); e != nil {
				t.Fatal(e)
			}
			p := filepath.Join(f.publicDirectory, "capacity.json")
			switch kind {
			case "content":
				mustChmod(t, p, 0600)
				changed := bytes.Clone(raw)
				changed[len(changed)-2] ^= 1
				mustWrite(t, p, changed, 0440)
			case "inode":
				mustWrite(t, filepath.Join(f.publicDirectory, "capacity.next"), raw, 0440)
				if e := os.Rename(filepath.Join(f.publicDirectory, "capacity.next"), p); e != nil {
					t.Fatal(e)
				}
			case "missing":
				mustRemove(t, p)
			case "mode":
				mustChmod(t, p, 0640)
			case "symlink":
				mustRemove(t, p)
				if e := os.Symlink("foreign", p); e != nil {
					t.Fatal(e)
				}
			}
			before := entryImage(t, f)
			e := s.Close()
			if !errors.Is(e, ErrPublicationUncertain) {
				t.Fatalf("unknown visibility %v", e)
			}
			if entryImage(t, f) != before {
				t.Fatal("drifted owned capacity deleted")
			}
			f.fs.assertReleased(t)
		})
	}
}
func TestStoreExactSentinelText(t *testing.T) {
	for _, tc := range []struct {
		e error
		s string
	}{{ErrPublicationInvalid, "publication_invalid"}, {ErrPublicationBusy, "publication_busy"}, {ErrPublicationStateUnavailable, "publication_state_unavailable"}, {ErrPublicationIOFailed, "publication_io_failed"}, {ErrPublicationUncertain, "publication_uncertain"}, {ErrPublicationClosed, "publication_closed"}} {
		if tc.e.Error() != tc.s {
			t.Fatal("sentinel text drift")
		}
		cause := context.DeadlineExceeded
		e := failure(tc.e, cause, "bounded_test")
		if e.Error() != tc.s || !errors.Is(e, tc.e) || !errors.Is(e, cause) {
			t.Fatal("wrapper changed fixed reason/context identities")
		}
		if got := e.(*publicationFailure).Unwrap(); len(got) != 2 || got[0] != tc.e || got[1] != cause {
			t.Fatal("unexpected exposed cause")
		}
	}
}

func TestStoreAbandonExecutable(t *testing.T) {
	f := newFixture(t)
	s := f.open(t)
	defer s.Close()
	n, err := s.ReserveSequence(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw := f.signedFacts(t, n, time.Now())
	if err = s.Commit(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	n, err = s.ReserveSequence(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AbandonSequence(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	r, err := capacitysnapshot.NewReader(f.publicDirectory)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Read()
	if err != nil || !bytes.Equal(raw, got) {
		t.Fatalf("old bytes lost: %v", err)
	}
	next, err := s.ReserveSequence(context.Background())
	if err != nil || next != n+1 {
		t.Fatalf("number reused: %d %v", next, err)
	}
}

func TestStoreAbandonPreservesAndRestart(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(fmt.Sprint(after), func(t *testing.T) {
			f := newFixture(t)
			s := f.open(t)
			requireReserve(t, s, 1)
			raw := f.signedFacts(t, 1, time.Now())
			if e := s.Commit(context.Background(), raw); e != nil {
				t.Fatal(e)
			}
			requireReserve(t, s, 2)
			before, err := os.ReadFile(filepath.Join(f.privateDirectory, "sequence.bin"))
			if err != nil {
				t.Fatal(err)
			}
			var image *fixture
			if !after {
				image = snapshotFixture(t, f)
			}
			f.fs.reset()
			if err = s.AbandonSequence(context.Background(), 2); err != nil {
				t.Fatal(err)
			}
			for _, ev := range f.fs.events {
				for _, prefix := range []string{"create:", "write:", "chmod:", "sync:", "rename:", "unlink:", "lock:"} {
					if strings.HasPrefix(ev, prefix) {
						t.Fatal("abandon mutated filesystem", ev)
					}
				}
			}
			if s.pending != 0 || !s.reservedAt.IsZero() || s.high != 2 {
				t.Fatal("pending not cleared or high changed")
			}
			journal, err := os.ReadFile(filepath.Join(f.privateDirectory, "sequence.bin"))
			if err != nil || !bytes.Equal(before, journal) || binary.LittleEndian.Uint64(journal[48:56]) != 2 {
				t.Fatal("journal changed")
			}
			reader, err := capacitysnapshot.NewReader(f.publicDirectory)
			if err != nil {
				t.Fatal(err)
			}
			got, err := reader.Read()
			if err != nil || !bytes.Equal(got, raw) {
				t.Fatal("old bytes/time changed", err)
			}
			// Writer lock remains held: no second instance may open while abandoned.
			if other, e := openStore(f.trust, f.privateDirectory, f.publicDirectory, &temporaryFiles{}, time.Now); other != nil || !errors.Is(e, ErrPublicationBusy) {
				t.Fatalf("abandon released lock %v", e)
			}
			if after {
				image = snapshotFixture(t, f)
			}
			requireReserve(t, s, 3)
			s.Close()
			f.fs.assertReleased(t)
			reopened := image.open(t)
			requireReserve(t, reopened, 3)
			reopened.Close()
			image.fs.assertReleased(t)
		})
	}
}
func TestStoreAbandonRejectPreservesPending(t *testing.T) {
	for _, name := range []string{"nil_context", "zero", "negative", "wrong", "repeat", "none", "failed", "closed", "nil_store", "zero_store", "commit_failed"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			s := f.open(t)
			defer s.Close()
			requireReserve(t, s, 1)
			ctx := context.Background()
			n := int64(1)
			target := s
			want := ErrPublicationInvalid
			switch name {
			case "nil_context":
				ctx = nil
			case "zero":
				n = 0
			case "negative":
				n = -1
			case "wrong":
				n = 2
			case "repeat":
				if e := s.AbandonSequence(ctx, 1); e != nil {
					t.Fatal(e)
				}
			case "none":
				s.pending = 0
				s.reservedAt = time.Time{}
			case "failed":
				s.failed = true
				want = ErrPublicationStateUnavailable
			case "closed":
				s.Close()
				want = ErrPublicationClosed
			case "nil_store":
				target = nil
			case "zero_store":
				target = &Store{}
			case "commit_failed":
				if e := s.Commit(ctx, []byte("bad")); e == nil {
					t.Fatal("commit accepted")
				}
				want = ErrPublicationStateUnavailable
			}
			pending, at, high := s.pending, s.reservedAt, s.high
			f.fs.reset()
			e := target.AbandonSequence(ctx, n)
			if !errors.Is(e, want) || s.pending != pending || s.reservedAt != at || s.high != high || len(f.fs.events) != 0 {
				t.Fatalf("invalid changed state %v", e)
			}
		})
	}
}
func abandonPrepared(t *testing.T) (*fixture, *Store) {
	t.Helper()
	f := newFixture(t)
	s := f.open(t)
	requireReserve(t, s, 1)
	if e := s.Commit(context.Background(), f.signedFacts(t, 1, time.Now())); e != nil {
		t.Fatal(e)
	}
	requireReserve(t, s, 2)
	f.fs.reset()
	return f, s
}
func TestStoreAbandonEveryReadFaultAndCancel(t *testing.T) {
	base, s := abandonPrepared(t)
	if e := s.AbandonSequence(context.Background(), 2); e != nil {
		t.Fatal(e)
	}
	events := append([]string(nil), base.fs.events...)
	s.Close()
	base.fs.assertReleased(t)
	for index, name := range events {
		for _, cancelled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%03d_%s_cancel_%v", index, name, cancelled), func(t *testing.T) {
				f, s := abandonPrepared(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				count := 0
				hit := false
				cause := errors.New("secret-canary")
				f.fs.hook = func(string) error {
					count++
					if count == index+1 {
						hit = true
						if cancelled {
							cancel()
							return nil
						}
						return cause
					}
					return nil
				}
				e := s.AbandonSequence(ctx, 2)
				f.fs.hook = nil
				if !hit || e == nil || s.pending != 2 || s.high != 2 || s.reservedAt.IsZero() {
					t.Fatalf("fault/cancel accepted %v", e)
				}
				fixedError(t, e)
				if errors.Is(e, cause) || strings.Contains(e.Error(), "secret") {
					t.Fatal("cause exposed")
				}
				if cancelled && !errors.Is(e, context.Canceled) {
					t.Fatal("context identity lost", e)
				}
				requireFenced(t, s)
				if e = s.AbandonSequence(context.Background(), 2); !errors.Is(e, ErrPublicationStateUnavailable) {
					t.Fatal("failed abandon retried", e)
				}
				s.Close()
				f.fs.assertReleased(t)
			})
		}
	}
	for _, at := range []int{1, 2} {
		t.Run(fmt.Sprintf("context_check_%d", at), func(t *testing.T) {
			f, s := abandonPrepared(t)
			ctx := &checkedCancelContext{Context: context.Background(), at: at, cause: context.DeadlineExceeded}
			e := s.AbandonSequence(ctx, 2)
			if !errors.Is(e, context.DeadlineExceeded) || s.pending != 2 || !s.failed {
				t.Fatal("context fence", e)
			}
			s.Close()
			f.fs.assertReleased(t)
		})
	}
}
func TestStoreAbandonConcurrent(t *testing.T) {
	for _, next := range []string{"reserve", "commit", "close", "abandon"} {
		t.Run(next, func(t *testing.T) {
			f, s := abandonPrepared(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			f.fs.hook = func(ev string) error {
				if ev == "canonical:private.before" {
					once.Do(func() { close(entered); <-release })
				}
				return nil
			}
			abandoned := make(chan error, 1)
			go func() { abandoned <- s.AbandonSequence(context.Background(), 2) }()
			<-entered
			result := make(chan error, 1)
			raw := f.signedFacts(t, 2, time.Now())
			go func() {
				switch next {
				case "reserve":
					n, e := s.ReserveSequence(context.Background())
					if e == nil && n != 3 {
						e = fmt.Errorf("wrong number %d", n)
					}
					result <- e
				case "commit":
					result <- s.Commit(context.Background(), raw)
				case "close":
					result <- s.Close()
				case "abandon":
					result <- s.AbandonSequence(context.Background(), 2)
				}
			}()
			select {
			case e := <-result:
				t.Fatalf("concurrent operation crossed mutex %v", e)
			default:
			}
			close(release)
			if e := <-abandoned; e != nil {
				t.Fatal(e)
			}
			e := <-result
			f.fs.hook = nil
			if next == "commit" || next == "abandon" {
				if !errors.Is(e, ErrPublicationInvalid) {
					t.Fatal("pending consumed twice", e)
				}
			} else if e != nil {
				t.Fatal(e)
			}
			s.Close()
			f.fs.assertReleased(t)
		})
	}
}
func TestStoreAbandonQueuedBehindCommitAndClose(t *testing.T) {
	for _, first := range []string{"commit", "close", "reserve"} {
		t.Run(first, func(t *testing.T) {
			f := newFixture(t)
			s := f.open(t)
			if first != "reserve" {
				requireReserve(t, s, 1)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			f.fs.hook = func(ev string) error {
				if ev == "canonical:private.before" {
					once.Do(func() { close(entered); <-release })
				}
				return nil
			}
			firstDone := make(chan error, 1)
			raw := f.signedFacts(t, 1, time.Now())
			go func() {
				switch first {
				case "commit":
					firstDone <- s.Commit(context.Background(), raw)
				case "close":
					firstDone <- s.Close()
				case "reserve":
					_, e := s.ReserveSequence(context.Background())
					firstDone <- e
				}
			}()
			<-entered
			done := make(chan error, 1)
			go func() { done <- s.AbandonSequence(context.Background(), 1) }()
			select {
			case e := <-done:
				t.Fatal("abandon crossed mutex", e)
			default:
			}
			close(release)
			if e := <-firstDone; e != nil {
				t.Fatal(e)
			}
			e := <-done
			f.fs.hook = nil
			switch first {
			case "commit":
				if !errors.Is(e, ErrPublicationInvalid) {
					t.Fatal(e)
				}
			case "close":
				if !errors.Is(e, ErrPublicationClosed) {
					t.Fatal(e)
				}
			case "reserve":
				if e != nil {
					t.Fatal(e)
				}
			}
			s.Close()
			f.fs.assertReleased(t)
		})
	}
}
