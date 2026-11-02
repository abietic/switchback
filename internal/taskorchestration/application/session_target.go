package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrSessionReuseInvalid = errors.New("exact WorkerSession reuse evidence is invalid")

type CreateSessionTargetIntentCommand struct {
	Actor           Actor
	WorkspaceID     string
	WorkerSessionID string
	TaskSpecDigest  string
	SourceRef       string
	SourceRevision  uint64
	BusyPolicy      string
	IdempotencyKey  string
}

type SessionTargetIntentRecord struct {
	Command   CreateSessionTargetIntentCommand
	IntentID  string
	CreatedAt time.Time
	ExpiresAt time.Time
}

type SessionTargetIntent struct {
	IntentID              string
	WorkerSessionID       string
	WorkerSessionRevision uint64
	WorkingCopyID         string
	TaskSpecDigest        string
	ExpiresAt             time.Time
}

type SessionTargetIntentStore interface {
	CreateSessionTargetIntent(context.Context, SessionTargetIntentRecord) (SessionTargetIntent, error)
}

type SessionTargetIntentService struct {
	Store SessionTargetIntentStore
	IDs   IDGenerator
	Now   func() time.Time
	TTL   time.Duration
}

func (service SessionTargetIntentService) Create(
	ctx context.Context,
	command CreateSessionTargetIntentCommand,
) (SessionTargetIntent, error) {
	if service.Store == nil || service.IDs == nil || service.Now == nil {
		return SessionTargetIntent{}, fmt.Errorf("Session target intent dependencies are required")
	}
	for name, value := range map[string]string{
		"tenant_id": command.Actor.TenantID, "actor_user_id": command.Actor.UserID,
		"workspace_id": command.WorkspaceID, "worker_session_id": command.WorkerSessionID,
		"task_spec_digest": command.TaskSpecDigest, "source_ref": command.SourceRef,
		"idempotency_key": command.IdempotencyKey,
	} {
		if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
			return SessionTargetIntent{}, fmt.Errorf("%w: %s must be canonical", ErrSessionReuseInvalid, name)
		}
	}
	if command.SourceRevision == 0 || command.BusyPolicy != "queue" {
		return SessionTargetIntent{}, fmt.Errorf("%w: source revision and busy_policy=queue are required", ErrSessionReuseInvalid)
	}
	intentID, err := service.IDs.NewID("worker-session-target-intent")
	if err != nil {
		return SessionTargetIntent{}, err
	}
	now := service.Now().UTC()
	ttl := service.TTL
	if ttl == 0 {
		ttl = 15 * time.Minute
	}
	if ttl < 0 {
		return SessionTargetIntent{}, fmt.Errorf("target intent TTL must be positive")
	}
	return service.Store.CreateSessionTargetIntent(ctx, SessionTargetIntentRecord{
		Command: command, IntentID: intentID, CreatedAt: now, ExpiresAt: now.Add(ttl),
	})
}
