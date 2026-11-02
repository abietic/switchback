package application

import (
	"context"
	"time"
)

const FragmentProjectionVersion = "langfuse.projection.v3"

type FragmentRef struct {
	Kind        string
	SourceID    string
	FrameDigest string
}

type FragmentJob struct {
	Source     FragmentRef
	Version    string
	LeaseToken int64
	Attempts   int
	LeaseUntil time.Time
}

type FragmentJobStore interface {
	Activate(context.Context, string) error
	Discover(context.Context, string, int) (int, error)
	Claim(context.Context, string, time.Duration) (FragmentJob, bool, error)
	Finish(context.Context, FragmentJob, ProjectionCompletion) error
}
