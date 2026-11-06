package capacitypublisher

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitypublication"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Only synthetic fixture metadata is root-owned. Temporary real file bytes are
// read by the finite test source; this is not Linux native or deployment proof.
func publisherFixture(t *testing.T, worker bool) (*publisherConfig, *publisherKey, capacityprobe.PoolObservation) {
	t.Helper()
	tr, core, o := fixture(worker)
	remap := func(v any) {
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		b = bytes.ReplaceAll(b, []byte("/pool"), []byte(capacityprobe.Root))
		if e = json.Unmarshal(b, v); e != nil {
			t.Fatal(e)
		}
	}
	remap(&tr)
	remap(&o)
	o.MountInfoBefore = bytes.ReplaceAll(o.MountInfoBefore, []byte("/pool"), []byte(capacityprobe.Root))
	o.MountInfoAfter = bytes.Clone(o.MountInfoBefore)
	tr.PublicKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x11}, 32)).Public().(ed25519.PublicKey)
	resource, _ := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
	network, _ := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(config.SingleUserLocalProfileName)
	data := map[string][]byte{"source-manifest.json": []byte(`{"synthetic":"source"}`), "host-boundary-receipt.json": []byte(`{"synthetic":"host"}`), "native-enforcement-receipt.json": []byte(`{"synthetic":"native"}`)}
	data["deployment-inventory.json"], _ = json.Marshal(capacitysnapshot.DeploymentInventory{Schema: capacitysnapshot.DeploymentInventoryVersion, SourceManifestDigest: capacitysnapshot.Digest(data["source-manifest.json"]), DeploymentInputsDigest: capacitysnapshot.Digest([]byte("synthetic-inputs")), WorkerImage: "synthetic-worker", WorkerImageDigest: syntheticWorkerImage, Binaries: []capacitysnapshot.AdmissionBinary{{Name: "publisher", Digest: capacitysnapshot.Digest([]byte("synthetic-binary"))}}})
	m := capacitysnapshot.AdmissionManifest{Schema: capacitysnapshot.AdmissionManifestVersion, Guarantee: *tr.AdmissionPolicy, Identity: tr.Identity, ResourceProfile: resource.Ref(), NetworkProfile: config.RuntimeResourceProfileRef{ID: network.ID, Revision: network.Revision, Digest: network.Digest}, KeyID: tr.KeyID, PublicKey: tr.PublicKey, SourceManifestDigest: capacitysnapshot.Digest(data["source-manifest.json"]), DeploymentInventoryDigest: capacitysnapshot.Digest(data["deployment-inventory.json"]), HostBoundaryReceiptDigest: capacitysnapshot.Digest(data["host-boundary-receipt.json"]), NativeEnforcementReceiptDigest: capacitysnapshot.Digest(data["native-enforcement-receipt.json"])}
	data["admission-manifest.json"], _ = json.Marshal(m)
	tr.ManifestDigest = capacitysnapshot.Digest(data["admission-manifest.json"])
	data["trust.json"], _ = json.Marshal(tr)
	b := publisherBinding{Schema: publisherBindingsVersion, ManifestDigest: tr.ManifestDigest, WorkerImageDigest: syntheticWorkerImage}
	for _, v := range core {
		b.Core = append(b.Core, publisherCoreBinding{v.ContainerID, v.ImageDigest, v.CoreID})
	}
	data["publisher-bindings.json"], _ = json.Marshal(b)
	fs := newPublisherTestFiles(t)
	cfgDir := t.TempDir()
	for n, b := range data {
		if e := os.WriteFile(filepath.Join(cfgDir, n), b, 0400); e != nil {
			t.Fatal(e)
		}
	}
	c, e := readPublisherConfig(context.Background(), fs, cfgDir)
	if e != nil {
		t.Fatal("complete config", e)
	}
	keyDir := t.TempDir()
	if e = os.WriteFile(filepath.Join(keyDir, publisherSeedLeaf), bytes.Repeat([]byte{0x11}, 32), 0400); e != nil {
		t.Fatal(e)
	}
	k, e := openPublisherKey(context.Background(), fs, keyDir, c.trust)
	if e != nil {
		t.Fatal("complete key", e)
	}
	t.Cleanup(k.clear)
	return c, k, o
}
func TestPublisherFixtureComplete(t *testing.T) {
	for _, worker := range []bool{false, true} {
		t.Run(map[bool]string{false: "zero", true: "one"}[worker], func(t *testing.T) {
			c, k, o := publisherFixture(t, worker)
			m, e := NewMapper(c.trust, c.core, c.binding.WorkerImageDigest)
			if e != nil {
				t.Fatal(e)
			}
			r, e := m.Map(context.Background(), o, 7)
			if e != nil {
				t.Fatal(e)
			}
			if !r.Facts.ObservedAt.Equal(o.Started) || capacitysnapshot.CheckFacts(r.Facts, c.trust) != nil {
				t.Fatal("incomplete facts")
			}
			if e = k.recheck(context.Background()); e != nil {
				t.Fatal(e)
			}
			if e = c.files.recheck(context.Background()); e != nil {
				t.Fatal(e)
			}
		})
	}
}

