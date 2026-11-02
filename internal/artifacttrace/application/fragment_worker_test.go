package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	app "github.com/abietic/switchback/internal/artifacttrace/application"
)

type emptyFragmentJobs struct {
	discoveryLimit int
	lease          time.Duration
}

type claimedFragmentJobs struct {
	emptyFragmentJobs
	attempts   int
	completion app.ProjectionCompletion
}

func (s *claimedFragmentJobs) Claim(context.Context, string, time.Duration) (app.FragmentJob, bool, error) {
	return app.FragmentJob{Attempts: s.attempts, Version: app.FragmentProjectionVersion}, true, nil
}
func (s *claimedFragmentJobs) Finish(_ context.Context, _ app.FragmentJob, c app.ProjectionCompletion) error {
	s.completion = c
	return nil
}

func TestFragmentWorkerPersistsOnlySafeBoundedCompletions(t *testing.T) {
	for _, test := range []struct {
		err          error
		status, code string
		attempts     int
		delay        time.Duration
	}{
		{&app.ProjectionError{Code: "parent_basis_pending"}, "retryable", "parent_basis_pending", 1, 5 * time.Second},
		{&app.ProjectionError{Code: "fragment_proof_invalid", Permanent: true}, "blocked", "fragment_proof_invalid", 2, 10 * time.Second},
		{&app.ProjectionError{Code: "synthetic private message"}, "retryable", "projection_unavailable", 1000, 5 * time.Minute},
	} {
		jobs := &claimedFragmentJobs{attempts: test.attempts}
		objects, err := artifactlocal.NewObjectStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		w := app.FragmentWorker{Jobs: jobs, Loader: app.FragmentLoader{Metadata: fragmentMetadata{err: test.err}, Objects: objects}, Projector: unusedFragmentIO{}}
		before := time.Now()
		if err := w.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		after := time.Now()
		c := jobs.completion
		if c.Status != test.status || c.ErrorCode != test.code || c.RetryAt.Before(before.Add(test.delay)) || c.RetryAt.After(after.Add(test.delay)) {
			t.Fatalf("completion %+v", c)
		}
	}
}

func (*emptyFragmentJobs) Activate(context.Context, string) error {
	panic("worker must not activate production")
}
func (s *emptyFragmentJobs) Discover(_ context.Context, version string, limit int) (int, error) {
	if version != app.FragmentProjectionVersion {
		panic("wrong version")
	}
	s.discoveryLimit = limit
	return 0, nil
}
func (s *emptyFragmentJobs) Claim(_ context.Context, _ string, lease time.Duration) (app.FragmentJob, bool, error) {
	s.lease = lease
	return app.FragmentJob{}, false, nil
}
func (*emptyFragmentJobs) Finish(context.Context, app.FragmentJob, app.ProjectionCompletion) error {
	panic("empty queue finished")
}

type unusedFragmentIO struct{}

func (unusedFragmentIO) Load(context.Context, app.FragmentJob) (app.LoadedFragmentProjection, error) {
	panic("empty queue loaded")
}
func (unusedFragmentIO) ExportAndVerify(context.Context, app.LoadedFragmentProjection) error {
	panic("empty queue exported")
}

func TestFragmentWorkerEmptyQueueAndCancellation(t *testing.T) {
	jobs := &emptyFragmentJobs{}
	w := app.FragmentWorker{Jobs: jobs, Loader: unusedFragmentIO{}, Projector: unusedFragmentIO{}}
	if err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if jobs.discoveryLimit != 50 || jobs.lease != 30*time.Second {
		t.Fatalf("bounds %+v", jobs)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Tick(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel %v", err)
	}
}
