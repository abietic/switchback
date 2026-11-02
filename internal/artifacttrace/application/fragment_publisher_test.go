package application_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	lf "github.com/abietic/switchback/internal/artifacttrace/adapter/langfuse"
	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	app "github.com/abietic/switchback/internal/artifacttrace/application"
	domain "github.com/abietic/switchback/internal/artifacttrace/domain"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

type fragmentFailureStore struct {
	err       error
	code      string
	permanent bool
	timeout   time.Duration
}

type fragmentObjectFailureStore struct {
	fragmentFailureStore
	evidence app.FragmentEvidence
}

func (s *fragmentObjectFailureStore) ClaimCandidate(context.Context, time.Duration) (app.FragmentCandidate, bool, error) {
	return app.FragmentCandidate{Source: s.evidence.Source, Frame: s.evidence.Frame, Receipt: s.evidence.Receipt}, true, nil
}
func (s *fragmentObjectFailureStore) ReadFragmentEvidence(context.Context, app.FragmentRef) (app.FragmentEvidence, error) {
	return s.evidence, nil
}

type fragmentFailingObjects struct {
	workerapp.ObjectStore
	stage  string
	err    error
	cancel context.CancelFunc
}

func (s fragmentFailingObjects) failure() error {
	if s.cancel != nil {
		s.cancel()
	}
	return s.err
}
func (s fragmentFailingObjects) Finalize(ctx context.Context, identity workerapp.ObjectIdentity, temp workerapp.TemporaryObject) (workerapp.StoredObject, error) {
	if s.stage == "finalize" {
		return workerapp.StoredObject{}, s.failure()
	}
	return s.ObjectStore.Finalize(ctx, identity, temp)
}
func (s fragmentFailingObjects) Verify(ctx context.Context, key, checksum string) error {
	if s.stage == "verify" && strings.Contains(key, "/observation-fragment/") {
		return s.failure()
	}
	return s.ObjectStore.Verify(ctx, key, checksum)
}

func TestFragmentPublisherClassifiesObjectFailures(t *testing.T) {
	objects, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	finalizer := validFinalizer(objects)
	command := validFinalizeCommand(t)
	core, err := finalizer.Execute(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	i := command.Identity
	f := domain.ObservationFragment{SchemaVersion: "observation.fragment.v1", Kind: domain.FragmentRunBasis,
		Scope:         domain.ObservationScope{TenantID: i.TenantID, UserID: i.UserID, WorkspaceID: i.WorkspaceID, WorkerSessionID: string(i.WorkerSessionID), WorkerRunID: string(i.WorkerRunID)},
		InitialTurnID: string(i.AgentTurnID), StartedAt: command.ServerRecordedAt, InputJSON: `{"text":"goal"}`}
	raw, err := domain.EncodeObservationFragment(f)
	if err != nil {
		t.Fatal(err)
	}
	e := app.FragmentEvidence{Core: app.FragmentCoreEvidence{Input: []byte(f.InputJSON), Manifest: core.Manifest, ManifestObject: core.ManifestObject}}
	for _, ref := range core.ArtifactRefs {
		if ref.Type == "report" {
			e.Core.Report = ref
			e.Core.ReportDigest = "sha256:" + ref.SHA256
		}
	}
	prepared := (app.ObservationCapture{Compiler: lf.FragmentCompiler{}, Scanner: fragmentNoTokens{}, Scope: app.ObservationScanScope{WorkerSessionID: string(i.WorkerSessionID), RuntimeInstanceID: string(i.RuntimeInstanceID), RuntimeGeneration: uint64(i.RuntimeGeneration)}}).Prepare(context.Background(), raw, map[string]string{"evidence": "test"}, app.ObservationProvenance{CheckpointDigest: domain.Checksum([]byte("checkpoint")), InputDigest: domain.Checksum(e.Core.Input), ReportDigest: e.Core.ReportDigest})
	if prepared.ErrorCode != "" {
		t.Fatal(prepared.ErrorCode)
	}
	id, _ := domain.ObservationFragmentID(f)
	e.Source = app.FragmentRef{Kind: string(f.Kind), SourceID: id, FrameDigest: domain.Checksum(raw)}
	e.Frame, e.Receipt = prepared.Frame, prepared.Receipt
	for _, stage := range []string{"finalize", "verify"} {
		for _, test := range []struct {
			name              string
			err               error
			permanent, cancel bool
		}{
			{name: "integrity", err: fmt.Errorf("private details: %w", workerapp.ErrObjectIntegrity), permanent: true},
			{name: "io", err: io.ErrUnexpectedEOF},
			{name: "misleading text", err: errors.New("immutable ObjectStore key conflict: checksum mismatch")},
			{name: "operation cancelled", err: context.Canceled},
			{name: "operation deadline", err: context.DeadlineExceeded},
			{name: "parent cancelled", err: context.Canceled, cancel: true},
		} {
			t.Run(stage+"/"+test.name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				s := &fragmentObjectFailureStore{evidence: e}
				failing := fragmentFailingObjects{ObjectStore: objects, stage: stage, err: test.err}
				if test.cancel {
					failing.cancel = cancel
				}
				p := app.FragmentPublisher{Store: s, Objects: failing}
				err := p.Tick(ctx)
				if test.cancel {
					if !errors.Is(err, context.Canceled) || s.code != "" {
						t.Fatalf("parent cancellation must exit without deferral: err=%v code=%s", err, s.code)
					}
					return
				}
				code := "source_object_unavailable"
				if test.permanent {
					code = "source_checksum_mismatch"
				} else if stage == "verify" && (errors.Is(test.err, context.Canceled) || errors.Is(test.err, context.DeadlineExceeded)) {
					code = "source_load_cancelled"
				}
				if err != nil || s.code != code || s.permanent != test.permanent {
					t.Fatalf("err=%v code=%s permanent=%v; want %s/%v", err, s.code, s.permanent, code, test.permanent)
				}
			})
		}
	}
}