type publisherCapture struct {
	events                                      []string
	next                                        int64
	raw                                         [][]byte
	abandoned                                   []int64
	reserveErr, abandonErr, commitErr, closeErr error
	closed                                      int
}

func (s *publisherCapture) ReserveSequence(ctx context.Context) (int64, error) {
	s.events = append(s.events, "reserve")
	if e := ctx.Err(); e != nil {
		return 0, e
	}
	if s.reserveErr != nil {
		return 0, s.reserveErr
	}
	s.next++
	return s.next, nil
}
func (s *publisherCapture) AbandonSequence(ctx context.Context, n int64) error {
	s.events = append(s.events, "abandon")
	if e := ctx.Err(); e != nil {
		return e
	}
	s.abandoned = append(s.abandoned, n)
	return s.abandonErr
}
func (s *publisherCapture) Commit(ctx context.Context, b []byte) error {
	s.events = append(s.events, "commit")
	if e := ctx.Err(); e != nil {
		return e
	}
	s.raw = append(s.raw, bytes.Clone(b))
	return s.commitErr
}
func (s *publisherCapture) Close() error {
	s.events = append(s.events, "close")
	s.closed++
	return s.closeErr
}
func publisherTestRuntime(t *testing.T, worker bool) (publisherRuntime, *publisherCapture, capacityprobe.PoolObservation, context.Context, context.CancelFunc) {
	t.Helper()
	c, k, o := publisherFixture(t, worker)
	s := &publisherCapture{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r := publisherRuntime{config: c, check: func(ctx context.Context) error { s.events = append(s.events, "check"); return ctx.Err() }, key: func(context.Context) (*publisherKey, error) { s.events = append(s.events, "key"); return k, nil }, store: func(capacitysnapshot.Trust) (publicationStore, error) {
		s.events = append(s.events, "open")
		return s, nil
	}, collect: func(context.Context, capacityprobe.PoolPlan) (capacityprobe.PoolObservation, error) {
		s.events = append(s.events, "collect")
		return o, nil
	}, now: func() time.Time { return o.Ended }, wait: func(ctx context.Context, d time.Duration) error {
		if d != 5*time.Second {
			t.Fatal(d)
		}
		s.events = append(s.events, "wait")
		cancel()
		return ctx.Err()
	}}
	return r, s, o, ctx, cancel
}
func TestPublisherExecutable(t *testing.T) {
	for _, worker := range []bool{false, true} {
		t.Run(map[bool]string{false: "zero", true: "one"}[worker], func(t *testing.T) {
			r, s, o, ctx, _ := publisherTestRuntime(t, worker)
			e := runPublisher(ctx, r)
			if !errors.Is(e, context.Canceled) || len(s.raw) != 1 || s.closed != 1 {
				t.Fatalf("pipeline error=%v committed=%d closed=%d", e, len(s.raw), s.closed)
			}
			v, e := capacitysnapshot.Verify(s.raw[0], r.config.trust)
			if e != nil || capacitysnapshot.CheckFacts(v.Facts(), r.config.trust) != nil {
				t.Fatal("original verifier", e)
			}
			f := v.Facts()
			if f.Sequence != 1 || !f.ObservedAt.Equal(o.Started) || len(f.Containers) != len(o.Workers) {
				t.Fatal("sequence/time/census changed")
			}
			var envelope capacitysnapshot.Envelope
			if json.Unmarshal(s.raw[0], &envelope) != nil || !ed25519.Verify(r.config.trust.PublicKey, append([]byte(capacitysnapshot.SignatureDomain), envelope.Payload...), envelope.Signature) || ed25519.Verify(r.config.trust.PublicKey, envelope.Payload, envelope.Signature) {
				t.Fatal("signature domain")
			}
			want := []string{"check", "key", "open", "check", "reserve", "collect", "check", "commit", "wait", "close"}
			if !reflect.DeepEqual(s.events, want) {
				t.Fatal(s.events)
			}
		})
	}
}

func TestPublisherTransientBurnAndCompatibility(t *testing.T) {
	r, s, o, ctx, cancel := publisherTestRuntime(t, true)
	calls, waits := 0, 0
	r.collect = func(context.Context, capacityprobe.PoolPlan) (capacityprobe.PoolObservation, error) {
		calls++
		s.events = append(s.events, "collect")
		if calls == 2 {
			return capacityprobe.PoolObservation{}, capacityprobe.ErrPoolUnstable
		}
		return o, nil
	}
	r.wait = func(c context.Context, d time.Duration) error {
		waits++
		if d != 5*time.Second {
			t.Fatal(d)
		}
		if waits == 2 {
			if len(s.raw) != 1 || len(s.abandoned) != 1 || s.abandoned[0] != 2 {
				t.Fatal("created transition did not preserve publication")
			}
			v, e := capacitysnapshot.Verify(s.raw[0], r.config.trust)
			if e != nil || v.Facts().Sequence != 1 || !v.Facts().ObservedAt.Equal(o.Started) {
				t.Fatal("old time/bytes changed")
			}
		}
		if waits == 3 {
			cancel()
			return c.Err()
		}
		return nil
	}
	if e := runPublisher(ctx, r); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if calls != 3 || s.next != 3 || len(s.raw) != 2 || s.closed != 1 {
		t.Fatal("wrong attempts", calls, s.next, len(s.raw), s.closed)
	}
	v, e := capacitysnapshot.Verify(s.raw[1], r.config.trust)
	if e != nil || v.Facts().Sequence != 3 {
		t.Fatal("burned sequence reused", e)
	}
}
func TestPublisherStartupAndCycleFailures(t *testing.T) {
	for _, kind := range []string{"startup_deployment", "startup_key", "startup_mapper", "startup_store", "cycle_deployment", "reserve", "collector", "mapper", "post_deployment", "key_replaced", "stale", "signature", "commit", "commit_uncertain", "abandon", "parent_cancel", "close"} {
		t.Run(kind, func(t *testing.T) {
			r, s, _, ctx, cancel := publisherTestRuntime(t, false)
			checks := 0
			r.check = func(context.Context) error {
				checks++
				s.events = append(s.events, "check")
				if (kind == "startup_deployment" && checks == 1) || (kind == "cycle_deployment" && checks == 2) || (kind == "post_deployment" && checks == 3) {
					return errors.New("native-secret-path")
				}
				return nil
			}
			oldCollect := r.collect
			switch kind {
			case "startup_key":
				r.key = func(context.Context) (*publisherKey, error) { return nil, errors.New("secret") }
			case "startup_mapper":
				r.config.trust.PublicKey = nil
			case "startup_store":
				r.store = func(capacitysnapshot.Trust) (publicationStore, error) { return nil, errors.New("secret") }
			case "reserve":
				s.reserveErr = errors.New("secret")
			case "collector":
				r.collect = func(context.Context, capacityprobe.PoolPlan) (capacityprobe.PoolObservation, error) {
					return capacityprobe.PoolObservation{}, capacityprobe.ErrPoolUnavailable
				}
			case "mapper":
				r.collect = func(c context.Context, p capacityprobe.PoolPlan) (capacityprobe.PoolObservation, error) {
					o, e := oldCollect(c, p)
					o.EngineBefore.Containers = nil
					return o, e
				}
			case "key_replaced":
				r.collect = func(c context.Context, p capacityprobe.PoolPlan) (capacityprobe.PoolObservation, error) {
					o, e := oldCollect(c, p)
					fs := r.config.files.fs.(*publisherTestFiles)
					fs.mutate = func(p string, m *publisherMeta) {
						if filepath.Base(p) == publisherSeedLeaf {
							m.inode++
						}
					}
					return o, e
				}
			case "stale":
				r.now = func() time.Time { return time.Now().Add(time.Minute) }
			case "signature":
				old := r.key
				r.key = func(c context.Context) (*publisherKey, error) {
					k, e := old(c)
					k.private = bytes.Repeat([]byte{0x22}, 64)
					return k, e
				}
			case "commit":
				s.commitErr = errors.New("secret")
			case "commit_uncertain":
				s.commitErr = capacitypublication.ErrPublicationUncertain
			case "abandon":
				s.abandonErr = errors.New("secret")
				r.collect = func(context.Context, capacityprobe.PoolPlan) (capacityprobe.PoolObservation, error) {
					return capacityprobe.PoolObservation{}, capacityprobe.ErrPoolUnstable
				}
			case "parent_cancel":
				r.collect = func(context.Context, capacityprobe.PoolPlan) (capacityprobe.PoolObservation, error) {
					cancel()
					return capacityprobe.PoolObservation{}, capacityprobe.ErrPoolUnstable
				}
			case "close":
				s.closeErr = errors.New("secret")
			}
			e := runPublisher(ctx, r)
			if e == nil || strings.Contains(e.Error(), "secret") {
				t.Fatal("failure missing/leaked", e)
			}
			if strings.HasPrefix(kind, "startup_") {
				if s.next != 0 || s.closed != 0 {
					t.Fatal("startup touched store", s.events)
				}
			} else if s.closed != 1 {
				t.Fatal("close count", s.closed)
			}
			if (kind == "commit" || kind == "commit_uncertain") && (s.next != 1 || len(s.abandoned) != 0 || len(s.raw) != 1) {
				t.Fatal("commit retried/abandoned", s.events)
			}
			if kind == "commit_uncertain" && !errors.Is(e, capacitypublication.ErrPublicationUncertain) {
				t.Fatal("visibility uncertainty lost", e)
			}
			if kind == "close" && !errors.Is(e, errPublisherClose) {
				t.Fatal("close uncertainty hidden", e)
			}
			if kind == "parent_cancel" && !errors.Is(e, context.Canceled) {
				t.Fatal("parent cancellation lost", e)
			}
		})
	}
}
func TestPublisherClockBoundaries(t *testing.T) {
	for _, name := range []string{"age30", "age30_plus", "future2", "future2_plus"} {
		t.Run(name, func(t *testing.T) {
			r, s, o, ctx, _ := publisherTestRuntime(t, false)
			when := o.Started.Add(30 * time.Second)
			want := true
			switch name {
			case "age30_plus":
				when = when.Add(time.Nanosecond)
				want = false
			case "future2":
				when = o.Ended.Add(-2 * time.Second)
			case "future2_plus":
				when = o.Ended.Add(-2*time.Second - time.Nanosecond)
				want = false
			}
			r.now = func() time.Time { return when }
			e := runPublisher(ctx, r)
			if e == nil || (len(s.raw) == 1) != want {
				t.Fatal(name, e, len(s.raw))
			}
			if want {
				v, _ := capacitysnapshot.Verify(s.raw[0], r.config.trust)
				if !v.Facts().ObservedAt.Equal(o.Started) {
					t.Fatal("ObservedAt refreshed")
				}
			}
		})
	}
}
func TestPublisherCancellationSynchronousLateRead(t *testing.T) {
	r, s, o, ctx, cancel := publisherTestRuntime(t, false)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	r.collect = func(c context.Context, p capacityprobe.PoolPlan) (capacityprobe.PoolObservation, error) {
		if deadline, ok := c.Deadline(); !ok || time.Until(deadline) > 25*time.Second {
			t.Error("attempt deadline absent")
		}
		close(entered)
		<-release
		return o, nil
	}
	go func() { done <- runPublisher(ctx, r) }()
	<-entered
	cancel()
	select {
	case <-done:
		t.Fatal("detached synchronous read")
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if len(s.raw) != 0 || s.closed != 1 || s.next != 1 {
		t.Fatal("late read published or overlap", s.events)
	}
}
func TestPublisherConcurrentNoOverlap(t *testing.T) {
	// A slow finite collector is never entered a second time while a cycle is live.
	r, s, o, ctx, cancel := publisherTestRuntime(t, false)
	var active, maxActive atomic.Int32
	calls := 0
	r.collect = func(context.Context, capacityprobe.PoolPlan) (capacityprobe.PoolObservation, error) {
		n := active.Add(1)
		if n > maxActive.Load() {
			maxActive.Store(n)
		}
		calls++
		time.Sleep(time.Millisecond)
		active.Add(-1)
		return o, nil
	}
	waits := 0
	r.wait = func(c context.Context, d time.Duration) error {
		if active.Load() != 0 {
			t.Fatal("overlapping wait/read")
		}
		waits++
		if waits == 3 {
			cancel()
			return c.Err()
		}
		return nil
	}
	if e := runPublisher(ctx, r); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if calls != 3 || maxActive.Load() != 1 || s.closed != 1 {
		t.Fatal("not serial")
	}
}
func TestPublisherCancellationAttemptAndWait(t *testing.T) {
	r, s, _, parent, _ := publisherTestRuntime(t, false)
	expired, cancel := context.WithDeadline(parent, time.Now().Add(-time.Second))
	defer cancel()
	k, e := r.key(parent)
	if e != nil {
		t.Fatal(e)
	}
	defer k.clear()
	m, e := NewMapper(r.config.trust, r.config.core, r.config.binding.WorkerImageDigest)
	if e != nil {
		t.Fatal(e)
	}
	r.collect = func(context.Context, capacityprobe.PoolPlan) (capacityprobe.PoolObservation, error) {
		return capacityprobe.PoolObservation{}, errors.Join(capacityprobe.ErrPoolUnstable, context.DeadlineExceeded)
	}
	unstable, e := publisherCycle(expired, r, m, k, s, 1)
	if !unstable || !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
	if e = s.AbandonSequence(parent, 1); e != nil {
		t.Fatal("attempt context incorrectly used", e)
	}
	c, stop := context.WithCancel(context.Background())
	stop()
	if e = publisherWait(c, 5*time.Second); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if e = publisherWait(context.Background(), time.Nanosecond); e != nil {
		t.Fatal(e)
	}
}

type publisherAttemptContextCapture struct {
	*publisherCapture
	attempt         context.Context
	abandonContexts []context.Context
	parentErrors    []error
	attemptErrors   []error
	attemptDone     []bool
}

func (s *publisherAttemptContextCapture) AbandonSequence(ctx context.Context, n int64) error {
	s.abandonContexts = append(s.abandonContexts, ctx)
	s.parentErrors = append(s.parentErrors, ctx.Err())
	s.attemptErrors = append(s.attemptErrors, s.attempt.Err())
	select {
	case <-s.attempt.Done():
		s.attemptDone = append(s.attemptDone, true)
	default:
		s.attemptDone = append(s.attemptDone, false)
	}
	return s.publisherCapture.AbandonSequence(ctx, n)
}

func TestPublisherCancellationUnstableAttemptAbandonsWithLiveParent(t *testing.T) {
	for _, abandonFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "wait_then_stop", true: "abandon_failure_stops"}[abandonFails], func(t *testing.T) {
			r, base, _, parent, cancel := publisherTestRuntime(t, false)
			s := &publisherAttemptContextCapture{publisherCapture: base}
			open := r.store
			r.store = func(trust capacitysnapshot.Trust) (publicationStore, error) {
				_, e := open(trust)
				return s, e
			}
			if abandonFails {
				s.abandonErr = errors.New("finite abandon failure")
			}
			calls := 0
			r.collect = func(attempt context.Context, _ capacityprobe.PoolPlan) (capacityprobe.PoolObservation, error) {
				calls++
				s.events = append(s.events, "collect_return")
				s.attempt = attempt
				if attempt == parent || attempt.Err() != nil || parent.Err() != nil {
					t.Fatal("collector did not receive a separate live attempt")
				}
				if deadline, ok := attempt.Deadline(); !ok || time.Until(deadline) > 25*time.Second {
					t.Fatal("attempt deadline absent")
				}
				// The finite source reports deadline + instability. No wall-clock
				// expiry is simulated: runPublisher cancels the attempt after return.
				return capacityprobe.PoolObservation{}, errors.Join(capacityprobe.ErrPoolUnstable, context.DeadlineExceeded)
			}
			waits := 0
			r.wait = func(ctx context.Context, d time.Duration) error {
				waits++
				s.events = append(s.events, "wait")
				if ctx != parent || ctx.Err() != nil || d != 5*time.Second || len(s.abandoned) != 1 {
					t.Fatal("wait preceded successful abandon or lost parent")
				}
				cancel()
				return ctx.Err()
			}
			e := runPublisher(parent, r)
			if len(s.abandonContexts) != 1 || s.abandonContexts[0] != parent || s.parentErrors[0] != nil || !errors.Is(s.attemptErrors[0], context.Canceled) || !s.attemptDone[0] {
				t.Fatalf("Abandon did not receive live original parent after attempt ended: contexts=%v parent=%v attempt=%v done=%v", s.abandonContexts, s.parentErrors, s.attemptErrors, s.attemptDone)
			}
			want := []string{"check", "key", "open", "check", "reserve", "collect_return", "abandon"}
			if abandonFails {
				if !errors.Is(e, errPublisherUnavailable) || waits != 0 || parent.Err() != nil {
					t.Fatalf("abandon failure did not stop: %v waits=%d", e, waits)
				}
			} else {
				want = append(want, "wait")
				if !errors.Is(e, context.Canceled) || waits != 1 {
					t.Fatalf("wait cancellation did not stop: %v waits=%d", e, waits)
				}
			}
			want = append(want, "close")
			if calls != 1 || s.next != 1 || !reflect.DeepEqual(s.abandoned, []int64{1}) || len(s.raw) != 0 || s.closed != 1 || !reflect.DeepEqual(s.events, want) {
				t.Fatalf("unexpected loop lifecycle: calls=%d events=%v abandoned=%v", calls, s.events, s.abandoned)
			}
		})
	}
}

