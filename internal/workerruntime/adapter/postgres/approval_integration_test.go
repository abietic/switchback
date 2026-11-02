//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestApprovalDecisionPersistsExactSessionGrantAndIdempotency(t *testing.T) {
	pool := postgrestest.Open(t)
	now := time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC)
	sessionID := allocateLifecycleSession(t, pool, now.Add(-48*time.Hour))
	seedDeadlineTurn(t, pool, sessionID, now)
	if _, err := pool.Exec(context.Background(), `INSERT INTO worker_approval_requests
  (approval_request_id,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id,
   agent_turn_id,capability,target,effect,reason,scope_options,status,revision,expires_at,created_at)
VALUES ('approval-1','tenant-local','user-local','workspace-local',$1,'run-deadline',
  'turn-deadline','github.push','repository:switchback','external_write','Publish the reviewed branch.',
  ARRAY['once','session'],'pending',1,$2,$3)`, sessionID, now.Add(time.Hour), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	ids := &approvalIDs{values: []string{
		"command-1", "grant-1", "event-1", "command-2", "grant-2", "event-2",
		"command-3", "grant-3", "event-3",
	}}
	service := workerapp.ApprovalDecisionService{
		Store: workerpostgres.NewApprovalDecisionStore(pool), IDs: ids, Now: func() time.Time { return now },
	}
	command := workerapp.DecideApprovalCommand{
		TenantID: "tenant-local", UserID: "user-local", WorkerSessionID: sessionID,
		WorkerRunID: "run-deadline", AgentTurnID: "turn-deadline", ApprovalRequestID: "approval-1",
		Capability: "github.push", Target: "repository:switchback", Effect: "external_write",
		Decision: "approve", Scope: "session", ExpectedSessionRevision: 1,
		ExpectedRunRevision: 1, ExpectedTurnRevision: 1, ExpectedApprovalRevision: 1,
		IdempotencyKey: "approve-1",
	}
	first, err := service.Execute(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.Execute(context.Background(), command)
	if err != nil || replayed.CommandID != first.CommandID || replayed.GrantID != first.GrantID {
		t.Fatalf("replayed decision=%+v error=%v first=%+v", replayed, err, first)
	}
	var requestStatus, scope, capability, target, effect, grantStatus string
	if err := pool.QueryRow(context.Background(), `SELECT request.status,grant_row.scope,grant_row.capability,
       grant_row.target,grant_row.effect,grant_row.status
FROM worker_approval_requests request
JOIN worker_approval_grants grant_row ON grant_row.approval_request_id=request.approval_request_id
WHERE request.approval_request_id='approval-1'`).Scan(
		&requestStatus, &scope, &capability, &target, &effect, &grantStatus); err != nil {
		t.Fatal(err)
	}
	if requestStatus != "approved" || scope != "session" || capability != command.Capability ||
		target != command.Target || effect != command.Effect || grantStatus != "active" {
		t.Fatalf("request=%s grant=%s/%s/%s/%s/%s", requestStatus, scope, capability, target, effect, grantStatus)
	}
	var clientCommandID, eventIdempotencyKey, serverCommandID string
	if err := pool.QueryRow(context.Background(), `
SELECT client_command_id,idempotency_key,correlation_id
FROM outbox_events WHERE event_id='event-1'`).Scan(
		&clientCommandID, &eventIdempotencyKey, &serverCommandID,
	); err != nil {
		t.Fatal(err)
	}
	if clientCommandID != command.IdempotencyKey ||
		eventIdempotencyKey != "worker-approval/"+command.IdempotencyKey ||
		serverCommandID != first.CommandID {
		t.Fatalf("Worker approval outbox identities client=%q idempotency=%q command=%q",
			clientCommandID, eventIdempotencyKey, serverCommandID)
	}
	conflict := command
	conflict.Target = "repository:other"
	if _, err := service.Execute(context.Background(), conflict); !errors.Is(err, workerapp.ErrApprovalDecisionIdempotencyConflict) {
		t.Fatalf("same-key changed decision error=%v", err)
	}
	var idleExpiresAt time.Time
	if err := pool.QueryRow(context.Background(), `SELECT idle_expires_at FROM worker_sessions
WHERE worker_session_id=$1`, sessionID).Scan(&idleExpiresAt); err != nil {
		t.Fatal(err)
	}
	if !idleExpiresAt.Equal(now.Add(48 * time.Hour)) {
		t.Fatalf("idle expiry=%s", idleExpiresAt)
	}
}

type approvalIDs struct {
	values []string
	index  int
}

func (ids *approvalIDs) NewID(string) (string, error) {
	if ids.index >= len(ids.values) {
		return "", errors.New("approval test IDs exhausted")
	}
	value := ids.values[ids.index]
	ids.index++
	return value, nil
}
