package langfuse

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
)

func navigationBasis(t *testing.T, turn bool) artifactapp.LoadedFragmentProjection {
	t.Helper()
	source := preparedFragmentProjection(t)
	source.Frames, source.Receipts = source.Frames[1:], source.Receipts[1:]
	if !turn {
		source.Frames, source.Receipts = source.Frames[1:], source.Receipts[1:]
	}
	f, err := artifactdomain.DecodeObservationFragment(source.Frames[0])
	if err != nil {
		t.Fatal(err)
	}
	id, _ := artifactdomain.ObservationFragmentID(f)
	source.Source = artifactapp.FragmentRef{Kind: string(f.Kind), SourceID: id, FrameDigest: artifactdomain.Checksum(source.Frames[0])}
	return source
}

func TestProjectionNavigationRejectsUnboundOrMultipleTargets(t *testing.T) {
	for _, mutation := range []string{"model", "wrong_version", "wrong_source", "wrong_digest", "malformed", "wire", "scope", "duplicate_root", "duplicate_turn", "root_with_turn", "missing_root", "missing_turn", "unpaired_permutation", "extra_receipt"} {
		t.Run(mutation, func(t *testing.T) {
			source := navigationBasis(t, true)
			switch mutation {
			case "model":
				source = preparedFragmentProjection(t)
			case "wrong_version":
				source.Version = "langfuse.projection.v2"
			case "wrong_source":
				source.Source.SourceID = "other-source"
			case "wrong_digest":
				source.Source.FrameDigest = artifactdomain.Checksum([]byte("other-frame"))
			case "malformed":
				source.Frames[0] = []byte(`{}`)
			case "wire", "scope":
				var receipt artifactapp.ObservationScanReceipt
				if err := json.Unmarshal(source.Receipts[0], &receipt); err != nil {
					t.Fatal(err)
				}
				if mutation == "wire" {
					receipt.WireSHA256 = artifactdomain.Checksum([]byte("wrong-wire"))
				} else {
					receipt.Scope.RuntimeInstanceID = "other-runtime"
				}
				source.Receipts[0], _ = json.Marshal(receipt)
			case "duplicate_root":
				source.Frames[0], source.Receipts[0] = source.Frames[1], source.Receipts[1]
			case "duplicate_turn":
				source.Frames[1], source.Receipts[1] = source.Frames[0], source.Receipts[0]
			case "root_with_turn":
				source.Source = navigationBasis(t, false).Source
			case "missing_root":
				source.Frames, source.Receipts = source.Frames[:1], source.Receipts[:1]
			case "missing_turn":
				source.Frames, source.Receipts = source.Frames[1:], source.Receipts[1:]
			case "unpaired_permutation":
				source.Frames[0], source.Frames[1] = source.Frames[1], source.Frames[0]
			case "extra_receipt":
				source.Receipts = append(source.Receipts, source.Receipts[0])
			}
			got, err := (ProjectionNavigationCompiler{}).CompileProjectionNavigation(context.Background(), source)
			var proof *artifactapp.ProjectionError
			if !errors.As(err, &proof) || proof.Code != "fragment_proof_invalid" || !proof.Permanent || got.TraceID != "" || got.SelectedObservationID != "" || len(got.Sources) != 0 {
				t.Fatalf("invalid proof created identity: %+v %v", got, err)
			}
		})
	}
}

func TestProjectionNavigationUsesCompilerBodyIdentityWithoutChangingWire(t *testing.T) {
	for _, turn := range []bool{false, true} {
		source := navigationBasis(t, turn)
		before, err := compileFragmentProjection(context.Background(), source)
		if err != nil {
			t.Fatal(err)
		}
		wireBefore, _ := json.Marshal(before)
		got, err := (ProjectionNavigationCompiler{}).CompileProjectionNavigation(context.Background(), source)
		if err != nil {
			t.Fatal(err)
		}
		if got.TraceID != before.Batch[0].Body["id"] || got.TraceID == before.Batch[0].ID || got.Version != artifactapp.FragmentProjectionVersion {
			t.Fatalf("identity=%+v batch=%+v", got, before)
		}
		if turn && got.SelectedObservationID != before.Batch[1].Body["id"] || !turn && got.SelectedObservationID != "" {
			t.Fatalf("selected identity=%+v", got)
		}
		after, err := compileFragmentProjection(context.Background(), source)
		if err != nil {
			t.Fatal(err)
		}
		wireAfter, _ := json.Marshal(after)
		if string(wireBefore) != string(wireAfter) {
			t.Fatal("navigation changed ingestion bytes")
		}
		if turn {
			source.Frames[0], source.Frames[1] = source.Frames[1], source.Frames[0]
			source.Receipts[0], source.Receipts[1] = source.Receipts[1], source.Receipts[0]
			permuted, err := (ProjectionNavigationCompiler{}).CompileProjectionNavigation(context.Background(), source)
			if err != nil || !reflect.DeepEqual(got, permuted) {
				t.Fatalf("paired permutation: %+v %v", permuted, err)
			}
		}
	}
}
