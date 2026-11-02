package langfuse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
)

type FragmentProjector struct {
	api     DurableAPI
	scanner KnownTokenScanner
}

func NewFragmentProjector(api DurableAPI, scanner KnownTokenScanner) (*FragmentProjector, error) {
	if api == nil || scanner == nil {
		return nil, errors.New("fragment projector dependencies are required")
	}
	return &FragmentProjector{api: api, scanner: scanner}, nil
}

func (p *FragmentProjector) ExportAndVerify(ctx context.Context, source artifactapp.LoadedFragmentProjection) error {
	if ctx.Err() != nil {
		return projectionError("projection_cancelled", false)
	}
	if p == nil || p.api == nil || p.scanner == nil {
		return projectionError("source_dependencies_invalid", true)
	}
	batch, err := compileFragmentProjection(ctx, source)
	if err != nil {
		return err
	}
	scanner := DurableProjector{scanner: p.scanner}
	for _, frame := range source.Frames {
		if err := scanner.scan(ctx, "langfuse.fragment", json.RawMessage(frame)); err != nil {
			return err
		}
	}
	if err := scanner.scan(ctx, "langfuse.ingestion.batch", batch); err != nil {
		return err
	}
	if err := p.api.Ingest(ctx, batch); err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return projectionError("projection_cancelled", false)
		}
		return projectionError("ingestion_unavailable", false)
	}
	for _, event := range batch.Batch {
		body, _ := json.Marshal(event.Body)
		metadata, err := fragmentReadbackMetadata(event.Body["metadata"])
		if err != nil {
			return projectionError("source_invalid", true)
		}
		if event.Type == "trace-create" {
			var expected Trace
			if json.Unmarshal(body, &expected) != nil {
				return projectionError("source_invalid", true)
			}
			actual, err := p.api.GetTraceRoot(ctx, expected.ID)
			if err != nil {
				return readbackError(ctx, err)
			}
			if actual.ID != expected.ID || actual.Name != expected.Name || !actual.Timestamp.Equal(expected.Timestamp) || !metadataEqual(actual.ExactMetadata, metadata) || !artifactapp.ProjectionJSONEqual(actual.Input, expected.Input) || !nullOrEmptyJSON(actual.Output) {
				return projectionError("projection_collision", true)
			}
		} else {
			var expected Observation
			decoder := json.NewDecoder(bytes.NewReader(body))
			decoder.UseNumber()
			if decoder.Decode(&expected) != nil {
				return projectionError("source_invalid", true)
			}
			expected.Metadata = metadata
			expected.Type = "SPAN"
			if event.Type == "generation-create" {
				expected.Type = "GENERATION"
				expected.Usage = Usage{Input: expected.UsageDetails["input"], Output: expected.UsageDetails["output"], Total: expected.UsageDetails["total"], Unit: "TOKENS"}
			}
			actual, err := p.api.GetObservation(ctx, expected.ID)
			if err != nil {
				return readbackError(ctx, err)
			}
			if !durableObservationEqual(actual, expected) {
				return projectionError("projection_collision", true)
			}
		}
	}
	return nil
}

// Only the compiler's receipt-verified events enter the batch. Sorting changes
// parent order, never any event body or original json-text string.
func compileFragmentProjection(ctx context.Context, source artifactapp.LoadedFragmentProjection) (IngestionBatch, error) {
	bad := func() (IngestionBatch, error) {
		return IngestionBatch{}, projectionError("fragment_proof_invalid", true)
	}
	if source.Version != FragmentProjectionVersion || len(source.Frames) < 1 || len(source.Frames) > 3 || len(source.Frames) != len(source.Receipts) {
		return bad()
	}
	frames := map[artifactdomain.FragmentKind]artifactdomain.ObservationFragment{}
	var target artifactdomain.ObservationFragment
	found := false
	batch := IngestionBatch{}
	for n, raw := range source.Frames {
		f, err := artifactdomain.DecodeObservationFragment(raw)
		if err != nil {
			return bad()
		}
		if _, exists := frames[f.Kind]; exists {
			return bad()
		}
		frames[f.Kind] = f
		id, _ := artifactdomain.ObservationFragmentID(f)
		if string(f.Kind) == source.Source.Kind && id == source.Source.SourceID && artifactdomain.Checksum(raw) == source.Source.FrameDigest {
			target = f
			found = true
		}
		receipt, err := artifactapp.DecodeObservationScanReceipt(source.Receipts[n], raw)
		if err != nil {
			return bad()
		}
		wire, err := (FragmentCompiler{}).CompileObservationFragment(ctx, raw)
		if err != nil {
			if ctx.Err() != nil {
				return IngestionBatch{}, projectionError("projection_cancelled", false)
			}
			return bad()
		}
		if artifactdomain.Checksum(wire) != receipt.WireSHA256 {
			return bad()
		}
		var single IngestionBatch
		decoder := json.NewDecoder(bytes.NewReader(wire))
		decoder.UseNumber()
		if decoder.Decode(&single) != nil || len(single.Batch) != 1 {
			return bad()
		}
		batch.Batch = append(batch.Batch, single.Batch[0])
	}
	root, ok := frames[artifactdomain.FragmentRunBasis]
	if !found || !ok {
		return bad()
	}
	want := 1
	if target.Kind != artifactdomain.FragmentRunBasis {
		want = 2
		turn, ok := frames[artifactdomain.FragmentTurnBasis]
		turnID, _ := artifactdomain.ObservationBasisSourceID(artifactdomain.FragmentTurnBasis, turn.Scope, turn.Turn)
		targetTurnID, _ := artifactdomain.ObservationBasisSourceID(artifactdomain.FragmentTurnBasis, target.Scope, target.Turn)
		if !ok || turn.Scope != target.Scope || turnID != targetTurnID {
			return bad()
		}
	}
	if target.Kind == artifactdomain.FragmentModel {
		want = 3
	}
	if len(frames) != want {
		return bad()
	}
	for _, f := range frames {
		if f.Scope != root.Scope {
			return bad()
		}
	}
	order := map[string]int{"trace-create": 0, "span-create": 1, "generation-create": 2}
	sort.Slice(batch.Batch, func(i, j int) bool { return order[batch.Batch[i].Type] < order[batch.Batch[j].Type] })
	return batch, nil
}

func fragmentReadbackMetadata(value any) (map[string]any, error) {
	transport, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("source_invalid")
	}
	metadata := make(map[string]any, len(transport))
	for key, value := range transport {
		raw, ok := value.(string)
		if !ok {
			return nil, errors.New("source_invalid")
		}
		var decoded any
		decoder := json.NewDecoder(bytes.NewBufferString(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded); err != nil {
			return nil, err
		}
		metadata[key] = decoded
	}
	return metadata, nil
}

var _ artifactapp.FragmentProjector = (*FragmentProjector)(nil)