func TestPublisherDeploymentAlwaysClosed(t *testing.T) {
	c, _, _ := publisherFixture(t, true)
	for _, name := range []string{"SWITCHBACK_CAPACITY_PUBLISHER_ENABLE", "SWITCHBACK_ALLOW_NATIVE", "SWITCHBACK_CAPACITY_READY", "DOCKER_HOST"} {
		t.Setenv(name, "enabled")
	}
	if e := verifyPublisherDeployment(context.Background(), c); !errors.Is(e, errPublisherDeployment) {
		t.Fatal("metadata/env bypass", e)
	}
	r, s, _, ctx, _ := publisherTestRuntime(t, false)
	r.check = func(ctx context.Context) error { return verifyPublisherDeployment(ctx, r.config) }
	if e := runPublisher(ctx, r); !errors.Is(e, errPublisherDeployment) || s.next != 0 || s.closed != 0 || len(s.events) != 0 {
		t.Fatal("gate performed key/store/collector work", e, s.events)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if e := verifyPublisherDeployment(canceled, c); !errors.Is(e, context.Canceled) || !errors.Is(e, errPublisherDeployment) {
		t.Fatal(e)
	}
	if e := Run(nil); !errors.Is(e, errPublisherInvalid) {
		t.Fatal(e)
	}
	if e := Run(canceled); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		if e := Run(context.Background()); !errors.Is(e, errPublisherUnsupported) {
			t.Fatal("unsupported platform reached fixed IO", e)
		}
	}
}

