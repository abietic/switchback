package application

import (
	"context"
	"errors"
	"time"
)

type FragmentWorker struct {
	Jobs      FragmentJobStore
	Loader    FragmentProjectionLoader
	Projector FragmentProjector
	Report    func(string)
}

func (w *FragmentWorker) Tick(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if w == nil || w.Jobs == nil || w.Loader == nil || w.Projector == nil {
		return errors.New("observability_worker_unavailable")
	}
	// Activation belongs to Task7; discovery is repeatable after every crash.
	if _, err := w.Jobs.Discover(ctx, FragmentProjectionVersion, 50); err != nil {
		return errors.New("observability_worker_unavailable")
	}
	job, ok, err := w.Jobs.Claim(ctx, FragmentProjectionVersion, 30*time.Second)
	if err != nil {
		return errors.New("observability_worker_unavailable")
	}
	if !ok {
		return nil
	}
	jobCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	source, err := w.Loader.Load(jobCtx, job)
	if err == nil {
		err = w.Projector.ExportAndVerify(jobCtx, source)
	}
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	completion := ProjectionCompletion{Status: "passed"}
	if err != nil {
		completion.Status, completion.ErrorCode = "retryable", "projection_unavailable"
		var typed *ProjectionError
		if errors.As(err, &typed) {
			if typed.Permanent {
				completion.Status = "blocked"
			}
			completion.ErrorCode = safeProjectionCode(typed.Code)
		}
		delay := 5 * time.Second
		for n := 1; n < job.Attempts && delay < 5*time.Minute; n++ {
			delay *= 2
		}
		if delay > 5*time.Minute {
			delay = 5 * time.Minute
		}
		completion.RetryAt = time.Now().Add(delay)
	}
	if err := w.Jobs.Finish(ctx, job, completion); err != nil {
		return errors.New("observability_worker_unavailable")
	}
	return nil
}

func (w *FragmentWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := w.Tick(ctx); err != nil && ctx.Err() == nil && w.Report != nil {
			w.Report("observability_worker_unavailable")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
