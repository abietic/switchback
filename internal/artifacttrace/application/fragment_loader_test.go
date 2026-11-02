package application_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	lf "github.com/abietic/switchback/internal/artifacttrace/adapter/langfuse"
	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	app "github.com/abietic/switchback/internal/artifacttrace/application"
	domain "github.com/abietic/switchback/internal/artifacttrace/domain"
)

type fragmentMetadata struct {
	evidence app.FragmentEvidence
	err      error
}

func (m fragmentMetadata) ReadFragmentEvidence(context.Context, app.FragmentRef) (app.FragmentEvidence, error) {
	return m.evidence, m.err
}
func (m fragmentMetadata) ReadFragmentParent(context.Context, app.FragmentRef, string) (app.FragmentEvidence, error) {
	return m.evidence, m.err
}

type fragmentNoTokens struct{}

func (fragmentNoTokens) ScanJSON(context.Context, string, any) error { return nil }

func TestFragmentLoaderRejectsUnboundedObjectMetadata(t *testing.T) {
	f := domain.ObservationFragment{SchemaVersion: "observation.fragment.v1", Kind: domain.FragmentRunBasis, Scope: domain.ObservationScope{TenantID: "tenant", UserID: "user", WorkspaceID: "workspace", WorkerSessionID: "session", WorkerRunID: "run"}, InitialTurnID: "turn", StartedAt: time.Now(), InputJSON: `{"text":"goal"}`}
	raw, err := domain.EncodeObservationFragment(f)
	if err != nil {
		t.Fatal(err)
	}
	digest := domain.Checksum([]byte("proof"))
	p := (app.ObservationCapture{Compiler: lf.FragmentCompiler{}, Scanner: fragmentNoTokens{}, Scope: app.ObservationScanScope{WorkerSessionID: "session", RuntimeInstanceID: "runtime", RuntimeGeneration: 1}}).Prepare(context.Background(), raw, map[string]string{"evidence": "test"}, app.ObservationProvenance{CheckpointDigest: digest, InputDigest: domain.Checksum([]byte(f.InputJSON)), ReportDigest: digest})
	if p.ErrorCode != "" {
		t.Fatal(p.ErrorCode)
	}
	id, _ := domain.ObservationFragmentID(f)
	e := app.FragmentEvidence{Source: app.FragmentRef{Kind: string(f.Kind), SourceID: id, FrameDigest: domain.Checksum(p.Frame)}, Frame: p.Frame, Receipt: p.Receipt, Status: "published"}
	e.Core.ManifestObject.Size = math.MaxInt64
	objects, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = (app.FragmentLoader{Metadata: fragmentMetadata{evidence: e}, Objects: objects}).Load(context.Background(), app.FragmentJob{Source: e.Source, Version: app.FragmentProjectionVersion})
	var typed *app.ProjectionError
	if !errors.As(err, &typed) || !typed.Permanent || typed.Code != "fragment_proof_invalid" {
		t.Fatalf("unbounded reference %v", err)
	}
}

func TestFragmentLoaderSanitizesMetadataFailure(t *testing.T) {
	objects, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = (app.FragmentLoader{Metadata: fragmentMetadata{err: errors.New("synthetic private backend details")}, Objects: objects}).Load(context.Background(), app.FragmentJob{Version: app.FragmentProjectionVersion})
	assertProjectionError(t, err, "source_metadata_unavailable", false, "synthetic private")
}