func (*fragmentFailureStore) ClaimCandidate(context.Context, time.Duration) (app.FragmentCandidate, bool, error) {
	return app.FragmentCandidate{}, true, nil
}
func (*fragmentFailureStore) Publish(context.Context, app.FragmentCandidate, workerapp.StoredObject) (app.FragmentRef, error) {
	panic("invalid source published")
}
func (s *fragmentFailureStore) DeferCandidate(_ context.Context, _ app.FragmentCandidate, code string, permanent bool) error {
	s.code, s.permanent = code, permanent
	return nil
}
func (s *fragmentFailureStore) ReadFragmentEvidence(ctx context.Context, _ app.FragmentRef) (app.FragmentEvidence, error) {
	deadline, ok := ctx.Deadline()
	if ok {
		s.timeout = time.Until(deadline)
	}
	return app.FragmentEvidence{}, s.err
}

func TestFragmentPublisherClassifiesEvidenceAndBoundsWork(t *testing.T) {
	for _, test := range []struct {
		name      string
		err       error
		code      string
		permanent bool
	}{
		{"pending", &app.ProjectionError{Code: "parent_basis_pending"}, "parent_basis_pending", false},
		{"blocked", &app.ProjectionError{Code: "parent_basis_unavailable", Permanent: true}, "parent_basis_unavailable", true},
		{"proof", &app.ProjectionError{Code: "fragment_proof_invalid", Permanent: true}, "fragment_proof_invalid", true},
		{"external", errors.New("synthetic private database details"), "source_metadata_unavailable", false},
		{"untrusted typed", &app.ProjectionError{Code: "synthetic private details"}, "projection_unavailable", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &fragmentFailureStore{err: test.err}
			objects, err := artifactlocal.NewObjectStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			p := app.FragmentPublisher{Store: s, Objects: objects}
			if err := p.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			if s.code != test.code || s.permanent != test.permanent || s.timeout <= 0 || s.timeout > 15*time.Second {
				t.Fatalf("completion %+v", s)
			}
		})
	}
}
