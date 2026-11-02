package application

import (
	"bytes"
	"context"
	"errors"
	"time"

	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

type FragmentPublisher struct {
	Store   FragmentPublicationStore
	Objects workerapp.ObjectStore
}

func (p *FragmentPublisher) Tick(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if p == nil || p.Store == nil || p.Objects == nil {
		return errors.New("observability_worker_unavailable")
	}
	c, ok, err := p.Store.ClaimCandidate(ctx, 30*time.Second)
	if err != nil {
		return errors.New("observability_worker_unavailable")
	}
	if !ok {
		return nil
	}
	jobCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	err = p.publish(jobCtx, c)
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return nil
	}
	code, permanent := "source_object_unavailable", false
	var typed *ProjectionError
	if errors.As(err, &typed) {
		code, permanent = safeProjectionCode(typed.Code), typed.Permanent
	}
	if err := p.Store.DeferCandidate(ctx, c, code, permanent); err != nil {
		return errors.New("observability_worker_unavailable")
	}
	return nil
}

func (p *FragmentPublisher) publish(ctx context.Context, c FragmentCandidate) error {
	e, err := p.Store.ReadFragmentEvidence(ctx, c.Source)
	if err != nil {
		return normalizeProjectionError(ctx, err, "source_metadata_unavailable")
	}
	if !bytes.Equal(e.Frame, c.Frame) || !bytes.Equal(e.Receipt, c.Receipt) {
		return projectionFailure("fragment_proof_invalid")
	}
	if err := verifyFragmentEvidence(ctx, p.Objects, e); err != nil {
		return err
	}
	f, _ := artifactdomain.DecodeObservationFragment(c.Frame)
	turn := f.InitialTurnID
	if f.Turn != nil {
		turn = string(f.Turn.AgentTurnID)
	}
	identity := workerapp.ObjectIdentity{TenantID: f.Scope.TenantID, UserID: f.Scope.UserID, WorkspaceID: f.Scope.WorkspaceID, WorkerSessionID: f.Scope.WorkerSessionID, WorkerRunID: f.Scope.WorkerRunID, AgentTurnID: turn, Kind: ObservationFragmentObjectKind}
	temporary, err := p.Objects.PutTemporary(ctx, identity, bytes.NewReader(c.Frame))
	if err != nil {
		return err
	}
	object, err := p.Objects.Finalize(ctx, identity, temporary)
	if err != nil {
		if errors.Is(err, workerapp.ErrObjectIntegrity) {
			return projectionFailure("source_checksum_mismatch")
		}
		return err
	}
	if object.Size != int64(len(c.Frame)) || object.Checksum != c.Source.FrameDigest {
		return projectionFailure("source_checksum_mismatch")
	}
	if err := p.Objects.Verify(ctx, object.Key, object.Checksum); err != nil {
		if errors.Is(err, workerapp.ErrObjectIntegrity) {
			return projectionFailure("source_checksum_mismatch")
		}
		return normalizeProjectionError(ctx, err, "source_object_unavailable")
	}
	c.Evidence = &e
	_, err = p.Store.Publish(ctx, c, object)
	return err
}

func (p *FragmentPublisher) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		_ = p.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
