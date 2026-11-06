package capacitypublisher

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitypublication"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	"runtime"
	"sort"
	"time"
)

var (
	errPublisherInvalid     = errors.New("publisher_invalid")
	errPublisherUnavailable = errors.New("publisher_unavailable")
	errPublisherDeployment  = errors.New("publisher_deployment_unavailable")
	errPublisherUnsupported = errors.New("publisher_unsupported")
	errPublisherClose       = errors.New("publisher_close_uncertain")
)

// publicationStore is the finite B1 lifecycle. Store alone owns durable numbers,
// publication bytes and uncertainty; the publisher never compensates a Commit.
type publicationStore interface {
	ReserveSequence(context.Context) (int64, error)
	AbandonSequence(context.Context, int64) error
	Commit(context.Context, []byte) error
	Close() error
}
type publisherRuntime struct {
	config  *publisherConfig
	check   func(context.Context) error
	key     func(context.Context) (*publisherKey, error)
	store   func(capacitysnapshot.Trust) (publicationStore, error)
	collect func(context.Context, capacityprobe.PoolPlan) (capacityprobe.PoolObservation, error)
	now     func() time.Time
	wait    func(context.Context, time.Duration) error
}

// Run is fixed production construction. There is no options, environment or
// command channel to replace the deployment verifier, key or native collector.
func Run(ctx context.Context) error {
	if ctx == nil {
		return errPublisherInvalid
	}
	if e := ctx.Err(); e != nil {
		return publisherError(ctx, errPublisherUnavailable)
	}
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return errPublisherUnsupported
	}
	q, e := bootstrapPublisher(ctx, capacitysnapshot.AwaitDeploymentQualification)
	if e != nil {
		return e
	}
	fs := newPublisherFiles()
	c, e := readPublisherConfig(ctx, fs, publisherConfigPath)
	if e != nil {
		return publisherError(ctx, errPublisherInvalid)
	}
	defer c.files.clear()
	// Existing readers are structural helpers only. Guarded bytes and inode
	// identity are checked before and after their independent fixed reads.
	trust, e := capacitysnapshot.ReadTrust(publisherConfigPath + "/trust.json")
	if e != nil {
		return publisherError(ctx, errPublisherInvalid)
	}
	if q == nil || q.Recheck(ctx) != nil {
		return publisherError(ctx, errPublisherDeployment)
	}
	q, e = capacitysnapshot.OpenDeploymentQualification(ctx, publisherConfigPath+"/admission-manifest.json", trust)
	if e != nil {
		return publisherError(ctx, errPublisherDeployment)
	}
	c.qualification = q

	if e = c.files.recheck(ctx); e != nil {
		return publisherError(ctx, errPublisherInvalid)
	}
	if e = verifyPublisherDeployment(ctx, c); e != nil {
		return e
	}
	return runPublisher(ctx, publisherRuntime{config: c, check: func(ctx context.Context) error {
		if e := c.files.recheck(ctx); e != nil {
			return e
		}
		return verifyPublisherDeployment(ctx, c)
	}, key: func(ctx context.Context) (*publisherKey, error) {
		return openPublisherKey(ctx, fs, publisherKeyPath, c.trust)
	}, store: func(t capacitysnapshot.Trust) (publicationStore, error) { return capacitypublication.Open(t) }, collect: capacityprobe.CollectPool, now: time.Now, wait: publisherWait})
}

// Qualification and current role checks precede key/Store/sequence ownership.
// A config parsed from structural metadata alone deliberately remains invalid.
func verifyPublisherDeployment(ctx context.Context, c *publisherConfig) error {
	if ctx == nil {
		return errPublisherInvalid
	}
	if ctx.Err() != nil {
		return publisherError(ctx, errPublisherDeployment)
	}
	if c == nil || c.qualification == nil {
		return errPublisherDeployment
	}
	expected := c.qualification.PublisherBinding()
	actual := append([]publisherCoreBinding(nil), c.binding.Core...)
	sort.Slice(actual, func(i, j int) bool {
		if actual[i].CoreID == actual[j].CoreID {
			return actual[i].ContainerID < actual[j].ContainerID
		}
		return actual[i].CoreID < actual[j].CoreID
	})
	if expected.WorkerImageDigest != c.binding.WorkerImageDigest || len(expected.Core) != len(actual) {
		return errPublisherDeployment
	}
	for i, b := range actual {
		if b.ContainerID != expected.Core[i].ContainerID || b.ImageDigest != expected.Core[i].ImageDigest || b.CoreID != expected.Core[i].CoreID {
			return errPublisherDeployment
		}
	}
	if e := checkPublisherPrivateDirectories(ctx, c); e != nil {
		return e
	}
	if e := checkPublisherRole(ctx, c.qualification); e != nil {
		return publisherError(ctx, errPublisherDeployment)
	}
	return nil
}

type publisherFailure struct{ reason, cause error }

