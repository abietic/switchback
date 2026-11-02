package application

import (
	"context"
	"errors"
	"testing"
	"time"
)

type workerJobs struct {
	completion ProjectionCompletion
	job        ProjectionJob
}

func (*workerJobs) Activate(context.Context, string) error             { return nil }
func (*workerJobs) Discover(context.Context, string, int) (int, error) { return 1, nil }
func (s *workerJobs) Claim(context.Context, string, time.Duration) (ProjectionJob, bool, error) {
	return s.job, true, nil
}
func (s *workerJobs) Finish(_ context.Context, _ ProjectionJob, c ProjectionCompletion) error {
	s.completion = c
	return nil
}
func (*workerJobs) RequestSource(context.Context, string, ProjectionSourceRef) error { return nil }

type workerLoader struct{}

func (workerLoader) Load(context.Context, ProjectionJob) (SourceProjection, error) {
	return SourceProjection{}, nil
}

type workerProjector struct{ err error }

func (p workerProjector) ExportAndVerify(context.Context, SourceProjection) error { return p.err }

func TestProjectionWorkerCompletesAndClassifiesFailures(t *testing.T) {
	for _, tt := range []struct {
		name         string
		err          error
		status, code string
	}{
		{"success", nil, "passed", ""},
		{"permanent", &ProjectionError{Code: "credential_detected", Permanent: true}, "blocked", "credential_detected"},
		{"transient", errors.New("sensitive remote payload"), "retryable", "projection_unavailable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
			jobs := &workerJobs{job: ProjectionJob{Attempts: 1}}
			worker := ProjectionWorker{Jobs: jobs, Loader: workerLoader{}, Projector: workerProjector{tt.err}, Now: func() time.Time { return now }}
			if err := worker.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			if jobs.completion.Status != tt.status || jobs.completion.ErrorCode != tt.code {
				t.Fatalf("completion: %+v", jobs.completion)
			}
			if tt.status == "retryable" && !jobs.completion.RetryAt.Equal(now.Add(5*time.Second)) {
				t.Fatal("initial retry must be five seconds")
			}
		})
	}
}

type blockingWorkerProjector struct{ entered chan struct{} }

func (p blockingWorkerProjector) ExportAndVerify(ctx context.Context, _ SourceProjection) error {
	close(p.entered)
	<-ctx.Done()
	return ctx.Err()
}

func TestProjectionWorkerRunStopsWhileExporterBlocked(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	projector := blockingWorkerProjector{entered: make(chan struct{})}
	worker := ProjectionWorker{Jobs: &workerJobs{}, Loader: workerLoader{}, Projector: projector}
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(ctx) }()
	select {
	case <-projector.entered:
	case <-time.After(time.Second):
		t.Fatal("exporter not entered")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker ignored cancellation")
	}
}

func TestProjectionWorkerCapsBackoffAndSanitizesTypedError(t *testing.T) {
	now := time.Now()
	jobs := &workerJobs{job: ProjectionJob{Attempts: 1000}}
	worker := ProjectionWorker{Jobs: jobs, Loader: workerLoader{}, Projector: workerProjector{&ProjectionError{Code: "secret remote output"}}, Now: func() time.Time { return now }}
	if err := worker.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if jobs.completion.ErrorCode != "projection_unavailable" || !jobs.completion.RetryAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("unsafe or unbounded completion %+v", jobs.completion)
	}
}