// Parse the production construction: no public options, and the unconditional
// deployment gate must dominate creation of key/store/native callback wiring.
func TestPublisherProductionConstructionAndUnit(t *testing.T) {
	tree, e := parser.ParseFile(token.NewFileSet(), "publish.go", nil, 0)
	if e != nil {
		t.Fatal(e)
	}
	var run *ast.FuncDecl
	for _, d := range tree.Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Name.Name == "Run" {
			run = f
		}
	}
	if run == nil || len(run.Type.Params.List) != 1 {
		t.Fatal("public injection surface")
	}
	gate, assembly := -1, -1
	for i, stmt := range run.Body.List {
		ast.Inspect(stmt, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if id, ok := c.Fun.(*ast.Ident); ok {
					if id.Name == "verifyPublisherDeployment" && gate < 0 {
						gate = i
					}
					if id.Name == "runPublisher" {
						assembly = i
					}
				}
			}
			return true
		})
	}
	if gate < 0 || assembly <= gate {
		t.Fatal("deployment gate does not precede production assembly")
	}
	unit, e := os.ReadFile("../../../../deploy/experience/switchback-capacity-publisher.service")
	if e != nil {
		t.Fatal(e)
	}
	settings := map[string]string{}
	for _, line := range strings.Split(string(unit), "\n") {
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		if line == "[Install]" {
			t.Fatal("unit enablement")
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			if _, exists := settings[k]; exists {
				t.Fatal("duplicate unit setting", k)
			}
			settings[k] = v
		}
	}
	expected := map[string]string{"Type": "simple", "ExecStart": "/opt/switchback/bin/switchback-capacity-publisher", "User": "root", "Group": "root", "Slice": "switchback-core.slice", "CapabilityBoundingSet": "CAP_SYS_ADMIN CAP_SYS_PTRACE", "NoNewPrivileges": "yes", "RestrictAddressFamilies": "AF_UNIX", "RestrictNamespaces": "yes", "SystemCallFilter": "~@mount @reboot @swap", "Restart": "no", "UMask": "0077", "LimitCORE": "0", "KillMode": "control-group", "TimeoutStopSec": "10s", "CPUQuota": "20%", "CPUQuotaPeriodSec": "100ms", "MemoryMax": "128M", "MemorySwapMax": "0", "TasksMax": "64"}
	for k, v := range expected {
		if settings[k] != v {
			t.Fatal("unit policy mismatch", k)
		}
	}
	delete(settings, "Description")
	if len(settings) != len(expected) {
		t.Fatal("additional unit policy", settings)
	}
}

