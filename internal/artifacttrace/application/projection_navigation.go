package application

import (
	"context"

	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
)

type ProjectionNavigationStatus string

const (
	ProjectionNavigationVerified            ProjectionNavigationStatus = "verified"
	ProjectionNavigationPending             ProjectionNavigationStatus = "pending"
	ProjectionNavigationBlocked             ProjectionNavigationStatus = "blocked"
	ProjectionNavigationIdentityUnavailable ProjectionNavigationStatus = "identity_unavailable"
)

// Scope and actor are supplied by the server's authorization boundary. No
// session-only/latest selection or caller-provided projection version exists.
type ProjectionNavigationQuery struct {
	ActorID     string
	Scope       artifactdomain.ObservationScope
	AgentTurnID string
}

// Verified means this exact source/version/digest historically passed projection
// verification. It proves neither the current remote project nor service health.
// This result deliberately contains no frame, receipt, content, URL or secrets.
type ProjectionNavigation struct {
	Status   ProjectionNavigationStatus
	Identity *ProjectionNavigationIdentity
}

type ProjectionNavigationIdentity struct {
	Version               string
	TraceID               string
	SelectedObservationID string
	Sources               []FragmentRef
}

type ProjectionNavigationReader interface {
	ReadProjectionNavigation(context.Context, ProjectionNavigationQuery) (ProjectionNavigation, error)
}

// The compiler validates sealed basis frames and extracts ingestion body IDs;
// the reader separately proves accepted selection/publication/passed authority.
type ProjectionNavigationCompiler interface {
	CompileProjectionNavigation(context.Context, LoadedFragmentProjection) (ProjectionNavigationIdentity, error)
}
