//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	artifactpostgres "github.com/abietic/switchback/internal/artifacttrace/adapter/postgres"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	workerdomain "github.com/abietic/switchback/internal/workerruntime/domain"
)

func TestBackendDeadlineDomainCommitKeepsRuntimeAuthorityForEvidencePublication(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 3, 20, 0, 0, time.UTC)
	started, cleaned, deadlineAt := prepareBackendDeadlineSourceForApply(
		t,
		pool,
		base,
		true,
	)
	tag, err := pool.Exec(ctx, `
UPDATE agent_turns
SET status='consumed',revision=revision+1
WHERE agent_turn_id=$1 AND status='delivered'`,
		started.AgentTurnID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatal("Backend deadline fixture did not enter an active consumed Turn")
	}
	var sessionRevision, runRevision, turnRevision, runtimeRevision uint64
	if err := pool.QueryRow(ctx, `
SELECT session.session_revision,run.revision,turn.revision,runtime.revision
FROM worker_sessions session
JOIN worker_runs run ON run.worker_run_id=$2
JOIN agent_turns turn ON turn.agent_turn_id=$3
JOIN runtime_instances runtime ON runtime.runtime_instance_id=$4
WHERE session.worker_session_id=$1`,
		started.WorkerSessionID,
		started.WorkerRunID,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	).Scan(
		&sessionRevision,
		&runRevision,
		&turnRevision,
		&runtimeRevision,
	); err != nil {
		t.Fatal(err)
	}
	stage := workerapp.CompleteResidentStageCommand{
		TenantID:                "tenant-local",
		WorkerSessionID:         started.WorkerSessionID,
		WorkerRunID:             started.WorkerRunID,
		AgentTurnID:             started.AgentTurnID,
		RuntimeInstanceID:       started.RuntimeInstanceID,
		RuntimeGeneration:       started.RuntimeGeneration,
		ExpectedSessionRevision: sessionRevision,
		ExpectedRunRevision:     runRevision,
		ExpectedTurnRevision:    turnRevision,
		ExpectedRuntimeRevision: runtimeRevision,
		SessionTarget:           workerdomain.SessionWaitingUser,
		RunTarget:               workerdomain.RunTimedOut,
		TurnTarget:              workerdomain.TurnTimedOut,
		TerminalReason:          "agent_turn_deadline",
	}
	var sourceState workerapp.BackendDeadlineSourceState
	if err := json.Unmarshal(cleaned.Source.State, &sourceState); err != nil {
		t.Fatal(err)
	}
	if sourceState.ForceStop == nil ||
		sourceState.ForceStop.ExpectedSessionRevision !=
			sessionRevision ||
		sourceState.ForceStop.ExpectedRuntimeRevision !=
			runtimeRevision {
		t.Fatalf(
			"force proof=%+v session_revision=%d runtime_revision=%d",
			sourceState.ForceStop,
			sessionRevision,
			runtimeRevision,
		)
	}
	serviceNow := cleaned.UpdatedAt.Add(time.Second)
	frozenAt := serviceNow
	settlements := workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return serviceNow },
	}
	acceptedCheckpoint := backendDeadlineDomainCheckpoint(
		t,
		started,
		stage,
		deadlineAt,
		frozenAt,
		workerapp.TurnSettlementAccepted,
		true,
	)
	acceptedCheckpoint = publicationScopeCheckpoint(t, pool, started, acceptedCheckpoint)
	current, err := settlements.FreezeCheckpoint(
		ctx,
		workerapp.FreezeTurnSettlementCheckpointCommand{
			SettlementID:     cleaned.SettlementID,
			ExpectedRevision: cleaned.Revision,
			LeaseOwnerID:     cleaned.LeaseOwnerID,
			LeaseFence:       cleaned.LeaseFence,
			Checkpoint: typedTurnSettlementCheckpoint(
				t,
				acceptedCheckpoint,
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	serviceNow = serviceNow.Add(time.Second)
	preparedCheckpoint := backendDeadlineDomainCheckpoint(
		t,
		started,
		stage,
		deadlineAt,
		frozenAt,
		workerapp.TurnSettlementEvidencePrepared,
		true,
	)
	preparedCheckpoint = publicationScopeCheckpoint(t, pool, started, preparedCheckpoint)
	current, err = settlements.Advance(
		ctx,
		workerapp.AdvanceTurnSettlementCommand{
			SettlementID:     current.SettlementID,
			ExpectedRevision: current.Revision,
			LeaseOwnerID:     current.LeaseOwnerID,
			LeaseFence:       current.LeaseFence,
			FromPhase:        workerapp.TurnSettlementAccepted,
			ToPhase: workerapp.
				TurnSettlementEvidencePrepared,
			Checkpoint: preparedCheckpoint,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	serviceNow = serviceNow.Add(time.Second)
	command := workerapp.CommitTurnSettlementDomainCommand{
		SettlementID:     current.SettlementID,
		ExpectedRevision: current.Revision,
		LeaseOwnerID:     current.LeaseOwnerID,
		LeaseFence:       current.LeaseFence,
		Resident:         stage,
	}
	committed, err := (workerapp.TurnSettlementDomainService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return serviceNow },
	}).Commit(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Settlement.Phase !=
		workerapp.TurnSettlementDomainCommitted ||
		committed.Resident.SessionStatus !=
			workerdomain.SessionWaitingUser ||
		committed.Resident.RunStatus != workerdomain.RunTimedOut ||
		committed.Resident.TurnStatus != workerdomain.TurnTimedOut ||
		committed.Resident.RuntimeStatus != "active" ||
		committed.Resident.SessionRevision != sessionRevision+1 ||
		committed.Resident.RuntimeRevision != runtimeRevision {
		t.Fatalf("committed=%+v", committed)
	}
	var activeRuntimeID *string
	var cleanupConfirmed bool
	if err := pool.QueryRow(ctx, `
SELECT session.active_runtime_instance_id,runtime.cleanup_confirmed
FROM worker_sessions session
JOIN runtime_instances runtime ON runtime.runtime_instance_id=$2
WHERE session.worker_session_id=$1`,
		started.WorkerSessionID,
		started.RuntimeInstanceID,
	).Scan(&activeRuntimeID, &cleanupConfirmed); err != nil {
		t.Fatal(err)
	}
	if activeRuntimeID == nil ||
		*activeRuntimeID != started.RuntimeInstanceID ||
		cleanupConfirmed {
		t.Fatalf(
			"active_runtime_id=%v cleanup_confirmed=%t",
			activeRuntimeID,
			cleanupConfirmed,
		)
	}

	replayed, err := (workerapp.TurnSettlementDomainService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return serviceNow.Add(time.Second) },
	}).Commit(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Settlement.Revision !=
		committed.Settlement.Revision ||
		replayed.Proof != committed.Proof {
		t.Fatalf(
			"response-loss replay changed commit: first=%+v replay=%+v",
			committed,
			replayed,
		)
	}
	// Forced-stop publication keeps its exact Runtime CAS even when the
	// resident path accepts journal progress. Renew this historical fixture's
	// settlement against the database clock before exercising the live fence.
	current, err = (workerapp.TurnSettlementService{Store: workerpostgres.NewTurnSettlementRepository(pool), Now: time.Now}).Claim(ctx, workerapp.ClaimTurnSettlementCommand{SettlementID: committed.Settlement.SettlementID, ExpectedRevision: committed.Settlement.Revision, OwnerID: "forced-publication-holder"})
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint struct {
		Evidence struct {
			Finalized artifactapp.FinalizedTurnEvidence `json:"finalized"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(current.PhaseState, &checkpoint); err != nil {
		t.Fatal(err)
	}
	publication := artifactapp.PublishTurnEvidenceRecord{Command: artifactapp.PublishTurnEvidenceCommand{Evidence: checkpoint.Evidence.Finalized, ExpectedSessionRevision: committed.Proof.SessionRevision, ExpectedRunRevision: committed.Proof.RunRevision, ExpectedTurnRevision: committed.Proof.TurnRevision, ExpectedRuntimeRevision: committed.Proof.RuntimeRevision, IdempotencyKey: "publish-turn/" + started.AgentTurnID}, EventID: "forced-publication-event", Now: time.Now()}
	metadata := artifactpostgres.NewMetadataStore(pool)
	if _, err := pool.Exec(ctx, `UPDATE runtime_instances SET revision=revision+1 WHERE runtime_instance_id=$1`, started.RuntimeInstanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := metadata.PublishSettlementEvidence(ctx, current, publication); !errors.Is(err, artifactpostgres.ErrEvidenceTargetStale) {
		t.Fatalf("forced-stop publication relaxed Runtime CAS: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE runtime_instances SET revision=revision-1 WHERE runtime_instance_id=$1`, started.RuntimeInstanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := metadata.PublishSettlementEvidence(ctx, current, publication); err != nil {
		t.Fatalf("forced-stop publication rejected exact frozen authority: %v", err)
	}
}

// The domain-only fixture intentionally uses synthetic scope; publication also
// needs the actual Task/Workspace provenance before the checkpoint is frozen.
func publicationScopeCheckpoint(t *testing.T, pool *pgxpool.Pool, started workerapp.StartedWorkerRun, raw json.RawMessage) json.RawMessage {
	t.Helper()
	var workspace, task string
	if err := pool.QueryRow(context.Background(), `SELECT s.workspace_id,COALESCE(r.task_id,'') FROM worker_sessions s JOIN worker_runs r ON r.worker_run_id=$2 WHERE s.worker_session_id=$1`, started.WorkerSessionID, started.WorkerRunID).Scan(&workspace, &task); err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	i := fields["evidence_input"].(map[string]any)
	i["workspace_id"] = workspace
	i["task_id"] = task
	if e, ok := fields["evidence"].(map[string]any); ok {
		f := e["finalized"].(map[string]any)
		f["Manifest"].(map[string]any)["workspace_id"] = workspace
		for _, r := range f["ArtifactRefs"].([]any) {
			a := r.(map[string]any)
			a["workspace_id"] = workspace
			a["task_id"] = task
		}
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestBackendDeadlineDomainCommitAcceptsFrozenSuspectRuntime(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 4, 20, 0, 0, time.UTC)
	started, cleaned, deadlineAt :=
		prepareSuspectBackendDeadlineSourceForDomain(
			t,
			pool,
			base,
		)
	current, command, committedAt :=
		prepareBackendDeadlineDomainCommit(
			t,
			pool,
			started,
			cleaned,
			deadlineAt,
		)

	before := loadBackendDeadlineDomainSnapshot(t, pool, started)
	if before.SessionStatus != "running" ||
		before.RuntimeStatus != "suspect" {
		t.Fatalf("suspect forced-domain prerequisite=%+v", before)
	}
	committed, err := (workerapp.TurnSettlementDomainService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return committedAt },
	}).Commit(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	after := loadBackendDeadlineDomainSnapshot(t, pool, started)
	if committed.Settlement.Phase !=
		workerapp.TurnSettlementDomainCommitted ||
		after.SessionStatus != "waiting_user" ||
		after.SessionRevision != before.SessionRevision+1 ||
		after.RunStatus != "timed_out" ||
		after.RunRevision != before.RunRevision+1 ||
		after.TurnStatus != "timed_out" ||
		after.TurnRevision != before.TurnRevision+1 ||
		after.RuntimeStatus != "suspect" ||
		after.RuntimeRevision != before.RuntimeRevision ||
		after.ActiveRuntimeInstanceID != started.RuntimeInstanceID ||
		after.RuntimeCleanupConfirmed {
		t.Fatalf(
			"suspect forced-domain before=%+v after=%+v committed=%+v",
			before,
			after,
			committed,
		)
	}
	if committed.Proof.SessionRevision != after.SessionRevision ||
		committed.Proof.RunRevision != after.RunRevision ||
		committed.Proof.TurnRevision != after.TurnRevision ||
		committed.Proof.RuntimeRevision != after.RuntimeRevision {
		t.Fatalf(
			"suspect forced-domain proof=%+v after=%+v",
			committed.Proof,
			after,
		)
	}

	replayed, err := (workerapp.TurnSettlementDomainService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return committedAt.Add(time.Second) },
	}).Commit(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Settlement.Revision != current.Revision+1 ||
		replayed.Settlement.Revision != committed.Settlement.Revision ||
		replayed.Proof != committed.Proof ||
		loadBackendDeadlineDomainSnapshot(t, pool, started) != after {
		t.Fatalf(
			"suspect forced-domain replay changed result: first=%+v replay=%+v",
			committed,
			replayed,
		)
	}
}

func TestBackendDeadlineDomainCommitAcceptsGracefulTerminalOnSuspectRuntime(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 4, 40, 0, 0, time.UTC)
	started, leased, deadlineAt :=
		setupLeasedBackendDeadlineSourceWithRuntimePredecessor(
			t,
			pool,
			base,
			"suspect",
		)

	var processNonce string
	var streamAttempt, ownerFence uint64
	if err := pool.QueryRow(ctx, `
SELECT process_nonce,stream_attempt,owner_fence
FROM runtime_control_channels
WHERE runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&processNonce,
		&streamAttempt,
		&ownerFence,
	); err != nil {
		t.Fatal(err)
	}
	terminalAt := deadlineAt.Add(time.Second)
	accepted, err := (workerapp.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return terminalAt },
	}).AcceptEvent(
		ctx,
		workerapp.AcceptRuntimeControlEventCommand{
			Authority: workerapp.RuntimeControlAuthority{
				WorkerSessionID:   started.WorkerSessionID,
				RuntimeInstanceID: started.RuntimeInstanceID,
				RuntimeGeneration: started.RuntimeGeneration,
				ChannelEpoch:      started.ControlChannelEpoch,
				ProcessNonce:      processNonce,
				StreamAttempt:     streamAttempt,
				OwnerFence:        ownerFence,
			},
			Sequence:       1,
			MessageID:      "graceful-terminal-on-suspect-runtime",
			IdempotencyKey: "graceful-terminal-on-suspect-runtime",
			Envelope: runtimeControlEnvelope(
				started,
				"event",
				1,
				"graceful-terminal-on-suspect-runtime",
				"graceful-terminal-on-suspect-runtime",
				"agent_turn.terminal",
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
	}).Load(ctx, leased.SettlementID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := workerapp.DecodeBackendDeadlineSourceState(terminal.Source)
	if err != nil {
		t.Fatal(err)
	}
	if source.Phase !=
		workerapp.BackendDeadlineSourceTerminalObserved ||
		terminal.RuntimeInstanceID != started.RuntimeInstanceID ||
		terminal.RuntimeGeneration != started.RuntimeGeneration ||
		terminal.ChannelEpoch != started.ControlChannelEpoch ||
		terminal.TerminalEventSequence != accepted.Message.Sequence {
		t.Fatalf(
			"graceful terminal source=%+v settlement=%+v accepted=%+v",
			source,
			terminal,
			accepted,
		)
	}

	current, command, committedAt :=
		prepareBackendDeadlineDomainCommit(
			t,
			pool,
			started,
			terminal,
			deadlineAt,
		)
	before := loadBackendDeadlineDomainSnapshot(t, pool, started)
	if before.SessionStatus != "running" ||
		before.RuntimeStatus != "suspect" {
		t.Fatalf("graceful terminal prerequisite=%+v", before)
	}

	committed, err := (workerapp.TurnSettlementDomainService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return committedAt },
	}).Commit(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	after := loadBackendDeadlineDomainSnapshot(t, pool, started)
	if committed.Settlement.Phase !=
		workerapp.TurnSettlementDomainCommitted ||
		after.SessionStatus != "waiting_user" ||
		after.SessionRevision != before.SessionRevision+1 ||
		after.RunStatus != "timed_out" ||
		after.RunRevision != before.RunRevision+1 ||
		after.TurnStatus != "timed_out" ||
		after.TurnRevision != before.TurnRevision+1 ||
		after.RuntimeStatus != "suspect" ||
		after.RuntimeRevision != before.RuntimeRevision ||
		after.ActiveRuntimeInstanceID != started.RuntimeInstanceID ||
		after.RuntimeCleanupConfirmed {
		t.Fatalf(
			"graceful terminal before=%+v after=%+v committed=%+v",
			before,
			after,
			committed,
		)
	}
	if committed.Proof.SessionRevision != after.SessionRevision ||
		committed.Proof.RunRevision != after.RunRevision ||
		committed.Proof.TurnRevision != after.TurnRevision ||
		committed.Proof.RuntimeRevision != after.RuntimeRevision {
		t.Fatalf(
			"graceful terminal proof=%+v after=%+v",
			committed.Proof,
			after,
		)
	}

	replayed, err := (workerapp.TurnSettlementDomainService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return committedAt.Add(time.Second) },
	}).Commit(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Settlement.Revision != current.Revision+1 ||
		replayed.Settlement.Revision != committed.Settlement.Revision ||
		replayed.Proof != committed.Proof ||
		loadBackendDeadlineDomainSnapshot(t, pool, started) != after {
		t.Fatalf(
			"graceful terminal replay changed result: first=%+v replay=%+v",
			committed,
			replayed,
		)
	}
}

func TestBackendDeadlineDomainCommitAcceptsExactPreemptedRecovery(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 5, 20, 0, 0, time.UTC)
	started, cleaned, deadlineAt, opened :=
		prepareRecoveryPreemptedBackendDeadlineSourceForDomain(
			t,
			pool,
			base,
		)
	current, command, committedAt :=
		prepareBackendDeadlineDomainCommit(
			t,
			pool,
			started,
			cleaned,
			deadlineAt,
		)

	var sourceState workerapp.BackendDeadlineSourceState
	if err := json.Unmarshal(cleaned.Source.State, &sourceState); err != nil {
		t.Fatal(err)
	}
	if sourceState.RecoveryIncidentID != opened.IncidentID ||
		sourceState.RecoveryIncidentRevision !=
			opened.IncidentRevision+1 ||
		sourceState.RecoveryPreemptedAt.IsZero() {
		t.Fatalf(
			"deadline source recovery binding=%+v opened=%+v",
			sourceState,
			opened,
		)
	}
	assertExactDeadlinePreemptedRecovery(
		t,
		pool,
		started,
		cleaned,
		sourceState,
	)
	before := loadBackendDeadlineDomainSnapshot(t, pool, started)
	if before.SessionStatus != "recovering" ||
		before.RuntimeStatus != "unhealthy" {
		t.Fatalf("recovery forced-domain prerequisite=%+v", before)
	}
	committed, err := (workerapp.TurnSettlementDomainService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return committedAt },
	}).Commit(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	after := loadBackendDeadlineDomainSnapshot(t, pool, started)
	if committed.Settlement.Phase !=
		workerapp.TurnSettlementDomainCommitted ||
		after.SessionStatus != "waiting_user" ||
		after.SessionRevision != before.SessionRevision+1 ||
		after.RunStatus != "timed_out" ||
		after.RunRevision != before.RunRevision+1 ||
		after.TurnStatus != "timed_out" ||
		after.TurnRevision != before.TurnRevision+1 ||
		after.RuntimeStatus != "unhealthy" ||
		after.RuntimeRevision != before.RuntimeRevision ||
		after.ActiveRuntimeInstanceID != started.RuntimeInstanceID ||
		after.RuntimeCleanupConfirmed {
		t.Fatalf(
			"recovery forced-domain before=%+v after=%+v committed=%+v",
			before,
			after,
			committed,
		)
	}
	if committed.Proof.SessionRevision != after.SessionRevision ||
		committed.Proof.RunRevision != after.RunRevision ||
		committed.Proof.TurnRevision != after.TurnRevision ||
		committed.Proof.RuntimeRevision != after.RuntimeRevision {
		t.Fatalf(
			"recovery forced-domain proof=%+v after=%+v",
			committed.Proof,
			after,
		)
	}

	replayed, err := (workerapp.TurnSettlementDomainService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return committedAt.Add(time.Second) },
	}).Commit(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Settlement.Revision != current.Revision+1 ||
		replayed.Settlement.Revision != committed.Settlement.Revision ||
		replayed.Proof != committed.Proof ||
		loadBackendDeadlineDomainSnapshot(t, pool, started) != after {
		t.Fatalf(
			"recovery forced-domain replay changed result: first=%+v replay=%+v",
			committed,
			replayed,
		)
	}
	assertExactDeadlinePreemptedRecovery(
		t,
		pool,
		started,
		cleaned,
		sourceState,
	)
}

func TestBackendDeadlineDomainCommitRejectsInexactPreemptedRecoveryWithoutMutation(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 5, 40, 0, 0, time.UTC)
	started, cleaned, deadlineAt, _ :=
		prepareRecoveryPreemptedBackendDeadlineSourceForDomain(
			t,
			pool,
			base,
		)
	current, command, committedAt :=
		prepareBackendDeadlineDomainCommit(
			t,
			pool,
			started,
			cleaned,
			deadlineAt,
		)
	before := loadBackendDeadlineDomainSnapshot(t, pool, started)

	tag, err := pool.Exec(ctx, `
UPDATE runtime_recovery_incidents
SET failed_channel_epoch=failed_channel_epoch+1
WHERE preempted_by_turn_settlement_id=$1`,
		cleaned.SettlementID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatal("did not make the deadline-preempted recovery identity inexact")
	}

	_, err = (workerapp.TurnSettlementDomainService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return committedAt },
	}).Commit(ctx, command)
	if err == nil {
		t.Fatal("inexact deadline-preempted recovery domain commit succeeded")
	}
	after := loadBackendDeadlineDomainSnapshot(t, pool, started)
	if after != before {
		t.Fatalf(
			"inexact recovery mutated resident graph: before=%+v after=%+v",
			before,
			after,
		)
	}
	reloaded, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
	}).Load(ctx, current.SettlementID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Phase != workerapp.TurnSettlementEvidencePrepared ||
		reloaded.Revision != current.Revision ||
		reloaded.PhaseStateDigest != current.PhaseStateDigest {
		t.Fatalf(
			"inexact recovery advanced settlement: before=%+v after=%+v",
			current,
			reloaded,
		)
	}
}

func prepareRecoveryPreemptedBackendDeadlineSourceForDomain(
	t *testing.T,
	pool *pgxpool.Pool,
	base time.Time,
) (
	workerapp.StartedWorkerRun,
	workerapp.TurnSettlement,
	time.Time,
	workerapp.RuntimeRecoveryState,
) {
	t.Helper()
	ctx := context.Background()
	started, active := startActiveRecoveryFixture(t, pool, base)
	deadlineAt, sourceRef := loadAgentTurnDeadlineSource(
		t,
		pool,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	)
	_, _ = bindAndClaimRuntimeControl(t, pool, started, deadlineAt)
	recoveryAt := deadlineAt
	opened, err := (workerapp.RuntimeRecoveryService{
		Store: workerpostgres.NewRuntimeRecoveryStore(pool),
		IDs:   &concurrentAllocationIDs{},
		Now:   func() time.Time { return recoveryAt },
	}).Open(
		ctx,
		runtimeRecoveryOpenCommand(
			started,
			active,
			recoveryAt.Add(-time.Second),
			workerdomain.RecoveryTriggerContainerExit,
		),
	)
	if err != nil {
		t.Fatalf("open Runtime recovery: %v", err)
	}
	consumeBackendDeadlineDomainTurn(t, pool, started)
	claimedAt := recoveryAt.Add(time.Microsecond)
	repository := workerpostgres.NewTurnSettlementRepository(pool)
	claimed, err := (workerapp.TurnSettlementSourceService{
		Store: repository,
		Now:   func() time.Time { return claimedAt },
	}).ClaimBackendDeadline(
		ctx,
		backendDeadlineClaimCommand(started, deadlineAt, sourceRef),
	)
	if err != nil {
		t.Fatalf("claim Backend deadline during Runtime recovery: %v", err)
	}
	leased, err := (workerapp.TurnSettlementService{
		Store: repository,
		Now:   func() time.Time { return claimedAt },
	}).Claim(
		ctx,
		workerapp.ClaimTurnSettlementCommand{
			SettlementID:     claimed.SettlementID,
			ExpectedRevision: claimed.Revision,
			OwnerID:          "recovery-deadline-domain-owner",
		},
	)
	if err != nil {
		t.Fatalf("lease recovery-preempting Turn settlement: %v", err)
	}
	return started,
		advanceBackendDeadlineSourceToCleanup(
			t,
			pool,
			started,
			leased,
		),
		deadlineAt,
		opened
}

func assertExactDeadlinePreemptedRecovery(
	t *testing.T,
	pool *pgxpool.Pool,
	started workerapp.StartedWorkerRun,
	settlement workerapp.TurnSettlement,
	source workerapp.BackendDeadlineSourceState,
) {
	t.Helper()
	var (
		tenantID, sessionID, runID, turnID, runtimeID string
		phase, outcome, outcomeReason, preemptedBy    string
		revision, generation, channelEpoch            uint64
		updatedAt, closedAt                           time.Time
	)
	if err := pool.QueryRow(context.Background(), `
SELECT tenant_id,worker_session_id,COALESCE(worker_run_id,''),
       COALESCE(agent_turn_id,''),failed_runtime_instance_id,
       failed_generation,failed_channel_epoch,phase,COALESCE(outcome,''),
       COALESCE(outcome_reason,''),COALESCE(preempted_by_turn_settlement_id,''),
       revision,updated_at,closed_at
FROM runtime_recovery_incidents
WHERE incident_id=$1`,
		source.RecoveryIncidentID,
	).Scan(
		&tenantID,
		&sessionID,
		&runID,
		&turnID,
		&runtimeID,
		&generation,
		&channelEpoch,
		&phase,
		&outcome,
		&outcomeReason,
		&preemptedBy,
		&revision,
		&updatedAt,
		&closedAt,
	); err != nil {
		t.Fatal(err)
	}
	if tenantID != "tenant-local" ||
		sessionID != started.WorkerSessionID ||
		runID != started.WorkerRunID ||
		turnID != started.AgentTurnID ||
		runtimeID != started.RuntimeInstanceID ||
		generation != started.RuntimeGeneration ||
		channelEpoch != started.ControlChannelEpoch ||
		phase != "exhausted" ||
		outcome != "exhausted" ||
		outcomeReason != "preempted_by_backend_deadline" ||
		preemptedBy != settlement.SettlementID ||
		revision != source.RecoveryIncidentRevision ||
		!updatedAt.Equal(source.RecoveryPreemptedAt) ||
		!closedAt.Equal(source.RecoveryPreemptedAt) {
		t.Fatalf(
			"deadline-preempted recovery tenant=%s session=%s run=%s turn=%s runtime=%s generation=%d epoch=%d phase=%s outcome=%s reason=%s settlement=%s revision=%d updated_at=%s closed_at=%s source=%+v",
			tenantID,
			sessionID,
			runID,
			turnID,
			runtimeID,
			generation,
			channelEpoch,
			phase,
			outcome,
			outcomeReason,
			preemptedBy,
			revision,
			updatedAt,
			closedAt,
			source,
		)
	}
}

func consumeBackendDeadlineDomainTurn(
	t *testing.T,
	pool *pgxpool.Pool,
	started workerapp.StartedWorkerRun,
) {
	t.Helper()
	tag, err := pool.Exec(context.Background(), `
UPDATE agent_turns
SET status='consumed',revision=revision+1
WHERE agent_turn_id=$1 AND status='delivered'`,
		started.AgentTurnID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatal("Backend deadline domain fixture did not consume AgentTurn")
	}
}

func prepareSuspectBackendDeadlineSourceForDomain(
	t *testing.T,
	pool *pgxpool.Pool,
	base time.Time,
) (
	workerapp.StartedWorkerRun,
	workerapp.TurnSettlement,
	time.Time,
) {
	t.Helper()
	ctx := context.Background()
	started, _ := startActiveRecoveryFixture(t, pool, base)
	ids := &concurrentAllocationIDs{}
	heartbeatAt := base.Add(6 * time.Minute)
	heartbeat, err := (workerapp.RuntimeHeartbeatService{
		Store: workerpostgres.NewRuntimeHeartbeatStore(pool),
		IDs:   ids,
	}).Observe(
		ctx,
		workerapp.ObserveRuntimeHeartbeatCommand{
			WorkerSessionID:   started.WorkerSessionID,
			RuntimeInstanceID: started.RuntimeInstanceID,
			RuntimeGeneration: started.RuntimeGeneration,
			ChannelEpoch:      started.ControlChannelEpoch,
			HeartbeatSequence: 1,
			ControlSequence:   1,
			ReceivedAt:        heartbeatAt,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	evaluatedAt := heartbeatAt.Add(workerapp.RuntimeSuspectAfter)
	suspect, err := (workerapp.RuntimeLivenessService{
		Store: workerpostgres.NewRuntimeHeartbeatStore(pool),
		IDs:   ids,
		Now:   func() time.Time { return evaluatedAt },
	}).Evaluate(
		ctx,
		workerapp.EvaluateRuntimeLivenessCommand{
			TenantID:          "tenant-local",
			RuntimeInstanceID: started.RuntimeInstanceID,
			RuntimeGeneration: started.RuntimeGeneration,
			ChannelEpoch:      started.ControlChannelEpoch,
			HeartbeatSequence: 1,
			Stage: workerapp.
				RuntimeLivenessStageSuspect,
			ExpectedObservationRevision: heartbeat.ObservationRevision,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !suspect.Applied ||
		suspect.LivenessStatus != workerapp.RuntimeLivenessSuspect {
		t.Fatalf("mark Runtime suspect=%+v", suspect)
	}

	deadlineAt, sourceRef := loadAgentTurnDeadlineSource(
		t,
		pool,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	)
	_, _ = bindAndClaimRuntimeControl(t, pool, started, deadlineAt)
	repository := workerpostgres.NewTurnSettlementRepository(pool)
	claimed, err := (workerapp.TurnSettlementSourceService{
		Store: repository,
		Now:   func() time.Time { return deadlineAt },
	}).ClaimBackendDeadline(
		ctx,
		backendDeadlineClaimCommand(started, deadlineAt, sourceRef),
	)
	if err != nil {
		t.Fatal(err)
	}
	leased, err := (workerapp.TurnSettlementService{
		Store: repository,
		Now:   func() time.Time { return deadlineAt },
	}).Claim(
		ctx,
		workerapp.ClaimTurnSettlementCommand{
			SettlementID:     claimed.SettlementID,
			ExpectedRevision: claimed.Revision,
			OwnerID:          "suspect-deadline-domain-owner",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return started,
		advanceBackendDeadlineSourceToCleanup(
			t,
			pool,
			started,
			leased,
		),
		deadlineAt
}

func advanceBackendDeadlineSourceToCleanup(
	t *testing.T,
	pool *pgxpool.Pool,
	started workerapp.StartedWorkerRun,
	leased workerapp.TurnSettlement,
) workerapp.TurnSettlement {
	t.Helper()
	ctx := context.Background()
	forceCommand := backendDeadlineForceStopAdvanceCommand(
		t,
		pool,
		started,
		leased,
	)
	forceAt := forceCommand.ForceStop.DecidedAt
	leased = renewBackendDeadlineSourceLeaseAt(
		t,
		pool,
		leased,
		forceAt.Add(-time.Second),
	)
	forceCommand.ExpectedRevision = leased.Revision
	forceCommand.ExpectedStateDigest = leased.Source.StateDigest
	service := workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return forceAt },
	}
	forced, err := service.AdvanceBackendDeadline(ctx, forceCommand)
	if err != nil {
		t.Fatal(err)
	}
	var channelOwnerFence, channelRevision uint64
	var channelFencedAt time.Time
	if err := pool.QueryRow(ctx, `
SELECT owner_fence,revision,updated_at
FROM runtime_control_channels
WHERE runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&channelOwnerFence,
		&channelRevision,
		&channelFencedAt,
	); err != nil {
		t.Fatal(err)
	}
	absentAt := forceAt.Add(time.Second)
	writeFence, err := workerapp.NewBackendDeadlineWriteFenceProof(
		channelOwnerFence,
		channelRevision,
		channelFencedAt,
		forceCommand.ForceStop.ExpectedContainerID,
		absentAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	service.Now = func() time.Time { return absentAt }
	current, err := service.AdvanceBackendDeadline(
		ctx,
		workerapp.AdvanceBackendDeadlineSourceCommand{
			SettlementID:        forced.SettlementID,
			ExpectedRevision:    forced.Revision,
			ExpectedStateDigest: forced.Source.StateDigest,
			LeaseOwnerID:        forced.LeaseOwnerID,
			LeaseFence:          forced.LeaseFence,
			ToPhase: workerapp.
				BackendDeadlineSourceWriteFenced,
			WriteFence: &writeFence,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	confirmedAt := absentAt.Add(time.Second)
	cleanup, err := workerapp.NewBackendDeadlineCleanupProof(
		forceCommand.ForceStop.ExpectedContainerID,
		"switchback-runtime-secret-"+started.RuntimeInstanceID,
		confirmedAt,
		confirmedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	service.Now = func() time.Time { return confirmedAt }
	current, err = service.AdvanceBackendDeadline(
		ctx,
		workerapp.AdvanceBackendDeadlineSourceCommand{
			SettlementID:        current.SettlementID,
			ExpectedRevision:    current.Revision,
			ExpectedStateDigest: current.Source.StateDigest,
			LeaseOwnerID:        current.LeaseOwnerID,
			LeaseFence:          current.LeaseFence,
			ToPhase: workerapp.
				BackendDeadlineSourceCleanupConfirmed,
			Cleanup: &cleanup,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return current
}

func prepareBackendDeadlineDomainCommit(
	t *testing.T,
	pool *pgxpool.Pool,
	started workerapp.StartedWorkerRun,
	cleaned workerapp.TurnSettlement,
	deadlineAt time.Time,
) (
	workerapp.TurnSettlement,
	workerapp.CommitTurnSettlementDomainCommand,
	time.Time,
) {
	t.Helper()
	ctx := context.Background()
	tag, err := pool.Exec(ctx, `
UPDATE agent_turns
SET status='consumed',revision=revision+1
WHERE agent_turn_id=$1 AND status='delivered'`,
		started.AgentTurnID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() == 0 {
		var status string
		if err := pool.QueryRow(ctx, `
SELECT status FROM agent_turns WHERE agent_turn_id=$1`,
			started.AgentTurnID,
		).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "consumed" {
			t.Fatalf("Backend deadline Turn prerequisite=%s", status)
		}
	}
	var sessionRevision, runRevision, turnRevision, runtimeRevision uint64
	if err := pool.QueryRow(ctx, `
SELECT session.session_revision,run.revision,turn.revision,runtime.revision
FROM worker_sessions session
JOIN worker_runs run ON run.worker_run_id=$2
JOIN agent_turns turn ON turn.agent_turn_id=$3
JOIN runtime_instances runtime ON runtime.runtime_instance_id=$4
WHERE session.worker_session_id=$1`,
		started.WorkerSessionID,
		started.WorkerRunID,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	).Scan(
		&sessionRevision,
		&runRevision,
		&turnRevision,
		&runtimeRevision,
	); err != nil {
		t.Fatal(err)
	}
	stage := workerapp.CompleteResidentStageCommand{
		TenantID:                "tenant-local",
		WorkerSessionID:         started.WorkerSessionID,
		WorkerRunID:             started.WorkerRunID,
		AgentTurnID:             started.AgentTurnID,
		RuntimeInstanceID:       started.RuntimeInstanceID,
		RuntimeGeneration:       started.RuntimeGeneration,
		ExpectedSessionRevision: sessionRevision,
		ExpectedRunRevision:     runRevision,
		ExpectedTurnRevision:    turnRevision,
		ExpectedRuntimeRevision: runtimeRevision,
		SessionTarget:           workerdomain.SessionWaitingUser,
		RunTarget:               workerdomain.RunTimedOut,
		TurnTarget:              workerdomain.TurnTimedOut,
		TerminalReason:          "agent_turn_deadline",
	}
	var sourceState workerapp.BackendDeadlineSourceState
	if err := json.Unmarshal(cleaned.Source.State, &sourceState); err != nil {
		t.Fatal(err)
	}
	forcedStop := sourceState.Phase ==
		workerapp.BackendDeadlineSourceCleanupConfirmed
	switch sourceState.Phase {
	case workerapp.BackendDeadlineSourceCleanupConfirmed:
		if sourceState.ForceStop == nil ||
			sourceState.ForceStop.ExpectedSessionRevision !=
				sessionRevision ||
			sourceState.ForceStop.ExpectedRuntimeRevision !=
				runtimeRevision {
			t.Fatalf(
				"force proof=%+v session_revision=%d runtime_revision=%d",
				sourceState.ForceStop,
				sessionRevision,
				runtimeRevision,
			)
		}
	case workerapp.BackendDeadlineSourceTerminalObserved:
		if sourceState.ForceStop != nil ||
			sourceState.WriteFence != nil ||
			sourceState.Cleanup != nil {
			t.Fatalf("graceful terminal carries force proof=%+v", sourceState)
		}
	default:
		t.Fatalf("Backend deadline source phase=%s", sourceState.Phase)
	}
	serviceNow := cleaned.UpdatedAt.Add(time.Second)
	frozenAt := serviceNow
	settlements := workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return serviceNow },
	}
	current, err := settlements.FreezeCheckpoint(
		ctx,
		workerapp.FreezeTurnSettlementCheckpointCommand{
			SettlementID:     cleaned.SettlementID,
			ExpectedRevision: cleaned.Revision,
			LeaseOwnerID:     cleaned.LeaseOwnerID,
			LeaseFence:       cleaned.LeaseFence,
			Checkpoint: typedTurnSettlementCheckpoint(
				t,
				backendDeadlineDomainCheckpoint(
					t,
					started,
					stage,
					deadlineAt,
					frozenAt,
					workerapp.TurnSettlementAccepted,
					forcedStop,
				),
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	serviceNow = serviceNow.Add(time.Second)
	current, err = settlements.Advance(
		ctx,
		workerapp.AdvanceTurnSettlementCommand{
			SettlementID:     current.SettlementID,
			ExpectedRevision: current.Revision,
			LeaseOwnerID:     current.LeaseOwnerID,
			LeaseFence:       current.LeaseFence,
			FromPhase:        workerapp.TurnSettlementAccepted,
			ToPhase: workerapp.
				TurnSettlementEvidencePrepared,
			Checkpoint: backendDeadlineDomainCheckpoint(
				t,
				started,
				stage,
				deadlineAt,
				frozenAt,
				workerapp.TurnSettlementEvidencePrepared,
				forcedStop,
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	serviceNow = serviceNow.Add(time.Second)
	return current,
		workerapp.CommitTurnSettlementDomainCommand{
			SettlementID:     current.SettlementID,
			ExpectedRevision: current.Revision,
			LeaseOwnerID:     current.LeaseOwnerID,
			LeaseFence:       current.LeaseFence,
			Resident:         stage,
		},
		serviceNow
}

func backendDeadlineDomainCheckpoint(
	t *testing.T,
	started workerapp.StartedWorkerRun,
	stage workerapp.CompleteResidentStageCommand,
	deadlineAt time.Time,
	recordedAt time.Time,
	phase workerapp.TurnSettlementPhase,
	forcedStop bool,
) json.RawMessage {
	t.Helper()
	checkpoint := validTurnSettlementCheckpoint(
		t,
		started,
		stage,
		"backend-domain-report-"+started.AgentTurnID,
		"backend-domain-patch-"+started.AgentTurnID,
		recordedAt,
		phase,
	)
	var fields map[string]any
	if err := json.Unmarshal(checkpoint, &fields); err != nil {
		t.Fatal(err)
	}
	source, ok := fields["source"].(map[string]any)
	if !ok {
		t.Fatalf("checkpoint source=%T", fields["source"])
	}
	source["kind"] =
		string(workerapp.TurnSettlementSourceBackendDeadline)
	source["reason"] = "agent_turn_deadline"
	source["occurred_at"] = deadlineAt
	source["timed_out"] = true
	source["forced_stop"] = forcedStop
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	canonical, _, err :=
		workerapp.CanonicalizeTurnSettlementCheckpoint(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := workerapp.ValidateTurnSettlementCheckpointProof(
		phase,
		canonical,
	); err != nil {
		t.Fatal(err)
	}
	return canonical
}
