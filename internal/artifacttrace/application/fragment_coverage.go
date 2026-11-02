package application

import (
	"context"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
)

// Counts describe accepted selected members, never a callback closure or an
// inventory of all model requests that could still arrive.
type FragmentCoverage struct {
	Version   string
	Sources   []FragmentRef
	Accepted  int
	Sealed    int
	Published int
	Verified  int
	GapCodes  []string
}
type FragmentCoverageReader interface {
	Read(context.Context, string, artifactdomain.ObservationScope, string) (FragmentCoverage, error)
}
