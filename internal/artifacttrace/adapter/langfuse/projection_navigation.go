package langfuse

import (
	"context"
	"regexp"
	"sort"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
)

// ProjectionNavigationCompiler is pure: no scanner, registry, object store or
// client. IDs are extracted from the existing receipt-verified wire compiler.
type ProjectionNavigationCompiler struct{}

var navigationIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (ProjectionNavigationCompiler) CompileProjectionNavigation(ctx context.Context, source artifactapp.LoadedFragmentProjection) (artifactapp.ProjectionNavigationIdentity, error) {
	bad := func() (artifactapp.ProjectionNavigationIdentity, error) {
		return artifactapp.ProjectionNavigationIdentity{}, projectionError("fragment_proof_invalid", true)
	}
	turn := source.Source.Kind == string(artifactdomain.FragmentTurnBasis)
	if source.Source.Kind != string(artifactdomain.FragmentRunBasis) && !turn || len(source.Frames) > 2 {
		return bad()
	}
	batch, err := compileFragmentProjection(ctx, source)
	if err != nil {
		return artifactapp.ProjectionNavigationIdentity{}, err
	}
	want := 1
	if turn {
		want = 2
	}
	if len(batch.Batch) != want {
		return bad()
	}
	out := artifactapp.ProjectionNavigationIdentity{Version: source.Version}
	var selectedTrace string
	for _, event := range batch.Batch {
		id, ok := event.Body["id"].(string)
		if !ok || !navigationIDPattern.MatchString(id) {
			return bad()
		}
		switch event.Type {
		case "trace-create":
			if out.TraceID != "" || event.Body["name"] != "worker.run" {
				return bad()
			}
			out.TraceID = id
		case "span-create":
			if !turn || out.SelectedObservationID != "" || event.Body["name"] != "agent.turn" {
				return bad()
			}
			selectedTrace, ok = event.Body["traceId"].(string)
			if !ok || !navigationIDPattern.MatchString(selectedTrace) {
				return bad()
			}
			if parent, present := event.Body["parentObservationId"]; present && parent != "" {
				return bad()
			}
			out.SelectedObservationID = id
		default:
			return bad()
		}
	}
	if out.TraceID == "" || turn && (out.SelectedObservationID == "" || out.SelectedObservationID == out.TraceID || selectedTrace != out.TraceID) {
		return bad()
	}
	for _, raw := range source.Frames {
		frame, err := artifactdomain.DecodeObservationFragment(raw)
		if err != nil {
			return bad()
		}
		id, err := artifactdomain.ObservationFragmentID(frame)
		if err != nil {
			return bad()
		}
		out.Sources = append(out.Sources, artifactapp.FragmentRef{Kind: string(frame.Kind), SourceID: id, FrameDigest: artifactdomain.Checksum(raw)})
	}
	sort.Slice(out.Sources, func(i, j int) bool { return out.Sources[i].Kind < out.Sources[j].Kind })
	return out, nil
}

var _ artifactapp.ProjectionNavigationCompiler = ProjectionNavigationCompiler{}