func TestPublisherSignerRejectsInvalidAndOversize(t *testing.T) {
	c, k, o := publisherFixture(t, false)
	m, e := NewMapper(c.trust, c.core, c.binding.WorkerImageDigest)
	if e != nil {
		t.Fatal(e)
	}
	r, e := m.Map(context.Background(), o, 1)
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"short_key", "marshal", "oversize", "check_facts", "wrong_public"} {
		t.Run(name, func(t *testing.T) {
			f := r.Facts
			key := k.private
			tr := c.trust
			switch name {
			case "short_key":
				key = make([]byte, 31)
			case "marshal":
				f.ObservedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			case "oversize":
				f.Identity.EngineID = strings.Repeat("x", capacitysnapshot.MaxBytes)
			case "check_facts":
				f.ManifestDigest = capacitysnapshot.Digest([]byte("wrong"))
			case "wrong_public":
				tr.PublicKey = make([]byte, 32)
			}
			raw, e := signPublisher(f, tr, key)
			if len(raw) != 0 || !errors.Is(e, errPublisherInvalid) {
				t.Fatal("invalid signed output", e)
			}
		})
	}
}
func TestPublisherInvalidDependencies(t *testing.T) {
	if e := runPublisher(nil, publisherRuntime{}); !errors.Is(e, errPublisherInvalid) {
		t.Fatal(e)
	}
	if e := runPublisher(context.Background(), publisherRuntime{}); !errors.Is(e, errPublisherInvalid) {
		t.Fatal(e)
	}
	if e := verifyPublisherDeployment(nil, nil); !errors.Is(e, errPublisherInvalid) {
		t.Fatal(e)
	}
}

func TestPublisherUnstableCycleStillRechecksKeyBeforeReserve(t *testing.T) {
	r, s, _, ctx, _ := publisherTestRuntime(t, false)
	calls := 0
	r.collect = func(context.Context, capacityprobe.PoolPlan) (capacityprobe.PoolObservation, error) {
		calls++
		return capacityprobe.PoolObservation{}, capacityprobe.ErrPoolUnstable
	}
	r.wait = func(context.Context, time.Duration) error {
		r.config.files.fs.(*publisherTestFiles).mutate = func(p string, m *publisherMeta) {
			if filepath.Base(p) == publisherSeedLeaf {
				m.inode++
			}
		}
		return nil
	}
	if e := runPublisher(ctx, r); e == nil {
		t.Fatal("key rotation accepted")
	}
	if calls != 1 || s.next != 1 || len(s.abandoned) != 1 || len(s.raw) != 0 || s.closed != 1 {
		t.Fatal("rotation reached another Reserve", s.events)
	}
}
