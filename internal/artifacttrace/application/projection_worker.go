package application

import (
	"context"
	"errors"
	"time"
)

type ProjectionLoader interface {
	Load(context.Context, ProjectionJob) (SourceProjection, error)
}

// ProjectionWorker owns remote observation I/O independently of execution.
// Tick and Run must have a single caller per instance; the store fences other workers.
type ProjectionWorker struct {
	Jobs      ProjectionJobStore
	Loader    ProjectionLoader
	Projector SourceProjector
	Now       func() time.Time
	Report    func(string)
}

func (worker *ProjectionWorker) Tick(ctx context.Context) error {
	if worker == nil || worker.Jobs == nil || worker.Loader == nil || worker.Projector == nil {
		return errors.New("observability_worker_unavailable")
	}
	if err := worker.Jobs.Activate(ctx, DurableProjectionSchemaVersion); err != nil {
		return errors.New("observability_worker_unavailable")
	}
	if _, err := worker.Jobs.Discover(ctx, DurableProjectionSchemaVersion, 50); err != nil {
		return errors.New("observability_worker_unavailable")
	}
	job, ok, err := worker.Jobs.Claim(ctx, DurableProjectionSchemaVersion, 30*time.Second)
	if err != nil {
		return errors.New("observability_worker_unavailable")
	}
	if !ok {
		return nil
	}
	jobCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	source, err := worker.Loader.Load(jobCtx, job)
	if err == nil {
		err = worker.Projector.ExportAndVerify(jobCtx, source)
	}
	cancel()
	completion := ProjectionCompletion{Status: "passed"}
	if err != nil {
		completion.Status = "retryable"
		completion.ErrorCode = "projection_unavailable"
		var typed *ProjectionError
		if errors.As(err, &typed) {
			if typed.Permanent {
				completion.Status = "blocked"
			}
			completion.ErrorCode = safeProjectionCode(typed.Code)
		}
		now := time.Now()
		if worker.Now != nil {
			now = worker.Now()
		}
		delay := 5 * time.Second
		for n := 1; n < job.Attempts && delay < 5*time.Minute; n++ {
			delay *= 2
		}
		if delay > 5*time.Minute {
			delay = 5 * time.Minute
		}
		completion.RetryAt = now.Add(delay)
	}
	if err := worker.Jobs.Finish(ctx, job, completion); err != nil {
		return errors.New("observability_worker_unavailable")
	}
	return nil
}

func (worker *ProjectionWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := worker.Tick(ctx); err != nil && ctx.Err() == nil && worker.Report != nil {
			worker.Report("observability_worker_unavailable")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Do not persist arbitrary adapter error text, including a purported typed code.
func safeProjectionCode(code string) string {
	switch code {
	case "fragment_proof_invalid", "parent_basis_pending", "parent_basis_unavailable":
		return code
	case "credential_detected", "ingestion_unavailable", "projection_cancelled", "projection_collision", "projection_not_ready", "readback_unavailable", "scanner_unavailable", "source_invalid",
		"source_checksum_mismatch", "source_identity_mismatch", "source_object_missing", "source_payload_invalid", "source_segment_duplicate", "source_segment_metadata_invalid", "source_segment_missing", "source_segment_uri_invalid", "source_size_mismatch", "source_store_invalid", "source_object_unavailable", "source_read_cancelled",
		"manifest_object_invalid", "manifest_object_missing", "manifest_object_unavailable", "report_encoding_invalid", "report_metadata_invalid", "report_object_invalid", "report_object_missing", "report_object_unavailable", "root_input_mismatch", "root_manifest_invalid", "root_source_incomplete", "root_source_unavailable", "source_dependencies_invalid", "source_incomplete", "source_metadata_unavailable", "source_load_cancelled":
		return code
	default:
		return "projection_unavailable"
	}
}
