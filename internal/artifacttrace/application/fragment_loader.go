package application

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"

	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

type LoadedFragmentProjection struct {
	Source   FragmentRef
	Version  string
	Frames   [][]byte
	Receipts [][]byte
}

type FragmentProjectionLoader interface {
	Load(context.Context, FragmentJob) (LoadedFragmentProjection, error)
}

type FragmentProjector interface {
	ExportAndVerify(context.Context, LoadedFragmentProjection) error
}

// Internal persisted authority and exact references, not a wire authorization.
type FragmentEvidence struct {
	Source                     FragmentRef
	Frame, Receipt, Provenance []byte
	Status                     string
	Object                     workerapp.StoredObject
	Core                       FragmentCoreEvidence
	Parents                    []FragmentRef
}

type FragmentCoreEvidence struct {
	Input          []byte
	ReportDigest   string
	Manifest       contractsv1.TraceManifest
	ManifestObject workerapp.StoredObject
	Report         contractsv1.ArtifactRef
}

type FragmentEvidenceReader interface {
	ReadFragmentEvidence(context.Context, FragmentRef) (FragmentEvidence, error)
	ReadFragmentParent(context.Context, FragmentRef, string) (FragmentEvidence, error)
}

type FragmentLoader struct {
	Metadata FragmentEvidenceReader
	Objects  workerapp.ObjectStore
}

func (l FragmentLoader) Load(ctx context.Context, job FragmentJob) (LoadedFragmentProjection, error) {
	result := LoadedFragmentProjection{Source: job.Source, Version: job.Version}
	if l.Metadata == nil || l.Objects == nil || job.Version != FragmentProjectionVersion {
		return result, projectionFailure("source_dependencies_invalid")
	}
	e, err := l.Metadata.ReadFragmentEvidence(ctx, job.Source)
	if err != nil {
		return result, normalizeProjectionError(ctx, err, "source_metadata_unavailable")
	}
	if e.Source != job.Source {
		return result, projectionFailure("fragment_proof_invalid")
	}
	f, err := artifactdomain.DecodeObservationFragment(e.Frame)
	if err != nil {
		return result, projectionFailure("fragment_proof_invalid")
	}
	refs := []FragmentRef{job.Source}
	if f.Kind != artifactdomain.FragmentRunBasis {
		id, _ := artifactdomain.ObservationBasisSourceID(artifactdomain.FragmentRunBasis, f.Scope, nil)
		refs = append(refs, FragmentRef{Kind: string(artifactdomain.FragmentRunBasis), SourceID: id})
	}
	if f.Kind == artifactdomain.FragmentModel {
		id, _ := artifactdomain.ObservationBasisSourceID(artifactdomain.FragmentTurnBasis, f.Scope, f.Turn)
		refs = append(refs, FragmentRef{Kind: string(artifactdomain.FragmentTurnBasis), SourceID: id})
	}
	for n, ref := range refs {
		current := e
		if n > 0 {
			current, err = l.Metadata.ReadFragmentParent(ctx, job.Source, ref.Kind)
			if err != nil {
				return result, normalizeProjectionError(ctx, err, "source_metadata_unavailable")
			}
			if current.Source.Kind != ref.Kind || current.Source.SourceID != ref.SourceID {
				return result, projectionFailure("fragment_proof_invalid")
			}
		}
		if current.Status != "published" {
			return result, retryableProjectionError("parent_basis_pending")
		}
		if err := verifyFragmentEvidence(ctx, l.Objects, current); err != nil {
			return result, err
		}
		if current.Object.Size != int64(len(current.Frame)) || current.Object.Checksum != current.Source.FrameDigest {
			return result, projectionFailure("fragment_proof_invalid")
		}
		raw, err := readVerifiedObject(ctx, l.Objects, current.Object, "source_object_unavailable", "source_object_missing", "source_checksum_mismatch")
		if err != nil {
			return result, err
		}
		if !bytes.Equal(raw, current.Frame) {
			return result, projectionFailure("source_checksum_mismatch")
		}
		result.Frames = append(result.Frames, raw)
		result.Receipts = append(result.Receipts, bytes.Clone(current.Receipt))
	}
	return result, nil
}

func verifyFragmentEvidence(ctx context.Context, objects workerapp.ObjectStore, e FragmentEvidence) error {
	f, err := artifactdomain.DecodeObservationFragment(e.Frame)
	r, receiptErr := DecodeObservationScanReceipt(e.Receipt, e.Frame)
	id, idErr := artifactdomain.ObservationFragmentID(f)
	if err != nil || receiptErr != nil || idErr != nil || e.Source != (FragmentRef{Kind: string(f.Kind), SourceID: id, FrameDigest: artifactdomain.Checksum(e.Frame)}) {
		return projectionFailure("fragment_proof_invalid")
	}
	c := e.Core
	if c.ManifestObject.Size < 0 || c.ManifestObject.Size > maxManifestBytes || c.Report.SizeBytes < 0 || c.Report.SizeBytes > maxObservationCaptureBytes || c.Manifest.Validate() != nil || c.Report.Validate() != nil {
		return projectionFailure("fragment_proof_invalid")
	}
	if f.Kind != artifactdomain.FragmentModel && (f.InputJSON != string(c.Input) || r.Provenance.InputDigest != artifactdomain.Checksum(c.Input) || r.Provenance.ReportDigest != c.ReportDigest) {
		return projectionFailure("fragment_proof_invalid")
	}
	manifest, err := readVerifiedObject(ctx, objects, c.ManifestObject, "manifest_object_unavailable", "manifest_object_missing", "manifest_object_invalid")
	if err != nil {
		return err
	}
	decoded, err := contractsv1.DecodeTraceManifest(manifest)
	if err != nil || !reflect.DeepEqual(decoded, c.Manifest) {
		return projectionFailure("manifest_object_invalid")
	}
	key, ok := strings.CutPrefix(c.Report.URI, "object://")
	if !ok {
		return projectionFailure("report_object_invalid")
	}
	report, err := readVerifiedObject(ctx, objects, workerapp.StoredObject{Key: key, Size: c.Report.SizeBytes, Checksum: "sha256:" + c.Report.SHA256}, "report_object_unavailable", "report_object_missing", "report_object_invalid")
	if err != nil {
		return err
	}
	if artifactdomain.Checksum(report) != c.ReportDigest {
		return projectionFailure("report_object_invalid")
	}
	if f.Kind == artifactdomain.FragmentTurnBasis {
		var text string
		if json.Unmarshal([]byte(f.OutputJSON), &text) != nil || text != string(report) {
			return projectionFailure("report_object_invalid")
		}
	}
	return nil
}