func (e *publisherFailure) Error() string { return e.reason.Error() }
func (e *publisherFailure) Unwrap() []error {
	if e.cause != nil {
		return []error{e.reason, e.cause}
	}
	return []error{e.reason}
}
func publisherError(ctx context.Context, reason error) error {
	var cause error
	if ctx != nil {
		cause = ctx.Err()
	}
	return &publisherFailure{reason, cause}
}
func publisherWait(ctx context.Context, d time.Duration) error {
	if ctx == nil {
		return errPublisherInvalid
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func runPublisher(ctx context.Context, r publisherRuntime) (err error) {
	if ctx == nil || r.config == nil || r.check == nil || r.key == nil || r.store == nil || r.collect == nil || r.now == nil || r.wait == nil {
		return errPublisherInvalid
	}
	if ctx.Err() != nil {
		return publisherError(ctx, errPublisherUnavailable)
	}
	if e := r.check(ctx); e != nil {
		return publisherError(ctx, errPublisherDeployment)
	}
	key, e := r.key(ctx)
	if e != nil || key == nil {
		return publisherError(ctx, errPublisherInvalid)
	}
	defer key.clear()
	mapper, e := NewMapper(r.config.trust, r.config.core, r.config.binding.WorkerImageDigest)
	if e != nil {
		return publisherError(ctx, errPublisherInvalid)
	}
	store, e := r.store(r.config.trust)
	if e != nil || store == nil {
		return publisherError(ctx, errPublisherUnavailable)
	}
	defer func() {
		if e := store.Close(); e != nil {
			err = errors.Join(err, publisherError(ctx, errPublisherClose))
		}
	}()
	for {
		if ctx.Err() != nil {
			return publisherError(ctx, errPublisherUnavailable)
		}
		if e = r.check(ctx); e != nil {
			return publisherError(ctx, errPublisherDeployment)
		}
		// Even unstable cycles must notice rotation before burning another number.
		if e = key.recheck(ctx); e != nil {
			return publisherOperationError(ctx, e)
		}
		seq, e := store.ReserveSequence(ctx)
		if e != nil {
			return publisherOperationError(ctx, e)
		}
		// Cancellation is cooperative: no goroutine detaches synchronous reads,
		// signing or Commit, and owned resources stay alive until they return.
		attempt, cancel := context.WithTimeout(ctx, 25*time.Second)
		unstable, e := publisherCycle(attempt, r, mapper, key, store, seq)
		cancel()
		if e != nil {
			if !unstable {
				return publisherOperationError(ctx, e)
			}
			// Only collector instability may abandon. The parent remains authoritative
			// after an expired attempt and must itself still be live.
			if e = store.AbandonSequence(ctx, seq); e != nil {
				return publisherOperationError(ctx, e)
			}
		}
		if e = r.wait(ctx, 5*time.Second); e != nil {
			return publisherOperationError(ctx, e)
		}
	}
}

// Preserve only fixed lifecycle uncertainty and context identity, never raw
// native/parser/error causes. Close cannot turn a failed Commit into rollback.
func publisherOperationError(ctx context.Context, e error) error {
	reason := errPublisherUnavailable
	if errors.Is(e, capacitypublication.ErrPublicationUncertain) {
		reason = capacitypublication.ErrPublicationUncertain
	}
	var cause error
	if ctx != nil {
		cause = ctx.Err()
	}
	if cause == nil {
		if errors.Is(e, context.DeadlineExceeded) {
			cause = context.DeadlineExceeded
		} else if errors.Is(e, context.Canceled) {
			cause = context.Canceled
		}
	}
	return &publisherFailure{reason, cause}
}
func publisherCycle(ctx context.Context, r publisherRuntime, m *Mapper, k *publisherKey, s publicationStore, seq int64) (bool, error) {
	o, e := r.collect(ctx, r.config.plan)
	if e != nil {
		return errors.Is(e, capacityprobe.ErrPoolUnstable), e
	}
	if e = ctx.Err(); e != nil {
		return false, e
	}
	result, e := m.Map(ctx, o, seq)
	if e != nil {
		return false, e
	}
	if e = r.check(ctx); e != nil {
		return false, e
	}
	if e = k.recheck(ctx); e != nil {
		return false, e
	}
	if e = window(o, seq, r.now()); e != nil {
		return false, e
	}
	if e = ctx.Err(); e != nil {
		return false, e
	}
	raw, e := signPublisher(result.Facts, r.config.trust, k.private)
	if e != nil {
		return false, e
	}
	if e = ctx.Err(); e != nil {
		return false, e
	}
	if e = s.Commit(ctx, raw); e != nil {
		return false, e
	}
	return false, ctx.Err()
}

// f comes only from the original Mapper, never an externally supplied request.
func signPublisher(f capacitysnapshot.Facts, trust capacitysnapshot.Trust, key ed25519.PrivateKey) ([]byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, errPublisherInvalid
	}
	payload, e := json.Marshal(f)
	if e != nil {
		return nil, errPublisherInvalid
	}
	message := append([]byte(capacitysnapshot.SignatureDomain), payload...)
	signature := ed25519.Sign(key, message)
	raw, e := json.Marshal(capacitysnapshot.Envelope{Version: capacitysnapshot.Version, KeyID: trust.KeyID, Payload: payload, Signature: signature})
	if e != nil || len(raw) == 0 || len(raw) > capacitysnapshot.MaxBytes {
		return nil, errPublisherInvalid
	}
	v, e := capacitysnapshot.Verify(raw, trust)
	if e != nil || capacitysnapshot.CheckFacts(v.Facts(), trust) != nil {
		return nil, errPublisherInvalid
	}
	return raw, nil
}
